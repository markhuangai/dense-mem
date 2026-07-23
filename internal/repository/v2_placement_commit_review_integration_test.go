package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestV2PlacementSemanticCommitDefaultsRelationshipReviewPolarity(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertV2SearchTestContract(t, adminDB, rls, "placement-review-polarity", 3, "exact", "")
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-review-polarity-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "placement-review-polarity-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)
	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Alex")
	object := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")

	ingest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement relationship review polarity", "Alex works on Dense-Mem.")
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-review-polarity", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	committed, err := ledgerRepo.CommitPlacementSemanticResult(ctx, V2CommitPlacementSemanticInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-review-polarity",
		ExpectedAttempts: claimed.Attempts,
		Status:           "accepted",
		EntityResolutions: []V2PlacementEntityResolutionInput{
			{MentionRef: "subject", Action: "reuse", EntityID: subject.EntityID},
			{MentionRef: "object", Action: "reuse", EntityID: object.EntityID},
		},
		RelationshipObservations: []V2PlacementRelationshipDecisionInput{
			{
				Ref:               "dependency-review",
				SubjectRef:        "missing-subject",
				OriginalPredicate: "missing endpoint",
				PredicateKey:      "works_on",
				ObjectRef:         "missing-object",
				Polarity:          "-",
				EvidenceVerdict:   "insufficient",
			},
			{
				Ref:               "predicate-review",
				SubjectRef:        "subject",
				OriginalPredicate: "unknown collaboration",
				PredicateKey:      "unknown_collaboration",
				ObjectRef:         "object",
				EvidenceVerdict:   "insufficient",
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "accepted", committed.Status)
	require.Len(t, committed.ReviewTaskIDs, 2)

	var defaultPolarity, dependencyPolarity, taskStatus, predicatePolicy, runStatus, itemStatus string
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT polarity
			FROM relationship_observations
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
			  AND original_predicate = 'unknown collaboration'
		`, teamID, ingest.Items[0].PlacementItemID).Scan(&defaultPolarity).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT polarity
			FROM relationship_observations
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
			  AND original_predicate = 'missing endpoint'
		`, teamID, ingest.Items[0].PlacementItemID).Scan(&dependencyPolarity).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status
			FROM review_tasks
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, teamID, ingest.Items[0].PlacementItemID).Scan(&taskStatus).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT payload->>'predicate_policy_version'
			FROM review_tasks
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
			  AND task_type = 'predicate_needs_review'
		`, teamID, ingest.Items[0].PlacementItemID).Scan(&predicatePolicy).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status
			FROM placement_runs
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Scan(&runStatus).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT status
			FROM placement_items
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, teamID, ingest.Items[0].PlacementItemID).Scan(&itemStatus).Error
	})
	require.NoError(t, err)
	assert.Equal(t, "+", defaultPolarity)
	assert.Equal(t, "-", dependencyPolarity)
	assert.Equal(t, "open", taskStatus)
	assert.Equal(t, domain.V2PredicatePolicyVersion, predicatePolicy)
	assert.Equal(t, "awaiting_review", runStatus)
	assert.Equal(t, "awaiting_review", itemStatus)
}

func TestV2PlacementSemanticCommitRegistersNovelPredicateAtomically(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertV2SearchTestContract(t, adminDB, rls, "placement-novel-predicate", 3, "exact", "")
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-novel-predicate-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "placement-novel-predicate-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)
	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	ingest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement novel predicate", "Dense-Mem integrates with GitVibe.")
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-novel-predicate", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	commitInput := V2CommitPlacementSemanticInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-novel-predicate",
		ExpectedAttempts: claimed.Attempts,
		EntityResolutions: []V2PlacementEntityResolutionInput{
			{MentionRef: "subject", Action: "reuse", EntityID: subject.EntityID},
			{MentionRef: "object", Action: "create", EntityKind: "product", CanonicalName: "GitVibe"},
		},
		RelationshipObservations: []V2PlacementRelationshipDecisionInput{{
			Ref:               "rel-integrates-with",
			SubjectRef:        "subject",
			OriginalPredicate: "integrates with",
			PredicateKey:      "integrates_with",
			PredicateVersion:  1,
			PredicateCandidate: &V2PlacementPredicateCandidateInput{
				PredicateKey:     "integrates_with",
				PredicateVersion: 1,
				RelationshipKind: "state",
			},
			ObjectRef:       "object",
			EvidenceVerdict: "entailed",
			Confidence:      v2Float64Pointer(0.96),
			Rationale:       "The evidence explicitly states the integration.",
			Model:           "test-verifier",
			ResponseHash:    "sha256:novel-predicate",
			Support: &V2EvidenceSupportInput{
				FragmentID:     ingest.Evidence[0].FragmentID,
				SourceGroupKey: "conversation:novel-predicate",
				SpanStart:      0,
				SpanEnd:        len("Dense-Mem integrates with GitVibe."),
				Quote:          "Dense-Mem integrates with GitVibe.",
				Authority:      "primary",
			},
		}},
	}
	committed, err := ledgerRepo.CommitPlacementSemanticResult(ctx, commitInput)
	require.NoError(t, err)
	require.Empty(t, committed.ReviewTaskIDs)
	require.Len(t, committed.RelationshipResults, 1)
	require.NotNil(t, committed.RelationshipResults[0].Relationship)
	assert.Equal(t, "integrates_with", committed.RelationshipResults[0].Relationship.PredicateKey)

	var replayAttempts int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			UPDATE placement_runs
			SET status = 'processing',
			    attempts = attempts + 1,
			    worker_id = 'worker-novel-predicate-replay',
			    lease_until = now() + interval '1 minute',
			    completed_at = NULL,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
			RETURNING attempts
		`, teamID, ingest.PlacementRunID).Row().Scan(&replayAttempts); err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE placement_items
			SET status = 'queued',
			    category = 'pending',
			    result = '{}'::jsonb,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, teamID, ingest.Items[0].PlacementItemID).Error
	}))
	commitInput.WorkerID = "worker-novel-predicate-replay"
	commitInput.ExpectedAttempts = replayAttempts
	replayed, err := ledgerRepo.CommitPlacementSemanticResult(ctx, commitInput)
	require.NoError(t, err)
	require.Empty(t, replayed.ReviewTaskIDs)
	require.Len(t, replayed.RelationshipResults, 1)

	var definitionCount, relationshipCount, createdEntityCount, supportCount int
	var origin string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT count(*)::int, min(origin)
			FROM team_predicate_definitions
			WHERE team_id = ?::uuid
			  AND predicate_key = 'integrates_with'
		`, teamID).Row().Scan(&definitionCount, &origin); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT count(*)::int
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND predicate_key = 'integrates_with'
		`, teamID).Scan(&relationshipCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT count(*)::int
			FROM entity_records AS entity
			JOIN entity_names AS name
			  ON name.team_id = entity.team_id
			 AND name.entity_id = entity.entity_id
			 AND name.name_kind = 'canonical'
			 AND name.valid_to IS NULL
			WHERE entity.team_id = ?::uuid
			  AND entity.entity_kind = 'product'
			  AND name.display_name = 'GitVibe'
		`, teamID).Scan(&createdEntityCount).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT count(*)::int
			FROM relationship_evidence_supports AS support
			JOIN relationship_records AS relationship
			  ON relationship.team_id = support.team_id
			 AND relationship.relationship_id = support.relationship_id
			WHERE relationship.team_id = ?::uuid
			  AND relationship.predicate_key = 'integrates_with'
		`, teamID).Scan(&supportCount).Error
	}))
	assert.Equal(t, 1, definitionCount)
	assert.Equal(t, "provider_generated", origin)
	assert.Equal(t, 1, relationshipCount)
	assert.Equal(t, 1, createdEntityCount)
	assert.Equal(t, 1, supportCount)
}

