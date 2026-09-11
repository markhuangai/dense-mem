package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var errPersistenceStub = errors.New("persistence stub")

type persistenceStub struct{}

func (persistenceStub) ReserveRelationshipConflictReviewRun(context.Context, ConflictReviewRunInput) (*ConflictReviewRunRecord, bool, error) {
	return nil, false, errPersistenceStub
}
func (persistenceStub) ClaimRelationshipConflictCases(context.Context, ClaimRelationshipConflictCasesInput) ([]RelationshipConflictCaseRecord, error) {
	return nil, errPersistenceStub
}
func (persistenceStub) ReleaseRelationshipConflictCaseClaim(context.Context, ReleaseRelationshipConflictCaseClaimInput) error {
	return errPersistenceStub
}
func (persistenceStub) ReviewRelationshipConflictCase(context.Context, ReviewRelationshipConflictCaseInput) (*ReviewRelationshipConflictCaseResult, error) {
	return nil, errPersistenceStub
}
func (persistenceStub) CompleteRelationshipConflictReviewRun(context.Context, ConflictReviewRunCompleteInput) error {
	return errPersistenceStub
}
func (persistenceStub) ResumePendingOverdueConflictResolution(context.Context, ResumePendingOverdueConflictResolutionInput) (*RelationshipConflictResolutionInput, bool, error) {
	return nil, false, errPersistenceStub
}
func (persistenceStub) ReserveOverdueConflictAssessment(context.Context, ReserveOverdueConflictAssessmentInput) (*OverdueConflictAssessmentReservation, *OverdueConflictAssessmentDossier, bool, error) {
	return nil, nil, false, errPersistenceStub
}
func (persistenceStub) CompleteOverdueConflictAssessment(context.Context, CompleteOverdueConflictAssessmentInput) (*CompleteOverdueConflictAssessmentResult, error) {
	return nil, errPersistenceStub
}
func (persistenceStub) PlanRelationshipConflictResolution(context.Context, RelationshipConflictResolutionInput) (*RelationshipConflictResolutionPlan, error) {
	return nil, errPersistenceStub
}
func (persistenceStub) CommitRelationshipConflictResolution(context.Context, CommitRelationshipConflictResolutionInput) (*ApplyOverdueConflictResolutionResult, error) {
	return nil, errPersistenceStub
}
func (persistenceStub) ClaimConflictDerivedEvidenceTasks(context.Context, ClaimConflictDerivedEvidenceTasksInput) ([]ConflictDerivedEvidenceTarget, error) {
	return nil, errPersistenceStub
}
func (persistenceStub) StageConflictDerivedEvidence(context.Context, ConflictDerivedEvidenceTarget) (*StageConflictDerivedEvidenceResult, error) {
	return nil, errPersistenceStub
}
func (persistenceStub) RecordConflictDerivedEvidenceFailure(context.Context, ConflictDerivedEvidenceTarget, string) error {
	return errPersistenceStub
}
func (persistenceStub) ListEvidenceConflicts(context.Context, EvidenceConflictListInput) (*EvidenceConflictListResult, error) {
	return nil, errPersistenceStub
}
func (persistenceStub) GetEvidenceConflict(context.Context, EvidenceConflictGetInput) (*EvidenceConflictGetResult, error) {
	return nil, errPersistenceStub
}
func (persistenceStub) ResolveEvidenceConflict(context.Context, EvidenceConflictResolutionInput) (*EvidenceConflictCaseRecord, error) {
	return nil, errPersistenceStub
}

var _ Persistence = persistenceStub{}

