package service

import (
	"context"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestSearchReconciliationRunsOneBatchAndFencesEveryDocument(t *testing.T) {
	contractID := "11111111-1111-1111-1111-111111111111"
	teamA := "22222222-2222-2222-2222-222222222222"
	teamB := "33333333-3333-3333-3333-333333333333"
	ownerA := "44444444-4444-4444-4444-444444444444"
	ownerB := "55555555-5555-5555-5555-555555555555"
	repo := &searchReconciliationRepositoryStub{
		contract: &repository.ActiveSearchContract{EmbeddingContractID: contractID, EmbeddingDimensions: 2, EmbeddingModel: "model"},
		run:      &repository.SearchReconciliationRun{RunID: "66666666-6666-6666-6666-666666666666", Status: "running"},
		documents: []repository.SearchDocumentForEmbedding{
			{SearchDocumentResult: repository.SearchDocumentResult{TeamID: teamA, SearchDocumentID: "77777777-7777-7777-7777-777777777777", OwnerProfileID: ownerA, SourceVersion: 2, ProjectionFormat: 2, DocumentVersion: 3, EmbeddingContractID: contractID, EmbeddingDimensions: 2, SpaceGeneration: 1}, DocumentText: "same", DocumentHash: "hash-a"},
			{SearchDocumentResult: repository.SearchDocumentResult{TeamID: teamB, SearchDocumentID: "88888888-8888-8888-8888-888888888888", OwnerProfileID: ownerB, SourceVersion: 4, ProjectionFormat: 2, DocumentVersion: 2, EmbeddingContractID: contractID, EmbeddingDimensions: 2, SpaceGeneration: 1}, DocumentText: "same", DocumentHash: "hash-a"},
		},
	}
	provider := &searchReconciliationProviderStub{
		model: "model", dimensions: 2,
		embedBatch: func(_ context.Context, texts []string) ([][]float32, string, error) {
			require.Equal(t, []string{"same"}, texts)
			return [][]float32{{0.25, 0.75}}, "model", nil
		},
	}
	svc := NewSearchReconciliationService(SearchReconciliationDependencies{Repository: repo, Provider: provider})

	result, err := svc.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.EqualValues(t, 2, result.SelectedCount)
	require.EqualValues(t, 1, result.EmbeddedCount)
	require.EqualValues(t, 2, result.UpdatedCount)
	require.Equal(t, 1, provider.batchCalls)
	require.Len(t, repo.applied.Documents, 2)
	require.Equal(t, "hash-a", repo.applied.Documents[0].DocumentHash)
	require.Equal(t, "hash-a", repo.applied.Documents[1].DocumentHash)
	require.Equal(t, "completed", repo.finished.Status)
}

func TestSearchReconciliationProviderFailureLeavesDocumentsUnchanged(t *testing.T) {
	repo := &searchReconciliationRepositoryStub{
		contract: &repository.ActiveSearchContract{EmbeddingContractID: "11111111-1111-1111-1111-111111111111", EmbeddingDimensions: 2, EmbeddingModel: "model"},
		run:      &repository.SearchReconciliationRun{RunID: "66666666-6666-6666-6666-666666666666", Status: "running"},
		documents: []repository.SearchDocumentForEmbedding{{
			SearchDocumentResult: repository.SearchDocumentResult{TeamID: "22222222-2222-2222-2222-222222222222", SearchDocumentID: "77777777-7777-7777-7777-777777777777", OwnerProfileID: "44444444-4444-4444-4444-444444444444", SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, EmbeddingContractID: "11111111-1111-1111-1111-111111111111", EmbeddingDimensions: 2, SpaceGeneration: 1}, DocumentText: "drift", DocumentHash: "hash"},
		},
	}
	provider := &searchReconciliationProviderStub{model: "model", dimensions: 2, embedBatch: func(context.Context, []string) ([][]float32, string, error) {
		return nil, "", errors.New("provider failure")
	}}
	svc := NewSearchReconciliationService(SearchReconciliationDependencies{Repository: repo, Provider: provider})

	result, err := svc.Run(context.Background())
	require.ErrorIs(t, err, ErrSearchReconciliationFailed)
	require.Equal(t, "failed", result.Status)
	require.Equal(t, "embedding_provider_failed", result.ErrorCode)
	require.Nil(t, repo.applied)
	require.Equal(t, "failed", repo.finished.Status)
}

func TestSearchReconciliationRetiredDocumentsFinalizeWithoutEmbedding(t *testing.T) {
	repo := &searchReconciliationRepositoryStub{
		contract: &repository.ActiveSearchContract{EmbeddingContractID: "11111111-1111-1111-1111-111111111111", EmbeddingDimensions: 2, EmbeddingModel: "model"},
		run:      &repository.SearchReconciliationRun{RunID: "66666666-6666-6666-6666-666666666666", Status: "running"},
		documents: []repository.SearchDocumentForEmbedding{{
			SearchDocumentResult: repository.SearchDocumentResult{
				TeamID: "22222222-2222-2222-2222-222222222222", SearchDocumentID: "77777777-7777-7777-7777-777777777777",
				OwnerProfileID: "44444444-4444-4444-4444-444444444444", SourceVersion: 1, ProjectionFormat: 1,
				DocumentVersion: 1, EmbeddingContractID: "11111111-1111-1111-1111-111111111111", EmbeddingDimensions: 2,
				SpaceGeneration: 1,
			},
			DocumentText: "retired", DocumentHash: "hash", Retired: true,
		}},
	}
	provider := &searchReconciliationProviderStub{model: "model", dimensions: 2, embedBatch: func(context.Context, []string) ([][]float32, string, error) {
		t.Fatalf("retired document should not call the embedding provider")
		return nil, "", nil
	}}
	svc := NewSearchReconciliationService(SearchReconciliationDependencies{Repository: repo, Provider: provider})

	result, err := svc.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Zero(t, result.EmbeddedCount)
	require.EqualValues(t, 1, result.UpdatedCount)
	require.Equal(t, 0, provider.batchCalls)
	require.Len(t, repo.applied.Documents, 1)
	require.True(t, repo.applied.Documents[0].Retired)
}

func TestSearchReconciliationSkipsWhenAnotherRunOwnsTheWindow(t *testing.T) {
	repo := &searchReconciliationRepositoryStub{
		contract:       &repository.ActiveSearchContract{EmbeddingContractID: "11111111-1111-1111-1111-111111111111", EmbeddingDimensions: 2, EmbeddingModel: "model"},
		run:            &repository.SearchReconciliationRun{RunID: "66666666-6666-6666-6666-666666666666", Status: "running"},
		reserveClaimed: false, reserveClaimedSet: true,
	}
	svc := NewSearchReconciliationService(SearchReconciliationDependencies{Repository: repo, Provider: &searchReconciliationProviderStub{model: "model", dimensions: 2}})
	result, err := svc.Run(context.Background())
	require.NoError(t, err)
	require.True(t, result.Skipped)
	require.Equal(t, "skipped", result.Status)
	require.Empty(t, repo.finished.Status)
}

func TestSearchReconciliationRejectsContractAndDocumentDrift(t *testing.T) {
	baseContract := &repository.ActiveSearchContract{EmbeddingContractID: "11111111-1111-1111-1111-111111111111", EmbeddingDimensions: 2, EmbeddingModel: "model"}
	for _, test := range []struct {
		name     string
		repo     *searchReconciliationRepositoryStub
		provider *searchReconciliationProviderStub
		wantCode string
	}{
		{name: "contract lookup", repo: &searchReconciliationRepositoryStub{contractErr: errors.New("db")}, provider: &searchReconciliationProviderStub{model: "model", dimensions: 2}, wantCode: ""},
		{name: "reservation", repo: &searchReconciliationRepositoryStub{contract: baseContract, reserveErr: errors.New("db")}, provider: &searchReconciliationProviderStub{model: "model", dimensions: 2}, wantCode: ""},
		{name: "selection", repo: &searchReconciliationRepositoryStub{contract: baseContract, run: &repository.SearchReconciliationRun{RunID: "66666666-6666-6666-6666-666666666666"}, selectErr: errors.New("db")}, provider: &searchReconciliationProviderStub{model: "model", dimensions: 2}, wantCode: "reconciliation_selection_failed"},
		{name: "invalid document", repo: &searchReconciliationRepositoryStub{contract: baseContract, run: &repository.SearchReconciliationRun{RunID: "66666666-6666-6666-6666-666666666666"}, documents: []repository.SearchDocumentForEmbedding{{DocumentHash: "", DocumentText: "text"}}}, provider: &searchReconciliationProviderStub{model: "model", dimensions: 2}, wantCode: "reconciliation_snapshot_invalid"},
		{name: "provider unavailable", repo: &searchReconciliationRepositoryStub{contract: baseContract, run: &repository.SearchReconciliationRun{RunID: "66666666-6666-6666-6666-666666666666"}, documents: []repository.SearchDocumentForEmbedding{{DocumentHash: "hash", DocumentText: "text"}}}, provider: &searchReconciliationProviderStub{model: "model", dimensions: 2, available: false, availableSet: true}, wantCode: "embedding_unavailable"},
		{name: "model mismatch", repo: &searchReconciliationRepositoryStub{contract: baseContract, run: &repository.SearchReconciliationRun{RunID: "66666666-6666-6666-6666-666666666666"}, documents: []repository.SearchDocumentForEmbedding{{DocumentHash: "hash", DocumentText: "text"}}}, provider: &searchReconciliationProviderStub{model: "other", dimensions: 2}, wantCode: "embedding_contract_mismatch"},
		{name: "dimensions mismatch", repo: &searchReconciliationRepositoryStub{contract: baseContract, run: &repository.SearchReconciliationRun{RunID: "66666666-6666-6666-6666-666666666666"}, documents: []repository.SearchDocumentForEmbedding{{DocumentHash: "hash", DocumentText: "text"}}}, provider: &searchReconciliationProviderStub{model: "model", dimensions: 3}, wantCode: "embedding_contract_mismatch"},
		{name: "invalid response", repo: &searchReconciliationRepositoryStub{contract: baseContract, run: &repository.SearchReconciliationRun{RunID: "66666666-6666-6666-6666-666666666666"}, documents: []repository.SearchDocumentForEmbedding{{DocumentHash: "hash", DocumentText: "text"}}}, provider: &searchReconciliationProviderStub{model: "model", dimensions: 2, embedBatch: func(context.Context, []string) ([][]float32, string, error) { return [][]float32{{1}}, "model", nil }}, wantCode: "embedding_response_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.repo.run != nil && test.repo.reserveClaimed == false && !test.repo.reserveClaimedSet {
				test.repo.reserveClaimed = true
			}
			svc := NewSearchReconciliationService(SearchReconciliationDependencies{Repository: test.repo, Provider: test.provider})
			result, err := svc.Run(context.Background())
			if test.wantCode == "" {
				require.Error(t, err)
				return
			}
			require.ErrorIs(t, err, ErrSearchReconciliationFailed)
			require.Equal(t, test.wantCode, result.ErrorCode)
		})
	}
}

