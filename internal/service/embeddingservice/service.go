package embeddingservice

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	defaultBatchSize      = 10
	defaultLease          = time.Minute
	defaultFailureTimeout = 10 * time.Second
)

type EmbeddingWorkerService interface {
	ProcessNextBatch(ctx context.Context) (EmbeddingWorkerResult, error)
}

type EmbeddingWorkerDependencies struct {
	Search         repository.V2SearchRepository
	Provider       embedding.EmbeddingProviderInterface
	Metrics        observability.DiscoverabilityMetrics
	TeamID         string
	WorkerID       string
	BatchSize      int
	Lease          time.Duration
	FailureTimeout time.Duration
}

type EmbeddingWorkerResult struct {
	Claimed   int
	Completed int
	Retried   int
	Failed    int
	Stale     int
	LeaseLost int
}

type embeddingWorkerService struct {
	search         repository.V2SearchRepository
	provider       embedding.EmbeddingProviderInterface
	metrics        observability.DiscoverabilityMetrics
	teamID         string
	workerID       string
	batchSize      int
	lease          time.Duration
	failureTimeout time.Duration
}

func NewEmbeddingWorkerService(deps EmbeddingWorkerDependencies) EmbeddingWorkerService {
	batchSize := deps.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	if batchSize > 100 {
		batchSize = 100
	}
	lease := deps.Lease
	if lease <= 0 {
		lease = defaultLease
	}
	failureTimeout := deps.FailureTimeout
	if failureTimeout <= 0 {
		failureTimeout = defaultFailureTimeout
	}
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	return &embeddingWorkerService{
		search:         deps.Search,
		provider:       deps.Provider,
		metrics:        metrics,
		teamID:         strings.TrimSpace(deps.TeamID),
		workerID:       strings.TrimSpace(deps.WorkerID),
		batchSize:      batchSize,
		lease:          lease,
		failureTimeout: failureTimeout,
	}
}

func (s *embeddingWorkerService) ProcessNextBatch(ctx context.Context) (EmbeddingWorkerResult, error) {
	var result EmbeddingWorkerResult
	if err := s.validate(); err != nil {
		return result, err
	}
	contract, err := s.search.GetActiveSearchContract(ctx)
	if err != nil {
		return result, err
	}
	jobs, err := s.search.ClaimEmbeddingJobs(ctx, repository.V2ClaimEmbeddingJobsInput{
		TeamID:   s.teamID,
		WorkerID: s.workerID,
		Limit:    s.batchSize,
		Lease:    s.lease,
	})
	if err != nil {
		return result, err
	}
	result.Claimed = len(jobs)
	if len(jobs) == 0 {
		return result, nil
	}
	if !s.provider.IsAvailable() {
		err := errors.New("embedding provider is unavailable")
		s.failJobs(ctx, contract, jobs, &result, err, "provider_unavailable", false, 0)
		return result, err
	}
	if err := s.validateProvider(contract); err != nil {
		s.failJobs(ctx, contract, jobs, &result, err, "contract_mismatch", true, 0)
		return result, err
	}
	eligible := make([]repository.V2EmbeddingJob, 0, len(jobs))
	texts := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if job.EmbeddingContractID != contract.EmbeddingContractID || job.EmbeddingDimensions != contract.EmbeddingDimensions {
			err := fmt.Errorf("embedding job contract mismatch: job contract %s dimensions %d, active contract %s dimensions %d",
				job.EmbeddingContractID, job.EmbeddingDimensions, contract.EmbeddingContractID, contract.EmbeddingDimensions)
			s.failJob(ctx, contract, job, &result, err, "contract_mismatch", true, 0)
			continue
		}
		eligible = append(eligible, job)
		texts = append(texts, job.DocumentText)
	}
	if len(eligible) == 0 {
		return result, nil
	}
	embeddings, model, err := s.provider.EmbedBatch(ctx, texts)
	if err != nil {
		code, terminal, retryAfter := classifyProviderFailure(err)
		s.failJobs(ctx, contract, eligible, &result, err, code, terminal, retryAfter)
		return result, err
	}
	if model != "" && model != contract.EmbeddingModel {
		err := fmt.Errorf("embedding provider returned model %q, active contract requires %q", model, contract.EmbeddingModel)
		s.failJobs(ctx, contract, eligible, &result, err, "contract_mismatch", true, 0)
		return result, err
	}
	if len(embeddings) != len(eligible) {
		err := fmt.Errorf("embedding provider returned %d vectors for %d jobs", len(embeddings), len(eligible))
		s.failJobs(ctx, contract, eligible, &result, err, "count_mismatch", true, 0)
		return result, err
	}
	var firstErr error
	for i, job := range eligible {
		vector := embeddings[i]
		if err := validateEmbeddingVector(vector, job.EmbeddingDimensions); err != nil {
			s.failJob(ctx, contract, job, &result, err, "invalid_vector", true, 0)
			firstErr = errors.Join(firstErr, err)
			continue
		}
		err := s.search.CompleteEmbeddingJob(ctx, repository.V2CompleteEmbeddingJobInput{
			TeamID:           job.TeamID,
			EmbeddingJobID:   job.EmbeddingJobID,
			WorkerID:         s.workerID,
			ExpectedAttempts: job.Attempts,
			Embedding:        vector,
		})
		switch {
		case err == nil:
			result.Completed++
		case errors.Is(err, repository.ErrV2SearchStaleVersion), errors.Is(err, repository.ErrV2SearchContractMismatch):
			result.Stale++
			observability.RecordEmbeddingError(ctx, s.metrics, contract.EmbeddingModel, "stale")
		case errors.Is(err, repository.ErrV2EmbeddingLeaseLost):
			result.LeaseLost++
			observability.RecordEmbeddingError(ctx, s.metrics, contract.EmbeddingModel, "lease_lost")
		default:
			firstErr = errors.Join(firstErr, err)
		}
	}
	return result, firstErr
}

