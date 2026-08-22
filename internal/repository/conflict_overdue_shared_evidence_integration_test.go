package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestOverdueConflictResolutionPreservesSharedEvidenceOutsideConflict(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "overdue-conflict-shared-evidence", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "overdue-conflict-shared-evidence-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "overdue-conflict-shared-evidence-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "overdue-conflict-shared-evidence-owner-b")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")
	unrelatedSubject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerB, "project", "Other Project")
	preferred := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-overdue-shared-preferred",
		"overdue-shared-preferred", "Dense-Mem uses PostgreSQL.", subject.EntityID, postgres.EntityID, "source-group-overdue-shared-preferred",
	)

	sharedContent := "Dense-Mem uses GraphDB. Other Project uses PostgreSQL."
	losingQuote := "Dense-Mem uses GraphDB."
	unrelatedQuote := "Other Project uses PostgreSQL."
	losingStart := strings.Index(sharedContent, losingQuote)
	unrelatedStart := strings.Index(sharedContent, unrelatedQuote)
	require.GreaterOrEqual(t, losingStart, 0)
	require.GreaterOrEqual(t, unrelatedStart, 0)
	sharedIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerB, "overdue-shared-evidence", sharedContent)
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-overdue-shared-loser", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	committed, err := commitAcceptedSubmissionFixture(t, ctx, ledgerRepo, CommitPlacementSemanticInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerB,
		IngestID:         sharedIngest.IngestID,
		PlacementRunID:   sharedIngest.PlacementRunID,
		PlacementItemID:  sharedIngest.Items[0].PlacementItemID,
		WorkerID:         "worker-overdue-shared-loser",
		ExpectedAttempts: claimed.Attempts,
		EntityResolutions: []PlacementEntityResolutionInput{
			{MentionRef: "losing-subject", Action: "reuse", EntityID: subject.EntityID},
			{MentionRef: "losing-object", Action: "reuse", EntityID: graphdb.EntityID},
			{MentionRef: "unrelated-subject", Action: "reuse", EntityID: unrelatedSubject.EntityID},
			{MentionRef: "unrelated-object", Action: "reuse", EntityID: postgres.EntityID},
		},
		RelationshipObservations: []PlacementRelationshipDecisionInput{
			{
				Ref:          "shared-losing-relationship",
				SubjectRef:   "losing-subject",
				PredicateKey: "primary_database",
				ObjectRef:    "losing-object",
				Support: &EvidenceSupportInput{
					FragmentID:     sharedIngest.Evidence[0].FragmentID,
					SourceGroupKey: "source-group-overdue-shared-loser",
					SpanStart:      losingStart,
					SpanEnd:        losingStart + len(losingQuote),
					Quote:          losingQuote,
					Authority:      "primary",
				},
			},
			{
				Ref:          "shared-unrelated-relationship",
				SubjectRef:   "unrelated-subject",
				PredicateKey: "primary_database",
				ObjectRef:    "unrelated-object",
				Support: &EvidenceSupportInput{
					FragmentID:     sharedIngest.Evidence[0].FragmentID,
					SourceGroupKey: "source-group-overdue-shared-unrelated",
					SpanStart:      unrelatedStart,
					SpanEnd:        unrelatedStart + len(unrelatedQuote),
					Quote:          unrelatedQuote,
					Authority:      "primary",
				},
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, committed.RelationshipResults, 2)
	require.NotNil(t, committed.RelationshipResults[0].Relationship)
	require.NotNil(t, committed.RelationshipResults[1].Relationship)
	losingRelationshipID := committed.RelationshipResults[0].Relationship.RelationshipID
	unrelatedRelationshipID := committed.RelationshipResults[1].Relationship.RelationshipID

	conflictID, _ := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	reviewNow := time.Now().UTC()
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_conflict_cases
			SET review_due_at = ?,
			    next_review_at = ?
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, reviewNow.Add(-time.Minute), reviewNow.Add(-time.Minute), teamID, conflictID).Error
	}))
	review := reviewConflictCaseForTest(t, ctx, ledgerRepo, teamID, "worker-overdue-shared-review", conflictID, reviewNow)
	require.Equal(t, ConflictReviewOutcomeOverdue, review.Outcome)

	reservation, _, reserved, err := ledgerRepo.ReserveOverdueConflictAssessment(ctx, ReserveOverdueConflictAssessmentInput{
		TeamID:              teamID,
		ConflictID:          conflictID,
		ReviewRunID:         uuid.NewString(),
		WorkerID:            "worker-overdue-shared-assessment",
		LocalAssessmentDate: reviewNow,
		Model:               "test-model",
		PolicyVersion:       domain.ConflictOverduePolicyVersion,
	})
	require.NoError(t, err)
	require.True(t, reserved)
	require.NotNil(t, reservation)

	preferredPositionID := ""
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT member.position_id::text
			FROM relationship_conflict_position_members AS member
			WHERE member.team_id = ?::uuid
			  AND member.conflict_id = ?::uuid
			  AND member.relationship_id = ?::uuid
			  AND member.active
		`, teamID, conflictID, preferred.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&preferredPositionID)
	}))
	confidence := 0.92
	_, err = ledgerRepo.CompleteOverdueConflictAssessment(ctx, CompleteOverdueConflictAssessmentInput{
		TeamID:              teamID,
		ConflictID:          conflictID,
		AssessmentAttemptID: reservation.AssessmentAttemptID,
		CaseVersion:         reservation.CaseVersion,
		ReviewRunID:         uuid.NewString(),
		Decision:            "selected",
		SelectedPositionID:  preferredPositionID,
		Confidence:          &confidence,
		ProviderTurns:       1,
		ResponseHash:        "sha256:shared-evidence",
	})
	require.NoError(t, err)
	applied, err := ledgerRepo.ApplyOverdueConflictResolution(ctx, ApplyOverdueConflictResolutionInput{
		TeamID:              teamID,
		ConflictID:          conflictID,
		ReviewRunID:         uuid.NewString(),
		WorkerID:            "worker-overdue-shared-apply",
		ExpectedCaseVersion: reservation.CaseVersion,
		PreferredPositionID: preferredPositionID,
		AssessmentAttemptID: reservation.AssessmentAttemptID,
		Method:              "ai",
		Now:                 reviewNow,
	})
	require.NoError(t, err)
	require.True(t, applied.Resolved)
	assert.Empty(t, applied.RetractedEvidenceIDs)
	assert.Empty(t, applied.DerivedEvidence)

	var conflictStatus, losingStatus, unrelatedStatus, evidenceSearchState string
	var losingSupportCount, unrelatedSupportCount, lifecycleCount, derivationCount int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT status
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Row().Scan(&conflictStatus))
		require.NoError(t, tx.Raw(`
			SELECT status, support_count
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, losingRelationshipID).Row().Scan(&losingStatus, &losingSupportCount))
		require.NoError(t, tx.Raw(`
			SELECT status, support_count
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, unrelatedRelationshipID).Row().Scan(&unrelatedStatus, &unrelatedSupportCount))
		require.NoError(t, tx.Raw(`
			SELECT search_state
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND source_kind = 'evidence'
			  AND source_id = ?::uuid
		`, teamID, sharedIngest.Evidence[0].FragmentID).Row().Scan(&evidenceSearchState))
		require.NoError(t, tx.Raw(`
			SELECT count(*)
			FROM evidence_lifecycle_events
			WHERE team_id = ?::uuid
			  AND target_fragment_id = ?::uuid
		`, teamID, sharedIngest.Evidence[0].FragmentID).Scan(&lifecycleCount).Error)
		return tx.Raw(`
			SELECT count(*)
			FROM relationship_conflict_evidence_derivations
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Scan(&derivationCount).Error
	}))
	assert.Equal(t, "resolved", conflictStatus)
	assert.Equal(t, "superseded", losingStatus)
	assert.Equal(t, int64(1), losingSupportCount)
	assert.Equal(t, "active", unrelatedStatus)
	assert.Equal(t, int64(1), unrelatedSupportCount)
	assert.NotEqual(t, "not_required", evidenceSearchState)
	assert.Equal(t, int64(0), lifecycleCount)
	assert.Equal(t, int64(0), derivationCount)
}
