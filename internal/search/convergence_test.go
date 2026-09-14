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
	input SearchConvergenceInput
}

func (s *searchConvergenceRepoStub) GetSearchConvergence(_ context.Context, input SearchConvergenceInput) (*SearchConvergence, error) {
	s.input = input
	return s.value, s.err
}

type searchContractReaderStub struct {
	contract *ActiveSearchContract
	err      error
}

func (s searchContractReaderStub) GetActiveSearchContract(context.Context) (*ActiveSearchContract, error) {
	return s.contract, s.err
}

func TestSearchConvergenceServiceDelegatesAndFailsClosed(t *testing.T) {
	value := &SearchConvergence{Status: "converged"}
	projection := &searchConvergenceRepoStub{value: value}
	contract := &ActiveSearchContract{EmbeddingContractID: "11111111-1111-1111-1111-111111111111", EmbeddingDimensions: 3}
	reader := NewSearchConvergenceService(searchContractReaderStub{contract: contract}, projection)
	got, err := reader.GetSearchConvergence(context.Background())
	require.NoError(t, err)
	require.Same(t, value, got)
	require.Same(t, contract, projection.input.Contract)
	require.Equal(t, contract.EmbeddingContractID, projection.input.EmbeddingContractID)
	require.Equal(t, contract.EmbeddingDimensions, projection.input.EmbeddingDimensions)

	reader = NewSearchConvergenceService(searchContractReaderStub{contract: contract}, &searchConvergenceRepoStub{err: errors.New("database details")})
	_, err = reader.GetSearchConvergence(context.Background())
	require.EqualError(t, err, "database details")
	_, err = (*searchConvergenceService)(nil).GetSearchConvergence(context.Background())
	require.ErrorIs(t, err, ErrSearchConvergenceUnavailable)
	reader = NewSearchConvergenceService(nil, projection)
	_, err = reader.GetSearchConvergence(context.Background())
	require.ErrorIs(t, err, ErrSearchConvergenceUnavailable)
	require.EqualError(t, ErrSearchConvergenceUnavailable, "search convergence unavailable")
}