func TestStoreDelegatesWorkflowAndEvidencePorts(t *testing.T) {
	store := NewStore(nil, nil, persistenceStub{})
	ctx := context.Background()

	_, _, err := store.ReserveRelationshipConflictReviewRun(ctx, ConflictReviewRunInput{})
	require.ErrorIs(t, err, errPersistenceStub)
	_, err = store.ClaimRelationshipConflictCases(ctx, ClaimRelationshipConflictCasesInput{})
	require.ErrorIs(t, err, errPersistenceStub)
	require.ErrorIs(t, store.ReleaseRelationshipConflictCaseClaim(ctx, ReleaseRelationshipConflictCaseClaimInput{}), errPersistenceStub)
	_, err = store.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{})
	require.ErrorIs(t, err, errPersistenceStub)
	require.ErrorIs(t, store.CompleteRelationshipConflictReviewRun(ctx, ConflictReviewRunCompleteInput{}), errPersistenceStub)
	_, _, err = store.ResumePendingOverdueConflictResolution(ctx, ResumePendingOverdueConflictResolutionInput{})
	require.ErrorIs(t, err, errPersistenceStub)
	_, _, _, err = store.ReserveOverdueConflictAssessment(ctx, ReserveOverdueConflictAssessmentInput{})
	require.ErrorIs(t, err, errPersistenceStub)
	_, err = store.CompleteOverdueConflictAssessment(ctx, CompleteOverdueConflictAssessmentInput{})
	require.ErrorIs(t, err, errPersistenceStub)
	_, err = store.PlanRelationshipConflictResolution(ctx, RelationshipConflictResolutionInput{})
	require.ErrorIs(t, err, errPersistenceStub)
	_, err = store.CommitRelationshipConflictResolution(ctx, CommitRelationshipConflictResolutionInput{})
	require.ErrorIs(t, err, errPersistenceStub)
	_, err = store.ClaimConflictDerivedEvidenceTasks(ctx, ClaimConflictDerivedEvidenceTasksInput{})
	require.ErrorIs(t, err, errPersistenceStub)
	_, err = store.StageConflictDerivedEvidence(ctx, ConflictDerivedEvidenceTarget{})
	require.ErrorIs(t, err, errPersistenceStub)
	require.ErrorIs(t, store.RecordConflictDerivedEvidenceFailure(ctx, ConflictDerivedEvidenceTarget{}, "failed"), errPersistenceStub)
	_, err = store.ResolveEvidenceConflict(ctx, EvidenceConflictResolutionInput{})
	require.ErrorIs(t, err, errPersistenceStub)
}

func TestStoreWorkflowMethodsRequirePersistence(t *testing.T) {
	store := NewStore(nil, nil, nil)
	ctx := context.Background()
	require.Error(t, func() error {
		_, _, err := store.ReserveRelationshipConflictReviewRun(ctx, ConflictReviewRunInput{})
		return err
	}())
	require.Error(t, func() error {
		_, err := store.ClaimRelationshipConflictCases(ctx, ClaimRelationshipConflictCasesInput{})
		return err
	}())
	require.Error(t, store.ReleaseRelationshipConflictCaseClaim(ctx, ReleaseRelationshipConflictCaseClaimInput{}))
	require.Error(t, func() error {
		_, err := store.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{})
		return err
	}())
	require.Error(t, store.CompleteRelationshipConflictReviewRun(ctx, ConflictReviewRunCompleteInput{}))
	require.Error(t, func() error {
		_, _, err := store.ResumePendingOverdueConflictResolution(ctx, ResumePendingOverdueConflictResolutionInput{})
		return err
	}())
	require.Error(t, func() error {
		_, _, _, err := store.ReserveOverdueConflictAssessment(ctx, ReserveOverdueConflictAssessmentInput{})
		return err
	}())
	require.Error(t, func() error {
		_, err := store.CompleteOverdueConflictAssessment(ctx, CompleteOverdueConflictAssessmentInput{})
		return err
	}())
	require.Error(t, func() error {
		_, err := store.PlanRelationshipConflictResolution(ctx, RelationshipConflictResolutionInput{})
		return err
	}())
	require.Error(t, func() error {
		_, err := store.CommitRelationshipConflictResolution(ctx, CommitRelationshipConflictResolutionInput{})
		return err
	}())
	require.Error(t, func() error {
		_, err := store.ClaimConflictDerivedEvidenceTasks(ctx, ClaimConflictDerivedEvidenceTasksInput{})
		return err
	}())
	require.Error(t, func() error {
		_, err := store.StageConflictDerivedEvidence(ctx, ConflictDerivedEvidenceTarget{})
		return err
	}())
	require.Error(t, store.RecordConflictDerivedEvidenceFailure(ctx, ConflictDerivedEvidenceTarget{}, "failed"))
}

func TestStoreTransactionGuardsRejectMissingDatabaseOrRLS(t *testing.T) {
	ctx := context.Background()
	var nilStore *Store
	require.Error(t, nilStore.withSystemTx(ctx, func(*gorm.DB) error { return nil }))
	require.Error(t, nilStore.withSystemReadOnlyRepeatableTx(ctx, func(*gorm.DB) error { return nil }))
	require.Error(t, NewStore(nil, nil, nil).withSystemTx(ctx, func(*gorm.DB) error { return nil }))
	require.Error(t, NewStore(nil, nil, nil).withSystemReadOnlyRepeatableTx(ctx, func(*gorm.DB) error { return nil }))
	require.Error(t, NewStore(&gorm.DB{}, nil, nil).withSystemTx(ctx, func(*gorm.DB) error { return nil }))
	require.Error(t, NewStore(&gorm.DB{}, nil, nil).withSystemReadOnlyRepeatableTx(ctx, func(*gorm.DB) error { return nil }))
	_, err := nilStore.persistenceRequired()
	require.Error(t, err)
}
