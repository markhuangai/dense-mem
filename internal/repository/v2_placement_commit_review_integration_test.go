package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestV2PlacementSemanticCommitDefaultsRelationshipReviewPolarity(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertV2SearchTestContract(t, adminDB, rls, "placement-review-polarity", 3, "exact", "")
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-review-polarity-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "placement-review-polarity-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)

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
		RelationshipObservations: []V2PlacementRelationshipDecisionInput{{
			Ref:               "dependency-review",
			SubjectRef:        "missing-subject",
			OriginalPredicate: "missing endpoint",
			PredicateKey:      "works_on",
			ObjectRef:         "missing-object",
			Polarity:          "-",
			EvidenceVerdict:   "insufficient",
		}},
		RelationshipReviews: []V2PlacementRelationshipReviewInput{{
			Ref:               "relationship-review",
			SubjectRef:        "subject",
			OriginalPredicate: "works on",
			ObjectRef:         "object",
			EvidenceVerdict:   "insufficient",
			Reason:            "predicate_needs_review",
		}},
	})
	require.NoError(t, err)
	require.Equal(t, "accepted", committed.Status)
	require.Len(t, committed.ReviewTaskIDs, 2)

	var defaultPolarity, dependencyPolarity, taskStatus, runStatus, itemStatus string
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT polarity
			FROM relationship_observations
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
			  AND original_predicate = 'works on'
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
	assert.Equal(t, "awaiting_review", runStatus)
	assert.Equal(t, "awaiting_review", itemStatus)
}
