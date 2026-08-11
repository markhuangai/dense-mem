package embeddingservice

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

type concurrentLeaseSearchStub struct {
	*embeddingSearchStub
	mu      sync.Mutex
	active  int
	max     int
	target  int
	started chan struct{}
	release chan struct{}
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
