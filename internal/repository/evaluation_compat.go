package repository

import (
	"gorm.io/gorm"

	dreampostgres "github.com/markhuangai/dense-mem/internal/dream/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// The legacy SemanticRepository surface remains a bounded compatibility
// forward until the final compatibility cleanup in #382.
func newCompatibilityEvaluationReader(db *gorm.DB, rls storagepostgres.RLSHelper) *EvaluationReader {
	return NewEvaluationReader(db, rls, compatibilityHypothesisQuery)
}

func compatibilityHypothesisQuery(input EvaluationListInput, limit, offset int, ids ...string) (string, []any, error) {
	return dreampostgres.HypothesisEvaluationQuery(input, limit, offset, ids...)
}