func TestSearchReconciliationReportsCommitAndFinalizationFailures(t *testing.T) {
	contract := &repository.ActiveSearchContract{EmbeddingContractID: "11111111-1111-1111-1111-111111111111", EmbeddingDimensions: 2, EmbeddingModel: "model"}
	base := func() *searchReconciliationRepositoryStub {
		return &searchReconciliationRepositoryStub{
			contract: contract, run: &repository.SearchReconciliationRun{RunID: "66666666-6666-6666-6666-666666666666"},
			documents: []repository.SearchDocumentForEmbedding{{DocumentHash: "hash", DocumentText: "text"}},
		}
	}
	repo := base()
	repo.completeErr = errors.New("commit failed")
	provider := &searchReconciliationProviderStub{model: "model", dimensions: 2, embedBatch: func(context.Context, []string) ([][]float32, string, error) { return [][]float32{{1, 2}}, "model", nil }}
	result, err := NewSearchReconciliationService(SearchReconciliationDependencies{Repository: repo, Provider: provider}).Run(context.Background())
	require.ErrorIs(t, err, ErrSearchReconciliationFailed)
	require.Equal(t, "reconciliation_commit_failed", result.ErrorCode)

	repo = base()
	repo.finishErr = errors.New("finalize failed")
	result, err = NewSearchReconciliationService(SearchReconciliationDependencies{Repository: repo, Provider: provider}).Run(context.Background())
	require.ErrorIs(t, err, ErrSearchReconciliationFailed)
	require.Empty(t, result.ErrorCode)
}

