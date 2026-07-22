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
		SourceKind:               "neo4j",
		State:                    domain.V2MigrationStateReady,
		Phase:                    "preflight",
		Required:                 true,
		PreflightApproved:        true,
		BackupReference:          "backup-20260717",
		PreflightChecks: map[string]any{
			"operator_backup_confirmation": true,
			"postgres_backup_confirmed":    true,
			"neo4j_backup_confirmed":       true,
			"confirmation_scope":           "operator",
			"backup_verification":          "not_performed",
		},
		Now: now,
	})
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationStateReady, run.State)
	require.True(t, run.PreflightApproved)

	started, err := repo.UpdateRunState(ctx, V2UpdateMigrationRunStateInput{
		RunID:                    run.RunID,
		FromState:                domain.V2MigrationStateReady,
		ToState:                  domain.V2MigrationStateRunning,
		Phase:                    "migration",
		MigrationContractVersion: "migration-contract-v2",
		CorpusVersion:            "corpus-v2",
		ClearBackupReference:     true,
		Retryable:                true,
		Now:                      now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationStateRunning, started.State)
	require.Equal(t, "migration-contract-v2", started.MigrationContractVersion)
	require.Equal(t, "corpus-v2", started.CorpusVersion)
	require.Empty(t, started.BackupReference)
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
		SourceKind:               "neo4j",
		State:                    domain.V2MigrationStateReadyCutover,
		Phase:                    "verifying",
		Required:                 true,
		PreflightApproved:        true,
		BackupReference:          "backup-20260721",
		PreflightChecks: map[string]any{
			"operator_backup_confirmation": true,
			"postgres_backup_confirmed":    true,
			"neo4j_backup_confirmed":       true,
			"confirmation_scope":           "operator",
			"backup_verification":          "not_performed",
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
			GateName:     "operator_backup_confirmation",
			Outcome:      domain.V2MigrationGateOutcomePass,
			EvidenceRef:  "local://backup",
			EvidenceHash: "sha256:backup",
			Message:      "operator confirmed PostgreSQL and Neo4j backups",
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
		SourceKind:               "neo4j",
		State:                    domain.V2MigrationStateReadyCutover,
		Phase:                    "verifying",
		Required:                 true,
		PreflightApproved:        true,
		BackupReference:          "backup-20260721",
		Now:                      now,
	})
	require.NoError(t, err)
	_, err = repo.UpsertMigrationCorpusItem(ctx, V2UpsertMigrationCorpusItemInput{
		RunID:          run.RunID,
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "neo4j",
		SourceID:       "source-1",
		SourceHash:     "sha256:source",
		ItemKind:       domain.V2MigrationItemKindEvidence,
		Outcome:        domain.V2MigrationOutcomePending,
		Now:            now.Add(time.Minute),
	})
	require.NoError(t, err)

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
		SourceKind:               "neo4j",
		State:                    domain.V2MigrationStateReadyCutover,
		Phase:                    "verifying",
		Required:                 true,
		PreflightApproved:        true,
		BackupReference:          "backup-20260721",
		Now:                      now,
	})
	require.NoError(t, err)
	_, err = repo.UpsertMigrationCorpusItem(ctx, V2UpsertMigrationCorpusItemInput{
		RunID:          run.RunID,
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "neo4j",
		SourceID:       "source-review",
		SourceHash:     "sha256:source-review",
		ItemKind:       domain.V2MigrationItemKindEvidence,
		Outcome:        domain.V2MigrationOutcomePending,
		Now:            now.Add(time.Minute),
	})
	require.NoError(t, err)
	_, err = repo.UpdateMigrationCorpusOutcome(ctx, V2UpdateMigrationCorpusOutcomeInput{
		RunID:      run.RunID,
		SourceKind: "neo4j",
		SourceID:   "source-review",
		Outcome:    domain.V2MigrationOutcomeNeedsReview,
		Now:        now.Add(2 * time.Minute),
	})
	require.NoError(t, err)

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
		SourceKind:               "neo4j",
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
			"operator_backup_confirmation": true,
			"postgres_backup_confirmed":    true,
			"neo4j_backup_confirmed":       true,
			"confirmation_scope":           "operator",
			"backup_verification":          "not_performed",
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
	require.Equal(t, domain.V2MigrationOutcomeNeedsReview, item.Outcome, "retry upsert must not reset staged outcome")
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
	require.Equal(t, 0, stats.CompletedItems)
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

