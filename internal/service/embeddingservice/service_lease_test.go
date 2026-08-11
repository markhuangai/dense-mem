package embeddingservice

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestEmbeddingWorkerRenewsBatchLeasesConcurrently(t *testing.T) {
	base := newEmbeddingSearchStub()
	base.jobs = []repository.EmbeddingJob{
		embeddingJobForTest("job-renew-a", 1),
		embeddingJobForTest("job-renew-b", 1),
		embeddingJobForTest("job-renew-c", 1),
		embeddingJobForTest("job-renew-d", 1),
	}
	search := &concurrentLeaseSearchStub{
		embeddingSearchStub: base,
		target:              2,
		started:             make(chan struct{}),
		release:             make(chan struct{}),
	}
	provider := &embeddingProviderStub{
		available: true,
		model:     "test-model",
		dims:      3,
		embedBatchFunc: func(ctx context.Context, texts []string) ([][]float32, string, error) {
			select {
			case <-search.started:
				close(search.release)
			case <-ctx.Done():
				return nil, "", ctx.Err()
			}
			vectors := make([][]float32, len(texts))
			for i := range vectors {
				vectors[i] = []float32{1, 0, 0}
			}
			return vectors, "test-model", nil
		},
	}
	worker := NewEmbeddingWorkerService(EmbeddingWorkerDependencies{
		Search: search, Provider: provider, TeamID: "team-a", WorkerID: "worker-a", Lease: 100 * time.Millisecond,
	})

	result, err := worker.ProcessNextBatch(context.Background())
	require.NoError(t, err)
	assert.Equal(t, len(base.jobs), result.Completed)
	assert.GreaterOrEqual(t, search.maxConcurrency(), 2, "batch renewals must overlap")
}

func TestEmbeddingWorkerRenewsJobsWaitingBehindRecursiveSplits(t *testing.T) {
	base := newEmbeddingSearchStub()
	base.jobs = []repository.EmbeddingJob{
		embeddingJobForTest("job-renew-recursive-a", 1),
		embeddingJobForTest("job-renew-recursive-b", 1),
		embeddingJobForTest("job-renew-recursive-c", 1),
		embeddingJobForTest("job-renew-recursive-d", 1),
	}
	search := &recursiveLeaseSearchStub{embeddingSearchStub: base, laterJobRenewed: make(chan struct{})}
	provider := &embeddingProviderStub{
		available: true,
		model:     "test-model",
		dims:      3,
		embedBatchFunc: func(ctx context.Context, texts []string) ([][]float32, string, error) {
			if len(texts) > 1 {
				return nil, "", &embedding.ProviderHTTPError{Status: 413}
			}
			if strings.Contains(texts[0], "job-renew-recursive-a") {
				select {
				case <-search.laterJobRenewed:
				case <-time.After(time.Second):
					return nil, "", context.DeadlineExceeded
				case <-ctx.Done():
					return nil, "", ctx.Err()
				}
			}
			return [][]float32{{1, 0, 0}}, "test-model", nil
		},
	}
	worker := NewEmbeddingWorkerService(EmbeddingWorkerDependencies{
		Search: search, Provider: provider, TeamID: "team-a", WorkerID: "worker-a", Lease: 30 * time.Millisecond,
	})

	result, err := worker.ProcessNextBatch(context.Background())
	require.NoError(t, err)
	assert.Equal(t, len(base.jobs), result.Completed)
}

func TestEmbeddingWorkerRenewsLeasesThroughFinalization(t *testing.T) {
	base := newEmbeddingSearchStub()
	base.jobs = []repository.EmbeddingJob{embeddingJobForTest("job-finalization", 1)}
	search := &finalizationLeaseSearchStub{
		embeddingSearchStub:       base,
		completeStarted:           make(chan struct{}),
		releaseComplete:           make(chan struct{}),
		renewedDuringFinalization: make(chan struct{}),
	}
	worker := NewEmbeddingWorkerService(EmbeddingWorkerDependencies{
		Search: search, Provider: &embeddingProviderStub{
			available: true, model: "test-model", dims: 3, vectors: [][]float32{{1, 0, 0}},
		},
		TeamID: "team-a", WorkerID: "worker-a", Lease: 30 * time.Millisecond,
	})

	type outcome struct {
		result EmbeddingWorkerResult
		err    error
	}
	outcomes := make(chan outcome, 1)
	go func() {
		result, err := worker.ProcessNextBatch(context.Background())
		outcomes <- outcome{result: result, err: err}
	}()

	select {
	case <-search.renewedDuringFinalization:
	case <-time.After(time.Second):
		t.Fatal("embedding lease was not renewed while finalization was blocked")
	}
	close(search.releaseComplete)

	select {
	case result := <-outcomes:
		require.NoError(t, result.err)
		assert.Equal(t, 1, result.result.Completed)
	case <-time.After(time.Second):
		t.Fatal("embedding worker did not finish after finalization was released")
	}
}

