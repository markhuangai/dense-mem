package repository

import (
	"context"
	"encoding/json"
	"errors"
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

func TestV2MigrationRepairQueryIndexMigrationContainsDDL(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "postgres", "2026072302_v2_migration_repair_query_indexes.sql")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	for _, indexName := range []string{
		"v2_migration_corpus_run_ingest_idx",
		"placement_items_migration_ingest_idx",
		"review_tasks_open_placement_item_idx",
	} {
		require.Truef(t, strings.Contains(string(data), indexName), "migration file missing %q", indexName)
	}
}

func TestV2MigrationRepairRequeuesExhaustedRetryableFailure(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewV2MigrationControlRepository(appDB, rls)
	fixture := seedV2MigrationExhaustedFailure(
		t,
		ctx,
		adminDB,
		appDB,
		rls,
		repo,
		[]string{
			"semantic_review: semantic review failed before completion",
			v2MigrationRetryExhaustedValidationError,
		},
	)

	summary, err := repo.AssessMigrationRepair(ctx, fixture.runID)
	require.NoError(t, err)
	require.True(t, summary.Required)
	require.Equal(t, 1, summary.RetryableFailures)
	require.Zero(t, summary.BlockedItems)
	require.Len(t, summary.FailureGroups, 1)
	require.Equal(t, domain.V2MigrationFailureGroup{Stage: "unknown", Class: "unknown", Count: 1}, summary.FailureGroups[0])

	resumed, err := repo.RepairAndResumeMigration(ctx, V2RepairAndResumeMigrationInput{
		RunID:     fixture.runID,
		FromState: domain.V2MigrationStatePaused,
		OperatorAction: domain.V2MigrationOperatorAction{
			RunID:    fixture.runID,
			Actor:    "operator",
			Reason:   "retry transient exhausted placement",
			RemoteIP: "127.0.0.1",
		},
		Now: fixture.now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationStateRunning, resumed.State)

	var runStatus, itemStatus, itemCategory, corpusOutcome, action string
	var attempts, repairOutcomes int
	var corpusPlacementItemID *string
	require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT status, attempts
			FROM placement_runs
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, fixture.teamID, fixture.placementRunID).Row().Scan(&runStatus, &attempts); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status, category
			FROM placement_items
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, fixture.teamID, fixture.placementItemID).Row().Scan(&itemStatus, &itemCategory); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT outcome, placement_item_id::text
			FROM v2_migration_corpus_items
			WHERE run_id = ?::uuid
			  AND source_id = ?
		`, fixture.runID, fixture.sourceID).Row().Scan(&corpusOutcome, &corpusPlacementItemID); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT action
			FROM v2_migration_operator_actions
			WHERE run_id = ?::uuid
			ORDER BY created_at DESC, action_id DESC
			LIMIT 1
		`, fixture.runID).Row().Scan(&action); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT count(*)::int
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
			  AND outcome_kind = 'migration_repair_requeued'
			  AND status = 'retryable'
		`, fixture.teamID, fixture.placementItemID).Scan(&repairOutcomes).Error
	}))
	require.Contains(t, []string{"queued", "guarded"}, runStatus)
	require.Zero(t, attempts)
	require.Equal(t, "queued", itemStatus)
	require.Equal(t, "pending", itemCategory)
	require.Equal(t, domain.V2MigrationOutcomePending, corpusOutcome)
	require.Nil(t, corpusPlacementItemID)
	require.Equal(t, domain.V2MigrationActionRepairResumed, action)
	require.Equal(t, 1, repairOutcomes)
}

func TestV2MigrationRepairBlocksResumeWhenCutoverExclusionsRemain(t *testing.T) {
	_, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewV2MigrationControlRepository(appDB, rls)
	now := time.Date(2026, 7, 23, 5, 0, 0, 0, time.UTC)
	run, err := repo.CreateRun(ctx, V2CreateMigrationRunInput{
		MigrationContractVersion: "migration-contract-v1",
		CorpusVersion:            "corpus-v1",
		SourceKind:               "neo4j",
		State:                    domain.V2MigrationStatePaused,
		Phase:                    "paused",
		Required:                 true,
		PreflightApproved:        true,
		Now:                      now,
	})
	require.NoError(t, err)
	require.NoError(t, repo.RecordMigrationExclusion(ctx, V2RecordMigrationExclusionInput{
		RunID:         run.RunID,
		SourceKind:    "neo4j",
		SourceID:      "legacy-bad-owner",
		Reason:        "legacy owner profile does not belong to team",
		BlocksCutover: true,
		Metadata: map[string]any{
			"reason_class":   "owner_mismatch",
			"exclusion_code": domain.V2MigrationExclusionAmbiguousOwnerProfile,
		},
		Now: now.Add(time.Minute),
	}))

	summary, err := repo.AssessMigrationRepair(ctx, run.RunID)
	require.NoError(t, err)
	require.False(t, summary.Required)
	require.Equal(t, 1, summary.BlockingExclusions)
	require.Zero(t, summary.RepairableExclusions)
	require.Equal(t, 1, summary.HardBlockingExclusions)
	require.Zero(t, summary.RetryableFailures)

	_, err = repo.RepairAndResumeMigration(ctx, V2RepairAndResumeMigrationInput{
		RunID:     run.RunID,
		FromState: domain.V2MigrationStatePaused,
		OperatorAction: domain.V2MigrationOperatorAction{
			RunID: run.RunID,
			Actor: "operator",
		},
		Now: now.Add(2 * time.Minute),
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrV2MigrationCutoverBlocked), err)
	require.Contains(t, err.Error(), "blocking_exclusions=1")
	require.Contains(t, err.Error(), "hard_blocking_exclusions=1")
}

func TestV2MigrationRepairRewindsLegacyMissingOwnerExclusions(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewV2MigrationControlRepository(appDB, rls)
	now := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	run, err := repo.CreateRun(ctx, V2CreateMigrationRunInput{
		MigrationContractVersion: "migration-contract-v1",
		CorpusVersion:            "corpus-v1",
		SourceKind:               "neo4j",
		State:                    domain.V2MigrationStatePaused,
		Phase:                    "paused",
		Required:                 true,
		PreflightApproved:        true,
		Now:                      now,
	})
	require.NoError(t, err)
	require.NoError(t, repo.UpsertMigrationCheckpoint(ctx, V2UpsertMigrationCheckpointInput{
		RunID:         run.RunID,
		CheckpointKey: domain.V2MigrationCheckpointLegacyNeo4jCursor,
		CheckpointValue: map[string]any{
			"after_source_id": "legacy-last-source",
			"done":            true,
		},
		LeaseOwner: "old-worker",
		Now:        now,
	}))
	require.NoError(t, repo.RecordMigrationExclusion(ctx, V2RecordMigrationExclusionInput{
		RunID:         run.RunID,
		SourceKind:    "neo4j",
		SourceID:      "legacy-ownerless",
		Reason:        v2MigrationMissingOwnerReasonPrefix,
		BlocksCutover: true,
		Metadata:      map[string]any{"legacy_source_id": "legacy-ownerless"},
		Now:           now.Add(time.Minute),
	}))

	summary, err := repo.AssessMigrationRepair(ctx, run.RunID)
	require.NoError(t, err)
	require.True(t, summary.Required)
	require.Equal(t, 1, summary.BlockingExclusions)
	require.Equal(t, 1, summary.RepairableExclusions)
	require.Zero(t, summary.HardBlockingExclusions)

	resumedAt := now.Add(2 * time.Minute)
	resumed, err := repo.RepairAndResumeMigration(ctx, V2RepairAndResumeMigrationInput{
		RunID:     run.RunID,
		FromState: domain.V2MigrationStatePaused,
		OperatorAction: domain.V2MigrationOperatorAction{
			RunID: run.RunID,
			Actor: "operator",
		},
		Now: resumedAt,
	})
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationStateRunning, resumed.State)

	var checkpointDone, checkpointCursor, checkpointReason, leaseOwner string
	require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT checkpoint_value->>'done',
			       checkpoint_value->>'after_source_id',
			       checkpoint_value->>'repair_reason',
			       lease_owner
			FROM v2_migration_checkpoints
			WHERE run_id = ?::uuid
			  AND checkpoint_key = ?
		`, run.RunID, domain.V2MigrationCheckpointLegacyNeo4jCursor).Row().Scan(
			&checkpointDone,
			&checkpointCursor,
			&checkpointReason,
			&leaseOwner,
		)
	}))
	require.Equal(t, "false", checkpointDone)
	require.Empty(t, checkpointCursor)
	require.Equal(t, domain.V2MigrationExclusionMissingOwnerProfile, checkpointReason)
	require.Empty(t, leaseOwner)

	ownerID := uuid.NewString()
	require.NoError(t, repo.ResolveMigrationExclusion(ctx, V2ResolveMigrationExclusionInput{
		RunID:          run.RunID,
		SourceKind:     "neo4j",
		SourceID:       "legacy-ownerless",
		OwnerProfileID: ownerID,
		Resolution:     domain.V2MigrationOwnerResolutionUniqueTeamOwner,
		Now:            resumedAt.Add(time.Minute),
	}))

	var blocksCutover bool
	var reason, resolution, resolvedOwner string
	require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT blocks_cutover, reason,
			       metadata->>'owner_resolution',
			       metadata->>'resolved_owner_profile_id'
			FROM v2_migration_exclusions
			WHERE run_id = ?::uuid
			  AND source_id = 'legacy-ownerless'
		`, run.RunID).Row().Scan(&blocksCutover, &reason, &resolution, &resolvedOwner)
	}))
	require.False(t, blocksCutover)
	require.Equal(t, v2MigrationMissingOwnerReasonPrefix, reason)
	require.Equal(t, domain.V2MigrationOwnerResolutionUniqueTeamOwner, resolution)
	require.Equal(t, ownerID, resolvedOwner)
}

func TestV2MigrationRepairKeepsDeterministicExhaustedFailureBlocked(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewV2MigrationControlRepository(appDB, rls)
	fixture := seedV2MigrationExhaustedFailure(
		t,
		ctx,
		adminDB,
		appDB,
		rls,
		repo,
		[]string{"proposal: relationship proposal is invalid"},
	)
	taskID := seedV2MigrationPredicateReviewTask(t, ctx, adminDB, rls, fixture, domain.V2PredicatePolicyVersion)

	summary, err := repo.AssessMigrationRepair(ctx, fixture.runID)
	require.NoError(t, err)
	require.False(t, summary.Required)
	require.Zero(t, summary.LegacyPredicateReviews)
	require.Equal(t, 1, summary.HeldReviews)
	require.Zero(t, summary.RetryableFailures)
	require.Equal(t, 1, summary.BlockedItems)

	_, err = repo.RepairAndResumeMigration(ctx, V2RepairAndResumeMigrationInput{
		RunID:     fixture.runID,
		FromState: domain.V2MigrationStatePaused,
		OperatorAction: domain.V2MigrationOperatorAction{
			RunID: fixture.runID,
			Actor: "operator",
		},
		Now: fixture.now.Add(time.Minute),
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrV2MigrationCutoverBlocked), err)

	var state, runStatus, taskStatus string
	var attempts, actionCount int
	require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT state
			FROM v2_migration_runs
			WHERE run_id = ?::uuid
		`, fixture.runID).Row().Scan(&state); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status, attempts
			FROM placement_runs
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, fixture.teamID, fixture.placementRunID).Row().Scan(&runStatus, &attempts); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status
			FROM review_tasks
			WHERE team_id = ?::uuid
			  AND review_task_id = ?::uuid
		`, fixture.teamID, taskID).Row().Scan(&taskStatus); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT count(*)::int
			FROM v2_migration_operator_actions
			WHERE run_id = ?::uuid
		`, fixture.runID).Scan(&actionCount).Error
	}))
	require.Equal(t, domain.V2MigrationStatePaused, state)
	require.Equal(t, "failed", runStatus)
	require.Equal(t, 5, attempts)
	require.Equal(t, "open", taskStatus)
	require.Zero(t, actionCount)
}

