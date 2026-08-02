//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecureSubmissionMigrationStagesLegacyWorkAndTerminalizesItsShell(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026073103)
	teamID, profileID := insertMigrationTeamProfile(t, ctx, sqlDB)
	queued := insertSecureSubmissionLegacyFixture(t, ctx, sqlDB, teamID, profileID, "queued")
	setSecureSubmissionLegacyProposal(t, ctx, sqlDB, teamID, queued.ingestID, `{
		"entity_hints": [
			{"ref":"legacy:subject","name":"Legacy","evidence":[{"evidence_index":0,"start":0,"end":6}]},
			{"ref":"legacy:object","name":"Ada","evidence":[{"evidence_index":0,"start":13,"end":16}]}
		],
		"relationship_hints": [{
			"proposal_id":"legacy:names","subject_ref":"legacy:subject","object_ref":"legacy:object",
			"predicate":{"surface":"names","evidence_index":0,"start":7,"end":12},
			"evidence":[{"evidence_index":0,"start":0,"end":17}]
		}]
	}`)
	incompatible := insertSecureSubmissionLegacyFixture(t, ctx, sqlDB, teamID, profileID, "queued")
	awaiting := insertSecureSubmissionLegacyFixture(t, ctx, sqlDB, teamID, profileID, "awaiting_review")

	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.UpToContext(ctx, sqlDB, getMigrationsDir(), 2026080105))

	var (
		submissionStatus         string
		stagedSourceKey          string
		stagedRevision           string
		stagedGroup              string
		legacyRunStatus          string
		legacyItemStatus         string
		legacyIngestStatus       string
		awaitingTaskStatus       string
		awaitingRunStatus        string
		awaitingItemStatus       string
		awaitingIngestStatus     string
		canonicalPromotionCount  int
		entityAssessmentFK       bool
		verificationAssessmentFK bool
		proposalRaw              []byte
		actorAuthMethod          string
		incompatibleStatus       string
		incompatibleErrorCode    string
		incompatibleStagedCount  int
		incompatibleIngestStatus string
	)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			SELECT status
			FROM submission_runs
			WHERE team_id = $1::uuid AND submission_id = $2::uuid
		`, teamID, queued.ingestID).Scan(&submissionStatus); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT source_key, source_revision, source_group
			FROM submission_staged_evidence
			WHERE team_id = $1::uuid AND submission_id = $2::uuid AND evidence_index = 0
		`, teamID, queued.ingestID).Scan(&stagedSourceKey, &stagedRevision, &stagedGroup); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT proposal
			FROM submission_staged_proposals
			WHERE team_id = $1::uuid AND submission_id = $2::uuid
		`, teamID, queued.ingestID).Scan(&proposalRaw); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT actor_auth_method
			FROM submission_runs
			WHERE team_id = $1::uuid AND submission_id = $2::uuid
		`, teamID, queued.ingestID).Scan(&actorAuthMethod); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT status, error_code
			FROM submission_runs
			WHERE team_id = $1::uuid AND submission_id = $2::uuid
		`, teamID, incompatible.ingestID).Scan(&incompatibleStatus, &incompatibleErrorCode); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*)
			FROM submission_staged_evidence
			WHERE team_id = $1::uuid AND submission_id = $2::uuid
		`, teamID, incompatible.ingestID).Scan(&incompatibleStagedCount); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT status FROM knowledge_ingests
			WHERE team_id = $1::uuid AND ingest_id = $2::uuid
		`, teamID, incompatible.ingestID).Scan(&incompatibleIngestStatus); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT status FROM placement_runs
			WHERE team_id = $1::uuid AND placement_run_id = $2::uuid
		`, teamID, queued.runID).Scan(&legacyRunStatus); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT status FROM placement_items
			WHERE team_id = $1::uuid AND placement_item_id = $2::uuid
		`, teamID, queued.itemID).Scan(&legacyItemStatus); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT status FROM knowledge_ingests
			WHERE team_id = $1::uuid AND ingest_id = $2::uuid
		`, teamID, queued.ingestID).Scan(&legacyIngestStatus); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT status FROM review_tasks
			WHERE team_id = $1::uuid AND review_task_id = $2::uuid
		`, teamID, awaiting.taskID).Scan(&awaitingTaskStatus); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT status FROM placement_runs
			WHERE team_id = $1::uuid AND placement_run_id = $2::uuid
		`, teamID, awaiting.runID).Scan(&awaitingRunStatus); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT status FROM placement_items
			WHERE team_id = $1::uuid AND placement_item_id = $2::uuid
		`, teamID, awaiting.itemID).Scan(&awaitingItemStatus); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT status FROM knowledge_ingests
			WHERE team_id = $1::uuid AND ingest_id = $2::uuid
		`, teamID, awaiting.ingestID).Scan(&awaitingIngestStatus); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*)
			FROM knowledge_ingests
			WHERE team_id = $1::uuid AND idempotency_key = $2
		`, teamID, "submission:"+queued.ingestID).Scan(&canonicalPromotionCount); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'entity_resolution_events_submission_assessment_ref'
			)
		`).Scan(&entityAssessmentFK); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'verification_events_submission_assessment_ref'
			)
		`).Scan(&verificationAssessmentFK)
	}))

	assert.Equal(t, "queued", submissionStatus)
	var proposal map[string]any
	require.NoError(t, json.Unmarshal(proposalRaw, &proposal))
	assert.Contains(t, proposal, "entities")
	assert.Contains(t, proposal, "relationships")
	assert.NotContains(t, proposal, "entity_hints")
	assert.NotContains(t, proposal, "relationship_hints")
	assert.Empty(t, actorAuthMethod)
	assert.Equal(t, queued.sourceKey, stagedSourceKey)
	assert.Equal(t, "rev-1", stagedRevision)
	assert.Equal(t, "legacy:source-group", stagedGroup)
	assert.Equal(t, "failed", legacyRunStatus)
	assert.Equal(t, "failed", legacyItemStatus)
	assert.Equal(t, "failed", legacyIngestStatus)
	assert.Equal(t, "canceled", awaitingTaskStatus)
	assert.Equal(t, "completed", awaitingRunStatus)
	assert.Equal(t, "failed", awaitingItemStatus)
	assert.Equal(t, "completed", awaitingIngestStatus)
	assert.Zero(t, canonicalPromotionCount)
	assert.True(t, entityAssessmentFK)
	assert.True(t, verificationAssessmentFK)
	assert.Equal(t, "failed", incompatibleStatus)
	assert.Equal(t, "legacy_proposal_incompatible", incompatibleErrorCode)
	assert.Zero(t, incompatibleStagedCount)
	assert.Equal(t, "failed", incompatibleIngestStatus)
}

func setSecureSubmissionLegacyProposal(t *testing.T, ctx context.Context, db *sql.DB, teamID, ingestID, proposal string) {
	t.Helper()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE knowledge_ingests
			SET proposal = $3::jsonb
			WHERE team_id = $1::uuid AND ingest_id = $2::uuid
		`, teamID, ingestID, proposal)
		return err
	}))
}

