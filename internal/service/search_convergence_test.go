package service

import (
	"context"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/stretchr/testify/require"
)

type searchConvergenceRepositoryStub struct {
	value *repository.SearchConvergence
	err   error
}

func (s searchConvergenceRepositoryStub) GetSearchConvergence(context.Context, repository.SearchConvergenceInput) (*repository.SearchConvergence, error) {
	return s.value, s.err
}

func (searchConvergenceRepositoryStub) ReserveEmbeddingReconciliationRun(context.Context, repository.ReserveEmbeddingReconciliationRunInput) (*repository.EmbeddingReconciliationRun, bool, error) {
	return nil, false, nil
}
func (searchConvergenceRepositoryStub) SelectEmbeddingReconciliationCanary(context.Context, repository.SelectEmbeddingReconciliationCanaryInput) (*repository.EmbeddingJob, error) {
	return nil, nil
}
func (searchConvergenceRepositoryStub) MarkEmbeddingReconciliationCanaryAttempt(context.Context, repository.MarkEmbeddingReconciliationCanaryAttemptInput) error {
	return nil
}
func (searchConvergenceRepositoryStub) CompleteEmbeddingReconciliationCanary(context.Context, repository.CompleteEmbeddingReconciliationCanaryInput) error {
	return nil
}
func (searchConvergenceRepositoryStub) ResetEmbeddingReconciliationCanary(context.Context, repository.ResetEmbeddingReconciliationCanaryInput) error {
	return nil
}
func (searchConvergenceRepositoryStub) RequeueEmbeddingReconciliationJobs(context.Context, repository.RequeueEmbeddingReconciliationJobsInput) (int64, error) {
	return 0, nil
}
func (searchConvergenceRepositoryStub) CompleteEmbeddingReconciliationRun(context.Context, repository.CompleteEmbeddingReconciliationRunInput) error {
	return nil
}

func TestSearchConvergenceServiceDelegatesAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	var nilService *searchConvergenceService
	_, err := nilService.GetSearchConvergence(ctx)
	require.ErrorIs(t, err, ErrSearchConvergenceUnavailable)

	_, err = NewSearchConvergenceService(nil).GetSearchConvergence(ctx)
	require.ErrorIs(t, err, ErrSearchConvergenceUnavailable)

	want := &repository.SearchConvergence{Status: "recovering"}
	got, err := NewSearchConvergenceService(searchConvergenceRepositoryStub{value: want}).GetSearchConvergence(ctx)
	require.NoError(t, err)
	require.Same(t, want, got)

	upstreamErr := errors.New("projection unavailable")
	_, err = NewSearchConvergenceService(searchConvergenceRepositoryStub{err: upstreamErr}).GetSearchConvergence(ctx)
	require.ErrorIs(t, err, upstreamErr)
}