func TestV2MigrationRepairReplaysLegacyPredicateReviews(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewV2MigrationControlRepository(appDB, rls)
	fixture := seedV2MigrationExhaustedFailure(
		t,
		ctx,
		adminDB,
		appDB,
		rls,
		repo,
		[]string{"proposal: relationship proposal is invalid"},
	)
	taskID := seedV2MigrationPredicateReviewTask(t, ctx, adminDB, rls, fixture, "")
	secondTaskID := seedV2MigrationPredicateReviewTask(t, ctx, adminDB, rls, fixture, "")

	summary, err := repo.AssessMigrationRepair(ctx, fixture.runID)
	require.NoError(t, err)
	require.True(t, summary.Required)
	require.Equal(t, 2, summary.LegacyPredicateReviews)
	require.Zero(t, summary.HeldReviews)
	require.Zero(t, summary.BlockedItems)

	resumed, err := repo.RepairAndResumeMigration(ctx, V2RepairAndResumeMigrationInput{
		RunID:     fixture.runID,
		FromState: domain.V2MigrationStatePaused,
		OperatorAction: domain.V2MigrationOperatorAction{
			RunID: fixture.runID,
			Actor: "operator",
		},
		Now: fixture.now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationStateRunning, resumed.State)

	var taskStatus, secondTaskStatus, runStatus, itemStatus, corpusOutcome string
	var attempts int
	var corpusPlacementItemID *string
	require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT status, attempts
			FROM placement_runs
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, fixture.teamID, fixture.placementRunID).Row().Scan(&runStatus, &attempts); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status
			FROM placement_items
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, fixture.teamID, fixture.placementItemID).Row().Scan(&itemStatus); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status
			FROM review_tasks
			WHERE team_id = ?::uuid
			  AND review_task_id = ?::uuid
		`, fixture.teamID, taskID).Row().Scan(&taskStatus); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status
			FROM review_tasks
			WHERE team_id = ?::uuid
			  AND review_task_id = ?::uuid
		`, fixture.teamID, secondTaskID).Row().Scan(&secondTaskStatus); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT outcome, placement_item_id::text
			FROM v2_migration_corpus_items
			WHERE run_id = ?::uuid
			  AND source_id = ?
		`, fixture.runID, fixture.sourceID).Row().Scan(&corpusOutcome, &corpusPlacementItemID)
	}))
	require.Contains(t, []string{"queued", "guarded"}, runStatus)
	require.Zero(t, attempts)
	require.Equal(t, "queued", itemStatus)
	require.Equal(t, "canceled", taskStatus)
	require.Equal(t, "canceled", secondTaskStatus)
	require.Equal(t, domain.V2MigrationOutcomePending, corpusOutcome)
	require.Nil(t, corpusPlacementItemID)
}

func TestV2MigrationRepairPreservesCurrentReviewAlongsideLegacyPredicateReview(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewV2MigrationControlRepository(appDB, rls)
	fixture := seedV2MigrationExhaustedFailure(
		t,
		ctx,
		adminDB,
		appDB,
		rls,
		repo,
		[]string{"proposal: relationship proposal is invalid"},
	)
	legacyTaskID := seedV2MigrationPredicateReviewTask(t, ctx, adminDB, rls, fixture, "")
	currentTaskID := seedV2MigrationPredicateReviewTask(
		t,
		ctx,
		adminDB,
		rls,
		fixture,
		domain.V2PredicatePolicyVersion,
	)

	summary, err := repo.AssessMigrationRepair(ctx, fixture.runID)
	require.NoError(t, err)
	require.Equal(t, 1, summary.LegacyPredicateReviews)
	require.Equal(t, 1, summary.HeldReviews)
	require.Zero(t, summary.BlockedItems)

	resumed, err := repo.RepairAndResumeMigration(ctx, V2RepairAndResumeMigrationInput{
		RunID:     fixture.runID,
		FromState: domain.V2MigrationStatePaused,
		OperatorAction: domain.V2MigrationOperatorAction{
			RunID: fixture.runID,
			Actor: "operator",
		},
		Now: fixture.now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationStateRunning, resumed.State)

	var legacyStatus, currentStatus, itemStatus, corpusOutcome string
	require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT status
			FROM review_tasks
			WHERE team_id = ?::uuid
			  AND review_task_id = ?::uuid
		`, fixture.teamID, legacyTaskID).Row().Scan(&legacyStatus); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status
			FROM review_tasks
			WHERE team_id = ?::uuid
			  AND review_task_id = ?::uuid
		`, fixture.teamID, currentTaskID).Row().Scan(&currentStatus); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status
			FROM placement_items
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, fixture.teamID, fixture.placementItemID).Row().Scan(&itemStatus); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT outcome
			FROM v2_migration_corpus_items
			WHERE run_id = ?::uuid
			  AND source_id = ?
		`, fixture.runID, fixture.sourceID).Row().Scan(&corpusOutcome)
	}))
	require.Equal(t, "canceled", legacyStatus)
	require.Equal(t, "open", currentStatus)
	require.Equal(t, "awaiting_review", itemStatus)
	require.Equal(t, domain.V2MigrationOutcomeNeedsReview, corpusOutcome)
}

