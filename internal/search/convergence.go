package search

import (
	"context"
)

// SearchConvergenceReader is the operator-facing projection of document drift.
type SearchConvergenceReader interface {
	GetSearchConvergence(context.Context) (*SearchConvergence, error)
}

type SearchConvergenceRepository interface {
	GetSearchConvergence(context.Context, SearchConvergenceInput) (*SearchConvergence, error)
}

type searchConvergenceService struct {
	repo SearchConvergenceRepository
}

func NewSearchConvergenceService(repo SearchConvergenceRepository) SearchConvergenceReader {
	return &searchConvergenceService{repo: repo}
}

func (s *searchConvergenceService) GetSearchConvergence(ctx context.Context) (*SearchConvergence, error) {
	if s == nil || s.repo == nil {
		return nil, ErrSearchConvergenceUnavailable
	}
	return s.repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
}

var ErrSearchConvergenceUnavailable = serviceError("search convergence unavailable")

type serviceError string

func (e serviceError) Error() string { return string(e) }