type searchReconciliationRepositoryStub struct {
	contract          *repository.ActiveSearchContract
	contractErr       error
	run               *repository.SearchReconciliationRun
	reserveErr        error
	reserveClaimed    bool
	reserveClaimedSet bool
	documents         []repository.SearchDocumentForEmbedding
	selectErr         error
	applied           *repository.ApplySearchReconciliationInput
	completeErr       error
	finished          repository.FinishSearchReconciliationRunInput
	finishErr         error
}

func (s *searchReconciliationRepositoryStub) GetActiveSearchContract(context.Context) (*repository.ActiveSearchContract, error) {
	return s.contract, s.contractErr
}

func (s *searchReconciliationRepositoryStub) CheckSearchReadiness(context.Context) (*repository.SearchReadiness, error) {
	return &repository.SearchReadiness{Ready: true, Contract: s.contract}, nil
}

func (s *searchReconciliationRepositoryStub) SearchFullText(context.Context, repository.FullTextSearchInput) ([]repository.SearchHit, error) {
	return nil, nil
}

func (s *searchReconciliationRepositoryStub) SearchExactVector(context.Context, repository.ExactVectorSearchInput) ([]repository.SearchHit, error) {
	return nil, nil
}

func (s *searchReconciliationRepositoryStub) ReserveSearchReconciliationRun(context.Context, repository.SearchReconciliationRunInput) (*repository.SearchReconciliationRun, bool, error) {
	claimed := true
	if s.reserveClaimedSet {
		claimed = s.reserveClaimed
	}
	return s.run, claimed, s.reserveErr
}

