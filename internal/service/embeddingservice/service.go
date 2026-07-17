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
	defaultProfileKey     = "default"
	defaultBatchSize      = 10
	defaultLease          = time.Minute
	defaultFailureTimeout = 10 * time.Second
)

type V2EmbeddingWorkerService interface {
	ProcessNextBatch(ctx context.Context) (V2EmbeddingWorkerResult, error)
}

type V2EmbeddingWorkerDependencies struct {
	Search         repository.V2SearchRepository
	Provider       embedding.EmbeddingProviderInterface
	Metrics        observability.DiscoverabilityMetrics
	TeamID         string
	WorkerID       string
	ProfileKey     string
	BatchSize      int
	Lease          time.Duration
	FailureTimeout time.Duration
}

type V2EmbeddingWorkerResult struct {
	Claimed   int
	Completed int
	Retried   int
	Failed    int
	Stale     int
	LeaseLost int
}

type v2EmbeddingWorkerService struct {
	search         repository.V2SearchRepository
	provider       embedding.EmbeddingProviderInterface
	metrics        observability.DiscoverabilityMetrics
	teamID         string
	workerID       string
	profileKey     string
	batchSize      int
	lease          time.Duration
	failureTimeout time.Duration
}

func NewV2EmbeddingWorkerService(deps V2EmbeddingWorkerDependencies) V2EmbeddingWorkerService {
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
	profileKey := strings.TrimSpace(deps.ProfileKey)
	if profileKey == "" {
		profileKey = defaultProfileKey
	}
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	return &v2EmbeddingWorkerService{
		search:         deps.Search,
		provider:       deps.Provider,
		metrics:        metrics,
		teamID:         strings.TrimSpace(deps.TeamID),
		workerID:       strings.TrimSpace(deps.WorkerID),
		profileKey:     profileKey,
		batchSize:      batchSize,
		lease:          lease,
		failureTimeout: failureTimeout,
	}
}

func (s *v2EmbeddingWorkerService) ProcessNextBatch(ctx context.Context) (V2EmbeddingWorkerResult, error) {
	var result V2EmbeddingWorkerResult
	if err := s.validate(); err != nil {
		return result, err
	}
	profile, err := s.search.GetActiveSearchProfile(ctx, s.profileKey)
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
		s.failJobs(ctx, profile, jobs, &result, err, "provider_unavailable", false, 0)
		return result, err
	}
	if err := s.validateProvider(profile); err != nil {
		s.failJobs(ctx, profile, jobs, &result, err, "profile_mismatch", true, 0)
		return result, err
	}
	eligible := make([]repository.V2EmbeddingJob, 0, len(jobs))
	texts := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if job.EmbeddingContractID != profile.EmbeddingContractID || job.EmbeddingDimensions != profile.EmbeddingDimensions {
			err := fmt.Errorf("embedding job profile mismatch: job contract %s dimensions %d, active contract %s dimensions %d",
				job.EmbeddingContractID, job.EmbeddingDimensions, profile.EmbeddingContractID, profile.EmbeddingDimensions)
			s.failJob(ctx, profile, job, &result, err, "profile_mismatch", true, 0)
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
		s.failJobs(ctx, profile, eligible, &result, err, code, terminal, retryAfter)
		return result, err
	}
	if model != "" && model != profile.EmbeddingModel {
		err := fmt.Errorf("embedding provider returned model %q, active profile requires %q", model, profile.EmbeddingModel)
		s.failJobs(ctx, profile, eligible, &result, err, "profile_mismatch", true, 0)
		return result, err
	}
	if len(embeddings) != len(eligible) {
		err := fmt.Errorf("embedding provider returned %d vectors for %d jobs", len(embeddings), len(eligible))
		s.failJobs(ctx, profile, eligible, &result, err, "count_mismatch", true, 0)
		return result, err
	}
	var firstErr error
	for i, job := range eligible {
		vector := embeddings[i]
		if err := validateEmbeddingVector(vector, job.EmbeddingDimensions); err != nil {
			s.failJob(ctx, profile, job, &result, err, "invalid_vector", true, 0)
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
		case errors.Is(err, repository.ErrV2SearchStaleVersion), errors.Is(err, repository.ErrV2SearchProfileMismatch):
			result.Stale++
			observability.RecordEmbeddingError(ctx, s.metrics, profile.EmbeddingModel, "stale")
		case errors.Is(err, repository.ErrV2EmbeddingLeaseLost):
			result.LeaseLost++
			observability.RecordEmbeddingError(ctx, s.metrics, profile.EmbeddingModel, "lease_lost")
		default:
			firstErr = errors.Join(firstErr, err)
		}
	}
	return result, firstErr
}

func (s *v2EmbeddingWorkerService) validate() error {
	if s.search == nil {
		return errors.New("v2 embedding worker: search repository is required")
	}
	if s.provider == nil {
		return errors.New("v2 embedding worker: embedding provider is required")
	}
	if s.teamID == "" {
		return errors.New("v2 embedding worker: team_id is required")
	}
	if s.workerID == "" {
		return errors.New("v2 embedding worker: worker_id is required")
	}
	return nil
}

func (s *v2EmbeddingWorkerService) validateProvider(profile *repository.V2SearchProfile) error {
	if got := s.provider.Dimensions(); got != 0 && got != profile.EmbeddingDimensions {
		return fmt.Errorf("embedding provider dimensions %d, active profile dimensions %d", got, profile.EmbeddingDimensions)
	}
	if got := strings.TrimSpace(s.provider.ModelName()); got != "" && got != profile.EmbeddingModel {
		return fmt.Errorf("embedding provider model %q, active profile model %q", got, profile.EmbeddingModel)
	}
	return nil
}

func (s *v2EmbeddingWorkerService) failJobs(
	ctx context.Context,
	profile *repository.V2SearchProfile,
	jobs []repository.V2EmbeddingJob,
	result *V2EmbeddingWorkerResult,
	err error,
	code string,
	terminal bool,
	retryAfter time.Duration,
) {
	for _, job := range jobs {
		s.failJob(ctx, profile, job, result, err, code, terminal, retryAfter)
	}
}

func (s *v2EmbeddingWorkerService) failJob(
	ctx context.Context,
	profile *repository.V2SearchProfile,
	job repository.V2EmbeddingJob,
	result *V2EmbeddingWorkerResult,
	err error,
	code string,
	terminal bool,
	retryAfter time.Duration,
) {
	if profile != nil {
		observability.RecordEmbeddingError(ctx, s.metrics, profile.EmbeddingModel, code)
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
