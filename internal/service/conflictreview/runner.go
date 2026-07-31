package conflictreview

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type Runner struct {
	ledger  *repository.LedgerRepositoryImpl
	service *Service
}

func NewRunner(
	ledger *repository.LedgerRepositoryImpl,
	provider Provider,
	timezone string,
	limits verifier.SemanticAssessmentLimits,
) (*Runner, error) {
	if ledger == nil {
		return nil, errors.New("conflict review runner: ledger is required")
	}
	service, err := New(Dependencies{
		Repository: ledger,
		Provider:   provider,
		Timezone:   timezone,
		Limits:     limits,
	})
	if err != nil {
		return nil, err
	}
	return &Runner{ledger: ledger, service: service}, nil
}

func (r *Runner) ReserveRelationshipConflictReviewRun(ctx context.Context, input repository.ConflictReviewRunInput) (*repository.ConflictReviewRunRecord, bool, error) {
	return r.ledger.ReserveRelationshipConflictReviewRun(ctx, input)
}

func (r *Runner) ClaimRelationshipConflictCases(ctx context.Context, input repository.ClaimRelationshipConflictCasesInput) ([]repository.RelationshipConflictCaseRecord, error) {
	return r.ledger.ClaimRelationshipConflictCases(ctx, input)
}

func (r *Runner) ReviewRelationshipConflictCase(ctx context.Context, input repository.ReviewRelationshipConflictCaseInput) (*repository.ReviewRelationshipConflictCaseResult, error) {
	return r.service.ReviewRelationshipConflictCase(ctx, input)
}

func (r *Runner) CompleteRelationshipConflictReviewRun(ctx context.Context, input repository.ConflictReviewRunCompleteInput) error {
	return r.ledger.CompleteRelationshipConflictReviewRun(ctx, input)
}