func TestSecureSubmissionMigrationRefusesUnfinishedLegacySearchOutput(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026073103)
	teamID, profileID := insertMigrationTeamProfile(t, ctx, sqlDB)
	legacy := insertSecureSubmissionLegacyFixture(t, ctx, sqlDB, teamID, profileID, "queued")
	contractID := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_contracts (
				embedding_contract_id, contract_key, version, provider, model,
				dimensions, distance_metric, vector_normalization,
				document_format_version, query_format_version, lifecycle_state
			) VALUES (
				$1::uuid, $2, 1, 'test', 'secure-submission-migration',
				3, 'cosine', 'provider', 1, 1, 'active'
			)
		`, contractID, "secure-submission-migration-"+contractID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO search_documents (
				team_id, owner_profile_id, source_kind, source_id,
				source_version, document_version, embedding_contract_id, embedding_dimensions,
				search_state, document_text, document_hash
			) VALUES (
				$1::uuid, $2::uuid, 'evidence', $3::uuid,
				1, 1, $4::uuid, 3,
				'pending', 'legacy unfinished search output', 'sha256:legacy-search-output'
			)
		`, teamID, profileID, legacy.fragmentID, contractID)
		return err
	}))

	require.NoError(t, goose.SetDialect("postgres"))
	err := goose.UpToContext(ctx, sqlDB, getMigrationsDir(), 2026080101)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unfinished legacy placement records already have semantic or search output")
}

