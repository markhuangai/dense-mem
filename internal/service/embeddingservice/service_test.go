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

func TestV2EmbeddingWorkerRequiresDependenciesAndScope(t *testing.T) {
	validDeps := func() V2EmbeddingWorkerDependencies {
		return V2EmbeddingWorkerDependencies{
			Search:   newV2EmbeddingSearchStub(),
			Provider: &v2EmbeddingProviderStub{available: true, model: "test-model", dims: 3},
			TeamID:   "team-a",
			WorkerID: "worker-a",
		}
	}
	tests := []struct {
		name string
		edit func(*V2EmbeddingWorkerDependencies)
	}{
		{name: "missing_search", edit: func(deps *V2EmbeddingWorkerDependencies) { deps.Search = nil }},
		{name: "missing_provider", edit: func(deps *V2EmbeddingWorkerDependencies) { deps.Provider = nil }},
		{name: "missing_team", edit: func(deps *V2EmbeddingWorkerDependencies) { deps.TeamID = " " }},
		{name: "missing_worker", edit: func(deps *V2EmbeddingWorkerDependencies) { deps.WorkerID = " " }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := validDeps()
			tt.edit(&deps)
			worker := NewV2EmbeddingWorkerService(deps)
			result, err := worker.ProcessNextBatch(context.Background())
			if err == nil {
				t.Fatal("ProcessNextBatch returned nil error")
			}
			if result != (V2EmbeddingWorkerResult{}) {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestV2EmbeddingWorkerHandlesNoClaimedJobsAndClaimErrors(t *testing.T) {
	search := newV2EmbeddingSearchStub()
	worker := NewV2EmbeddingWorkerService(V2EmbeddingWorkerDependencies{
		Search:    search,
		Provider:  &v2EmbeddingProviderStub{available: true, model: "test-model", dims: 3},
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
	if result != (V2EmbeddingWorkerResult{}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestV2EmbeddingWorkerCompletesValidBatch(t *testing.T) {
	search := newV2EmbeddingSearchStub()
	search.jobs = []repository.V2EmbeddingJob{
		v2EmbeddingJobForTest("job-a", 1),
		v2EmbeddingJobForTest("job-b", 2),
	}
	provider := &v2EmbeddingProviderStub{
		available: true,
		model:     "test-model",
		dims:      3,
		vectors: [][]float32{
			{1, 0, 0},
			{0, 1, 0},
		},
	}
	worker := NewV2EmbeddingWorkerService(V2EmbeddingWorkerDependencies{
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

func TestV2EmbeddingWorkerFailsProviderCountMismatch(t *testing.T) {
	search := newV2EmbeddingSearchStub()
	search.jobs = []repository.V2EmbeddingJob{
		v2EmbeddingJobForTest("job-a", 1),
		v2EmbeddingJobForTest("job-b", 1),
	}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	worker := NewV2EmbeddingWorkerService(V2EmbeddingWorkerDependencies{
		Search: search,
		Provider: &v2EmbeddingProviderStub{
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

func TestV2EmbeddingWorkerRejectsInvalidVectorsWithoutDroppingValidJobs(t *testing.T) {
	search := newV2EmbeddingSearchStub()
	search.jobs = []repository.V2EmbeddingJob{
		v2EmbeddingJobForTest("job-a", 1),
		v2EmbeddingJobForTest("job-b", 1),
	}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	worker := NewV2EmbeddingWorkerService(V2EmbeddingWorkerDependencies{
		Search: search,
		Provider: &v2EmbeddingProviderStub{
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

func TestV2EmbeddingWorkerClassifiesProviderFailures(t *testing.T) {
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
			search := newV2EmbeddingSearchStub()
			search.jobs = []repository.V2EmbeddingJob{v2EmbeddingJobForTest("job-a", 1)}
			metrics := observability.NewInMemoryDiscoverabilityMetrics()
			worker := NewV2EmbeddingWorkerService(V2EmbeddingWorkerDependencies{
				Search: search,
				Provider: &v2EmbeddingProviderStub{
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

func TestV2EmbeddingWorkerFailsProfileMismatchBeforeProviderCall(t *testing.T) {
	search := newV2EmbeddingSearchStub()
	search.jobs = []repository.V2EmbeddingJob{v2EmbeddingJobForTest("job-a", 1)}
	provider := &v2EmbeddingProviderStub{
		available: true,
		model:     "wrong-model",
		dims:      3,
		vectors:   [][]float32{{1, 0, 0}},
	}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	worker := NewV2EmbeddingWorkerService(V2EmbeddingWorkerDependencies{
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
	if got := metrics.EmbeddingErrorCount("profile_mismatch"); got != 1 {
		t.Fatalf("profile_mismatch metric = %d, want 1", got)
	}
}

func TestV2EmbeddingWorkerFailsDimensionMismatchBeforeProviderCall(t *testing.T) {
	search := newV2EmbeddingSearchStub()
	search.jobs = []repository.V2EmbeddingJob{v2EmbeddingJobForTest("job-a", 1)}
	provider := &v2EmbeddingProviderStub{
		available: true,
		model:     "test-model",
		dims:      4,
		vectors:   [][]float32{{1, 0, 0}},
	}
	worker := NewV2EmbeddingWorkerService(V2EmbeddingWorkerDependencies{
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

func TestV2EmbeddingWorkerMarksIneligibleJobProfileMismatch(t *testing.T) {
	search := newV2EmbeddingSearchStub()
	job := v2EmbeddingJobForTest("job-a", 1)
	job.EmbeddingContractID = "old-contract"
	search.jobs = []repository.V2EmbeddingJob{job}
	provider := &v2EmbeddingProviderStub{
		available: true,
		model:     "test-model",
		dims:      3,
		vectors:   [][]float32{{1, 0, 0}},
	}
	worker := NewV2EmbeddingWorkerService(V2EmbeddingWorkerDependencies{
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

func TestV2EmbeddingWorkerTreatsUnavailableProviderAsRetryable(t *testing.T) {
	search := newV2EmbeddingSearchStub()
	search.jobs = []repository.V2EmbeddingJob{v2EmbeddingJobForTest("job-a", 1)}
	provider := &v2EmbeddingProviderStub{
		available: false,
		model:     "test-model",
		dims:      3,
		vectors:   [][]float32{{1, 0, 0}},
	}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	worker := NewV2EmbeddingWorkerService(V2EmbeddingWorkerDependencies{
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

func TestV2EmbeddingWorkerCountsStaleAndLeaseLostCompletions(t *testing.T) {
	search := newV2EmbeddingSearchStub()
	search.jobs = []repository.V2EmbeddingJob{
		v2EmbeddingJobForTest("job-stale", 1),
		v2EmbeddingJobForTest("job-lease", 1),
	}
	search.completeErrs["job-stale"] = repository.ErrV2SearchStaleVersion
	search.completeErrs["job-lease"] = repository.ErrV2EmbeddingLeaseLost
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	worker := NewV2EmbeddingWorkerService(V2EmbeddingWorkerDependencies{
		Search: search,
		Provider: &v2EmbeddingProviderStub{
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

func newV2EmbeddingSearchStub() *v2EmbeddingSearchStub {
	return &v2EmbeddingSearchStub{
		profile: repository.V2SearchProfile{
			ProfileKey:          "default",
			EmbeddingContractID: "contract-a",
			EmbeddingDimensions: 3,
			EmbeddingModel:      "test-model",
		},
		completeErrs: map[string]error{},
	}
}

func v2EmbeddingJobForTest(id string, attempts int) repository.V2EmbeddingJob {
	return repository.V2EmbeddingJob{
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

type v2EmbeddingSearchStub struct {
	profile        repository.V2SearchProfile
	jobs           []repository.V2EmbeddingJob
	completeInputs []repository.V2CompleteEmbeddingJobInput
	failInputs     []repository.V2FailEmbeddingJobInput
	completeErrs   map[string]error
	claimErr       error
	claimLimit     int
}

func (s *v2EmbeddingSearchStub) GetActiveSearchProfile(context.Context, string) (*repository.V2SearchProfile, error) {
	return &s.profile, nil
}

func (s *v2EmbeddingSearchStub) CheckSearchReadiness(context.Context, string) (*repository.V2SearchReadiness, error) {
	return &repository.V2SearchReadiness{Ready: true, Profile: &s.profile}, nil
}

func (s *v2EmbeddingSearchStub) UpsertSearchDocument(context.Context, repository.V2UpsertSearchDocumentInput) (*repository.V2SearchDocumentResult, error) {
	return nil, errors.New("not implemented")
}

func (s *v2EmbeddingSearchStub) ClaimEmbeddingJobs(_ context.Context, input repository.V2ClaimEmbeddingJobsInput) ([]repository.V2EmbeddingJob, error) {
	s.claimLimit = input.Limit
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	return s.jobs, nil
}

func (s *v2EmbeddingSearchStub) CompleteEmbeddingJob(_ context.Context, input repository.V2CompleteEmbeddingJobInput) error {
	s.completeInputs = append(s.completeInputs, input)
	if err := s.completeErrs[input.EmbeddingJobID]; err != nil {
		return err
	}
	return nil
}

func (s *v2EmbeddingSearchStub) FailEmbeddingJob(_ context.Context, input repository.V2FailEmbeddingJobInput) (*repository.V2EmbeddingJobFailureResult, error) {
	s.failInputs = append(s.failInputs, input)
	status := "queued"
	if input.Terminal {
		status = "failed"
	}
	return &repository.V2EmbeddingJobFailureResult{Status: status, Terminal: input.Terminal}, nil
}

func (s *v2EmbeddingSearchStub) GetEmbeddingQueueStats(context.Context, repository.V2EmbeddingQueueStatsInput) (*repository.V2EmbeddingQueueStats, error) {
	return &repository.V2EmbeddingQueueStats{}, nil
}

func (s *v2EmbeddingSearchStub) SearchFullText(context.Context, repository.V2FullTextSearchInput) ([]repository.V2SearchHit, error) {
	return nil, errors.New("not implemented")
}

func (s *v2EmbeddingSearchStub) SearchExactVector(context.Context, repository.V2ExactVectorSearchInput) ([]repository.V2SearchHit, error) {
	return nil, errors.New("not implemented")
}

type v2EmbeddingProviderStub struct {
	available bool
	model     string
	dims      int
	vectors   [][]float32
	err       error
	gotTexts  []string
}

func (p *v2EmbeddingProviderStub) Embed(context.Context, string) ([]float32, string, error) {
	return nil, "", errors.New("not implemented")
}

func (p *v2EmbeddingProviderStub) EmbedBatch(_ context.Context, texts []string) ([][]float32, string, error) {
	p.gotTexts = append([]string(nil), texts...)
	if p.err != nil {
		return nil, "", p.err
	}
	return p.vectors, p.model, nil
}

func (p *v2EmbeddingProviderStub) ModelName() string {
	return p.model
}

func (p *v2EmbeddingProviderStub) Dimensions() int {
	return p.dims
}

func (p *v2EmbeddingProviderStub) IsAvailable() bool {
	return p.available
}

var _ repository.V2SearchRepository = (*v2EmbeddingSearchStub)(nil)
var _ embedding.EmbeddingProviderInterface = (*v2EmbeddingProviderStub)(nil)
