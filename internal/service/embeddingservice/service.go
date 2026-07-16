package embeddingservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
)

type Provider interface {
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, string, error)
	Dimensions() int
	ModelName() string
}

type Store interface {
	ClaimSemanticEmbeddingJobs(ctx context.Context, batchSize int) ([]repository.SemanticEmbeddingJob, error)
	CompleteSemanticEmbeddingJob(ctx context.Context, job repository.SemanticEmbeddingJob, vec []float32, model string) error
	FailSemanticEmbeddingJob(ctx context.Context, job repository.SemanticEmbeddingJob, cause error) error
}

type Service struct {
	store    Store
	provider Provider
	logger   *slog.Logger
}

func New(store Store, provider Provider, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: store, provider: provider, logger: logger}
}

func (s *Service) ProcessNextBatch(ctx context.Context, batchSize int) (int, error) {
	if s == nil || s.store == nil || s.provider == nil {
		return 0, errors.New("embedding worker: store and provider are required")
	}
	jobs, err := s.store.ClaimSemanticEmbeddingJobs(ctx, batchSize)
	if err != nil {
		return 0, err
	}
	if len(jobs) == 0 {
		return 0, nil
	}
	if err := s.processClaimed(ctx, jobs); err != nil {
		return len(jobs), err
	}
	return len(jobs), nil
}

func (s *Service) StartWorker(ctx context.Context, batchSize int, pollInterval time.Duration) {
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			for {
				processed, err := s.ProcessNextBatch(ctx, batchSize)
				if err != nil {
					s.logger.Warn("semantic embedding worker failed", slog.String("error", err.Error()))
					break
				}
				if processed == 0 {
					break
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *Service) processClaimed(ctx context.Context, jobs []repository.SemanticEmbeddingJob) error {
	if len(jobs) == 0 {
		return nil
	}
	texts := make([]string, len(jobs))
	for i, job := range jobs {
		texts[i] = job.DocumentText
	}
	vecs, model, err := s.provider.EmbedBatch(ctx, texts)
	if err != nil {
		return s.handleBatchFailure(ctx, jobs, err)
	}
	if len(vecs) != len(jobs) {
		return s.splitOrFail(ctx, jobs, fmt.Errorf("embedding batch returned %d vectors for %d jobs", len(vecs), len(jobs)))
	}
	for i, vec := range vecs {
		if len(vec) != s.provider.Dimensions() {
			return s.splitOrFail(ctx, jobs, fmt.Errorf("embedding batch vector %d dimensions = %d, want %d", i, len(vec), s.provider.Dimensions()))
		}
	}
	for i, job := range jobs {
		if err := s.store.CompleteSemanticEmbeddingJob(ctx, job, vecs[i], model); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) handleBatchFailure(ctx context.Context, jobs []repository.SemanticEmbeddingJob, cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return s.failJobs(ctx, jobs, cause)
	}
	return s.splitOrFail(ctx, jobs, cause)
}

func (s *Service) splitOrFail(ctx context.Context, jobs []repository.SemanticEmbeddingJob, cause error) error {
	if len(jobs) <= 1 {
		return s.failJobs(ctx, jobs, cause)
	}
	mid := len(jobs) / 2
	if err := s.processClaimed(ctx, jobs[:mid]); err != nil {
		return err
	}
	return s.processClaimed(ctx, jobs[mid:])
}

func (s *Service) failJobs(ctx context.Context, jobs []repository.SemanticEmbeddingJob, cause error) error {
	for _, job := range jobs {
		if err := s.store.FailSemanticEmbeddingJob(ctx, job, cause); err != nil {
			return err
		}
	}
	return nil
}