func TestV2PlacementSemanticCommitRoutesUnsafePredicateCollisionToReview(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertV2SearchTestContract(t, adminDB, rls, "placement-predicate-collision", 3, "exact", "")
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-predicate-collision-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "placement-predicate-collision-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)
	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Mark")
	object := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	ingest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement predicate collision", "Mark works on Dense-Mem.")
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-predicate-collision", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	committed, err := ledgerRepo.CommitPlacementSemanticResult(ctx, V2CommitPlacementSemanticInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-predicate-collision",
		ExpectedAttempts: claimed.Attempts,
		EntityResolutions: []V2PlacementEntityResolutionInput{
			{MentionRef: "subject", Action: "reuse", EntityID: subject.EntityID},
			{MentionRef: "object", Action: "reuse", EntityID: object.EntityID},
		},
		RelationshipObservations: []V2PlacementRelationshipDecisionInput{{
			Ref:               "rel-collision",
			SubjectRef:        "subject",
			OriginalPredicate: "works on",
			PredicateKey:      "works_on",
			PredicateVersion:  1,
			PredicateCandidate: &V2PlacementPredicateCandidateInput{
				PredicateKey:     "works_on",
				PredicateVersion: 1,
				RelationshipKind: "event",
			},
			ObjectRef:       "object",
			EvidenceVerdict: "entailed",
			Confidence:      v2Float64Pointer(0.9),
			Rationale:       "The evidence states the relationship.",
			Model:           "test-verifier",
			ResponseHash:    "sha256:predicate-collision",
			Support: &V2EvidenceSupportInput{
				FragmentID:     ingest.Evidence[0].FragmentID,
				SourceGroupKey: "conversation:predicate-collision",
				SpanStart:      0,
				SpanEnd:        len("Mark works on Dense-Mem."),
				Quote:          "Mark works on Dense-Mem.",
				Authority:      "primary",
			},
		}},
	})
	require.NoError(t, err)
	require.Len(t, committed.ReviewTaskIDs, 1)
	require.Len(t, committed.RelationshipResults, 1)
	require.Nil(t, committed.RelationshipResults[0].Relationship)

	var taskPolicy, itemStatus string
	var verificationCount int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT payload->>'predicate_policy_version'
			FROM review_tasks
			WHERE team_id = ?::uuid
			  AND review_task_id = ?::uuid
		`, teamID, committed.ReviewTaskIDs[0]).Row().Scan(&taskPolicy); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT count(*)::int
			FROM verification_events AS verification
			JOIN relationship_observations AS observation
			  ON observation.team_id = verification.team_id
			 AND observation.observation_id = verification.observation_id
			WHERE observation.team_id = ?::uuid
			  AND observation.placement_item_id = ?::uuid
		`, teamID, ingest.Items[0].PlacementItemID).Scan(&verificationCount).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT status
			FROM placement_items
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, teamID, ingest.Items[0].PlacementItemID).Scan(&itemStatus).Error
	}))
	assert.Equal(t, domain.V2PredicatePolicyVersion, taskPolicy)
	assert.Equal(t, 1, verificationCount)
	assert.Equal(t, "awaiting_review", itemStatus)
}

func v2Float64Pointer(value float64) *float64 {
	return &value
}
