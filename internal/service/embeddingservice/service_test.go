package embeddingservice

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestEmbeddingWorkerRequiresDependenciesAndScope(t *testing.T) {
	validDeps := func() EmbeddingWorkerDependencies {
		return EmbeddingWorkerDependencies{
			Search:   newEmbeddingSearchStub(),
			Provider: &embeddingProviderStub{available: true, model: "test-model", dims: 3},
			TeamID:   "team-a",
			WorkerID: "worker-a",
		}
	}
	tests := []struct {
		name string
		edit func(*EmbeddingWorkerDependencies)
	}{
		{name: "missing_search", edit: func(deps *EmbeddingWorkerDependencies) { deps.Search = nil }},
		{name: "missing_provider", edit: func(deps *EmbeddingWorkerDependencies) { deps.Provider = nil }},
		{name: "missing_team", edit: func(deps *EmbeddingWorkerDependencies) { deps.TeamID = " " }},
		{name: "missing_worker", edit: func(deps *EmbeddingWorkerDependencies) { deps.WorkerID = " " }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := validDeps()
			tt.edit(&deps)
			worker := NewEmbeddingWorkerService(deps)
			result, err := worker.ProcessNextBatch(context.Background())
			if err == nil {
				t.Fatal("ProcessNextBatch returned nil error")
			}
			if result != (EmbeddingWorkerResult{}) {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestEmbeddingWorkerHandlesNoClaimedJobsAndClaimErrors(t *testing.T) {
	search := newEmbeddingSearchStub()
	worker := NewEmbeddingWorkerService(EmbeddingWorkerDependencies{
		Search:    search,
		Provider:  &embeddingProviderStub{available: true, model: "test-model", dims: 3},
		TeamID:    "team-a",
		WorkerID:  "worker-a",
		BatchSize: 200,
	})

	result, err := worker.ProcessNextBatch(context.Background())
	if err != nil {
		t.Fatalf("ProcessNextBatch returned error: %v", err)
	}
	if result.Claimed != 0 {
		t.Fatalf("result = %#v", result)
	}
	if search.claimLimit != 100 {
		t.Fatalf("claim limit = %d, want capped 100", search.claimLimit)
	}

	search.claimErr = errors.New("claim failed")
	result, err = worker.ProcessNextBatch(context.Background())
	if err == nil {
		t.Fatal("ProcessNextBatch returned nil error")
	}
	if result != (EmbeddingWorkerResult{}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestEmbeddingWorkerCompletesValidBatch(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.jobs = []repository.EmbeddingJob{
		embeddingJobForTest("job-a", 1),
		embeddingJobForTest("job-b", 2),
	}
	provider := &embeddingProviderStub{
		available: true,
		model:     "test-model",
		dims:      3,
		vectors: [][]float32{
			{1, 0, 0},
			{0, 1, 0},
		},
	}
	worker := NewEmbeddingWorkerService(EmbeddingWorkerDependencies{
		Search:   search,
		Provider: provider,
		TeamID:   "team-a",
		WorkerID: "worker-a",
	})

	result, err := worker.ProcessNextBatch(context.Background())
	if err != nil {
		t.Fatalf("ProcessNextBatch returned error: %v", err)
	}
	if result.Claimed != 2 || result.Completed != 2 || result.Failed != 0 {
		t.Fatalf("result = %#v", result)
	}
	if len(search.completeInputs) != 2 {
		t.Fatalf("completed jobs = %d, want 2", len(search.completeInputs))
	}
	if search.completeInputs[0].ExpectedAttempts != 1 || search.completeInputs[1].ExpectedAttempts != 2 {
		t.Fatalf("complete attempt fences = %#v", search.completeInputs)
	}
	if len(provider.gotTexts) != 2 || provider.gotTexts[0] != "document job-a" {
		t.Fatalf("provider texts = %#v", provider.gotTexts)
	}
}

func TestEmbeddingWorkerFailsProviderCountMismatch(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.jobs = []repository.EmbeddingJob{
		embeddingJobForTest("job-a", 1),
		embeddingJobForTest("job-b", 1),
	}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	worker := NewEmbeddingWorkerService(EmbeddingWorkerDependencies{
		Search: search,
		Provider: &embeddingProviderStub{
			available: true,
			model:     "test-model",
			dims:      3,
			vectors:   [][]float32{{1, 0, 0}},
		},
		Metrics:  metrics,
		TeamID:   "team-a",
		WorkerID: "worker-a",
	})

	result, err := worker.ProcessNextBatch(context.Background())
	if err == nil {
		t.Fatal("ProcessNextBatch returned nil error")
	}
	if result.Failed != 2 || result.Completed != 0 {
		t.Fatalf("result = %#v", result)
	}
	if len(search.failInputs) != 2 {
		t.Fatalf("failed jobs = %d, want 2", len(search.failInputs))
	}
	for _, input := range search.failInputs {
		if !input.Terminal || input.RetryAfter != 0 {
			t.Fatalf("failure input = %#v", input)
		}
	}
	if got := metrics.EmbeddingErrorCount("count_mismatch"); got != 2 {
		t.Fatalf("count_mismatch metric = %d, want 2", got)
	}
}

func TestEmbeddingWorkerRejectsInvalidVectorsWithoutDroppingValidJobs(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.jobs = []repository.EmbeddingJob{
		embeddingJobForTest("job-a", 1),
		embeddingJobForTest("job-b", 1),
	}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	worker := NewEmbeddingWorkerService(EmbeddingWorkerDependencies{
		Search: search,
		Provider: &embeddingProviderStub{
			available: true,
			model:     "test-model",
			dims:      3,
			vectors: [][]float32{
				{1, 0, 0},
				{float32(math.NaN()), 0, 0},
			},
		},
		Metrics:  metrics,
		TeamID:   "team-a",
		WorkerID: "worker-a",
	})

	result, err := worker.ProcessNextBatch(context.Background())
	if err == nil {
		t.Fatal("ProcessNextBatch returned nil error")
	}
	if result.Completed != 1 || result.Failed != 1 {
		t.Fatalf("result = %#v", result)
	}
	if len(search.completeInputs) != 1 || len(search.failInputs) != 1 {
		t.Fatalf("complete=%d fail=%d", len(search.completeInputs), len(search.failInputs))
	}
	if !search.failInputs[0].Terminal {
		t.Fatalf("failure input = %#v", search.failInputs[0])
	}
	if got := metrics.EmbeddingErrorCount("invalid_vector"); got != 1 {
		t.Fatalf("invalid_vector metric = %d, want 1", got)
	}
}

func TestEmbeddingWorkerClassifiesProviderFailures(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantCode   string
		wantFailed int
		wantRetry  int
		wantTerm   bool
		wantAfter  time.Duration
	}{
		{
			name:      "timeout",
			err:       &embedding.TimeoutError{Provider: "stub", Message: "deadline"},
			wantCode:  "timeout",
			wantRetry: 1,
		},
		{
			name:      "cancelled",
			err:       context.Canceled,
			wantCode:  "cancelled",
			wantRetry: 1,
		},
		{
			name:      "rate_limited",
			err:       &embedding.RateLimitError{Provider: "stub", Message: "429", RetryAfter: 7},
			wantCode:  "rate_limited",
			wantRetry: 1,
			wantAfter: 7 * time.Second,
		},
		{
			name:      "http_429",
			err:       &embedding.ProviderHTTPError{Status: 429, RetryAfter: 3 * time.Second},
			wantCode:  "rate_limited",
			wantRetry: 1,
			wantAfter: 3 * time.Second,
		},
		{
			name:       "permanent_http_400",
			err:        &embedding.ProviderHTTPError{Status: 400, Message: "bad input"},
			wantCode:   "provider_permanent",
			wantFailed: 1,
			wantTerm:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search := newEmbeddingSearchStub()
			search.jobs = []repository.EmbeddingJob{embeddingJobForTest("job-a", 1)}
			metrics := observability.NewInMemoryDiscoverabilityMetrics()
			worker := NewEmbeddingWorkerService(EmbeddingWorkerDependencies{
				Search: search,
				Provider: &embeddingProviderStub{
					available: true,
					model:     "test-model",
					dims:      3,
					err:       tt.err,
				},
				Metrics:  metrics,
				TeamID:   "team-a",
				WorkerID: "worker-a",
			})

			result, err := worker.ProcessNextBatch(context.Background())
			if err == nil {
				t.Fatal("ProcessNextBatch returned nil error")
			}
			if result.Failed != tt.wantFailed || result.Retried != tt.wantRetry {
				t.Fatalf("result = %#v", result)
			}
			if len(search.failInputs) != 1 {
				t.Fatalf("failed jobs = %d, want 1", len(search.failInputs))
			}
			if search.failInputs[0].Terminal != tt.wantTerm {
				t.Fatalf("failure input = %#v", search.failInputs[0])
			}
			if search.failInputs[0].RetryAfter != tt.wantAfter {
				t.Fatalf("retry_after = %s, want %s", search.failInputs[0].RetryAfter, tt.wantAfter)
			}
			if got := metrics.EmbeddingErrorCount(tt.wantCode); got != 1 {
				t.Fatalf("%s metric = %d, want 1", tt.wantCode, got)
			}
		})
	}
}

func TestEmbeddingWorkerFailsContractMismatchBeforeProviderCall(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.jobs = []repository.EmbeddingJob{embeddingJobForTest("job-a", 1)}
	provider := &embeddingProviderStub{
		available: true,
		model:     "wrong-model",
		dims:      3,
		vectors:   [][]float32{{1, 0, 0}},
	}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	worker := NewEmbeddingWorkerService(EmbeddingWorkerDependencies{
		Search:   search,
		Provider: provider,
		Metrics:  metrics,
		TeamID:   "team-a",
		WorkerID: "worker-a",
	})

	result, err := worker.ProcessNextBatch(context.Background())
	if err == nil {
		t.Fatal("ProcessNextBatch returned nil error")
	}
	if len(provider.gotTexts) != 0 {
		t.Fatalf("provider was called with %#v", provider.gotTexts)
	}
	if result.Failed != 1 || len(search.failInputs) != 1 || !search.failInputs[0].Terminal {
		t.Fatalf("result = %#v failInputs=%#v", result, search.failInputs)
	}
	if got := metrics.EmbeddingErrorCount("contract_mismatch"); got != 1 {
		t.Fatalf("contract_mismatch metric = %d, want 1", got)
	}
}

func TestEmbeddingWorkerFailsDimensionMismatchBeforeProviderCall(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.jobs = []repository.EmbeddingJob{embeddingJobForTest("job-a", 1)}
	provider := &embeddingProviderStub{
		available: true,
		model:     "test-model",
		dims:      4,
		vectors:   [][]float32{{1, 0, 0}},
	}
	worker := NewEmbeddingWorkerService(EmbeddingWorkerDependencies{
		Search:   search,
		Provider: provider,
		TeamID:   "team-a",
		WorkerID: "worker-a",
	})

	result, err := worker.ProcessNextBatch(context.Background())
	if err == nil {
		t.Fatal("ProcessNextBatch returned nil error")
	}
	if len(provider.gotTexts) != 0 {
		t.Fatalf("provider was called with %#v", provider.gotTexts)
	}
	if result.Failed != 1 || len(search.failInputs) != 1 || !search.failInputs[0].Terminal {
		t.Fatalf("result = %#v failInputs=%#v", result, search.failInputs)
	}
}

func TestEmbeddingWorkerMarksIneligibleJobContractMismatch(t *testing.T) {
	search := newEmbeddingSearchStub()
	job := embeddingJobForTest("job-a", 1)
	job.EmbeddingContractID = "old-contract"
	search.jobs = []repository.EmbeddingJob{job}
	provider := &embeddingProviderStub{
		available: true,
		model:     "test-model",
		dims:      3,
		vectors:   [][]float32{{1, 0, 0}},
	}
	worker := NewEmbeddingWorkerService(EmbeddingWorkerDependencies{
		Search:   search,
		Provider: provider,
		TeamID:   "team-a",
		WorkerID: "worker-a",
	})

	result, err := worker.ProcessNextBatch(context.Background())
	if err != nil {
		t.Fatalf("ProcessNextBatch returned error: %v", err)
	}
	if len(provider.gotTexts) != 0 {
		t.Fatalf("provider was called with %#v", provider.gotTexts)
	}
	if result.Failed != 1 || len(search.failInputs) != 1 || !search.failInputs[0].Terminal {
		t.Fatalf("result = %#v failInputs=%#v", result, search.failInputs)
	}
}

func TestEmbeddingWorkerTreatsUnavailableProviderAsRetryable(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.jobs = []repository.EmbeddingJob{embeddingJobForTest("job-a", 1)}
	provider := &embeddingProviderStub{
		available: false,
		model:     "test-model",
		dims:      3,
		vectors:   [][]float32{{1, 0, 0}},
	}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	worker := NewEmbeddingWorkerService(EmbeddingWorkerDependencies{
		Search:   search,
		Provider: provider,
		Metrics:  metrics,
		TeamID:   "team-a",
		WorkerID: "worker-a",
	})

	result, err := worker.ProcessNextBatch(context.Background())
	if err == nil {
		t.Fatal("ProcessNextBatch returned nil error")
	}
	if len(provider.gotTexts) != 0 {
		t.Fatalf("provider was called with %#v", provider.gotTexts)
	}
	if result.Retried != 1 || len(search.failInputs) != 1 || search.failInputs[0].Terminal {
		t.Fatalf("result = %#v failInputs=%#v", result, search.failInputs)
	}
	if got := metrics.EmbeddingErrorCount("provider_unavailable"); got != 1 {
		t.Fatalf("provider_unavailable metric = %d, want 1", got)
	}
}

func TestEmbeddingWorkerCountsStaleAndLeaseLostCompletions(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.jobs = []repository.EmbeddingJob{
		embeddingJobForTest("job-stale", 1),
		embeddingJobForTest("job-lease", 1),
	}
	search.completeErrs["job-stale"] = repository.ErrSearchStaleVersion
	search.completeErrs["job-lease"] = repository.ErrEmbeddingLeaseLost
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	worker := NewEmbeddingWorkerService(EmbeddingWorkerDependencies{
		Search: search,
		Provider: &embeddingProviderStub{
			available: true,
			model:     "test-model",
			dims:      3,
			vectors: [][]float32{
				{1, 0, 0},
				{0, 1, 0},
			},
		},
		Metrics:  metrics,
		TeamID:   "team-a",
		WorkerID: "worker-a",
	})

	result, err := worker.ProcessNextBatch(context.Background())
	if err != nil {
		t.Fatalf("ProcessNextBatch returned error: %v", err)
	}
	if result.Stale != 1 || result.LeaseLost != 1 {
		t.Fatalf("result = %#v", result)
	}
	if got := metrics.EmbeddingErrorCount("stale"); got != 1 {
		t.Fatalf("stale metric = %d, want 1", got)
	}
	if got := metrics.EmbeddingErrorCount("lease_lost"); got != 1 {
		t.Fatalf("lease_lost metric = %d, want 1", got)
	}
}

func newEmbeddingSearchStub() *embeddingSearchStub {
	return &embeddingSearchStub{
		contract: repository.ActiveSearchContract{
			EmbeddingContractID: "contract-a",
			EmbeddingDimensions: 3,
			EmbeddingModel:      "test-model",
		},
		completeErrs: map[string]error{},
	}
}

func embeddingJobForTest(id string, attempts int) repository.EmbeddingJob {
	return repository.EmbeddingJob{
		TeamID:              "team-a",
		EmbeddingJobID:      id,
		SearchDocumentID:    "doc-" + id,
		OwnerProfileID:      "owner-a",
		SourceKind:          "evidence",
		SourceID:            "source-" + id,
		SourceVersion:       1,
		DocumentVersion:     1,
		EmbeddingContractID: "contract-a",
		EmbeddingDimensions: 3,
		Status:              "processing",
		Attempts:            attempts,
		DocumentText:        "document " + id,
	}
}

type embeddingSearchStub struct {
	contract       repository.ActiveSearchContract
	jobs           []repository.EmbeddingJob
	completeInputs []repository.CompleteEmbeddingJobInput
	failInputs     []repository.FailEmbeddingJobInput
	completeErrs   map[string]error
	claimErr       error
	claimLimit     int
}

func (s *embeddingSearchStub) GetActiveSearchContract(context.Context) (*repository.ActiveSearchContract, error) {
	return &s.contract, nil
}

func (s *embeddingSearchStub) CheckSearchReadiness(context.Context) (*repository.SearchReadiness, error) {
	return &repository.SearchReadiness{Ready: true, Contract: &s.contract}, nil
}

func (s *embeddingSearchStub) UpsertSearchDocument(context.Context, repository.UpsertSearchDocumentInput) (*repository.SearchDocumentResult, error) {
	return nil, errors.New("not implemented")
}

func (s *embeddingSearchStub) ClaimEmbeddingJobs(_ context.Context, input repository.ClaimEmbeddingJobsInput) ([]repository.EmbeddingJob, error) {
	s.claimLimit = input.Limit
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	return s.jobs, nil
}

func (s *embeddingSearchStub) CompleteEmbeddingJob(_ context.Context, input repository.CompleteEmbeddingJobInput) error {
	s.completeInputs = append(s.completeInputs, input)
	if err := s.completeErrs[input.EmbeddingJobID]; err != nil {
		return err
	}
	return nil
}

func (s *embeddingSearchStub) FailEmbeddingJob(_ context.Context, input repository.FailEmbeddingJobInput) (*repository.EmbeddingJobFailureResult, error) {
	s.failInputs = append(s.failInputs, input)
	status := "queued"
	if input.Terminal {
		status = "failed"
	}
	return &repository.EmbeddingJobFailureResult{Status: status, Terminal: input.Terminal}, nil
}

func (s *embeddingSearchStub) GetEmbeddingQueueStats(context.Context, repository.EmbeddingQueueStatsInput) (*repository.EmbeddingQueueStats, error) {
	return &repository.EmbeddingQueueStats{}, nil
}

func (s *embeddingSearchStub) SearchFullText(context.Context, repository.FullTextSearchInput) ([]repository.SearchHit, error) {
	return nil, errors.New("not implemented")
}

func (s *embeddingSearchStub) SearchExactVector(context.Context, repository.ExactVectorSearchInput) ([]repository.SearchHit, error) {
	return nil, errors.New("not implemented")
}

type embeddingProviderStub struct {
	available bool
	model     string
	dims      int
	vectors   [][]float32
	err       error
	gotTexts  []string
}

func (p *embeddingProviderStub) Embed(context.Context, string) ([]float32, string, error) {
	return nil, "", errors.New("not implemented")
}

func (p *embeddingProviderStub) EmbedBatch(_ context.Context, texts []string) ([][]float32, string, error) {
	p.gotTexts = append([]string(nil), texts...)
	if p.err != nil {
		return nil, "", p.err
	}
	return p.vectors, p.model, nil
}

func (p *embeddingProviderStub) ModelName() string {
	return p.model
}

func (p *embeddingProviderStub) Dimensions() int {
	return p.dims
}

func (p *embeddingProviderStub) IsAvailable() bool {
	return p.available
}

var _ repository.SearchRepository = (*embeddingSearchStub)(nil)
var _ embedding.EmbeddingProviderInterface = (*embeddingProviderStub)(nil)
