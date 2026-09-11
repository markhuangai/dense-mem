package postgres

import (
	"context"
	"errors"

	"gorm.io/gorm"

	conflictcontract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// Persistence combines the Conflict workflow port with the cited-evidence
// mutation port. Canonical terminal effects remain implemented by Knowledge;
// this adapter supplies Conflict-owned reads and transaction boundaries.
type Persistence interface {
	conflictcontract.ReviewRunLedger
	conflictcontract.EvidenceConflictRepository
}

type Store struct {
	db          *gorm.DB
	rls         storagepostgres.RLSHelper
	persistence Persistence
}

func NewStore(db *gorm.DB, rls storagepostgres.RLSHelper, persistence Persistence) *Store {
	return &Store{db: db, rls: rls, persistence: persistence}
}

func (r *Store) withSystemTx(ctx context.Context, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("conflict: database is required")
	}
	if r.rls == nil {
		return errors.New("conflict: rls helper is required")
	}
	return r.rls.WithSystemTx(ctx, r.db, fn)
}

func (r *Store) withSystemReadOnlyRepeatableTx(ctx context.Context, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("conflict: database is required")
	}
	if r.rls == nil {
		return errors.New("conflict: rls helper is required")
	}
	return r.rls.WithSystemReadOnlyRepeatableTx(ctx, r.db, fn)
}

func (r *Store) persistenceRequired() (Persistence, error) {
	if r == nil || r.persistence == nil {
		return nil, errors.New("conflict: persistence owner is required")
	}
	return r.persistence, nil
}

func (r *Store) ReserveRelationshipConflictReviewRun(ctx context.Context, input ConflictReviewRunInput) (*ConflictReviewRunRecord, bool, error) {
	persistence, err := r.persistenceRequired()
	if err != nil {
		return nil, false, err
	}
	return persistence.ReserveRelationshipConflictReviewRun(ctx, input)
}

func (r *Store) ClaimRelationshipConflictCases(ctx context.Context, input ClaimRelationshipConflictCasesInput) ([]RelationshipConflictCaseRecord, error) {
	persistence, err := r.persistenceRequired()
	if err != nil {
		return nil, err
	}
	return persistence.ClaimRelationshipConflictCases(ctx, input)
}

func (r *Store) ReleaseRelationshipConflictCaseClaim(ctx context.Context, input ReleaseRelationshipConflictCaseClaimInput) error {
	persistence, err := r.persistenceRequired()
	if err != nil {
		return err
	}
	return persistence.ReleaseRelationshipConflictCaseClaim(ctx, input)
}

func (r *Store) ReviewRelationshipConflictCase(ctx context.Context, input ReviewRelationshipConflictCaseInput) (*ReviewRelationshipConflictCaseResult, error) {
	persistence, err := r.persistenceRequired()
	if err != nil {
		return nil, err
	}
	return persistence.ReviewRelationshipConflictCase(ctx, input)
}

func (r *Store) CompleteRelationshipConflictReviewRun(ctx context.Context, input ConflictReviewRunCompleteInput) error {
	persistence, err := r.persistenceRequired()
	if err != nil {
		return err
	}
	return persistence.CompleteRelationshipConflictReviewRun(ctx, input)
}

func (r *Store) ResumePendingOverdueConflictResolution(ctx context.Context, input ResumePendingOverdueConflictResolutionInput) (*RelationshipConflictResolutionInput, bool, error) {
	persistence, err := r.persistenceRequired()
	if err != nil {
		return nil, false, err
	}
	return persistence.ResumePendingOverdueConflictResolution(ctx, input)
}

func (r *Store) ReserveOverdueConflictAssessment(ctx context.Context, input ReserveOverdueConflictAssessmentInput) (*OverdueConflictAssessmentReservation, *OverdueConflictAssessmentDossier, bool, error) {
	persistence, err := r.persistenceRequired()
	if err != nil {
		return nil, nil, false, err
	}
	return persistence.ReserveOverdueConflictAssessment(ctx, input)
}

func (r *Store) CompleteOverdueConflictAssessment(ctx context.Context, input CompleteOverdueConflictAssessmentInput) (*CompleteOverdueConflictAssessmentResult, error) {
	persistence, err := r.persistenceRequired()
	if err != nil {
		return nil, err
	}
	return persistence.CompleteOverdueConflictAssessment(ctx, input)
}

func (r *Store) PlanRelationshipConflictResolution(ctx context.Context, input RelationshipConflictResolutionInput) (*RelationshipConflictResolutionPlan, error) {
	persistence, err := r.persistenceRequired()
	if err != nil {
		return nil, err
	}
	return persistence.PlanRelationshipConflictResolution(ctx, input)
}

func (r *Store) CommitRelationshipConflictResolution(ctx context.Context, input CommitRelationshipConflictResolutionInput) (*ApplyOverdueConflictResolutionResult, error) {
	persistence, err := r.persistenceRequired()
	if err != nil {
		return nil, err
	}
	return persistence.CommitRelationshipConflictResolution(ctx, input)
}

func (r *Store) ClaimConflictDerivedEvidenceTasks(ctx context.Context, input ClaimConflictDerivedEvidenceTasksInput) ([]ConflictDerivedEvidenceTarget, error) {
	persistence, err := r.persistenceRequired()
	if err != nil {
		return nil, err
	}
	return persistence.ClaimConflictDerivedEvidenceTasks(ctx, input)
}

func (r *Store) StageConflictDerivedEvidence(ctx context.Context, target ConflictDerivedEvidenceTarget) (*StageConflictDerivedEvidenceResult, error) {
	persistence, err := r.persistenceRequired()
	if err != nil {
		return nil, err
	}
	return persistence.StageConflictDerivedEvidence(ctx, target)
}

func (r *Store) RecordConflictDerivedEvidenceFailure(ctx context.Context, target ConflictDerivedEvidenceTarget, failureClass string) error {
	persistence, err := r.persistenceRequired()
	if err != nil {
		return err
	}
	return persistence.RecordConflictDerivedEvidenceFailure(ctx, target, failureClass)
}

var _ conflictcontract.ReviewRunLedger = (*Store)(nil)
var _ conflictcontract.EvidenceConflictRepository = (*Store)(nil)