func (s *embeddingWorkerService) validate() error {
	if s.search == nil {
		return errors.New("embedding worker: search repository is required")
	}
	if s.provider == nil {
		return errors.New("embedding worker: embedding provider is required")
	}
	if s.teamID == "" {
		return errors.New("embedding worker: team_id is required")
	}
	if s.workerID == "" {
		return errors.New("embedding worker: worker_id is required")
	}
	return nil
}

func (s *embeddingWorkerService) validateProvider(contract *repository.V2ActiveSearchContract) error {
	if got := s.provider.Dimensions(); got != 0 && got != contract.EmbeddingDimensions {
		return fmt.Errorf("embedding provider dimensions %d, active contract dimensions %d", got, contract.EmbeddingDimensions)
	}
	if got := strings.TrimSpace(s.provider.ModelName()); got != "" && got != contract.EmbeddingModel {
		return fmt.Errorf("embedding provider model %q, active contract model %q", got, contract.EmbeddingModel)
	}
	return nil
}

func (s *embeddingWorkerService) failJobs(
	ctx context.Context,
	contract *repository.V2ActiveSearchContract,
	jobs []repository.V2EmbeddingJob,
	result *EmbeddingWorkerResult,
	err error,
	code string,
	terminal bool,
	retryAfter time.Duration,
) {
	for _, job := range jobs {
		s.failJob(ctx, contract, job, result, err, code, terminal, retryAfter)
	}
}

func (s *embeddingWorkerService) failJob(
	ctx context.Context,
	contract *repository.V2ActiveSearchContract,
	job repository.V2EmbeddingJob,
	result *EmbeddingWorkerResult,
	err error,
	code string,
	terminal bool,
	retryAfter time.Duration,
) {
	if contract != nil {
		observability.RecordEmbeddingError(ctx, s.metrics, contract.EmbeddingModel, code)
	}
	failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.failureTimeout)
	defer cancel()
	failed, failErr := s.search.FailEmbeddingJob(failureCtx, repository.V2FailEmbeddingJobInput{
		TeamID:           job.TeamID,
		EmbeddingJobID:   job.EmbeddingJobID,
		WorkerID:         s.workerID,
		ExpectedAttempts: job.Attempts,
		Error:            err.Error(),
		RetryAfter:       retryAfter,
		Terminal:         terminal,
	})
	switch {
	case failErr == nil && failed != nil && failed.Stale:
		result.Stale++
	case failErr == nil && failed != nil && failed.Terminal:
		result.Failed++
	case failErr == nil:
		result.Retried++
	case errors.Is(failErr, repository.ErrV2EmbeddingLeaseLost):
		result.LeaseLost++
	default:
		result.Failed++
	}
}

func validateEmbeddingVector(vector []float32, dimensions int) error {
	if len(vector) != dimensions {
		return fmt.Errorf("embedding dimensions %d, expected %d", len(vector), dimensions)
	}
	for i, value := range vector {
		f := float64(value)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("embedding contains non-finite value at index %d", i)
		}
	}
	return nil
}

func classifyProviderFailure(err error) (string, bool, time.Duration) {
	if err == nil {
		return "ok", false, 0
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled", false, 0
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, embedding.ErrEmbeddingTimeout) {
		return "timeout", false, 0
	}
	if errors.Is(err, embedding.ErrEmbeddingRateLimit) {
		return "rate_limited", false, retryAfterFromError(err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout", false, 0
	}
	var httpErr *embedding.ProviderHTTPError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.Status == 429:
			return "rate_limited", false, retryAfterFromError(err)
		case httpErr.Status >= 400 && httpErr.Status < 500:
			return "provider_permanent", true, 0
		}
	}
	return "provider_error", false, 0
}

func retryAfterFromError(err error) time.Duration {
	var rateErr *embedding.RateLimitError
	if errors.As(err, &rateErr) && rateErr.RetryAfter > 0 {
		return time.Duration(rateErr.RetryAfter) * time.Second
	}
	var httpErr *embedding.ProviderHTTPError
	if errors.As(err, &httpErr) && httpErr.RetryAfter > 0 {
		return httpErr.RetryAfter
	}
	return 0
}
