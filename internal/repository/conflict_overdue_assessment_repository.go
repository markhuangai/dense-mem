package repository

import (
	"context"
	"errors"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

const (
	ConflictAssessmentMaxFailedDays = 5
	conflictResolutionMaxFragments  = 200
)

var (
	ErrConflictAssessmentUnavailable = knowledgecontract.ErrConflictAssessmentUnavailable
	ErrConflictAssessmentStale       = knowledgecontract.ErrConflictAssessmentStale
	ErrConflictAssessmentReserved    = knowledgecontract.ErrConflictAssessmentReserved
)

type ReserveOverdueConflictAssessmentInput = knowledgecontract.ReserveOverdueConflictAssessmentInput
type OverdueConflictAssessmentReservation = knowledgecontract.OverdueConflictAssessmentReservation
type OverdueConflictAssessmentDossier = knowledgecontract.OverdueConflictAssessmentDossier
type OverdueConflictAssessmentPosition = knowledgecontract.OverdueConflictAssessmentPosition
type OverdueConflictAssessmentEvidence = knowledgecontract.OverdueConflictAssessmentEvidence
type CompleteOverdueConflictAssessmentInput = knowledgecontract.CompleteOverdueConflictAssessmentInput
type CompleteOverdueConflictAssessmentResult = knowledgecontract.CompleteOverdueConflictAssessmentResult
type ApplyOverdueConflictResolutionInput = knowledgecontract.ApplyOverdueConflictResolutionInput
type ApplyOverdueConflictResolutionResult = knowledgecontract.ApplyOverdueConflictResolutionResult
type ResumePendingOverdueConflictResolutionInput = knowledgecontract.ResumePendingOverdueConflictResolutionInput
type ConflictDerivedEvidenceTarget = knowledgecontract.ConflictDerivedEvidenceTarget
type ClaimConflictDerivedEvidenceTasksInput = knowledgecontract.ClaimConflictDerivedEvidenceTasksInput
type StageConflictDerivedEvidenceResult = knowledgecontract.StageConflictDerivedEvidenceResult

func (r *LedgerRepositoryImpl) ReserveOverdueConflictAssessment(
	ctx context.Context,
	input ReserveOverdueConflictAssessmentInput,
) (*OverdueConflictAssessmentReservation, *OverdueConflictAssessmentDossier, bool, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, nil, false, errors.New("conflict review: knowledge write owner is required")
	}
	return owner.ReserveOverdueConflictAssessment(ctx, input)
}

func (r *LedgerRepositoryImpl) CompleteOverdueConflictAssessment(
	ctx context.Context,
	input CompleteOverdueConflictAssessmentInput,
) (*CompleteOverdueConflictAssessmentResult, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("conflict review: knowledge write owner is required")
	}
	return owner.CompleteOverdueConflictAssessment(ctx, input)
}
