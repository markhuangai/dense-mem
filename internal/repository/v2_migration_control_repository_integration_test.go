package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestV2MigrationControlPlaneMigrationContainsDDL(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "postgres", "2026071909_v2_migration_control_plane.sql")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	sql := string(data)
	for _, ddl := range []string{
		"CREATE TABLE IF NOT EXISTS v2_migration_runs",
		"CREATE TABLE IF NOT EXISTS v2_migration_corpus_items",
		"CREATE TABLE IF NOT EXISTS v2_migration_checkpoints",
		"CREATE TABLE IF NOT EXISTS v2_migration_errors",
		"CREATE TABLE IF NOT EXISTS v2_migration_gate_results",
		"CREATE TABLE IF NOT EXISTS v2_compatibility_markers",
		"ALTER TABLE v2_migration_runs ENABLE ROW LEVEL SECURITY",
	} {
		require.Truef(t, strings.Contains(sql, ddl), "migration file missing %q", ddl)
	}
}

func TestV2MigrationControlRepositoryPersistsStateAndRLS(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewV2MigrationControlRepository(appDB, rls)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	latest, err := repo.GetLatestRun(ctx)
	require.NoError(t, err)
	require.Nil(t, latest)
	marker, err := repo.GetLatestMarker(ctx)
	require.NoError(t, err)
	require.Nil(t, marker)

	run, err := repo.CreateRun(ctx, V2CreateMigrationRunInput{
		MigrationContractVersion: "migration-contract-v1",
		CorpusVersion:            "corpus-v1",
		SourceKind:               "legacy",
		State:                    domain.V2MigrationStateReady,
		Phase:                    "preflight",
		Required:                 true,
		PreflightApproved:        true,
		BackupReference:          "backup-20260717",
		PreflightChecks: map[string]any{
			"postgres_restore_verified": true,
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
	marker, err = repo.GetLatestMarker(ctx)
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

func TestV2MigrationControlRepositoryCommitsCutoverAtomically(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewV2MigrationControlRepository(appDB, rls)
	now := time.Date(2026, 7, 21, 15, 0, 0, 0, time.UTC)

	run, err := repo.CreateRun(ctx, V2CreateMigrationRunInput{
		MigrationContractVersion: "migration-contract-v1",
		CorpusVersion:            "corpus-v1",
		SourceKind:               "legacy",
		State:                    domain.V2MigrationStateReadyCutover,
		Phase:                    "verifying",
		Required:                 true,
		PreflightApproved:        true,
		BackupReference:          "backup-20260721",
		PreflightChecks: map[string]any{
			"postgres_restore_verified": true,
		},
		Now: now,
	})
	require.NoError(t, err)

	marker, err := repo.CommitCutover(ctx, V2CommitCutoverInput{
		RunID:          run.RunID,
		FromState:      domain.V2MigrationStateReadyCutover,
		MarkerVersion:  "dense-mem.v2.1.cutover.v1",
		CorpusHash:     "sha256:corpus",
		GateReportHash: "sha256:gates",
		GateResults: []domain.V2MigrationGateResult{{
			GateName:     "backup_restore",
			Outcome:      domain.V2MigrationGateOutcomePass,
			EvidenceRef:  "local://backup",
			EvidenceHash: "sha256:backup",
			Message:      "backup restored",
			Metadata:     map[string]any{"version": "test-v1"},
		}},
		Metadata: map[string]any{"release": "v2.1.1"},
		OperatorAction: domain.V2MigrationOperatorAction{
			Action:   domain.V2MigrationActionCutoverCommitted,
			Actor:    "operator",
			RemoteIP: "192.0.2.10",
			Reason:   "final cutover",
			Metadata: map[string]any{"gate_report_hash": "sha256:gates"},
		},
		Now: now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationMarkerCompatible, marker.Status)
	require.Equal(t, "sha256:corpus", marker.CorpusHash)

	latestRun, err := repo.GetLatestRun(ctx)
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationStateCutOver, latestRun.State)
	require.NotNil(t, latestRun.CutoverAt)

	actions, err := repo.ListOperatorActions(ctx, run.RunID, 10)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	require.Equal(t, domain.V2MigrationActionCutoverCommitted, actions[0].Action)

	var gateCount int64
	require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM v2_migration_gate_results WHERE run_id = ?::uuid`, run.RunID).Scan(&gateCount).Error
	}))
	require.EqualValues(t, 1, gateCount)
}

func TestV2MigrationControlRepositoryBlocksCutoverWithPendingItems(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewV2MigrationControlRepository(appDB, rls)
	now := time.Date(2026, 7, 21, 16, 0, 0, 0, time.UTC)
	teamID := createV2LedgerTeam(t, adminDB, rls, "cutover-block-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "cutover-block-owner")

	run, err := repo.CreateRun(ctx, V2CreateMigrationRunInput{
		MigrationContractVersion: "migration-contract-v1",
		CorpusVersion:            "corpus-v1",
		SourceKind:               "legacy",
		State:                    domain.V2MigrationStateReadyCutover,
		Phase:                    "verifying",
		Required:                 true,
		PreflightApproved:        true,
		BackupReference:          "backup-20260721",
		Now:                      now,
	})
	require.NoError(t, err)
	require.NoError(t, insertV2MigrationCorpusItem(ctx, adminDB, rls, run.RunID, teamID, ownerID, "source-1", domain.V2MigrationOutcomePending, now.Add(time.Minute)))

	_, err = repo.CommitCutover(ctx, V2CommitCutoverInput{
		RunID:          run.RunID,
		FromState:      domain.V2MigrationStateReadyCutover,
		MarkerVersion:  "dense-mem.v2.1.cutover.v1",
		CorpusHash:     "sha256:corpus",
		GateReportHash: "sha256:gates",
		Now:            now.Add(2 * time.Minute),
	})
	require.ErrorIs(t, err, ErrV2MigrationCutoverBlocked)
}

func TestV2MigrationControlRepositoryBlocksCutoverWithNeedsReviewItems(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewV2MigrationControlRepository(appDB, rls)
	now := time.Date(2026, 7, 21, 16, 30, 0, 0, time.UTC)
	teamID := createV2LedgerTeam(t, adminDB, rls, "cutover-review-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "cutover-review-owner")

	run, err := repo.CreateRun(ctx, V2CreateMigrationRunInput{
		MigrationContractVersion: "migration-contract-v1",
		CorpusVersion:            "corpus-v1",
		SourceKind:               "legacy",
		State:                    domain.V2MigrationStateReadyCutover,
		Phase:                    "verifying",
		Required:                 true,
		PreflightApproved:        true,
		BackupReference:          "backup-20260721",
		Now:                      now,
	})
	require.NoError(t, err)
	require.NoError(t, insertV2MigrationCorpusItem(ctx, adminDB, rls, run.RunID, teamID, ownerID, "source-review", domain.V2MigrationOutcomeNeedsReview, now.Add(time.Minute)))

	_, err = repo.CommitCutover(ctx, V2CommitCutoverInput{
		RunID:          run.RunID,
		FromState:      domain.V2MigrationStateReadyCutover,
		MarkerVersion:  "dense-mem.v2.1.cutover.v1",
		CorpusHash:     "sha256:corpus",
		GateReportHash: "sha256:gates",
		Now:            now.Add(3 * time.Minute),
	})
	require.ErrorIs(t, err, ErrV2MigrationCutoverBlocked)
}

func TestV2MigrationControlRepositoryCommitsFreshV2AuthorityOnlyWhenApplicationDataEmpty(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewV2MigrationControlRepository(appDB, rls)
	now := time.Date(2026, 7, 21, 17, 0, 0, 0, time.UTC)

	marker, err := repo.CommitFreshV2Authority(ctx, V2CommitFreshV2AuthorityInput{
		MarkerVersion: "dense-mem.v2.1.cutover.v1",
		Metadata:      map[string]any{"source": "startup"},
		Now:           now,
	})
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationMarkerCompatible, marker.Status)
	require.Empty(t, marker.RunID)
	require.Equal(t, true, marker.Metadata["fresh_install"])

	var appConfigRows int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM app_config`).Scan(&appConfigRows).Error
	}))
	require.Positive(t, appConfigRows, "seeded app_config rows must not block fresh authority")
}