func TestV2MigrationExecutorRepositoryFinalizesReadyToCutoverWhenPlacementsTerminal(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "migration-finalize-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "migration-finalize-owner")
	now := time.Date(2026, 7, 21, 19, 0, 0, 0, time.UTC)
	ledger := NewV2LedgerRepository(appDB, rls)
	ingest, err := ledger.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "migration-finalize-ingest",
		RequestHash:    "migration-finalize-request",
		Evidence: []V2EvidenceInput{{
			Content:    "Legacy evidence that completed placement.",
			SourceType: "document",
			Authority:  "primary",
		}},
	})
	require.NoError(t, err)
	require.Len(t, ingest.Items, 1)
	reviewIngest, err := ledger.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "migration-finalize-review-ingest",
		RequestHash:    "migration-finalize-review-request",
		Evidence: []V2EvidenceInput{{
			Content:    "Legacy evidence that requires normal placement review.",
			SourceType: "document",
			Authority:  "primary",
		}},
	})
	require.NoError(t, err)
	require.Len(t, reviewIngest.Items, 1)

	repo := NewV2MigrationControlRepository(appDB, rls)
	run, err := repo.CreateRun(ctx, V2CreateMigrationRunInput{
		MigrationContractVersion: "migration-contract-v1",
		CorpusVersion:            "corpus-v1",
		SourceKind:               "neo4j",
		State:                    domain.V2MigrationStateRunning,
		Phase:                    "migration",
		Required:                 true,
		PreflightApproved:        true,
		BackupReference:          "backup-20260721",
		Now:                      now,
	})
	require.NoError(t, err)
	_, err = repo.UpsertMigrationCorpusItem(ctx, V2UpsertMigrationCorpusItemInput{
		RunID:          run.RunID,
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "neo4j",
		SourceID:       "sf-terminal",
		SourceHash:     "sha256:terminal",
		ItemKind:       domain.V2MigrationItemKindEvidence,
		Outcome:        domain.V2MigrationOutcomePending,
		Now:            now.Add(time.Minute),
	})
	require.NoError(t, err)
	_, err = repo.UpdateMigrationCorpusOutcome(ctx, V2UpdateMigrationCorpusOutcomeInput{
		RunID:      run.RunID,
		SourceKind: "neo4j",
		SourceID:   "sf-terminal",
		Outcome:    domain.V2MigrationOutcomePending,
		IngestID:   ingest.IngestID,
		Now:        now.Add(2 * time.Minute),
	})
	require.NoError(t, err)
	_, err = repo.UpsertMigrationCorpusItem(ctx, V2UpsertMigrationCorpusItemInput{
		RunID:          run.RunID,
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "neo4j",
		SourceID:       "sf-review",
		SourceHash:     "sha256:review",
		ItemKind:       domain.V2MigrationItemKindEvidence,
		Outcome:        domain.V2MigrationOutcomePending,
		Now:            now.Add(2*time.Minute + 10*time.Second),
	})
	require.NoError(t, err)
	_, err = repo.UpdateMigrationCorpusOutcome(ctx, V2UpdateMigrationCorpusOutcomeInput{
		RunID:      run.RunID,
		SourceKind: "neo4j",
		SourceID:   "sf-review",
		Outcome:    domain.V2MigrationOutcomePending,
		IngestID:   reviewIngest.IngestID,
		Now:        now.Add(2*time.Minute + 20*time.Second),
	})
	require.NoError(t, err)
	require.NoError(t, repo.UpsertMigrationCheckpoint(ctx, V2UpsertMigrationCheckpointInput{
		RunID:         run.RunID,
		CheckpointKey: domain.V2MigrationCheckpointLegacyNeo4jCursor,
		CheckpointValue: map[string]any{
			"after_source_id": "",
			"done":            true,
		},
		LeaseOwner: "worker-final",
		Now:        now.Add(3 * time.Minute),
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE placement_items
			SET status = 'completed',
			    category = 'fact',
			    result = result || '{"relationship_outcomes":[]}'::jsonb,
			    updated_at = ?
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, now.Add(4*time.Minute), teamID, ingest.Items[0].PlacementItemID).Error
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE placement_items
			SET status = 'completed',
			    category = 'candidate',
			    result = result || '{"status":"review_required","review_task":"identity_needs_review"}'::jsonb,
			    updated_at = ?
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, now.Add(4*time.Minute+10*time.Second), teamID, reviewIngest.Items[0].PlacementItemID).Error
	}))

	finalized, err := repo.FinalizeMigrationRun(ctx, run.RunID, now.Add(5*time.Minute))
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationStateReadyCutover, finalized.State)
	require.Equal(t, 2, finalized.CompletedItems)
	require.Equal(t, 0, finalized.FailedItems)
	require.Equal(t, 0, finalized.ExcludedItems)
	require.NotNil(t, finalized.CompletedAt)
	require.True(t, strings.HasPrefix(finalized.CorpusHash, "sha256:"))

	var expectedCorpusHash string
	require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		var hashErr error
		expectedCorpusHash, hashErr = v2MigrationCorpusHashTx(tx, run.RunID)
		return hashErr
	}))
	require.Equal(t, expectedCorpusHash, finalized.CorpusHash)

	var outcome, placementItemID string
	require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT outcome, COALESCE(placement_item_id::text, '')
			FROM v2_migration_corpus_items
			WHERE run_id = ?::uuid
			  AND source_id = 'sf-terminal'
		`, run.RunID).Row().Scan(&outcome, &placementItemID)
	}))
	require.Equal(t, domain.V2MigrationOutcomeAccepted, outcome)
	require.Equal(t, ingest.Items[0].PlacementItemID, placementItemID)

	var reviewOutcome, reviewPlacementItemID string
	require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT outcome, COALESCE(placement_item_id::text, '')
			FROM v2_migration_corpus_items
			WHERE run_id = ?::uuid
			  AND source_id = 'sf-review'
		`, run.RunID).Row().Scan(&reviewOutcome, &reviewPlacementItemID)
	}))
	require.Equal(t, domain.V2MigrationOutcomeNeedsReview, reviewOutcome)
	require.Equal(t, reviewIngest.Items[0].PlacementItemID, reviewPlacementItemID)

	var sourceMapCount int64
	require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*)
			FROM v2_migration_source_maps
			WHERE run_id = ?::uuid
			  AND target_type = ?
			  AND target_id = ?
		`, run.RunID, domain.V2MigrationTargetPlacementItem, ingest.Items[0].PlacementItemID).Scan(&sourceMapCount).Error
	}))
	require.EqualValues(t, 1, sourceMapCount)
}

func TestV2MigrationExecutorRepositoryFinalizeKeepsRunRunningUntilPlacementsTerminal(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "migration-pending-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "migration-pending-owner")
	now := time.Date(2026, 7, 21, 20, 0, 0, 0, time.UTC)
	ledger := NewV2LedgerRepository(appDB, rls)
	ingest, err := ledger.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "migration-pending-ingest",
		RequestHash:    "migration-pending-request",
		Evidence: []V2EvidenceInput{{
			Content:    "Legacy evidence that remains queued.",
			SourceType: "document",
			Authority:  "primary",
		}},
	})
	require.NoError(t, err)

	repo := NewV2MigrationControlRepository(appDB, rls)
	run, err := repo.CreateRun(ctx, V2CreateMigrationRunInput{
		MigrationContractVersion: "migration-contract-v1",
		CorpusVersion:            "corpus-v1",
		SourceKind:               "neo4j",
		State:                    domain.V2MigrationStateRunning,
		Phase:                    "migration",
		Required:                 true,
		PreflightApproved:        true,
		BackupReference:          "backup-20260721",
		Now:                      now,
	})
	require.NoError(t, err)
	_, err = repo.UpsertMigrationCorpusItem(ctx, V2UpsertMigrationCorpusItemInput{
		RunID:          run.RunID,
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "neo4j",
		SourceID:       "sf-pending",
		SourceHash:     "sha256:pending",
		ItemKind:       domain.V2MigrationItemKindEvidence,
		Outcome:        domain.V2MigrationOutcomePending,
		Now:            now.Add(time.Minute),
	})
	require.NoError(t, err)
	_, err = repo.UpdateMigrationCorpusOutcome(ctx, V2UpdateMigrationCorpusOutcomeInput{
		RunID:      run.RunID,
		SourceKind: "neo4j",
		SourceID:   "sf-pending",
		Outcome:    domain.V2MigrationOutcomePending,
		IngestID:   ingest.IngestID,
		Now:        now.Add(2 * time.Minute),
	})
	require.NoError(t, err)
	require.NoError(t, repo.UpsertMigrationCheckpoint(ctx, V2UpsertMigrationCheckpointInput{
		RunID:         run.RunID,
		CheckpointKey: domain.V2MigrationCheckpointLegacyNeo4jCursor,
		CheckpointValue: map[string]any{
			"done": true,
		},
		LeaseOwner: "worker-pending",
		Now:        now.Add(3 * time.Minute),
	}))

	finalized, err := repo.FinalizeMigrationRun(ctx, run.RunID, now.Add(4*time.Minute))
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationStateRunning, finalized.State)
	require.Equal(t, 1, finalized.TotalItems)
	require.Equal(t, 0, finalized.CompletedItems)
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
