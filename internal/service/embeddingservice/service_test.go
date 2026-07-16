package embeddingservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
)

type fakeStore struct {
	claimed     []repository.SemanticEmbeddingJob
	completed   []repository.SemanticEmbeddingJob
	failed      []repository.SemanticEmbeddingJob
	completedCh chan repository.SemanticEmbeddingJob
}

func (s *fakeStore) ClaimSemanticEmbeddingJobs(context.Context, int) ([]repository.SemanticEmbeddingJob, error) {
	jobs := s.claimed
	s.claimed = nil
	return jobs, nil
}

func (s *fakeStore) CompleteSemanticEmbeddingJob(_ context.Context, job repository.SemanticEmbeddingJob, _ []float32, _ string) error {
	s.completed = append(s.completed, job)
	if s.completedCh != nil {
		s.completedCh <- job
	}
	return nil
}

func (s *fakeStore) FailSemanticEmbeddingJob(_ context.Context, job repository.SemanticEmbeddingJob, _ error) error {
	s.failed = append(s.failed, job)
	return nil
}

type fakeProvider struct {
	dimensions int
	calls      [][]string
	responses  [][][]float32
	errs       []error
}

func (p *fakeProvider) EmbedBatch(_ context.Context, texts []string) ([][]float32, string, error) {
	p.calls = append(p.calls, append([]string(nil), texts...))
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		if err != nil {
			return nil, "", err
		}
	}
	if len(p.responses) > 0 {
		resp := p.responses[0]
		p.responses = p.responses[1:]
		return resp, "test-embedding", nil
	}
	vecs := make([][]float32, len(texts))
	for i := range texts {
		vecs[i] = []float32{1, 0}
	}
	return vecs, "test-embedding", nil
}

func (p *fakeProvider) Dimensions() int { return p.dimensions }
func (p *fakeProvider) ModelName() string {
	return "test-embedding"
}

func TestProcessNextBatchCompletesClaimedJobsWithOneProviderCall(t *testing.T) {
	store := &fakeStore{claimed: embeddingJobs("one", "two", "three")}
	provider := &fakeProvider{dimensions: 2}
	service := New(store, provider, nil)

	processed, err := service.ProcessNextBatch(context.Background(), 64)
	require.NoError(t, err)
	require.Equal(t, 3, processed)
	require.Len(t, provider.calls, 1)
	require.Equal(t, []string{"one", "two", "three"}, provider.calls[0])
	require.Len(t, store.completed, 3)
	require.Empty(t, store.failed)
}

func TestProcessNextBatchSplitsBadBatchAndIsolatesSingleFailure(t *testing.T) {
	store := &fakeStore{claimed: embeddingJobs("bad", "good")}
	provider := &fakeProvider{
		dimensions: 2,
		errs:       []error{errors.New("batch rejected"), errors.New("bad document")},
	}
	service := New(store, provider, nil)

	processed, err := service.ProcessNextBatch(context.Background(), 64)
	require.NoError(t, err)
	require.Equal(t, 2, processed)
	require.Len(t, provider.calls, 3)
	require.Equal(t, []string{"bad", "good"}, provider.calls[0])
	require.Equal(t, []string{"bad"}, provider.calls[1])
	require.Equal(t, []string{"good"}, provider.calls[2])
	require.Len(t, store.failed, 1)
	require.Equal(t, "bad", store.failed[0].DocumentText)
	require.Len(t, store.completed, 1)
	require.Equal(t, "good", store.completed[0].DocumentText)
}

func TestProcessNextBatchSplitsDimensionMismatchBeforeCompleting(t *testing.T) {
	store := &fakeStore{claimed: embeddingJobs("bad-dim", "good")}
	provider := &fakeProvider{
		dimensions: 2,
		responses: [][][]float32{
			{{1}, {1, 0}},
			{{1}},
			{{1, 0}},
		},
	}
	service := New(store, provider, nil)

	processed, err := service.ProcessNextBatch(context.Background(), 64)
	require.NoError(t, err)
	require.Equal(t, 2, processed)
	require.Len(t, store.failed, 1)
	require.Equal(t, "bad-dim", store.failed[0].DocumentText)
	require.Len(t, store.completed, 1)
	require.Equal(t, "good", store.completed[0].DocumentText)
}

func TestStartWorkerProcessesQueuedBatch(t *testing.T) {
	store := &fakeStore{
		claimed:     embeddingJobs("queued"),
		completedCh: make(chan repository.SemanticEmbeddingJob, 1),
	}
	provider := &fakeProvider{dimensions: 2}
	service := New(store, provider, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	service.StartWorker(ctx, 64, time.Millisecond)

	select {
	case job := <-store.completedCh:
		require.Equal(t, "queued", job.DocumentText)
	case <-time.After(time.Second):
		t.Fatal("StartWorker did not complete queued embedding job")
	}
}

func embeddingJobs(texts ...string) []repository.SemanticEmbeddingJob {
	jobs := make([]repository.SemanticEmbeddingJob, len(texts))
	for i, text := range texts {
		jobs[i] = repository.SemanticEmbeddingJob{
			TeamID:           uuid.NewString(),
			JobID:            uuid.NewString(),
			SearchDocumentID: uuid.NewString(),
			SourceType:       "evidence",
			SourceID:         uuid.NewString(),
			DocumentVersion:  int64(i + 1),
			DocumentText:     text,
			Attempts:         1,
		}
	}
	return jobs
}