type secureSubmissionLegacyFixture struct {
	ingestID   string
	runID      string
	itemID     string
	fragmentID string
	taskID     string
	sourceKey  string
}

func insertSecureSubmissionLegacyFixture(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	teamID string,
	profileID string,
	status string,
) secureSubmissionLegacyFixture {
	t.Helper()
	fixture := secureSubmissionLegacyFixture{
		ingestID:   uuid.NewString(),
		runID:      uuid.NewString(),
		itemID:     uuid.NewString(),
		fragmentID: uuid.NewString(),
		taskID:     uuid.NewString(),
		sourceKey:  "document://legacy-submission-" + uuid.NewString(),
	}
	sourceID := uuid.NewString()
	revisionID := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_team_refs (team_id) VALUES ($1::uuid)
			ON CONFLICT (team_id) DO NOTHING
		`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_profile_refs (team_id, profile_id) VALUES ($1::uuid, $2::uuid)
			ON CONFLICT (team_id, profile_id) DO NOTHING
		`, teamID, profileID); err != nil {
			return err
		}
		ingestStatus := status
		if status == "awaiting_review" {
			ingestStatus = "processing"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, request_hash, source_summary, status, proposal
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, 'sha256:legacy-request', 'legacy source', $4, '{}'::jsonb
			)
		`, teamID, fixture.ingestID, profileID, ingestStatus); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_sources (team_id, source_id, owner_profile_id, source_key, source_kind, authority)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'document', 'primary')
		`, teamID, sourceID, profileID, fixture.sourceKey); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_source_revisions (
				team_id, source_revision_id, source_id, owner_profile_id,
				revision_token, expected_previous_revision_token, content_hash
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'rev-1', '', 'sha256:legacy-revision')
		`, teamID, revisionID, sourceID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id, source_id, source_revision_id,
				evidence_index, content, content_hash, source_type, authority, source_ref, metadata
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid,
				0, 'Legacy names Ada.', 'sha256:legacy-fragment', 'document', 'primary', 'legacy-source',
				'{"contract_source_group":"legacy:source-group","evidence_idempotency_key":"legacy-evidence"}'::jsonb
			)
		`, teamID, fixture.fragmentID, fixture.ingestID, profileID, sourceID, revisionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_runs (
				team_id, placement_run_id, ingest_id, owner_profile_id, status, completed_at
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid, $5,
				CASE WHEN $6 THEN clock_timestamp() ELSE NULL END
			)
		`, teamID, fixture.runID, fixture.ingestID, profileID, status, status == "awaiting_review"); err != nil {
			return err
		}
		itemStatus := status
		itemCategory := "pending"
		if status == "awaiting_review" {
			itemCategory = "candidate"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_items (
				team_id, placement_item_id, placement_run_id, ingest_id,
				owner_profile_id, fragment_id, evidence_index, status, category
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid,
				$5::uuid, $6::uuid, 0, $7, $8
			)
		`, teamID, fixture.itemID, fixture.runID, fixture.ingestID, profileID, fixture.fragmentID, itemStatus, itemCategory); err != nil {
			return err
		}
		if status != "awaiting_review" {
			return nil
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO review_tasks (
				team_id, review_task_id, owner_profile_id, ingest_id, placement_item_id,
				task_type, status, reason, payload, dedupe_key
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid,
				'identity_needs_review', 'open', 'legacy review', '{}'::jsonb, ''
			)
		`, teamID, fixture.taskID, profileID, fixture.ingestID, fixture.itemID)
		return err
	}))
	return fixture
}