func TestV2MigrationControlRepositoryBlocksFreshV2AuthorityWhenApplicationDataExists(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewV2MigrationControlRepository(appDB, rls)
	createV2LedgerTeam(t, adminDB, rls, "fresh-block-team")

	_, err := repo.CommitFreshV2Authority(ctx, V2CommitFreshV2AuthorityInput{
		MarkerVersion: "dense-mem.v2.1.cutover.v1",
		Now:           time.Date(2026, 7, 21, 17, 30, 0, 0, time.UTC),
	})
	require.ErrorIs(t, err, ErrV2MigrationFreshInitBlocked)
	require.Contains(t, err.Error(), "teams")
}

func TestV2MigrationControlRepositoryBlocksFreshV2AuthorityWhenMigrationStateExists(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewV2MigrationControlRepository(appDB, rls)

	_, err := repo.CreateRun(ctx, V2CreateMigrationRunInput{
		MigrationContractVersion: "migration-contract-v1",
		CorpusVersion:            "corpus-v1",
		SourceKind:               "legacy",
		State:                    domain.V2MigrationStateReady,
		Phase:                    "preflight",
		Required:                 true,
		PreflightApproved:        true,
		BackupReference:          "backup-20260721",
		Now:                      time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	_, err = repo.CommitFreshV2Authority(ctx, V2CommitFreshV2AuthorityInput{
		MarkerVersion: "dense-mem.v2.1.cutover.v1",
		Now:           time.Date(2026, 7, 21, 18, 5, 0, 0, time.UTC),
	})
	require.ErrorIs(t, err, ErrV2MigrationFreshInitBlocked)
	require.Contains(t, err.Error(), "v2_migration_runs")

	var markerCount int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM v2_compatibility_markers`).Scan(&markerCount).Error
	}))
	require.Zero(t, markerCount)
}

func TestV2MigrationControlRepositoryBlocksFreshV2AuthorityWhenLegacyTablesExist(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewV2MigrationControlRepository(appDB, rls)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`CREATE TABLE profiles (id uuid PRIMARY KEY)`).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO profiles (id) VALUES (?::uuid)`, uuid.NewString()).Error
	}))

	_, err := repo.CommitFreshV2Authority(ctx, V2CommitFreshV2AuthorityInput{
		MarkerVersion: "dense-mem.v2.1.cutover.v1",
		Now:           time.Date(2026, 7, 21, 18, 30, 0, 0, time.UTC),
	})
	require.ErrorIs(t, err, ErrV2MigrationFreshInitBlocked)
	require.Contains(t, err.Error(), "profiles")
}

func insertV2MigrationCorpusItem(
	ctx context.Context,
	db *gorm.DB,
	rls *storagepostgres.RLS,
	runID string,
	teamID string,
	ownerID string,
	sourceID string,
	outcome string,
	now time.Time,
) error {
	return rls.WithMigrationTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO v2_migration_corpus_items (
			    item_id, run_id, team_id, owner_profile_id, source_kind, source_id,
			    source_hash, item_kind, outcome, metadata, created_at, updated_at
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid, 'legacy', ?, ?, ?, ?, '{}'::jsonb, ?, ?
			)
		`, uuid.NewString(), runID, teamID, ownerID, sourceID, "sha256:"+sourceID,
			domain.V2MigrationItemKindEvidence, outcome, now, now).Error
	})
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
