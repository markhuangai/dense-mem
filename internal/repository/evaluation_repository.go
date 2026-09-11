//go:build evaluation

package repository

import (
	"context"
	"errors"

	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	evaluationpostgres "github.com/markhuangai/dense-mem/internal/evaluation/postgres"
)

// EvaluationReader and its query policy now live in the evaluation-owned
// PostgreSQL adapter. These aliases preserve the legacy repository surface
// until the final compatibility cleanup in #382.
type EvaluationReader = evaluationpostgres.EvaluationReader
type EvaluationHypothesisQuery = evaluationpostgres.EvaluationHypothesisQuery
type EvaluationRepository = dreamcontract.EvaluationRepository
type EvaluationListInput = dreamcontract.EvaluationListInput
type EvaluationGetInput = dreamcontract.EvaluationGetInput
type EvaluationPage = dreamcontract.EvaluationPage

var _ EvaluationRepository = (*SemanticRepositoryImpl)(nil)

func (r *SemanticRepositoryImpl) ListEvaluationRefs(ctx context.Context, input EvaluationListInput) (*EvaluationPage, error) {
	reader := r.evaluationReader()
	if reader == nil {
		return nil, errors.New("semantic: database is required")
	}
	return reader.ListEvaluationRefs(ctx, input)
}

func (r *SemanticRepositoryImpl) GetEvaluationItem(ctx context.Context, input EvaluationGetInput) (map[string]any, error) {
	reader := r.evaluationReader()
	if reader == nil {
		return nil, errors.New("semantic: database is required")
	}
	return reader.GetEvaluationItem(ctx, input)
}

func (r *SemanticRepositoryImpl) evaluationReader() *EvaluationReader {
	if r == nil {
		return nil
	}
	return newCompatibilityEvaluationReader(r.db, r.rls)
}
