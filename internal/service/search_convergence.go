package service

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/repository"
)

// SearchConvergenceReader is the operator-facing projection of asynchronous
// embedding progress. It is intentionally separate from structural readiness.
type SearchConvergenceReader interface {
	GetSearchConvergence(context.Context) (*repository.SearchConvergence, error)
}

type searchConvergenceService struct {
	repo repository.EmbeddingReconciliationRepository
}

func NewSearchConvergenceService(repo repository.EmbeddingReconciliationRepository) SearchConvergenceReader {
	return &searchConvergenceService{repo: repo}
}

func (s *searchConvergenceService) GetSearchConvergence(ctx context.Context) (*repository.SearchConvergence, error) {
	if s == nil || s.repo == nil {
		return nil, ErrSearchConvergenceUnavailable
	}
	return s.repo.GetSearchConvergence(ctx, repository.SearchConvergenceInput{})
}

var ErrSearchConvergenceUnavailable = serviceError("search convergence unavailable")

type serviceError string

func (e serviceError) Error() string { return string(e) }
