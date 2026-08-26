package service

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/repository"
)

// SearchConvergenceReader is the operator-facing projection of document drift.
type SearchConvergenceReader interface {
	GetSearchConvergence(context.Context) (*repository.SearchConvergence, error)
}

type searchConvergenceService struct {
	repo interface {
		GetSearchConvergence(context.Context, repository.SearchConvergenceInput) (*repository.SearchConvergence, error)
	}
}

func NewSearchConvergenceService(repo interface {
	GetSearchConvergence(context.Context, repository.SearchConvergenceInput) (*repository.SearchConvergence, error)
}) SearchConvergenceReader {
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
