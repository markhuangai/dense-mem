package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSemanticEntityCorrectionMergeDryRunApplyOwnerScoped(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "correction-merge-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "owner-b")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	source := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "person", "Mark")
	survivor := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "person", "Mark Huang")
	denseMem := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")

	ownerAIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerA, "merge-owner-a", "Mark works on Dense-Mem.")
	ownerARelationship := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerA,
		IngestID:        ownerAIngest.IngestID,
		SubjectRef:      "Mark",
		SubjectEntityID: source.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ownerAIngest.Evidence[0].FragmentID,
			SourceGroupKey: "correction:merge:a",
			SpanStart:      0,
			SpanEnd:        len("Mark works on Dense-Mem."),
			Authority:      "primary",
		},
	})
	ownerBIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerB, "merge-owner-b", "Mark works on Dense-Mem.")
	ownerBRelationship := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerB,
		IngestID:        ownerBIngest.IngestID,
		SubjectRef:      "Mark",
		SubjectEntityID: source.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ownerBIngest.Evidence[0].FragmentID,
			SourceGroupKey: "correction:merge:b",
			SpanStart:      0,
			SpanEnd:        len("Mark works on Dense-Mem."),
			Authority:      "primary",
		},
	})

	plan, err := semanticRepo.CorrectEntityResolution(ctx, CorrectEntityResolutionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerA,
		Action:         "merge",
		SourceEntityID: source.EntityID,
		TargetEntityID: survivor.EntityID,
		DryRun:         true,
		IdempotencyKey: "merge-mark",
		Evidence: []CorrectionEvidenceInput{{
			Content: "Mark means Mark Huang for this owner's memory.",
		}},
	})
	require.NoError(t, err)
	require.True(t, plan.DryRun)
	require.NotEmpty(t, plan.PlanToken)
	assert.Equal(t, []string{ownerARelationship.ObservationID}, plan.SelectedObservationIDs)
	assert.Empty(t, plan.BlockedObservationIDs)

	retriedPlan, err := semanticRepo.CorrectEntityResolution(ctx, CorrectEntityResolutionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerA,
		Action:         "merge",
		SourceEntityID: source.EntityID,
		TargetEntityID: survivor.EntityID,
		DryRun:         true,
		IdempotencyKey: "merge-mark",
	})
	require.NoError(t, err)
	assert.Equal(t, plan.PlanToken, retriedPlan.PlanToken)

	applied, err := semanticRepo.CorrectEntityResolution(ctx, CorrectEntityResolutionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerA,
		Action:         "merge",
		SourceEntityID: source.EntityID,
		TargetEntityID: survivor.EntityID,
		DryRun:         false,
		PlanToken:      plan.PlanToken,
	})
	require.NoError(t, err)
	require.False(t, applied.DryRun)
	require.NotEmpty(t, applied.CorrectionEventID)
	assert.Equal(t, plan.SelectedObservationIDs, applied.SelectedObservationIDs)

	appliedAgain, err := semanticRepo.CorrectEntityResolution(ctx, CorrectEntityResolutionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerA,
		Action:         "merge",
		SourceEntityID: source.EntityID,
		TargetEntityID: survivor.EntityID,
		DryRun:         false,
		PlanToken:      plan.PlanToken,
	})
	require.NoError(t, err)
	assert.Equal(t, applied.CorrectionEventID, appliedAgain.CorrectionEventID)

	assertRelationshipSubject(t, ctx, appDB, rls, teamID, ownerA, ownerARelationship.Relationship.RelationshipID, survivor.EntityID)
	assertRelationshipSubject(t, ctx, appDB, rls, teamID, ownerB, ownerBRelationship.Relationship.RelationshipID, source.EntityID)
	assertCorrectionEventCount(t, ctx, appDB, rls, teamID, ownerA, applied.CorrectionEventID, 1)
}

