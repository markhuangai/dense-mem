package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestV2SemanticEntityCorrectionMergeDryRunApplyOwnerScoped(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "correction-merge-team")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamID, "owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamID, "owner-b")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	source := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "person", "Mark")
	survivor := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "person", "Mark Huang")
	denseMem := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")

	ownerAIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerA, "merge-owner-a", "Mark works on Dense-Mem.")
	ownerARelationship := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerA,
		IngestID:        ownerAIngest.IngestID,
		SubjectRef:      "Mark",
		SubjectEntityID: source.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     ownerAIngest.Evidence[0].FragmentID,
			SourceGroupKey: "correction:merge:a",
			SpanStart:      0,
			SpanEnd:        len("Mark works on Dense-Mem."),
			Authority:      "primary",
		},
	})
	ownerBIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerB, "merge-owner-b", "Mark works on Dense-Mem.")
	ownerBRelationship := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerB,
		IngestID:        ownerBIngest.IngestID,
		SubjectRef:      "Mark",
		SubjectEntityID: source.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     ownerBIngest.Evidence[0].FragmentID,
			SourceGroupKey: "correction:merge:b",
			SpanStart:      0,
			SpanEnd:        len("Mark works on Dense-Mem."),
			Authority:      "primary",
		},
	})

	plan, err := semanticRepo.CorrectEntityResolution(ctx, V2CorrectEntityResolutionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerA,
		Action:         "merge",
		SourceEntityID: source.EntityID,
		TargetEntityID: survivor.EntityID,
		DryRun:         true,
		IdempotencyKey: "merge-mark",
		Evidence: []V2CorrectionEvidenceInput{{
			Content: "Mark means Mark Huang for this owner's memory.",
		}},
	})
	require.NoError(t, err)
	require.True(t, plan.DryRun)
	require.NotEmpty(t, plan.PlanToken)
	assert.Equal(t, []string{ownerARelationship.ObservationID}, plan.SelectedObservationIDs)
	assert.Empty(t, plan.BlockedObservationIDs)

	retriedPlan, err := semanticRepo.CorrectEntityResolution(ctx, V2CorrectEntityResolutionInput{
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

	applied, err := semanticRepo.CorrectEntityResolution(ctx, V2CorrectEntityResolutionInput{
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

	appliedAgain, err := semanticRepo.CorrectEntityResolution(ctx, V2CorrectEntityResolutionInput{
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

	assertV2RelationshipSubject(t, ctx, appDB, rls, teamID, ownerA, ownerARelationship.Relationship.RelationshipID, survivor.EntityID)
	assertV2RelationshipSubject(t, ctx, appDB, rls, teamID, ownerB, ownerBRelationship.Relationship.RelationshipID, source.EntityID)
	assertV2CorrectionEventCount(t, ctx, appDB, rls, teamID, ownerA, applied.CorrectionEventID, 1)
}

func TestV2SemanticEntityCorrectionSplitAndStalePlan(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "correction-split-team")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamID, "owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamID, "owner-b")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	source := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "person", "Mark")
	denseMem := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	billing := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Billing")

	ownerAIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerA, "split-owner-a", "Billing Mark works on Billing.")
	ownerARelationship := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerA,
		IngestID:        ownerAIngest.IngestID,
		SubjectRef:      "Billing Mark",
		SubjectEntityID: source.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  billing.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     ownerAIngest.Evidence[0].FragmentID,
			SourceGroupKey: "correction:split:a",
			SpanStart:      0,
			SpanEnd:        len("Billing Mark works on Billing."),
			Authority:      "primary",
		},
	})
	ownerBIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerB, "split-owner-b", "Mark works on Dense-Mem.")
	ownerBRelationship := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerB,
		IngestID:        ownerBIngest.IngestID,
		SubjectRef:      "Mark",
		SubjectEntityID: source.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     ownerBIngest.Evidence[0].FragmentID,
			SourceGroupKey: "correction:split:b",
			SpanStart:      0,
			SpanEnd:        len("Mark works on Dense-Mem."),
			Authority:      "primary",
		},
	})

	blocked, err := semanticRepo.CorrectEntityResolution(ctx, V2CorrectEntityResolutionInput{
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

	plan, err := semanticRepo.CorrectEntityResolution(ctx, V2CorrectEntityResolutionInput{
		TeamID:                 teamID,
		OwnerProfileID:         ownerA,
		Action:                 "split",
		SourceEntityID:         source.EntityID,
		SelectedObservationIDs: []string{ownerARelationship.ObservationID},
		DryRun:                 true,
		IdempotencyKey:         "split-billing-mark",
		Evidence: []V2CorrectionEvidenceInput{{
			Content: "Billing Mark is distinct from Mark on Dense-Mem.",
		}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, plan.PlanToken)
	require.Equal(t, []string{ownerARelationship.ObservationID}, plan.SelectedObservationIDs)

	applied, err := semanticRepo.CorrectEntityResolution(ctx, V2CorrectEntityResolutionInput{
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
	assertV2RelationshipSubject(t, ctx, appDB, rls, teamID, ownerA, ownerARelationship.Relationship.RelationshipID, applied.NewEntityID)
	assertV2RelationshipSubject(t, ctx, appDB, rls, teamID, ownerB, ownerBRelationship.Relationship.RelationshipID, source.EntityID)

	staleIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerA, "split-stale-owner-a", "Mark works on Dense-Mem.")
	staleRelationship := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerA,
		IngestID:        staleIngest.IngestID,
		SubjectRef:      "Mark",
		SubjectEntityID: source.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     staleIngest.Evidence[0].FragmentID,
			SourceGroupKey: "correction:split:stale",
			SpanStart:      0,
			SpanEnd:        len("Mark works on Dense-Mem."),
			Authority:      "primary",
		},
	})
	stalePlan, err := semanticRepo.CorrectEntityResolution(ctx, V2CorrectEntityResolutionInput{
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
	_, err = semanticRepo.CorrectEntityResolution(ctx, V2CorrectEntityResolutionInput{
		TeamID:                 teamID,
		OwnerProfileID:         ownerA,
		Action:                 "split",
		SourceEntityID:         source.EntityID,
		SelectedObservationIDs: []string{staleRelationship.ObservationID},
		DryRun:                 false,
		PlanToken:              stalePlan.PlanToken,
	})
	require.ErrorIs(t, err, ErrV2SemanticCorrectionPlanStale)
}

func assertV2RelationshipSubject(
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

func assertV2CorrectionEventCount(
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
