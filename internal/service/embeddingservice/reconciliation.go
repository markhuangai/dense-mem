package embeddingservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	reconciliationTickInterval   = time.Minute
	reconciliationLease          = 10 * time.Minute
	reconciliationCallTimeout    = 2 * time.Minute
	reconciliationCleanupTimeout = 30 * time.Second
	reconciliationBatchSize      = 500
)

type reconciliationRuntimeConfig interface {
	GeneralRuntimeConfig(context.Context) (domain.GeneralRuntimeConfig, error)
}

type EmbeddingReconciliationDependencies struct {
	Search         repository.SearchRepository
	Reconciliation repository.EmbeddingReconciliationRepository
	Provider       embedding.EmbeddingProviderInterface
	AppConfig      reconciliationRuntimeConfig
	Logger         observability.LogProvider
	Metrics        observability.DiscoverabilityMetrics
	WorkerID       string
	Now            func() time.Time
}

type EmbeddingReconciliationResult struct {
	RunID           string
	Status          string
	CanaryAttempted bool
	CanarySucceeded bool
	RequeuedCount   int64
	RecoveredCount  int64
	FailureCode     string
}

type EmbeddingReconciliationService interface {
	ProcessDue(context.Context) (EmbeddingReconciliationResult, error)
	Start(context.Context)
	Stop()
}