type v2MigrationExhaustedFailureFixture struct {
	runID           string
	teamID          string
	ownerID         string
	ingestID        string
	placementRunID  string
	placementItemID string
	sourceID        string
	now             time.Time
}

func seedV2MigrationExhaustedFailure(
	t *testing.T,
	ctx context.Context,
	adminDB *gorm.DB,
	appDB *gorm.DB,
	rls *storagepostgres.RLS,
	repo *V2MigrationControlRepositoryImpl,
	validationErrors []string,
) v2MigrationExhaustedFailureFixture {
	t.Helper()

	now := time.Date(2026, 7, 23, 4, 0, 0, 0, time.UTC)
	teamID := createV2LedgerTeam(t, adminDB, rls, "migration-repair-team-"+uuid.NewString())
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "migration-repair-owner")
	run, err := repo.CreateRun(ctx, V2CreateMigrationRunInput{
		MigrationContractVersion: "migration-contract-v1",
		CorpusVersion:            "corpus-v1",
		SourceKind:               "neo4j",
		State:                    domain.V2MigrationStatePaused,
		Phase:                    "paused",
		Required:                 true,
		PreflightApproved:        true,
		Now:                      now,
	})
	require.NoError(t, err)

	ledger := NewV2LedgerRepository(appDB, rls)
	ingest, err := ledger.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "migration-repair-" + uuid.NewString(),
		RequestHash:    "sha256:migration-repair",
		MigrationRunID: run.RunID,
		Evidence: []V2EvidenceInput{{
			Content:    "Legacy evidence whose transient verifier calls exhausted placement attempts.",
			SourceType: "conversation",
			Authority:  "primary",
		}},
	})
	require.NoError(t, err)
	require.Len(t, ingest.Items, 1)

	sourceID := "legacy-" + uuid.NewString()
	_, err = repo.UpsertMigrationCorpusItem(ctx, V2UpsertMigrationCorpusItemInput{
		RunID:          run.RunID,
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "neo4j",
		SourceID:       sourceID,
		SourceHash:     "sha256:legacy-repair",
		ItemKind:       domain.V2MigrationItemKindEvidence,
		Outcome:        domain.V2MigrationOutcomePending,
		Now:            now,
	})
	require.NoError(t, err)
	_, err = repo.UpdateMigrationCorpusOutcome(ctx, V2UpdateMigrationCorpusOutcomeInput{
		RunID:           run.RunID,
		SourceKind:      "neo4j",
		SourceID:        sourceID,
		Outcome:         domain.V2MigrationOutcomeFailed,
		IngestID:        ingest.IngestID,
		PlacementItemID: ingest.Items[0].PlacementItemID,
		Now:             now,
	})
	require.NoError(t, err)

	payload, err := json.Marshal(map[string]any{
		"contract_version":  domain.V2ContractVersion,
		"status":            string(domain.V2SemanticReviewTerminalFailure),
		"validation_errors": validationErrors,
	})
	require.NoError(t, err)
	require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE placement_runs
			SET status = 'failed',
			    attempts = max_attempts,
			    error = 'semantic review exhausted placement attempts',
			    completed_at = ?,
			    updated_at = ?
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, now, now, teamID, ingest.PlacementRunID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE placement_items
			SET status = 'failed',
			    category = 'failed',
			    result = ?::jsonb,
			    error = 'semantic review exhausted placement attempts',
			    updated_at = ?
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, string(payload), now, teamID, ingest.Items[0].PlacementItemID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO placement_outcomes (
			    outcome_id, team_id, placement_run_id, placement_item_id, owner_profile_id,
			    outcome_kind, status, idempotency_key, payload, created_at
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    'semantic_review_terminal', 'terminal_failure', '', ?::jsonb, ?
			)
		`, uuid.NewString(), teamID, ingest.PlacementRunID, ingest.Items[0].PlacementItemID,
			ownerID, string(payload), now).Error
	}))

	return v2MigrationExhaustedFailureFixture{
		runID:           run.RunID,
		teamID:          teamID,
		ownerID:         ownerID,
		ingestID:        ingest.IngestID,
		placementRunID:  ingest.PlacementRunID,
		placementItemID: ingest.Items[0].PlacementItemID,
		sourceID:        sourceID,
		now:             now,
	}
}

func seedV2MigrationPredicateReviewTask(
	t *testing.T,
	ctx context.Context,
	adminDB *gorm.DB,
	rls *storagepostgres.RLS,
	fixture v2MigrationExhaustedFailureFixture,
	policyVersion string,
) string {
	t.Helper()
	taskID := uuid.NewString()
	observationID := uuid.NewString()
	payload, err := json.Marshal(map[string]any{"predicate_policy_version": policyVersion})
	require.NoError(t, err)
	require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO relationship_observations (
			    team_id, observation_id, ingest_id, placement_item_id, owner_profile_id,
			    subject_ref, original_predicate, object_ref, polarity, evidence, metadata
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    'subject', 'legacy predicate', 'object', '+', '[]'::jsonb, '{}'::jsonb
			)
		`, fixture.teamID, observationID, fixture.ingestID, fixture.placementItemID, fixture.ownerID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO review_tasks (
			    team_id, review_task_id, owner_profile_id, ingest_id, placement_item_id,
			    observation_id, task_type, status, reason, payload
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    ?::uuid, 'predicate_needs_review', 'open', 'predicate_needs_review', ?::jsonb
			)
		`, fixture.teamID, taskID, fixture.ownerID, fixture.ingestID, fixture.placementItemID,
			observationID, string(payload)).Error
	}))
	return taskID
}
