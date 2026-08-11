package embeddingservice

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	defaultBatchSize      = 64
	maxBatchSize          = 256
	defaultLease          = time.Minute
	defaultFailureTimeout = 10 * time.Second
)

type EmbeddingWorkerService interface {
	ProcessNextBatch(ctx context.Context) (EmbeddingWorkerResult, error)
}

type EmbeddingWorkerDependencies struct {
	Search         repository.SearchRepository
	Provider       embedding.EmbeddingProviderInterface
	Metrics        observability.DiscoverabilityMetrics
	Logger         observability.LogProvider
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
	search         repository.SearchRepository
	provider       embedding.EmbeddingProviderInterface
	metrics        observability.DiscoverabilityMetrics
	logger         observability.LogProvider
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
	if batchSize > maxBatchSize {
		batchSize = maxBatchSize
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
		logger:         deps.Logger,
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
	jobs, err := s.search.ClaimEmbeddingJobs(ctx, repository.ClaimEmbeddingJobsInput{
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
		err := &embedding.ProviderError{
			Provider:     "configured embedding provider",
			Message:      "embedding provider is unavailable",
			FailureClass: string(domain.EmbeddingFailureTransient),
			FailureCode:  string(domain.EmbeddingFailureProviderNetworkError),
		}
		s.failJobs(ctx, contract, jobs, &result, string(domain.EmbeddingFailureTransient), string(domain.EmbeddingFailureProviderNetworkError), false, 0)
		return result, err
	}
	if err := s.validateProvider(contract); err != nil {
		s.failJobs(ctx, contract, jobs, &result, string(domain.EmbeddingFailurePermanent), string(domain.EmbeddingFailureContractMismatch), true, 0)
		return result, err
	}
	eligible := make([]repository.EmbeddingJob, 0, len(jobs))
	texts := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if job.EmbeddingContractID != contract.EmbeddingContractID || job.EmbeddingDimensions != contract.EmbeddingDimensions {
			s.failJob(ctx, contract, job, &result, string(domain.EmbeddingFailurePermanent), string(domain.EmbeddingFailureContractMismatch), true, 0, true)
			continue
		}
		eligible = append(eligible, job)
		texts = append(texts, job.DocumentText)
	}
	if len(eligible) == 0 {
		return result, nil
	}
	providerCtx := observability.WithMetricIdentity(ctx, s.teamID, "")
	providerCtx = observability.WithAIOperation(providerCtx, observability.AIOperationBackgroundEmbedding, len(eligible))
	embeddings, model, err := s.provider.EmbedBatch(providerCtx, texts)
	if err != nil {
		if errors.Is(err, context.Canceled) && errors.Is(ctx.Err(), context.Canceled) {
			return result, ctx.Err()
		}
		failureClass, code, terminal, retryAfter := classifyProviderFailure(err)
		s.failJobs(ctx, contract, eligible, &result, failureClass, code, terminal, retryAfter)
		return result, fmt.Errorf("embedding processing failed: %s", code)
	}
	if model != "" && model != contract.EmbeddingModel {
		err := fmt.Errorf("embedding processing failed: %s", domain.EmbeddingFailureContractMismatch)
		s.failJobs(ctx, contract, eligible, &result, string(domain.EmbeddingFailurePermanent), string(domain.EmbeddingFailureContractMismatch), true, 0)
		return result, err
	}
	if len(embeddings) != len(eligible) {
		err := fmt.Errorf("embedding provider returned %d vectors for %d jobs", len(embeddings), len(eligible))
		s.failJobs(ctx, contract, eligible, &result, string(domain.EmbeddingFailureProviderAction), string(domain.EmbeddingFailureProviderResponseInvalid), true, 0)
		return result, err
	}
	var firstErr error
	recoveredJobs := 0
	for i, job := range eligible {
		vector := embeddings[i]
		if err := validateEmbeddingVector(vector, job.EmbeddingDimensions); err != nil {
			s.failJob(ctx, contract, job, &result, string(domain.EmbeddingFailureProviderAction), string(domain.EmbeddingFailureProviderResponseInvalid), true, 0, true)
			firstErr = errors.Join(firstErr, err)
			continue
		}
		err := s.search.CompleteEmbeddingJob(ctx, repository.CompleteEmbeddingJobInput{
			TeamID:           job.TeamID,
			EmbeddingJobID:   job.EmbeddingJobID,
			WorkerID:         s.workerID,
			ExpectedAttempts: job.Attempts,
			Embedding:        vector,
		})
		switch {
		case err == nil:
			result.Completed++
			if job.FirstFailedAt != nil {
				recoveredJobs++
			}
		case errors.Is(err, repository.ErrSearchStaleVersion), errors.Is(err, repository.ErrSearchContractMismatch):
			result.Stale++
			observability.RecordEmbeddingError(ctx, s.metrics, contract.EmbeddingModel, "stale")
		case errors.Is(err, repository.ErrEmbeddingLeaseLost):
			result.LeaseLost++
			observability.RecordEmbeddingError(ctx, s.metrics, contract.EmbeddingModel, "lease_lost")
		default:
			firstErr = errors.Join(firstErr, err)
		}
	}
	if recoveredJobs > 0 {
		logEmbeddingRecoveryCompleted(s.logger, s.teamID, "batch", recoveredJobs)
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

func (s *embeddingWorkerService) validateProvider(contract *repository.ActiveSearchContract) error {
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
	contract *repository.ActiveSearchContract,
	jobs []repository.EmbeddingJob,
	result *EmbeddingWorkerResult,
	failureClass string,
	code string,
	terminal bool,
	retryAfter time.Duration,
) {
	recorded := 0
	for _, job := range jobs {
		if s.failJob(ctx, contract, job, result, failureClass, code, terminal, retryAfter, false) {
			recorded++
		}
	}
	if recorded > 0 {
		logEmbeddingFailureRecorded(s.logger, s.teamID, "mixed", failureClass, code, "batch", recorded)
	}
}

func (s *embeddingWorkerService) failJob(
	ctx context.Context,
	contract *repository.ActiveSearchContract,
	job repository.EmbeddingJob,
	result *EmbeddingWorkerResult,
	failureClass string,
	code string,
	terminal bool,
	retryAfter time.Duration,
	emitLog bool,
) bool {
	if contract != nil {
		observability.RecordEmbeddingError(ctx, s.metrics, contract.EmbeddingModel, code)
		for _, legacy := range legacyEmbeddingMetricCodes(code) {
			if legacy != code {
				observability.RecordEmbeddingError(ctx, s.metrics, contract.EmbeddingModel, legacy)
			}
		}
	}
	failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.failureTimeout)
	defer cancel()
	failed, failErr := s.search.FailEmbeddingJob(failureCtx, repository.FailEmbeddingJobInput{
		TeamID:           job.TeamID,
		EmbeddingJobID:   job.EmbeddingJobID,
		WorkerID:         s.workerID,
		ExpectedAttempts: job.Attempts,
		Error:            domain.EmbeddingFailureMessage(code),
		FailureClass:     failureClass,
		FailureCode:      code,
		RetryAfter:       retryAfter,
		Terminal:         terminal,
	})
	switch {
	case failErr == nil && failed != nil && failed.Stale:
		result.Stale++
	case failErr == nil && failed != nil && failed.Terminal:
		result.Failed++
		if emitLog {
			logEmbeddingFailureRecorded(s.logger, job.TeamID, job.SourceKind, failureClass, code, "single", 1)
		}
		return true
	case failErr == nil && failed != nil:
		result.Retried++
		if emitLog {
			logEmbeddingFailureRecorded(s.logger, job.TeamID, job.SourceKind, failureClass, code, "single", 1)
		}
		return true
	case failErr == nil:
		result.Failed++
	case errors.Is(failErr, repository.ErrEmbeddingLeaseLost):
		result.LeaseLost++
	default:
		result.Failed++
	}
	return false
}

func logEmbeddingFailureRecorded(
	logger observability.LogProvider,
	teamID, sourceKind, failureClass, failureCode, aggregation string,
	jobCount int,
) {
	if logger == nil || jobCount <= 0 {
		return
	}
	logger.Warn("embedding_failure_recorded",
		observability.String("team_id", teamID),
		observability.String("source_kind", sourceKind),
		observability.String("failure_class", failureClass),
		observability.String("failure_code", failureCode),
		observability.String("aggregation", aggregation),
		observability.Int("job_count", jobCount),
	)
}

func logEmbeddingRecoveryCompleted(
	logger observability.LogProvider,
	teamID, aggregation string,
	jobCount int,
	extra ...observability.LogAttr,
) {
	if logger == nil || jobCount <= 0 {
		return
	}
	attrs := []observability.LogAttr{
		observability.String("team_id", teamID),
		observability.String("aggregation", aggregation),
		observability.Int("job_count", jobCount),
	}
	logger.Info("embedding_recovery_completed", append(attrs, extra...)...)
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

func classifyProviderFailure(err error) (string, string, bool, time.Duration) {
	if err == nil {
		return "", "ok", false, 0
	}
	metadata := embedding.ClassifyFailure(err)
	terminal := metadata.Class == string(domain.EmbeddingFailureProviderAction) || metadata.Class == string(domain.EmbeddingFailurePermanent)
	return metadata.Class, metadata.Code, terminal, metadata.RetryAfter
}

func legacyEmbeddingMetricCodes(code string) []string {
	switch code {
	case string(domain.EmbeddingFailureProviderTimeout):
		return []string{"timeout"}
	case string(domain.EmbeddingFailureProviderRateLimited):
		return []string{"rate_limited"}
	case string(domain.EmbeddingFailureProviderNetworkError):
		return []string{"provider_unavailable"}
	case string(domain.EmbeddingFailureProviderResponseInvalid):
		return []string{"invalid_vector"}
	case string(domain.EmbeddingFailureContractMismatch):
		return []string{"contract_mismatch"}
	case string(domain.EmbeddingFailureProviderContractRejected):
		return []string{"provider_permanent"}
	default:
		return nil
	}
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