type embeddingReconciliationService struct {
	search         repository.SearchRepository
	reconciliation repository.EmbeddingReconciliationRepository
	provider       embedding.EmbeddingProviderInterface
	appConfig      reconciliationRuntimeConfig
	logger         observability.LogProvider
	metrics        observability.DiscoverabilityMetrics
	workerID       string
	now            func() time.Time

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewEmbeddingReconciliationService(deps EmbeddingReconciliationDependencies) EmbeddingReconciliationService {
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &embeddingReconciliationService{
		search: deps.Search, reconciliation: deps.Reconciliation, provider: deps.Provider,
		appConfig: deps.AppConfig, logger: deps.Logger, metrics: metrics,
		workerID: strings.TrimSpace(deps.WorkerID), now: now,
	}
}

func (s *embeddingReconciliationService) ProcessDue(ctx context.Context) (EmbeddingReconciliationResult, error) {
	var result EmbeddingReconciliationResult
	if err := s.validate(); err != nil {
		return result, err
	}
	now := s.now().UTC()
	runtime, err := s.appConfig.GeneralRuntimeConfig(ctx)
	if err != nil {
		return result, err
	}
	location, err := loadReconciliationLocation(runtime.Timezone)
	if err != nil {
		return result, err
	}
	start, err := time.Parse("15:04", runtime.EmbeddingReconciliationStartTimeLocal)
	if err != nil {
		return result, fmt.Errorf("embedding reconciliation: invalid configured start time: %w", err)
	}
	localNow := now.In(location)
	if localNow.Hour() < start.Hour() ||
		(localNow.Hour() == start.Hour() && localNow.Minute() < start.Minute()) {
		return result, nil
	}
	contract, err := s.search.GetActiveSearchContract(ctx)
	if err != nil {
		return result, err
	}
	run, claimed, err := s.reconciliation.ReserveEmbeddingReconciliationRun(ctx, repository.ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate:        localNow,
		WorkerID:            s.workerID,
		Lease:               reconciliationLease,
		Now:                 now,
	})
	if err != nil {
		return result, err
	}
	if run == nil || !claimed {
		return result, nil
	}
	result.RunID = run.RunID
	result.Status = run.Status
	started := time.Now()
	if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
		metrics.ObserveEmbeddingReconciliationRun("reserved")
	}
	s.logInfo(ctx, "embedding_reconciliation_reserved", run, nil)
	if run.CanaryAttemptedAt != nil {
		result.Status = string(domain.EmbeddingReconciliationAmbiguous)
		return result, s.deferRun(ctx, run, string(domain.EmbeddingReconciliationAmbiguous), "", "", "daily embedding canary outcome was ambiguous after lease expiry", nil)
	}

	job, err := s.reconciliation.SelectEmbeddingReconciliationCanary(ctx, repository.SelectEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions, CandidateCutoff: run.CandidateCutoff,
	})
	if err != nil {
		return result, s.deferRun(ctx, run, string(domain.EmbeddingReconciliationDeferred), "", "", "canary selection failed", err)
	}
	if job == nil {
		result.Status = string(domain.EmbeddingReconciliationCompleted)
		err := s.reconciliation.CompleteEmbeddingReconciliationRun(ctx, repository.CompleteEmbeddingReconciliationRunInput{
			RunID: run.RunID, WorkerID: s.workerID, LeaseToken: run.LeaseToken,
			Status: string(domain.EmbeddingReconciliationCompleted), CanaryOutcome: "",
			CompletedAt: s.now().UTC(),
		})
		if err == nil {
			if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
				metrics.ObserveEmbeddingReconciliationCanary("skipped")
				metrics.ObserveEmbeddingReconciliationRun("completed")
				metrics.ObserveEmbeddingReconciliationDuration(time.Since(started).Seconds(), "completed")
			}
			s.logInfo(ctx, "embedding_reconciliation_completed", run, map[string]any{"canary_outcome": "skipped"})
		}
		return result, err
	}
	result.CanaryAttempted = true
	attemptedAt := s.now().UTC()
	if err := s.reconciliation.MarkEmbeddingReconciliationCanaryAttempt(ctx, repository.MarkEmbeddingReconciliationCanaryAttemptInput{
		RunID: run.RunID, CanaryJobID: job.EmbeddingJobID, WorkerID: s.workerID,
		LeaseToken: run.LeaseToken, AttemptedAt: attemptedAt, Lease: reconciliationLease,
	}); err != nil {
		return result, s.deferRun(ctx, run, string(domain.EmbeddingReconciliationDeferred), "", "", "daily embedding canary attempt persistence failed", err)
	}

	providerCtx, cancel := context.WithTimeout(ctx, reconciliationCallTimeout)
	defer cancel()
	providerCtx = observability.WithMetricIdentity(providerCtx, job.TeamID, "")
	providerCtx = observability.WithAIOperation(providerCtx, observability.AIOperationBackgroundEmbedding, 1)
	if !s.provider.IsAvailable() {
		return result, s.finishFailedCanary(ctx, run, job, string(domain.EmbeddingFailureTransient), string(domain.EmbeddingFailureProviderNetworkError), "provider unavailable")
	}
	if got := strings.TrimSpace(s.provider.ModelName()); got != "" && got != contract.EmbeddingModel {
		return result, s.finishFailedCanary(ctx, run, job, string(domain.EmbeddingFailurePermanent), string(domain.EmbeddingFailureContractMismatch), "active embedding contract does not match provider model")
	}
	if got := s.provider.Dimensions(); got != 0 && got != contract.EmbeddingDimensions {
		return result, s.finishFailedCanary(ctx, run, job, string(domain.EmbeddingFailurePermanent), string(domain.EmbeddingFailureContractMismatch), "active embedding contract does not match provider dimensions")
	}
	vector, model, providerErr := s.provider.Embed(providerCtx, job.DocumentText)
	if providerErr != nil {
		metadata := embedding.ClassifyFailure(providerErr)
		failureClass, failureCode := metadata.Class, metadata.Code
		if failureClass == "" {
			failureClass = string(domain.EmbeddingFailurePermanent)
		}
		if failureCode == "" {
			failureCode = string(domain.EmbeddingFailureUnknown)
		}
		return result, s.finishFailedCanary(ctx, run, job, failureClass, failureCode, "daily embedding canary failed")
	}
	if model != "" && model != contract.EmbeddingModel {
		return result, s.finishFailedCanary(ctx, run, job, string(domain.EmbeddingFailurePermanent), string(domain.EmbeddingFailureContractMismatch), "daily embedding canary returned an incompatible model")
	}
	if err := validateEmbeddingVector(vector, job.EmbeddingDimensions); err != nil {
		return result, s.finishFailedCanary(ctx, run, job, string(domain.EmbeddingFailureProviderAction), string(domain.EmbeddingFailureProviderResponseInvalid), "daily embedding canary returned an invalid vector")
	}
	workerID := repository.EmbeddingReconciliationWorkerIDPrefix + run.RunID
	staleCanary := false
	if err := s.search.CompleteEmbeddingJob(ctx, repository.CompleteEmbeddingJobInput{
		TeamID: job.TeamID, EmbeddingJobID: job.EmbeddingJobID, WorkerID: workerID,
		ExpectedAttempts: 1, Embedding: vector,
	}); err != nil {
		if !isExpectedStaleCanary(err) {
			return result, s.deferRun(ctx, run, string(domain.EmbeddingReconciliationAmbiguous), "", "", "daily embedding canary completion was ambiguous", err)
		}
		staleCanary = true
	}
	if err := s.reconciliation.CompleteEmbeddingReconciliationCanary(ctx, repository.CompleteEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, CanaryJobID: job.EmbeddingJobID, WorkerID: s.workerID,
		LeaseToken: run.LeaseToken, Succeeded: true,
	}); err != nil {
		return result, s.deferRun(ctx, run, string(domain.EmbeddingReconciliationAmbiguous), "", "", "daily embedding canary completion was ambiguous", err)
	}
	result.CanarySucceeded = true
	if !staleCanary {
		result.RecoveredCount = 1
	} else {
		s.logInfo(ctx, "embedding_reconciliation_stale_canary_skipped", run, map[string]any{"canary_job_id": job.EmbeddingJobID})
	}
	requeued, err := s.reconciliation.RequeueEmbeddingReconciliationJobs(ctx, repository.RequeueEmbeddingReconciliationJobsInput{
		RunID: run.RunID, WorkerID: s.workerID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions, CandidateCutoff: run.CandidateCutoff,
		BatchSize: reconciliationBatchSize, Lease: reconciliationLease,
	})
	result.RequeuedCount = requeued
	if err != nil {
		result.Status = string(domain.EmbeddingReconciliationDeferred)
		return result, s.deferRunAfterCanarySuccess(ctx, run, "reconciliation backlog release failed", requeued, result.RecoveredCount, err)
	}
	result.Status = string(domain.EmbeddingReconciliationCompleted)
	if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
		metrics.ObserveEmbeddingReconciliationCanary("succeeded")
		metrics.ObserveEmbeddingReconciliationJobs("requeued", "mixed", "mixed", "", int(requeued))
	}
	err = s.reconciliation.CompleteEmbeddingReconciliationRun(ctx, repository.CompleteEmbeddingReconciliationRunInput{
		RunID: run.RunID, WorkerID: s.workerID, LeaseToken: run.LeaseToken,
		Status: string(domain.EmbeddingReconciliationCompleted), CanaryOutcome: "succeeded",
		RequeuedCount: requeued, RecoveredCount: result.RecoveredCount, CompletedAt: s.now().UTC(),
	})
	if err == nil {
		if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
			metrics.ObserveEmbeddingReconciliationRun("completed")
			metrics.ObserveEmbeddingReconciliationDuration(time.Since(started).Seconds(), "completed")
		}
		s.logInfo(ctx, "embedding_reconciliation_completed", run, map[string]any{"requeued_count": requeued, "recovered_count": result.RecoveredCount})
	}
	return result, err
}

