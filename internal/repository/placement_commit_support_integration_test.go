package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPlacementSemanticCommitPersistsEveryAcceptedSupportSpan(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "placement-multi-support", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "placement-multi-support-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "placement-multi-support-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Alex")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	content := "Alex works on Dense-Mem. Alex works on Dense-Mem."
	quote := "Alex works on Dense-Mem."
	firstStart := strings.Index(content, quote)
	secondStart := strings.LastIndex(content, quote)
	require.NotEqual(t, firstStart, secondStart)
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID, "placement multi support", content)
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-multi-support", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	committed, err := ledgerRepo.CommitPlacementSemanticResult(ctx, CommitPlacementSemanticInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-multi-support",
		ExpectedAttempts: claimed.Attempts,
		EntityResolutions: []PlacementEntityResolutionInput{
			{MentionRef: "subject", Action: "reuse", EntityID: subject.EntityID},
			{MentionRef: "object", Action: "reuse", EntityID: object.EntityID},
		},
		RelationshipObservations: []PlacementRelationshipDecisionInput{{
			Ref:          "rel-multi-support",
			SubjectRef:   "subject",
			PredicateKey: "works_on",
			ObjectRef:    "object",
			Support: &EvidenceSupportInput{
				FragmentID:     ingest.Evidence[0].FragmentID,
				SourceGroupKey: "semantic-assessment:first",
				SpanStart:      firstStart,
				SpanEnd:        firstStart + len(quote),
				Quote:          quote,
				Authority:      "primary",
			},
			Supports: []EvidenceSupportInput{{
				FragmentID:     ingest.Evidence[0].FragmentID,
				SourceGroupKey: "semantic-assessment:second",
				SpanStart:      secondStart,
				SpanEnd:        secondStart + len(quote),
				Quote:          quote,
				Authority:      "primary",
			}},
		}},
	})
	require.NoError(t, err)
	require.Len(t, committed.RelationshipResults, 1)
	assert.Len(t, committed.RelationshipResults[0].SupportIDs, 2)

	var supportCount, observationEvidenceCount int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_evidence_supports
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, committed.RelationshipResults[0].Relationship.RelationshipID).Scan(&supportCount).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT jsonb_array_length(evidence)
			FROM relationship_observations
			WHERE team_id = ?::uuid
			  AND observation_id = ?::uuid
		`, teamID, committed.RelationshipResults[0].ObservationID).Scan(&observationEvidenceCount).Error
	}))
	assert.Equal(t, 2, supportCount)
	assert.Equal(t, 2, observationEvidenceCount)
}
