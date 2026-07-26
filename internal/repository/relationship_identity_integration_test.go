package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSemanticGroupKeyExcludesMutableValidTo(t *testing.T) {
	open := ApplyRelationshipDecisionInput{
		SubjectEntityID: "8f8a7889-93e0-4332-b22a-abf5c09dc8b7",
		PredicateKey:    "ahead_of",
		ObjectEntityID:  "1d5fce8f-cfaf-4350-a612-0268bd8296bd",
		Polarity:        "+",
	}
	bounded := open
	validTo := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	bounded.ValidTo = &validTo

	assert.Equal(t, "sg:29bf9ddd7c106f1ec0ece804ed5c304a9d5dd3af1bfd22bc8f89188e6edf3d17", semanticGroupKey(open))
	assert.Equal(t, semanticGroupKey(open), semanticGroupKey(bounded))
}

func TestSemanticValidToDisagreementCreatesReviewWithoutMutatingCanonicalRelationship(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "semantic-valid-to-review-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Subject")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Object")
	firstIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"valid-to-open", "Subject works on Object.")
	canonical := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        firstIngest.IngestID,
		ProposalRef:     "relationship:0",
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     firstIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:first",
			SpanStart:      0,
			SpanEnd:        len("Subject works on Object."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, canonical.Relationship)

	secondIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"valid-to-bounded", "Subject stopped working on Object.")
	validTo := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	review := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        secondIngest.IngestID,
		ProposalRef:     "relationship:0",
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		ValidTo:         &validTo,
		Support: &EvidenceSupportInput{
			FragmentID:     secondIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:second",
			SpanStart:      0,
			SpanEnd:        len("Subject stopped working on Object."),
			Authority:      "primary",
		},
	})
	assert.Nil(t, review.Relationship)
	assert.NotEmpty(t, review.ObservationID)
	assert.NotEmpty(t, review.VerificationEventID)
	assert.NotEmpty(t, review.ReviewTaskID)
	assert.Equal(t, "relationship_needs_review", review.Category)

	trace, err := semanticRepo.TraceRelationship(ctx, TraceRelationshipInput{
		TeamID:         teamID,
		RelationshipID: canonical.Relationship.RelationshipID,
	})
	require.NoError(t, err)
	require.NotNil(t, trace.Relationship)
	assert.Empty(t, trace.Relationship.IdentityAliasOfID)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var relationshipCount, observationCount, unresolvedObservationCount, supportCount int
		if err := tx.Raw(`
			SELECT count(*)::int
			FROM relationship_records
			WHERE team_id = ?::uuid
		`, teamID).Scan(&relationshipCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT count(*)::int,
			       count(*) FILTER (WHERE relationship_id IS NULL)::int
			FROM relationship_observations
			WHERE team_id = ?::uuid
		`, teamID).Row().Scan(&observationCount, &unresolvedObservationCount); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT count(*)::int
			FROM relationship_evidence_supports
			WHERE team_id = ?::uuid
		`, teamID).Scan(&supportCount).Error; err != nil {
			return err
		}
		assert.Equal(t, 1, relationshipCount)
		assert.Equal(t, 2, observationCount)
		assert.Equal(t, 1, unresolvedObservationCount)
		assert.Equal(t, 1, supportCount)

		var persistedValidTo sql.NullTime
		var relationshipSupportCount int
		if err := tx.Raw(`
			SELECT valid_to, support_count
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, canonical.Relationship.RelationshipID).Row().Scan(&persistedValidTo, &relationshipSupportCount); err != nil {
			return err
		}
		assert.False(t, persistedValidTo.Valid)
		assert.Equal(t, 1, relationshipSupportCount)

		var taskRelationshipID, taskReason, proposedValidTo string
		if err := tx.Raw(`
			SELECT relationship_id::text,
			       reason,
			       payload->>'proposed_valid_to'
			FROM review_tasks
			WHERE team_id = ?::uuid
			  AND review_task_id = ?::uuid
		`, teamID, review.ReviewTaskID).Row().Scan(&taskRelationshipID, &taskReason, &proposedValidTo); err != nil {
			return err
		}
		assert.Equal(t, canonical.Relationship.RelationshipID, taskRelationshipID)
		assert.Equal(t, "relationship_identity_valid_to_conflict", taskReason)
		assert.Equal(t, validTo.Format(time.RFC3339Nano), proposedValidTo)
		return nil
	}))
}