func isExpectedStaleCanary(err error) bool {
	return errors.Is(err, repository.ErrSearchStaleVersion) ||
		errors.Is(err, repository.ErrSearchContractMismatch) ||
		errors.Is(err, repository.ErrTeamInactive)
}

func (s *embeddingReconciliationService) finishFailedCanary(ctx context.Context, run *repository.EmbeddingReconciliationRun, job *repository.EmbeddingJob, failureClass, failureCode, message string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reconciliationCleanupTimeout)
	defer cancel()
	workerID := repository.EmbeddingReconciliationWorkerIDPrefix + run.RunID
	_, failErr := s.search.FailEmbeddingJob(cleanupCtx, repository.FailEmbeddingJobInput{
		TeamID: job.TeamID, EmbeddingJobID: job.EmbeddingJobID, WorkerID: workerID,
		ExpectedAttempts: 1, Error: message,
		FailureClass: failureClass, FailureCode: failureCode, Terminal: true,
	})
	if failErr != nil {
		return s.deferRun(cleanupCtx, run, string(domain.EmbeddingReconciliationAmbiguous), failureClass, failureCode, "daily embedding canary failure persistence was ambiguous", failErr)
	}
	if err := s.reconciliation.CompleteEmbeddingReconciliationCanary(cleanupCtx, repository.CompleteEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, CanaryJobID: job.EmbeddingJobID, WorkerID: s.workerID,
		LeaseToken: run.LeaseToken, Succeeded: false, FailureClass: failureClass, FailureCode: failureCode,
	}); err != nil {
		return s.deferRun(cleanupCtx, run, string(domain.EmbeddingReconciliationAmbiguous), failureClass, failureCode, "daily embedding canary completion was ambiguous", err)
	}
	return s.deferRun(cleanupCtx, run, string(domain.EmbeddingReconciliationDeferred), failureClass, failureCode, message, nil)
}

func (s *embeddingReconciliationService) deferRun(ctx context.Context, run *repository.EmbeddingReconciliationRun, status, failureClass, failureCode, message string, cause error) error {
	err := s.reconciliation.CompleteEmbeddingReconciliationRun(ctx, repository.CompleteEmbeddingReconciliationRunInput{
		RunID: run.RunID, WorkerID: s.workerID, LeaseToken: run.LeaseToken,
		Status: status, CanaryOutcome: func() string {
			if status == string(domain.EmbeddingReconciliationAmbiguous) {
				return "ambiguous"
			}
			return "failed"
		}(),
		FailureClass: failureClass, FailureCode: failureCode, LastError: message, CompletedAt: s.now().UTC(),
	})
	if err == nil {
		if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
			metrics.ObserveEmbeddingReconciliationCanary("failed")
			metrics.ObserveEmbeddingReconciliationRun(status)
		}
		s.logWarn(ctx, "embedding_reconciliation_deferred", run, map[string]any{"failure_code": failureCode})
	}
	if cause != nil {
		return errors.Join(cause, err)
	}
	return err
}