func TestEmbeddingWorkerRenewsLeasesThroughEarlyFailureFinalization(t *testing.T) {
	base := newEmbeddingSearchStub()
	base.jobs = []repository.EmbeddingJob{embeddingJobForTest("job-early-failure", 1)}
	search := &earlyFailureLeaseSearchStub{
		embeddingSearchStub:  base,
		failureStarted:       make(chan struct{}),
		releaseFailure:       make(chan struct{}),
		renewedDuringFailure: make(chan struct{}),
	}
	worker := NewEmbeddingWorkerService(EmbeddingWorkerDependencies{
		Search: search, Provider: &embeddingProviderStub{available: false, model: "test-model", dims: 3},
		TeamID: "team-a", WorkerID: "worker-a", Lease: 30 * time.Millisecond,
	})

	type outcome struct {
		result EmbeddingWorkerResult
		err    error
	}
	outcomes := make(chan outcome, 1)
	go func() {
		result, err := worker.ProcessNextBatch(context.Background())
		outcomes <- outcome{result: result, err: err}
	}()

	select {
	case <-search.renewedDuringFailure:
	case <-time.After(time.Second):
		t.Fatal("embedding lease was not renewed while early failure finalization was blocked")
	}
	close(search.releaseFailure)

	select {
	case result := <-outcomes:
		require.Error(t, result.err)
		assert.Equal(t, 1, result.result.Retried)
	case <-time.After(time.Second):
		t.Fatal("embedding worker did not finish after early failure finalization was released")
	}
}

type concurrentLeaseSearchStub struct {
	*embeddingSearchStub
	mu      sync.Mutex
	active  int
	max     int
	target  int
	started chan struct{}
	release chan struct{}
}

type recursiveLeaseSearchStub struct {
	*embeddingSearchStub
	mu              sync.Mutex
	renewed         map[string]struct{}
	laterJobRenewed chan struct{}
	once            sync.Once
}

type finalizationLeaseSearchStub struct {
	*embeddingSearchStub
	completeStarted               chan struct{}
	releaseComplete               chan struct{}
	renewedDuringFinalization     chan struct{}
	completeOnce                  sync.Once
	renewedDuringFinalizationOnce sync.Once
}

type earlyFailureLeaseSearchStub struct {
	*embeddingSearchStub
	failureStarted       chan struct{}
	releaseFailure       chan struct{}
	renewedDuringFailure chan struct{}
	failureOnce          sync.Once
	renewedFailureOnce   sync.Once
}

func (s *earlyFailureLeaseSearchStub) FailEmbeddingJob(ctx context.Context, input repository.FailEmbeddingJobInput) (*repository.EmbeddingJobFailureResult, error) {
	s.failureOnce.Do(func() { close(s.failureStarted) })
	select {
	case <-s.releaseFailure:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.embeddingSearchStub.FailEmbeddingJob(ctx, input)
}

func (s *earlyFailureLeaseSearchStub) RenewEmbeddingJobLease(ctx context.Context, input repository.RenewEmbeddingJobLeaseInput) error {
	select {
	case <-s.failureStarted:
		s.renewedFailureOnce.Do(func() { close(s.renewedDuringFailure) })
	default:
	}
	return s.embeddingSearchStub.RenewEmbeddingJobLease(ctx, input)
}

func (s *finalizationLeaseSearchStub) CompleteEmbeddingJob(ctx context.Context, input repository.CompleteEmbeddingJobInput) error {
	s.completeOnce.Do(func() { close(s.completeStarted) })
	select {
	case <-s.releaseComplete:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.embeddingSearchStub.CompleteEmbeddingJob(ctx, input)
}

func (s *finalizationLeaseSearchStub) RenewEmbeddingJobLease(ctx context.Context, input repository.RenewEmbeddingJobLeaseInput) error {
	select {
	case <-s.completeStarted:
		s.renewedDuringFinalizationOnce.Do(func() { close(s.renewedDuringFinalization) })
	default:
	}
	return s.embeddingSearchStub.RenewEmbeddingJobLease(ctx, input)
}

func (s *recursiveLeaseSearchStub) RenewEmbeddingJobLease(_ context.Context, input repository.RenewEmbeddingJobLeaseInput) error {
	s.mu.Lock()
	if s.renewed == nil {
		s.renewed = make(map[string]struct{})
	}
	s.renewed[input.EmbeddingJobID] = struct{}{}
	s.mu.Unlock()
	if strings.HasSuffix(input.EmbeddingJobID, "-c") {
		s.once.Do(func() { close(s.laterJobRenewed) })
	}
	return nil
}

func (s *concurrentLeaseSearchStub) RenewEmbeddingJobLease(ctx context.Context, _ repository.RenewEmbeddingJobLeaseInput) error {
	s.mu.Lock()
	s.active++
	if s.active > s.max {
		s.max = s.active
	}
	if s.max >= s.target {
		select {
		case <-s.started:
		default:
			close(s.started)
		}
	}
	s.mu.Unlock()
	select {
	case <-s.release:
	case <-ctx.Done():
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
		return ctx.Err()
	}
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	return nil
}

func (s *concurrentLeaseSearchStub) maxConcurrency() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.max
}
