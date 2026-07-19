package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestV2MigrationControlRepositoryPersistsStateAndRLS(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewV2MigrationControlRepository(appDB, rls)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

	run, err := repo.CreateRun(ctx, V2CreateMigrationRunInput{
		MigrationContractVersion: "migration-contract-v1",
		CorpusVersion:            "corpus-v1",
		SourceKind:               "neo4j",
		State:                    domain.V2MigrationStateReady,
		Phase:                    "preflight",
		Required:                 true,
		PreflightApproved:        true,
		BackupReference:          "backup-20260717",
		PreflightChecks: map[string]any{
			"postgres_restore_verified": true,
			"neo4j_snapshot_verified":   true,
		},
		Now: now,
	})
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationStateReady, run.State)
	require.True(t, run.PreflightApproved)

	started, err := repo.UpdateRunState(ctx, V2UpdateMigrationRunStateInput{
		RunID:     run.RunID,
		FromState: domain.V2MigrationStateReady,
		ToState:   domain.V2MigrationStateRunning,
		Phase:     "migration",
		Retryable: true,
		Now:       now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationStateRunning, started.State)
	require.NotNil(t, started.StartedAt)

	require.NoError(t, repo.RecordOperatorAction(ctx, domain.V2MigrationOperatorAction{
		RunID:     run.RunID,
		Action:    domain.V2MigrationActionStarted,
		Actor:     "operator",
		RemoteIP:  "127.0.0.1",
		Reason:    "start rehearsal",
		Metadata:  map[string]any{"ticket": float64(92)},
		CreatedAt: now.Add(time.Minute),
	}))
	actions, err := repo.ListOperatorActions(ctx, run.RunID, 10)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	require.Equal(t, domain.V2MigrationActionStarted, actions[0].Action)

	require.NoError(t, insertV2MigrationMarker(ctx, adminDB, rls, run.RunID))
	marker, err := repo.GetLatestMarker(ctx)
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationMarkerCompatible, marker.Status)
	require.Equal(t, run.RunID, marker.RunID)

	var appVisible int64
	require.NoError(t, appDB.Raw(`SELECT count(*) FROM v2_migration_runs`).Scan(&appVisible).Error)
	require.Zero(t, appVisible)
	var systemVisible int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM v2_migration_runs`).Scan(&systemVisible).Error
	}))
	require.EqualValues(t, 1, systemVisible)
}

func insertV2MigrationMarker(ctx context.Context, db *gorm.DB, rls *storagepostgres.RLS, runID string) error {
	return rls.WithMigrationTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO v2_compatibility_markers (
			    marker_id, marker_kind, version, status, run_id, corpus_hash, gate_report_hash, metadata
			) VALUES (
			    ?::uuid, ?, ?, ?, ?::uuid, 'corpus-hash', 'gate-hash', '{}'::jsonb
			)
		`, uuid.NewString(), domain.V2MigrationMarkerKindCutover, "v2.1.7",
			domain.V2MigrationMarkerCompatible, runID).Error
	})
}