func (s *searchReconciliationRepositoryStub) SelectSearchReconciliationDocuments(context.Context, repository.SearchReconciliationSelectionInput) ([]repository.SearchDocumentForEmbedding, error) {
	return s.documents, s.selectErr
}

func (s *searchReconciliationRepositoryStub) CompleteSearchReconciliationDocuments(_ context.Context, input repository.ApplySearchReconciliationInput) (*repository.SearchReconciliationApplyResult, error) {
	s.applied = &input
	return &repository.SearchReconciliationApplyResult{UpdatedCount: int64(len(input.Documents))}, s.completeErr
}

func (s *searchReconciliationRepositoryStub) FinishSearchReconciliationRun(_ context.Context, input repository.FinishSearchReconciliationRunInput) error {
	s.finished = input
	return s.finishErr
}

type searchReconciliationProviderStub struct {
	model        string
	dimensions   int
	available    bool
	availableSet bool
	batchCalls   int
	embedBatch   func(context.Context, []string) ([][]float32, string, error)
}

func (s *searchReconciliationProviderStub) Embed(context.Context, string) ([]float32, string, error) {
	return nil, "", errors.New("single embedding call is not allowed")
}

func (s *searchReconciliationProviderStub) EmbedBatch(ctx context.Context, texts []string) ([][]float32, string, error) {
	s.batchCalls++
	return s.embedBatch(ctx, texts)
}

func (s *searchReconciliationProviderStub) ModelName() string { return s.model }
func (s *searchReconciliationProviderStub) Dimensions() int   { return s.dimensions }
func (s *searchReconciliationProviderStub) IsAvailable() bool {
	if s.availableSet {
		return s.available
	}
	return true
}

var _ repository.SearchReconciliationRepository = (*searchReconciliationRepositoryStub)(nil)
