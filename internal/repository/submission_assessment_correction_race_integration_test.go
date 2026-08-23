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

func TestSubmissionAssessmentCommitRejectsChangedCorrectionTargetAtomically(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-correction-race-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-correction-race-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-correction-race-search", 3, "exact", "")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Dense-Mem")
	oldObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "GraphDB")
	newObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "PostgreSQL")
	targetContent := "Dense-Mem uses GraphDB."
	targetIngest := createSemanticIngest(
		t, ctx, ledger, teamID, ownerID,
		"submission-correction-race-target", targetContent,
	)
	targetClaim, err := ledger.ClaimNextPlacementRun(ctx, teamID, "submission-correction-target-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, targetClaim)
	target := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: targetIngest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "uses", ObjectEntityID: oldObject.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: targetIngest.Evidence[0].FragmentID, SourceGroupKey: "submission-correction-race-target",
			SpanStart: 0, SpanEnd: len(targetContent), Quote: targetContent, Authority: "primary",
		},
	})
	require.NotNil(t, target.Relationship)
	_, err = ledger.FinishPlacementRun(
		ctx, teamID, targetClaim.PlacementRunID, "submission-correction-target-worker",
		string(domain.PlacementRunCompleted), "",
	)
	require.NoError(t, err)

	newContent := "Dense-Mem uses PostgreSQL."
	ingest := createSemanticIngest(
		t, ctx, ledger, teamID, ownerID,
		"submission-correction-race-new", newContent,
	)
	claimed, err := ledger.ClaimNextPlacementRun(ctx, teamID, "submission-correction-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assessment := persistSubmissionAssessment(t, ctx, ledger, *claimed)
	input := CommitSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
			PlacementRunID: ingest.PlacementRunID, WorkerID: "submission-correction-worker", ExpectedAttempts: claimed.Attempts,
		},
		AssessmentID: assessment.AssessmentID,
		Items: []SubmissionAssessmentItemInput{{
			PlacementItemID: ingest.Items[0].PlacementItemID,
			FragmentID:      ingest.Evidence[0].FragmentID,
		}},
		EntityResolutions: []SubmissionAssessmentEntityResolutionInput{
			{
				PlacementItemID: ingest.Items[0].PlacementItemID,
				Resolution: PlacementEntityResolutionInput{
					MentionRef: "subject", Action: string(domain.EntityResolutionReuse), EntityID: subject.EntityID,
					FragmentID: ingest.Evidence[0].FragmentID, AssessmentID: assessment.AssessmentID,
				},
			},
			{
				PlacementItemID: ingest.Items[0].PlacementItemID,
				Resolution: PlacementEntityResolutionInput{
					MentionRef: "object", Action: string(domain.EntityResolutionReuse), EntityID: newObject.EntityID,
					FragmentID: ingest.Evidence[0].FragmentID, AssessmentID: assessment.AssessmentID,
				},
			},
		},
		RelationshipObservations: []SubmissionAssessmentRelationshipObservationInput{{
			PlacementItemID: ingest.Items[0].PlacementItemID,
			RelationshipRef: "uses-postgresql",
			SplitIndex:      0,
			Observation: PlacementRelationshipDecisionInput{
				Ref: "uses-postgresql", SubjectRef: "subject", OriginalPredicate: "uses",
				PredicateKey: "uses", PredicateVersion: 1, ObjectRef: "object", Polarity: "+",
				AssessorAccepted: true, Model: "submission-correction-race", ResponseHash: "sha256:submission-correction-race",
				AssessmentID: assessment.AssessmentID,
				CorrectionTarget: &PlacementCorrectionTargetInput{
					RelationshipID:  target.Relationship.RelationshipID,
					ExpectedVersion: target.Relationship.Version,
				},
				Support: &EvidenceSupportInput{
					FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "submission-correction-race-new",
					SpanStart: 0, SpanEnd: len(newContent), Quote: newContent, Authority: "primary",
				},
			},
		}},
		RelationshipResults: []SubmissionRelationshipResultInput{{
			RelationshipRef: "uses-postgresql", Disposition: "stored",
		}},
		Payload: map[string]any{"assessor_contract": domain.ContractVersion},
	}

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records
			SET version = version + 1, updated_at = now()
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamID, target.Relationship.RelationshipID).Error
	}))
	before := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, ingest.PlacementRunID)

	_, err = ledger.CommitSubmissionAssessment(ctx, input)

	require.ErrorIs(t, err, ErrCorrectionTargetStale)
	after := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, ingest.PlacementRunID)
	assert.Equal(t, before, after)
}
