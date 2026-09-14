package search

import (
	"context"

	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

// SearchConvergenceReader is the operator-facing projection of document drift.
type SearchConvergenceReader interface {
	GetSearchConvergence(context.Context) (*SearchConvergence, error)
}

type SearchConvergenceRepository interface {
	GetSearchConvergence(context.Context, SearchConvergenceInput) (*SearchConvergence, error)
}

type SearchContractReader interface {
	GetActiveSearchContract(context.Context) (*searchcontract.ActiveSearchContract, error)
}

type searchConvergenceService struct {
	contractReader SearchContractReader
	repo           SearchConvergenceRepository
}

func NewSearchConvergenceService(contractReader SearchContractReader, repo SearchConvergenceRepository) SearchConvergenceReader {
	return &searchConvergenceService{contractReader: contractReader, repo: repo}
}

func (s *searchConvergenceService) GetSearchConvergence(ctx context.Context) (*SearchConvergence, error) {
	if s == nil || s.contractReader == nil || s.repo == nil {
		return nil, ErrSearchConvergenceUnavailable
	}
	contract, err := s.contractReader.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	if contract == nil {
		return nil, ErrSearchConvergenceUnavailable
	}
	return s.repo.GetSearchConvergence(ctx, SearchConvergenceInput{
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions,
		Contract:            contract,
	})
}

var ErrSearchConvergenceUnavailable = serviceError("search convergence unavailable")

type serviceError string

func (e serviceError) Error() string { return string(e) }
