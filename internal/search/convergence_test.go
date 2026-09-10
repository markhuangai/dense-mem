package search

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type searchConvergenceRepoStub struct {
	value *SearchConvergence
	err   error
}

func (s searchConvergenceRepoStub) GetSearchConvergence(context.Context, SearchConvergenceInput) (*SearchConvergence, error) {
	return s.value, s.err
}

func TestSearchConvergenceServiceDelegatesAndFailsClosed(t *testing.T) {
	value := &SearchConvergence{Status: "converged"}
	reader := NewSearchConvergenceService(searchConvergenceRepoStub{value: value})
	got, err := reader.GetSearchConvergence(context.Background())
	require.NoError(t, err)
	require.Same(t, value, got)

	reader = NewSearchConvergenceService(searchConvergenceRepoStub{err: errors.New("database details")})
	_, err = reader.GetSearchConvergence(context.Background())
	require.EqualError(t, err, "database details")
	_, err = (*searchConvergenceService)(nil).GetSearchConvergence(context.Background())
	require.ErrorIs(t, err, ErrSearchConvergenceUnavailable)
	reader = NewSearchConvergenceService(nil)
	_, err = reader.GetSearchConvergence(context.Background())
	require.ErrorIs(t, err, ErrSearchConvergenceUnavailable)
	require.EqualError(t, ErrSearchConvergenceUnavailable, "search convergence unavailable")
}
