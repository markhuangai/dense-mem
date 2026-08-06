//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubmissionAssessmentMigrationReconcilesLegacyRuns(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026080501)
	teamID, profileID := insertMigrationTeamProfile(t, ctx, sqlDB)
	now := time.Now().UTC()

	clean := submissionAssessmentMigrationRun{
		ingestID:   uuid.NewString(),
		runID:      uuid.NewString(),
		status:     "processing",
		attempts:   3,
		workerID:   "legacy-worker",
		leaseUntil: timePointer(now.Add(time.Hour)),
		items: []submissionAssessmentMigrationItem{{
			itemID: uuid.NewString(), fragmentID: uuid.NewString(), status: "processing", category: "pending", result: `{}`,
		}},
	}
	held := submissionAssessmentMigrationRun{
		ingestID:    uuid.NewString(),
		runID:       uuid.NewString(),
		status:      "awaiting_review",
		completedAt: timePointer(now.Add(-time.Hour)),
		items: []submissionAssessmentMigrationItem{
			{itemID: uuid.NewString(), fragmentID: uuid.NewString(), status: "awaiting_review", category: "candidate", result: `{"kept":"value"}`},
			{itemID: uuid.NewString(), fragmentID: uuid.NewString(), status: "completed", category: "validated_claim", result: `{"accepted":true}`},
		},
	}
	partial := submissionAssessmentMigrationRun{
		ingestID: uuid.NewString(),
		runID:    uuid.NewString(),
		status:   "guarded",
		attempts: 2,
		items: []submissionAssessmentMigrationItem{
			{itemID: uuid.NewString(), fragmentID: uuid.NewString(), status: "completed", category: "validated_claim", result: `{"accepted":true}`},
			{itemID: uuid.NewString(), fragmentID: uuid.NewString(), status: "queued", category: "pending", result: `{}`},
		},
	}
	reviewTaskID := uuid.NewString()
	acknowledgedTaskID := uuid.NewString()
	unrelatedTaskID := uuid.NewString()
	assessmentID := uuid.NewString()
	entityID := uuid.NewString()

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_team_refs (team_id) VALUES ($1::uuid)
		`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_profile_refs (team_id, profile_id) VALUES ($1::uuid, $2::uuid)
		`, teamID, profileID); err != nil {
			return err
		}
		for _, fixture := range []submissionAssessmentMigrationRun{clean, held, partial} {
			if err := insertSubmissionAssessmentMigrationRun(ctx, tx, teamID, profileID, fixture); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_security_events (
			    team_id, fragment_id, ingest_id, owner_profile_id,
			    event_kind, decision, scan_policy_hash, reason
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          'deterministic_scan', 'guarded', 'legacy-policy', 'legacy guard')
		`, teamID, clean.items[0].fragmentID, clean.ingestID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_assessments (
			    team_id, assessment_id, placement_item_id, claim_key, owner_profile_id,
			    request_id, assessor_contract_version, model, tokenizer,
			    input_tokens, output_tokens, candidate_context_tokens,
			    candidate_context_truncated, normalized_response, response_hash, validated_at
			)
			SELECT $1::uuid, $2::uuid, item.placement_item_id, item.claim_key, $3::uuid,
			       'legacy-request', 'dense-mem.v2.4', 'legacy-model', 'legacy-tokenizer',
			       10, 5, 3, false, '{"request_id":"legacy-request"}'::jsonb,
			       'sha256:legacy-response', now()
			FROM placement_items AS item
			WHERE item.team_id = $1::uuid AND item.placement_item_id = $4::uuid
		`, teamID, assessmentID, profileID, clean.items[0].itemID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO entity_records (team_id, entity_id, entity_kind)
			VALUES ($1::uuid, $2::uuid, 'project')
		`, teamID, entityID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO entity_resolution_events (
			    team_id, ingest_id, placement_item_id, owner_profile_id,
			    mention_ref, action, entity_id, fragment_id, verifier_result
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          'legacy-project', 'create', $5::uuid, $6::uuid, '{"accepted":true}'::jsonb)
		`, teamID, held.ingestID, held.items[1].itemID, profileID, entityID, held.items[1].fragmentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO relationship_observations (
			    team_id, ingest_id, placement_item_id, owner_profile_id,
			    subject_ref, original_predicate, object_ref
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          'legacy-project', 'uses', 'postgres')
		`, teamID, held.ingestID, held.items[1].itemID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO review_tasks (
			    team_id, review_task_id, owner_profile_id, ingest_id,
			    placement_item_id, task_type, status, reason, payload, dedupe_key
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          $5::uuid, 'identity_needs_review', 'open', 'identity_needs_review',
			          '{"semantic_kind":"identity"}'::jsonb, $6)
		`, teamID, reviewTaskID, profileID, held.ingestID, held.items[0].itemID, "legacy-review:"+reviewTaskID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO review_tasks (
			    team_id, review_task_id, owner_profile_id, ingest_id,
			    placement_item_id, task_type, status, reason, payload, dedupe_key
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          $5::uuid, 'predicate_needs_review', 'acknowledged', 'predicate_needs_review',
			          '{"semantic_kind":"predicate"}'::jsonb, $6)
		`, teamID, acknowledgedTaskID, profileID, held.ingestID, held.items[0].itemID, "legacy-review:"+acknowledgedTaskID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO review_tasks (
			    team_id, review_task_id, owner_profile_id,
			    task_type, status, reason, payload, dedupe_key
			) VALUES ($1::uuid, $2::uuid, $3::uuid,
			          'policy_needs_review', 'open', 'unrelated_policy', '{}'::jsonb, $4)
		`, teamID, unrelatedTaskID, profileID, "unrelated-review:"+unrelatedTaskID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO placement_outcomes (
			    team_id, placement_run_id, owner_profile_id,
			    outcome_kind, status, idempotency_key, payload
			) VALUES ($1::uuid, $2::uuid, $3::uuid,
			          'telemetry_first_disposition', 'awaiting_review', $4, '{"telemetry":"first_disposition"}'::jsonb)
		`, teamID, held.runID, profileID, "telemetry:first_disposition:"+held.runID)
		return err
	}))

	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.UpToContext(ctx, sqlDB, getMigrationsDir(), 2026080502))

	var runStatus, workerID, runError string
	var attempts int
	var leaseCleared bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT status, attempts, worker_id, lease_until IS NULL, error
		FROM placement_runs
		WHERE team_id = $1::uuid AND placement_run_id = $2::uuid
	`, teamID, clean.runID).Scan(&runStatus, &attempts, &workerID, &leaseCleared, &runError))
	assert.Equal(t, "guarded", runStatus)
	assert.Zero(t, attempts)
	assert.Empty(t, workerID)
	assert.True(t, leaseCleared)
	assert.Empty(t, runError)
	assertPlacementItemState(t, ctx, sqlDB, teamID, clean.items[0].itemID, "queued", "pending")

	var assessmentScope string
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT assessment_scope FROM placement_assessments
		WHERE team_id = $1::uuid AND assessment_id = $2::uuid
	`, teamID, assessmentID).Scan(&assessmentScope))
	assert.Equal(t, "item", assessmentScope)

	assertPlacementRunFailed(t, ctx, sqlDB, teamID, held.runID)
	assertPlacementItemState(t, ctx, sqlDB, teamID, held.items[0].itemID, "failed", "failed")
	assertPlacementItemState(t, ctx, sqlDB, teamID, held.items[1].itemID, "completed", "validated_claim")
	assertPlacementRunFailed(t, ctx, sqlDB, teamID, partial.runID)
	assertPlacementItemState(t, ctx, sqlDB, teamID, partial.items[0].itemID, "completed", "validated_claim")
	assertPlacementItemState(t, ctx, sqlDB, teamID, partial.items[1].itemID, "failed", "failed")

	var taskStatus, taskReason string
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT status, resolution->>'reason'
		FROM review_tasks
		WHERE team_id = $1::uuid AND review_task_id = $2::uuid
	`, teamID, reviewTaskID).Scan(&taskStatus, &taskReason))
	assert.Equal(t, "canceled", taskStatus)
	assert.Equal(t, "legacy_submission_cutover", taskReason)
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT status, resolution->>'reason'
		FROM review_tasks
		WHERE team_id = $1::uuid AND review_task_id = $2::uuid
	`, teamID, acknowledgedTaskID).Scan(&taskStatus, &taskReason))
	assert.Equal(t, "canceled", taskStatus)
	assert.Equal(t, "legacy_submission_cutover", taskReason)
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT status FROM review_tasks
		WHERE team_id = $1::uuid AND review_task_id = $2::uuid
	`, teamID, unrelatedTaskID).Scan(&taskStatus))
	assert.Equal(t, "open", taskStatus)

	var entityEvents, observations, submissionAssessments, cutoverOutcomes, successfulCutoverOutcomes, semanticCommitClaims int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM entity_resolution_events WHERE team_id = $1::uuid AND ingest_id = $2::uuid`, teamID, held.ingestID).Scan(&entityEvents))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM relationship_observations WHERE team_id = $1::uuid AND ingest_id = $2::uuid`, teamID, held.ingestID).Scan(&observations))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM placement_assessments WHERE assessment_scope = 'submission'`).Scan(&submissionAssessments))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE status IN ('accepted', 'completed')),
		       count(*) FILTER (WHERE payload->>'semantic_commit_performed' IS DISTINCT FROM 'false')
		FROM placement_outcomes
		WHERE outcome_kind IN ('submission_cutover_requeued', 'submission_cutover_closed')
	`).Scan(&cutoverOutcomes, &successfulCutoverOutcomes, &semanticCommitClaims))
	assert.Equal(t, 1, entityEvents)
	assert.Equal(t, 1, observations)
	assert.Zero(t, submissionAssessments)
	assert.Equal(t, 5, cutoverOutcomes)
	assert.Zero(t, successfulCutoverOutcomes)
	assert.Zero(t, semanticCommitClaims)
	var acceptedResult bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT result->>'accepted' = 'true'
		FROM placement_items
		WHERE team_id = $1::uuid AND placement_item_id = $2::uuid
	`, teamID, held.items[1].itemID).Scan(&acceptedResult))
	assert.True(t, acceptedResult)

	var firstDispositionCount int
	var firstDispositionStatus string
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*), min(status)
		FROM placement_outcomes
		WHERE team_id = $1::uuid
		  AND placement_run_id = $2::uuid
		  AND outcome_kind = 'telemetry_first_disposition'
	`, teamID, held.runID).Scan(&firstDispositionCount, &firstDispositionStatus))
	assert.Equal(t, 1, firstDispositionCount)
	assert.Equal(t, "awaiting_review", firstDispositionStatus)

	require.NoError(t, goose.UpToContext(ctx, sqlDB, getMigrationsDir(), 2026080601))
	var holdCount int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM submission_holds`).Scan(&holdCount))
	assert.Zero(t, holdCount)

	err := goose.DownToContext(ctx, sqlDB, getMigrationsDir(), 2026080501)
	require.ErrorContains(t, err, "legacy reconciliation outcomes")
}

type submissionAssessmentMigrationRun struct {
	ingestID    string
	runID       string
	status      string
	attempts    int
	workerID    string
	leaseUntil  *time.Time
	completedAt *time.Time
	items       []submissionAssessmentMigrationItem
}

type submissionAssessmentMigrationItem struct {
	itemID     string
	fragmentID string
	status     string
	category   string
	result     string
}

func insertSubmissionAssessmentMigrationRun(
	ctx context.Context,
	tx *sql.Tx,
	teamID string,
	profileID string,
	fixture submissionAssessmentMigrationRun,
) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO knowledge_ingests (
		    team_id, ingest_id, owner_profile_id, status, proposal
		) VALUES ($1::uuid, $2::uuid, $3::uuid, 'queued',
		          '{"relationship_hints":[{"ref":"legacy","subject_ref":"subject","object_ref":"object","original_predicate":"uses","evidence":[{"evidence_index":0,"start":0,"end":6}]}]}'::jsonb)
	`, teamID, fixture.ingestID, profileID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO placement_runs (
		    team_id, placement_run_id, ingest_id, owner_profile_id,
		    status, attempts, worker_id, lease_until, completed_at
		) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
		          $5, $6, $7, $8, $9)
	`, teamID, fixture.runID, fixture.ingestID, profileID, fixture.status,
		fixture.attempts, fixture.workerID, fixture.leaseUntil, fixture.completedAt); err != nil {
		return err
	}
	for index, item := range fixture.items {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_fragments (
			    team_id, fragment_id, ingest_id, owner_profile_id,
			    evidence_index, content, content_hash
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          $5, $6, $7)
		`, teamID, item.fragmentID, fixture.ingestID, profileID, index,
			"Legacy evidence "+item.fragmentID, "sha256:"+item.fragmentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_items (
			    team_id, placement_item_id, placement_run_id, ingest_id,
			    owner_profile_id, fragment_id, evidence_index, status, category, result
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          $5::uuid, $6::uuid, $7, $8, $9, $10::jsonb)
		`, teamID, item.itemID, fixture.runID, fixture.ingestID, profileID,
			item.fragmentID, index, item.status, item.category, item.result); err != nil {
			return err
		}
	}
	return nil
}

func assertPlacementRunFailed(t *testing.T, ctx context.Context, db *sql.DB, teamID, runID string) {
	t.Helper()
	var status, message string
	var cleared bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT status, error, worker_id = '' AND lease_until IS NULL
		FROM placement_runs
		WHERE team_id = $1::uuid AND placement_run_id = $2::uuid
	`, teamID, runID).Scan(&status, &message, &cleared))
	assert.Equal(t, "failed", status)
	assert.Equal(t, "legacy per-item placement closed by submission-wide cutover", message)
	assert.True(t, cleared)
}

func assertPlacementItemState(t *testing.T, ctx context.Context, db *sql.DB, teamID, itemID, wantStatus, wantCategory string) {
	t.Helper()
	var status, category string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT status, category
		FROM placement_items
		WHERE team_id = $1::uuid AND placement_item_id = $2::uuid
	`, teamID, itemID).Scan(&status, &category))
	assert.Equal(t, wantStatus, status)
	assert.Equal(t, wantCategory, category)
}

func timePointer(value time.Time) *time.Time {
	return &value
}