func TestSemanticEntityCorrectionSplitAndStalePlan(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "correction-split-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "owner-b")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	source := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "person", "Mark")
	denseMem := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	billing := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Billing")

	ownerAIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerA, "split-owner-a", "Billing Mark works on Billing.")
	ownerARelationship := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerA,
		IngestID:        ownerAIngest.IngestID,
		SubjectRef:      "Billing Mark",
		SubjectEntityID: source.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  billing.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ownerAIngest.Evidence[0].FragmentID,
			SourceGroupKey: "correction:split:a",
			SpanStart:      0,
			SpanEnd:        len("Billing Mark works on Billing."),
			Authority:      "primary",
		},
	})
	ownerBIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerB, "split-owner-b", "Mark works on Dense-Mem.")
	ownerBRelationship := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerB,
		IngestID:        ownerBIngest.IngestID,
		SubjectRef:      "Mark",
		SubjectEntityID: source.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ownerBIngest.Evidence[0].FragmentID,
			SourceGroupKey: "correction:split:b",
			SpanStart:      0,
			SpanEnd:        len("Mark works on Dense-Mem."),
			Authority:      "primary",
		},
	})

	blocked, err := semanticRepo.CorrectEntityResolution(ctx, CorrectEntityResolutionInput{
		TeamID:                 teamID,
		OwnerProfileID:         ownerA,
		Action:                 "split",
		SourceEntityID:         source.EntityID,
		SelectedObservationIDs: []string{ownerBRelationship.ObservationID},
		DryRun:                 true,
	})
	require.NoError(t, err)
	assert.Empty(t, blocked.SelectedObservationIDs)
	assert.Equal(t, []string{ownerBRelationship.ObservationID}, blocked.BlockedObservationIDs)
	assert.Empty(t, blocked.PlanToken)

	plan, err := semanticRepo.CorrectEntityResolution(ctx, CorrectEntityResolutionInput{
		TeamID:                 teamID,
		OwnerProfileID:         ownerA,
		Action:                 "split",
		SourceEntityID:         source.EntityID,
		SelectedObservationIDs: []string{ownerARelationship.ObservationID},
		DryRun:                 true,
		IdempotencyKey:         "split-billing-mark",
		Evidence: []CorrectionEvidenceInput{{
			Content: "Billing Mark is distinct from Mark on Dense-Mem.",
		}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, plan.PlanToken)
	require.Equal(t, []string{ownerARelationship.ObservationID}, plan.SelectedObservationIDs)

	applied, err := semanticRepo.CorrectEntityResolution(ctx, CorrectEntityResolutionInput{
		TeamID:                 teamID,
		OwnerProfileID:         ownerA,
		Action:                 "split",
		SourceEntityID:         source.EntityID,
		SelectedObservationIDs: []string{ownerARelationship.ObservationID},
		DryRun:                 false,
		PlanToken:              plan.PlanToken,
	})
	require.NoError(t, err)
	require.NotEmpty(t, applied.NewEntityID)
	require.NotEqual(t, source.EntityID, applied.NewEntityID)
	assertRelationshipSubject(t, ctx, appDB, rls, teamID, ownerA, ownerARelationship.Relationship.RelationshipID, applied.NewEntityID)
	assertRelationshipSubject(t, ctx, appDB, rls, teamID, ownerB, ownerBRelationship.Relationship.RelationshipID, source.EntityID)

	staleIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerA, "split-stale-owner-a", "Mark works on Dense-Mem.")
	staleRelationship := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerA,
		IngestID:        staleIngest.IngestID,
		SubjectRef:      "Mark",
		SubjectEntityID: source.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     staleIngest.Evidence[0].FragmentID,
			SourceGroupKey: "correction:split:stale",
			SpanStart:      0,
			SpanEnd:        len("Mark works on Dense-Mem."),
			Authority:      "primary",
		},
	})
	stalePlan, err := semanticRepo.CorrectEntityResolution(ctx, CorrectEntityResolutionInput{
		TeamID:                 teamID,
		OwnerProfileID:         ownerA,
		Action:                 "split",
		SourceEntityID:         source.EntityID,
		SelectedObservationIDs: []string{staleRelationship.ObservationID},
		DryRun:                 true,
	})
	require.NoError(t, err)
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records
			SET metadata = metadata || '{"stale_test": true}'::jsonb,
			    version = version + 1
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
			  AND owner_profile_id = ?::uuid
		`, teamID, staleRelationship.Relationship.RelationshipID, ownerA).Error
	})
	require.NoError(t, err)
	_, err = semanticRepo.CorrectEntityResolution(ctx, CorrectEntityResolutionInput{
		TeamID:                 teamID,
		OwnerProfileID:         ownerA,
		Action:                 "split",
		SourceEntityID:         source.EntityID,
		SelectedObservationIDs: []string{staleRelationship.ObservationID},
		DryRun:                 false,
		PlanToken:              stalePlan.PlanToken,
	})
	require.ErrorIs(t, err, ErrSemanticCorrectionPlanStale)
}

func assertRelationshipSubject(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithTeamProfileTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error
	},
	teamID string,
	ownerID string,
	relationshipID string,
	wantSubjectEntityID string,
) {
	t.Helper()
	var subjectEntityID string
	err := rls.WithTeamProfileTx(ctx, db, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT subject_entity_id::text
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, ownerID, relationshipID).Scan(&subjectEntityID).Error
	})
	require.NoError(t, err)
	assert.Equal(t, wantSubjectEntityID, subjectEntityID)
}

func assertCorrectionEventCount(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithTeamProfileTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error
	},
	teamID string,
	ownerID string,
	correctionEventID string,
	want int64,
) {
	t.Helper()
	var count int64
	err := rls.WithTeamProfileTx(ctx, db, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM entity_correction_events
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND correction_event_id = ?::uuid
		`, teamID, ownerID, correctionEventID).Scan(&count).Error
	})
	require.NoError(t, err)
	assert.Equal(t, want, count)
}
