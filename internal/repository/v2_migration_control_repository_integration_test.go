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
	latest, err := repo.GetLatestRun(ctx)
	require.NoError(t, err)
	require.Nil(t, latest)
	marker, err := repo.GetLatestMarker(ctx)
	require.NoError(t, err)
	require.Nil(t, marker)

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

func TestV2MigrationExecutorRepositoryValidatesOwnerProfilePairs(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewV2MigrationControlRepository(appDB, rls)

	teamA := createV2LedgerTeam(t, adminDB, rls, "migration-owner-team-a")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamA, "migration-owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamA, "migration-owner-b")
	teamC := createV2LedgerTeam(t, adminDB, rls, "migration-owner-team-c")
	ownerC := createV2LedgerProfile(t, adminDB, rls, teamC, "migration-owner-c")

	for _, tc := range []struct {
		name           string
		teamID         string
		ownerProfileID string
		want           bool
	}{
		{name: "team A owner A", teamID: teamA, ownerProfileID: ownerA, want: true},
		{name: "team A owner B", teamID: teamA, ownerProfileID: ownerB, want: true},
		{name: "team A owner C", teamID: teamA, ownerProfileID: ownerC, want: false},
		{name: "team C owner A", teamID: teamC, ownerProfileID: ownerA, want: false},
		{name: "missing owner", teamID: teamA, ownerProfileID: uuid.NewString(), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.ValidateMigrationOwnerProfile(ctx, V2ValidateMigrationOwnerProfileInput{
				TeamID:         tc.teamID,
				OwnerProfileID: tc.ownerProfileID,
			})
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestV2MigrationExecutorRepositoryPersistsProgressAndStats(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "migration-executor-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "migration-executor-owner")
	otherOwnerID := createV2LedgerProfile(t, adminDB, rls, teamID, "migration-executor-other-owner")
	now := time.Date(2026, 7, 17, 13, 0, 0, 0, time.UTC)
	ledger := NewV2LedgerRepository(appDB, rls)
	ingest, err := ledger.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "migration-executor-ingest",
		RequestHash:    "migration-executor-request",
		Evidence: []V2EvidenceInput{{
			Content:    "Legacy Neo4j evidence is submitted through the V2 ledger.",
			SourceType: "document",
			Authority:  "primary",
		}},
	})
	require.NoError(t, err)
	require.Len(t, ingest.Items, 1)

	repo := NewV2MigrationControlRepository(appDB, rls)
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
	run, err = repo.UpdateRunState(ctx, V2UpdateMigrationRunStateInput{
		RunID:     run.RunID,
		FromState: domain.V2MigrationStateReady,
		ToState:   domain.V2MigrationStateRunning,
		Phase:     "migration",
		Retryable: true,
		Now:       now.Add(time.Minute),
	})
	require.NoError(t, err)

	item, err := repo.UpsertMigrationCorpusItem(ctx, V2UpsertMigrationCorpusItemInput{
		RunID:          run.RunID,
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "neo4j",
		SourceID:       "sf-1",
		SourceHash:     "sha256:legacy",
		ItemKind:       domain.V2MigrationItemKindEvidence,
		Outcome:        domain.V2MigrationOutcomePending,
		Metadata:       map[string]any{"legacy": true},
		Now:            now.Add(2 * time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationOutcomePending, item.Outcome)

	item, err = repo.UpdateMigrationCorpusOutcome(ctx, V2UpdateMigrationCorpusOutcomeInput{
		RunID:           run.RunID,
		SourceKind:      "neo4j",
		SourceID:        "sf-1",
		Outcome:         domain.V2MigrationOutcomeNeedsReview,
		IngestID:        ingest.IngestID,
		PlacementItemID: ingest.Items[0].PlacementItemID,
		Metadata:        map[string]any{"submitted": true},
		Now:             now.Add(3 * time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationOutcomeNeedsReview, item.Outcome)
	require.Equal(t, ingest.IngestID, item.IngestID)
	require.Equal(t, ingest.Items[0].PlacementItemID, item.PlacementItemID)

	item, err = repo.UpsertMigrationCorpusItem(ctx, V2UpsertMigrationCorpusItemInput{
		RunID:          run.RunID,
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "neo4j",
		SourceID:       "sf-1",
		SourceHash:     "sha256:legacy",
		ItemKind:       domain.V2MigrationItemKindEvidence,
		Outcome:        domain.V2MigrationOutcomePending,
		Metadata:       map[string]any{"retry_seen": true},
		Now:            now.Add(4 * time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationOutcomeNeedsReview, item.Outcome, "retry upsert must not reset terminal outcome")
	require.True(t, item.Metadata["retry_seen"].(bool))

	_, err = repo.UpsertMigrationCorpusItem(ctx, V2UpsertMigrationCorpusItemInput{
		RunID:          run.RunID,
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "neo4j",
		SourceID:       "sf-1",
		SourceHash:     "sha256:legacy-mutated",
		ItemKind:       domain.V2MigrationItemKindEvidence,
		Outcome:        domain.V2MigrationOutcomePending,
		Metadata:       map[string]any{"retry_seen": true},
		Now:            now.Add(4*time.Minute + time.Second),
	})
	require.ErrorIs(t, err, ErrV2MigrationCorpusSourceMetadataMismatch)

	_, err = repo.UpsertMigrationCorpusItem(ctx, V2UpsertMigrationCorpusItemInput{
		RunID:          run.RunID,
		TeamID:         teamID,
		OwnerProfileID: otherOwnerID,
		SourceKind:     "neo4j",
		SourceID:       "sf-1",
		SourceHash:     "sha256:legacy",
		ItemKind:       domain.V2MigrationItemKindEvidence,
		Outcome:        domain.V2MigrationOutcomePending,
		Metadata:       map[string]any{"retry_seen": true},
		Now:            now.Add(4*time.Minute + 2*time.Second),
	})
	require.ErrorIs(t, err, ErrV2MigrationCorpusSourceMetadataMismatch)

	_, err = repo.UpsertMigrationCorpusItem(ctx, V2UpsertMigrationCorpusItemInput{
		RunID:          run.RunID,
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "neo4j",
		SourceID:       "sf-2",
		SourceHash:     "sha256:excluded",
		ItemKind:       domain.V2MigrationItemKindEvidence,
		Outcome:        domain.V2MigrationOutcomePending,
		Now:            now.Add(5 * time.Minute),
	})
	require.NoError(t, err)
	_, err = repo.UpdateMigrationCorpusOutcome(ctx, V2UpdateMigrationCorpusOutcomeInput{
		RunID:           run.RunID,
		SourceKind:      "neo4j",
		SourceID:        "sf-2",
		Outcome:         domain.V2MigrationOutcomeExcluded,
		ExclusionReason: "missing source owner",
		Now:             now.Add(6 * time.Minute),
	})
	require.NoError(t, err)
	require.NoError(t, repo.RecordMigrationExclusion(ctx, V2RecordMigrationExclusionInput{
		RunID:         run.RunID,
		SourceKind:    "neo4j",
		SourceID:      "sf-2",
		Reason:        "missing source owner",
		BlocksCutover: true,
		Now:           now.Add(6 * time.Minute),
	}))
	require.NoError(t, repo.UpsertMigrationSourceMap(ctx, V2UpsertMigrationSourceMapInput{
		RunID:      run.RunID,
		SourceKind: "neo4j",
		SourceID:   "sf-1",
		TargetType: domain.V2MigrationTargetIngest,
		TargetID:   ingest.IngestID,
		Metadata:   map[string]any{"processing_state": ingest.Status},
		Now:        now.Add(7 * time.Minute),
	}))
	require.NoError(t, repo.UpsertMigrationCheckpoint(ctx, V2UpsertMigrationCheckpointInput{
		RunID:         run.RunID,
		CheckpointKey: domain.V2MigrationCheckpointLegacyNeo4jCursor,
		CheckpointValue: map[string]any{
			"after_source_id": "sf-2",
			"done":            false,
		},
		LeaseOwner: "worker-a",
		Now:        now.Add(8 * time.Minute),
	}))
	require.NoError(t, repo.RecordMigrationError(ctx, V2RecordMigrationErrorInput{
		RunID:      run.RunID,
		SourceKind: "neo4j",
		SourceID:   "sf-2",
		Phase:      "validate_legacy_item",
		ErrorCode:  "invalid_legacy_item",
		Message:    "missing source owner",
		Retryable:  false,
		Now:        now.Add(9 * time.Minute),
	}))

	checkpoint, err := repo.GetMigrationCheckpoint(ctx, run.RunID, domain.V2MigrationCheckpointLegacyNeo4jCursor)
	require.NoError(t, err)
	require.Equal(t, "sf-2", checkpoint["after_source_id"])

	stats, err := repo.RefreshMigrationRunStats(ctx, run.RunID, now.Add(10*time.Minute))
	require.NoError(t, err)
	require.Equal(t, 2, stats.TotalItems)
	require.Equal(t, 1, stats.CompletedItems)
	require.Equal(t, 0, stats.FailedItems)
	require.Equal(t, 1, stats.ExcludedItems)
	require.Equal(t, domain.V2MigrationCheckpointLegacyNeo4jCursor, stats.CheckpointKey)
	require.Equal(t, "sf-2", stats.CheckpointValue["after_source_id"])

	var appVisible int64
	require.NoError(t, appDB.Raw(`SELECT count(*) FROM v2_migration_corpus_items`).Scan(&appVisible).Error)
	require.Zero(t, appVisible)
	var migrationVisible int64
	require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM v2_migration_corpus_items`).Scan(&migrationVisible).Error
	}))
	require.EqualValues(t, 2, migrationVisible)
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