func (s *embeddingReconciliationService) deferRunAfterCanarySuccess(ctx context.Context, run *repository.EmbeddingReconciliationRun, message string, requeuedCount, recoveredCount int64, cause error) error {
	err := s.reconciliation.CompleteEmbeddingReconciliationRun(ctx, repository.CompleteEmbeddingReconciliationRunInput{
		RunID: run.RunID, WorkerID: s.workerID, LeaseToken: run.LeaseToken,
		Status: string(domain.EmbeddingReconciliationDeferred), CanaryOutcome: "succeeded",
		RequeuedCount: requeuedCount, RecoveredCount: recoveredCount,
		LastError: message, CompletedAt: s.now().UTC(),
	})
	if err == nil {
		if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
			metrics.ObserveEmbeddingReconciliationCanary("succeeded")
			metrics.ObserveEmbeddingReconciliationRun(string(domain.EmbeddingReconciliationDeferred))
			if requeuedCount > 0 {
				metrics.ObserveEmbeddingReconciliationJobs("requeued", "mixed", "mixed", "", int(requeuedCount))
			}
		}
		s.logWarn(ctx, "embedding_reconciliation_deferred", run, map[string]any{"requeued_count": requeuedCount, "recovered_count": recoveredCount})
	}
	if cause != nil {
		return errors.Join(cause, err)
	}
	return err
}

func (s *embeddingReconciliationService) validate() error {
	if s.search == nil {
		return errors.New("embedding reconciliation: search repository is required")
	}
	if s.reconciliation == nil {
		return errors.New("embedding reconciliation: reconciliation repository is required")
	}
	if s.provider == nil {
		return errors.New("embedding reconciliation: embedding provider is required")
	}
	if s.appConfig == nil {
		return errors.New("embedding reconciliation: app config is required")
	}
	if s.workerID == "" {
		return errors.New("embedding reconciliation: worker_id is required")
	}
	return nil
}

func (s *embeddingReconciliationService) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		ticker := time.NewTicker(reconciliationTickInterval)
		defer ticker.Stop()
		for {
			if _, err := s.ProcessDue(runCtx); err != nil {
				s.recordProcessDueError()
			}
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}(s.done)
}

func (s *embeddingReconciliationService) Stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (s *embeddingReconciliationService) recordProcessDueError() {
	if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
		metrics.ObserveEmbeddingReconciliationRun("scheduler_error")
	}
	if s.logger != nil {
		s.logger.Warn("embedding_reconciliation_process_due_failed", observability.String("error_code", "process_due"))
	}
}

func loadReconciliationLocation(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "Local" {
		return time.Local, nil
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("invalid APP_TIMEZONE %q: %w", name, err)
	}
	return location, nil
}

func (s *embeddingReconciliationService) logInfo(ctx context.Context, message string, run *repository.EmbeddingReconciliationRun, extra map[string]any) {
	if s.logger == nil {
		return
	}
	attrs := []observability.LogAttr{observability.String("reconciliation_run_id", run.RunID), observability.String("status", run.Status)}
	attrs = appendEmbeddingReconciliationLogAttrs(attrs, extra)
	s.logger.Info(message, attrs...)
}

func (s *embeddingReconciliationService) logWarn(ctx context.Context, message string, run *repository.EmbeddingReconciliationRun, extra map[string]any) {
	if s.logger == nil {
		return
	}
	attrs := []observability.LogAttr{observability.String("reconciliation_run_id", run.RunID), observability.String("status", string(domain.EmbeddingReconciliationDeferred))}
	attrs = appendEmbeddingReconciliationLogAttrs(attrs, extra)
	s.logger.Warn(message, attrs...)
}

func appendEmbeddingReconciliationLogAttrs(attrs []observability.LogAttr, extra map[string]any) []observability.LogAttr {
	for key, value := range extra {
		switch typed := value.(type) {
		case int:
			attrs = append(attrs, observability.Int(key, typed))
		case int64:
			attrs = append(attrs, observability.Int(key, int(typed)))
		case string:
			attrs = append(attrs, observability.String(key, typed))
		}
	}
	return attrs
}
