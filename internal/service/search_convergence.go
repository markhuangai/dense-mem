package service

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/repository"
	searchapp "github.com/markhuangai/dense-mem/internal/search"
)

// Search convergence names remain a compatibility facade over the native
// search application service.
type SearchConvergenceReader interface {
	GetSearchConvergence(context.Context) (*repository.SearchConvergence, error)
}

type SearchConvergenceRepository interface {
	GetSearchConvergence(context.Context, repository.SearchConvergenceInput) (*repository.SearchConvergence, error)
}

type searchConvergenceService struct {
	delegate SearchConvergenceRepository
}

func NewSearchConvergenceService(repo SearchConvergenceRepository) SearchConvergenceReader {
	return &searchConvergenceService{delegate: repo}
}

func (s *searchConvergenceService) GetSearchConvergence(ctx context.Context) (*repository.SearchConvergence, error) {
	if s == nil || s.delegate == nil {
		return nil, ErrSearchConvergenceUnavailable
	}
	return s.delegate.GetSearchConvergence(ctx, repository.SearchConvergenceInput{})
}

var ErrSearchConvergenceUnavailable = searchapp.ErrSearchConvergenceUnavailable

// NewSearchConvergenceCompatibility adapts the native search projection to
// the retained control-portal service shape without reimplementing policy.
func NewSearchConvergenceCompatibility(native searchapp.SearchConvergenceReader) SearchConvergenceReader {
	return &nativeSearchConvergenceCompatibility{native: native}
}

type nativeSearchConvergenceCompatibility struct {
	native searchapp.SearchConvergenceReader
}

func (s *nativeSearchConvergenceCompatibility) GetSearchConvergence(ctx context.Context) (*repository.SearchConvergence, error) {
	if s == nil || s.native == nil {
		return nil, ErrSearchConvergenceUnavailable
	}
	value, err := s.native.GetSearchConvergence(ctx)
	if err != nil || value == nil {
		return nil, err
	}
	result := &repository.SearchConvergence{
		ObservedAt: value.ObservedAt, Status: value.Status, Contract: value.Contract,
		ExpectedDocuments: value.ExpectedDocuments, CurrentDocuments: value.CurrentDocuments,
		DriftedDocuments: value.DriftedDocuments, AffectedTeamCount: value.AffectedTeamCount,
		OldestDriftAge: value.OldestDriftAge, DriftClasses: make([]repository.SearchDocumentDriftCount, len(value.DriftClasses)),
	}
	for index, drift := range value.DriftClasses {
		result.DriftClasses[index] = repository.SearchDocumentDriftCount{Class: drift.Class, Count: drift.Count}
	}
	if value.LatestRun != nil {
		result.LatestRun = &repository.SearchReconciliationRun{
			RunID: value.LatestRun.RunID, LocalRunDate: value.LatestRun.LocalRunDate, Status: value.LatestRun.Status,
			SelectedCount: value.LatestRun.SelectedCount, EmbeddedCount: value.LatestRun.EmbeddedCount,
			UpdatedCount: value.LatestRun.UpdatedCount, DriftedCount: value.LatestRun.DriftedCount,
			LastError: value.LatestRun.LastError, StartedAt: value.LatestRun.StartedAt,
			CompletedAt: value.LatestRun.CompletedAt, UpdatedAt: value.LatestRun.UpdatedAt,
		}
	}
	return result, nil
}
