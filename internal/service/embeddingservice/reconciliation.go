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
	reconciliationTickInterval       = time.Minute
	reconciliationLease              = 10 * time.Minute
	reconciliationCallTimeout        = 2 * time.Minute
	reconciliationCleanupTimeout     = 30 * time.Second
	reconciliationBatchSize          = 500
	reconciliationCanaryAttemptLimit = 8
)

type reconciliationRuntimeConfig interface {
	GeneralRuntimeConfig(context.Context) (domain.GeneralRuntimeConfig, error)
}

type EmbeddingReconciliationDependencies struct {
	Search                          repository.SearchRepository
	Reconciliation                  repository.EmbeddingReconciliationRepository
	Provider                        embedding.EmbeddingProviderInterface
	AppConfig                       reconciliationRuntimeConfig
	Logger                          observability.LogProvider
	Metrics                         observability.DiscoverabilityMetrics
	WorkerID                        string
	Now                             func() time.Time
	ProviderTimeout                 time.Duration
	DistributedCoordinationRequired bool
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
	search          repository.SearchRepository
	reconciliation  repository.EmbeddingReconciliationRepository
	provider        embedding.EmbeddingProviderInterface
	appConfig       reconciliationRuntimeConfig
	logger          observability.LogProvider
	metrics         observability.DiscoverabilityMetrics
	workerID        string
	now             func() time.Time
	providerTimeout time.Duration
	lease           time.Duration
	distributed     bool

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
	providerTimeout, lease := embeddingReconciliationTiming(deps.ProviderTimeout)
	return &embeddingReconciliationService{
		search: deps.Search, reconciliation: deps.Reconciliation, provider: deps.Provider,
		appConfig: deps.AppConfig, logger: deps.Logger, metrics: metrics,
		workerID: strings.TrimSpace(deps.WorkerID), now: now,
		providerTimeout: providerTimeout, lease: lease,
		distributed: deps.DistributedCoordinationRequired,
	}
}

func embeddingReconciliationTiming(providerTimeout time.Duration) (time.Duration, time.Duration) {
	if providerTimeout <= 0 {
		providerTimeout = reconciliationCallTimeout
	}
	lease := reconciliationLease
	if required := providerTimeout + reconciliationCleanupTimeout; required > lease {
		lease = required
	}
	return providerTimeout, lease
}

func (s *embeddingReconciliationService) ProcessDue(ctx context.Context) (EmbeddingReconciliationResult, error) {
	var result EmbeddingReconciliationResult
	if err := s.validate(); err != nil {
		return result, err
	}
	runtime, err := s.appConfig.GeneralRuntimeConfig(ctx)
	if err != nil {
		return result, err
	}
	timezone := strings.TrimSpace(runtime.Timezone)
	if s.distributed && (timezone == "" || timezone == "Local") {
		return result, errors.New("embedding reconciliation: APP_TIMEZONE must be an explicit IANA timezone when distributed coordination is required")
	}
	location, err := loadReconciliationLocation(runtime.Timezone)
	if err != nil {
		return result, err
	}
	start, err := time.Parse("15:04", runtime.EmbeddingReconciliationStartTimeLocal)
	if err != nil {
		return result, fmt.Errorf("embedding reconciliation: invalid configured start time: %w", err)
	}
	now, err := s.reconciliation.GetEmbeddingReconciliationTime(ctx)
	if err != nil {
		return result, err
	}
	localNow := now.In(location)
	createIfMissing := localNow.Hour() == start.Hour() && localNow.Minute() == start.Minute()
	contract, err := s.search.GetActiveSearchContract(ctx)
	if err != nil {
		return result, err
	}
	run, claimed, err := s.reconciliation.ReserveEmbeddingReconciliationRun(ctx, repository.ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate:        localNow,
		CreateIfMissing:     createIfMissing,
		WorkerID:            s.workerID,
		Lease:               s.lease,
	})
	if err != nil {
		return result, err
	}
	if run == nil || !claimed {
		return result, nil
	}
	result.RunID = run.RunID
	result.Status = run.Status
	if run.StartedAt == nil {
		started := now
		run.StartedAt = &started
	}
	if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
		metrics.ObserveEmbeddingReconciliationRun("reserved")
	}
	s.logInfo(ctx, "embedding_reconciliation_reserved", run, nil)
	if run.CanaryAttemptedAt != nil {
		result.CanaryAttempted = true
		if run.CanaryOutcome == "succeeded" {
			result.CanarySucceeded = true
			result.RecoveredCount = run.RecoveredCount
			return result, s.releaseReconciliationBacklog(ctx, run, contract, &result)
		}
		if run.CanaryOutcome == "failed" {
			result.Status = string(domain.EmbeddingReconciliationDeferred)
			return result, s.deferRun(ctx, run, string(domain.EmbeddingReconciliationDeferred),
				run.CanaryFailureClass, run.CanaryFailureCode, "daily embedding canary failed before run completion", true, nil)
		}
		result.Status = string(domain.EmbeddingReconciliationAmbiguous)
		return result, s.deferRun(ctx, run, string(domain.EmbeddingReconciliationAmbiguous), "", "", "daily embedding canary outcome was ambiguous after lease expiry", true, nil)
	}

	for canaryAttempt := 0; canaryAttempt < reconciliationCanaryAttemptLimit; canaryAttempt++ {
		job, err := s.reconciliation.SelectEmbeddingReconciliationCanary(ctx, repository.SelectEmbeddingReconciliationCanaryInput{
			RunID: run.RunID, EmbeddingContractID: contract.EmbeddingContractID,
			EmbeddingDimensions: contract.EmbeddingDimensions, CandidateCutoff: run.CandidateCutoff,
		})
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			return result, s.deferRun(ctx, run, string(domain.EmbeddingReconciliationDeferred), "", "", "canary selection failed", false, err)
		}
		if job == nil {
			result.Status = string(domain.EmbeddingReconciliationCompleted)
			err := s.reconciliation.CompleteEmbeddingReconciliationRun(ctx, repository.CompleteEmbeddingReconciliationRunInput{
				RunID: run.RunID, WorkerID: s.workerID, LeaseToken: run.LeaseToken,
				Status: string(domain.EmbeddingReconciliationCompleted), CanaryOutcome: "",
			})
			if err == nil {
				if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
					metrics.ObserveEmbeddingReconciliationCanary("skipped")
					metrics.ObserveEmbeddingReconciliationRun("completed")
					metrics.ObserveEmbeddingReconciliationDuration(s.reconciliationElapsedSeconds(run, s.now().UTC()), "completed")
				}
				run.Status = result.Status
				s.logInfo(ctx, "embedding_reconciliation_completed", run, map[string]any{"canary_outcome": "skipped"})
			}
			return result, err
		}
		attemptedAt := s.now().UTC()
		if err := s.reconciliation.MarkEmbeddingReconciliationCanaryAttempt(ctx, repository.MarkEmbeddingReconciliationCanaryAttemptInput{
			TeamID: job.TeamID, RunID: run.RunID, CanaryJobID: job.EmbeddingJobID, WorkerID: s.workerID,
			LeaseToken: run.LeaseToken, AttemptedAt: attemptedAt, Lease: s.lease,
		}); err != nil {
			if errors.Is(err, repository.ErrEmbeddingReconciliationCanarySkipped) {
				continue
			}
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			return result, s.deferRun(ctx, run, string(domain.EmbeddingReconciliationDeferred), "", "", "daily embedding canary attempt persistence failed", false, err)
		}
		result.CanaryAttempted = true

		providerCtx, cancel := context.WithTimeout(ctx, s.providerTimeout)
		providerCtx = observability.WithMetricIdentity(providerCtx, job.TeamID, "")
		providerCtx = observability.WithAIOperation(providerCtx, observability.AIOperationBackgroundEmbedding, 1)
		if !s.provider.IsAvailable() {
			cancel()
			return result, s.finishFailedCanary(ctx, run, job, string(domain.EmbeddingFailureTransient), string(domain.EmbeddingFailureProviderNetworkError), "provider unavailable", true)
		}
		if got := strings.TrimSpace(s.provider.ModelName()); got != "" && got != contract.EmbeddingModel {
			cancel()
			return result, s.finishFailedCanary(ctx, run, job, string(domain.EmbeddingFailurePermanent), string(domain.EmbeddingFailureContractMismatch), "active embedding contract does not match provider model", true)
		}
		if got := s.provider.Dimensions(); got != 0 && got != contract.EmbeddingDimensions {
			cancel()
			return result, s.finishFailedCanary(ctx, run, job, string(domain.EmbeddingFailurePermanent), string(domain.EmbeddingFailureContractMismatch), "active embedding contract does not match provider dimensions", true)
		}
		vector, model, providerErr := s.provider.Embed(providerCtx, job.DocumentText)
		cancel()
		if providerErr != nil {
			if errors.Is(providerErr, context.Canceled) && errors.Is(ctx.Err(), context.Canceled) {
				result.Status = string(domain.EmbeddingReconciliationAmbiguous)
				return result, s.deferRun(ctx, run, result.Status, "", "", "daily embedding canary completion was ambiguous", true, nil)
			}
			metadata := embedding.ClassifyFailure(providerErr)
			failureClass, failureCode := metadata.Class, metadata.Code
			if failureClass == "" {
				failureClass = string(domain.EmbeddingFailurePermanent)
			}
			if failureCode == "" {
				failureCode = string(domain.EmbeddingFailureUnknown)
			}
			if failureClass == string(domain.EmbeddingFailurePermanent) &&
				failureCode == string(domain.EmbeddingFailureInputRejected) {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), reconciliationCleanupTimeout)
				workerID := repository.EmbeddingReconciliationWorkerIDPrefix + run.RunID
				_, failErr := s.search.FailEmbeddingJob(cleanupCtx, repository.FailEmbeddingJobInput{
					TeamID: job.TeamID, EmbeddingJobID: job.EmbeddingJobID, WorkerID: workerID,
					ExpectedAttempts: 1, Error: "daily embedding canary rejected the input", FailureClass: failureClass,
					FailureCode: failureCode, Terminal: true, SpaceID: job.SpaceID,
				})
				if failErr != nil {
					cleanupCancel()
					deferErr := s.deferRun(ctx, run, string(domain.EmbeddingReconciliationAmbiguous), failureClass, failureCode, "daily embedding input rejection persistence was ambiguous", true, failErr)
					return result, deferErr
				}
				if err := s.reconciliation.CompleteEmbeddingReconciliationCanary(cleanupCtx, repository.CompleteEmbeddingReconciliationCanaryInput{
					RunID: run.RunID, CanaryJobID: job.EmbeddingJobID, WorkerID: s.workerID,
					LeaseToken: run.LeaseToken, Succeeded: false, FailureClass: failureClass, FailureCode: failureCode,
				}); err != nil {
					cleanupCancel()
					deferErr := s.deferRun(ctx, run, string(domain.EmbeddingReconciliationAmbiguous), failureClass, failureCode, "daily embedding input rejection outcome was ambiguous", true, err)
					return result, deferErr
				}
				if err := s.reconciliation.ResetEmbeddingReconciliationCanary(cleanupCtx, repository.ResetEmbeddingReconciliationCanaryInput{
					RunID: run.RunID, CanaryJobID: job.EmbeddingJobID, WorkerID: s.workerID, LeaseToken: run.LeaseToken,
				}); err != nil {
					cleanupCancel()
					deferErr := s.deferRun(ctx, run, string(domain.EmbeddingReconciliationAmbiguous), failureClass, failureCode, "daily embedding input rejection retry was ambiguous", true, err)
					return result, deferErr
				}
				cleanupCancel()
				if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
					metrics.ObserveEmbeddingReconciliationCanary("skipped")
				}
				s.logInfo(ctx, "embedding_reconciliation_input_rejected_canary_skipped", run, map[string]any{
					"canary_job_id": job.EmbeddingJobID, "failure_code": failureCode,
				})
				continue
			}
			return result, s.finishFailedCanary(ctx, run, job, failureClass, failureCode, "daily embedding canary failed", true)
		}
		if model != "" && model != contract.EmbeddingModel {
			return result, s.finishFailedCanary(ctx, run, job, string(domain.EmbeddingFailurePermanent), string(domain.EmbeddingFailureContractMismatch), "daily embedding canary returned an incompatible model", true)
		}
		if err := validateEmbeddingVector(vector, job.EmbeddingDimensions); err != nil {
			return result, s.finishFailedCanary(ctx, run, job, string(domain.EmbeddingFailureProviderAction), string(domain.EmbeddingFailureProviderResponseInvalid), "daily embedding canary returned an invalid vector", true)
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), reconciliationCleanupTimeout)
		workerID := repository.EmbeddingReconciliationWorkerIDPrefix + run.RunID
		staleCanary := false
		if err := s.search.CompleteEmbeddingJob(cleanupCtx, repository.CompleteEmbeddingJobInput{
			TeamID: job.TeamID, EmbeddingJobID: job.EmbeddingJobID, WorkerID: workerID,
			ExpectedAttempts: 1, Embedding: vector, SpaceID: job.SpaceID,
		}); err != nil {
			if !isExpectedStaleCanary(err) {
				deferErr := s.deferRun(cleanupCtx, run, string(domain.EmbeddingReconciliationAmbiguous), "", "", "daily embedding canary completion was ambiguous", true, err)
				cleanupCancel()
				return result, deferErr
			}
			staleCanary = true
		}
		if !staleCanary {
			result.RecoveredCount = 1
			logEmbeddingRecoveryCompleted(s.logger, job.TeamID, "canary", 1,
				observability.String("reconciliation_run_id", run.RunID))
		} else {
			s.logInfo(ctx, "embedding_reconciliation_stale_canary_skipped", run, map[string]any{"canary_job_id": job.EmbeddingJobID})
		}
		if err := s.reconciliation.CompleteEmbeddingReconciliationCanary(cleanupCtx, repository.CompleteEmbeddingReconciliationCanaryInput{
			RunID: run.RunID, CanaryJobID: job.EmbeddingJobID, WorkerID: s.workerID,
			LeaseToken: run.LeaseToken, Succeeded: true, RecoveredCount: result.RecoveredCount,
		}); err != nil {
			deferErr := s.deferRun(cleanupCtx, run, string(domain.EmbeddingReconciliationAmbiguous), "", "", "daily embedding canary completion was ambiguous", true, err)
			cleanupCancel()
			return result, deferErr
		}
		result.CanarySucceeded = true
		cleanupCancel()
		return result, s.releaseReconciliationBacklog(ctx, run, contract, &result)
	}
	result.Status = string(domain.EmbeddingReconciliationDeferred)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), reconciliationCleanupTimeout)
	deferErr := s.deferRun(cleanupCtx, run, string(domain.EmbeddingReconciliationDeferred), string(domain.EmbeddingFailurePermanent), string(domain.EmbeddingFailureInputRejected), "daily embedding canary input rejection limit reached", true, nil)
	cleanupCancel()
	return result, deferErr
}

func (s *embeddingReconciliationService) releaseReconciliationBacklog(
	ctx context.Context,
	run *repository.EmbeddingReconciliationRun,
	contract *repository.ActiveSearchContract,
	result *EmbeddingReconciliationResult,
) error {
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), reconciliationCleanupTimeout)
	canarySucceededNow := run.CanaryOutcome != "succeeded"
	newlyRequeued, err := s.reconciliation.RequeueEmbeddingReconciliationJobs(cleanupCtx, repository.RequeueEmbeddingReconciliationJobsInput{
		RunID: run.RunID, WorkerID: s.workerID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions, CandidateCutoff: run.CandidateCutoff,
		BatchSize: reconciliationBatchSize, Lease: s.lease,
	})
	requeued := newlyRequeued + run.RequeuedCount
	result.RequeuedCount = requeued
	cleanupCancel()
	if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
		if canarySucceededNow {
			metrics.ObserveEmbeddingReconciliationCanary("succeeded")
		}
		if newlyRequeued > 0 {
			metrics.ObserveEmbeddingReconciliationJobs("requeued", "mixed", "mixed", "", int(newlyRequeued))
		}
	}

	finalizeCtx, finalizeCancel := context.WithTimeout(context.WithoutCancel(ctx), reconciliationCleanupTimeout)
	defer finalizeCancel()
	if err != nil {
		result.Status = string(domain.EmbeddingReconciliationDeferred)
		return s.deferRunAfterCanarySuccess(finalizeCtx, run, "reconciliation backlog release failed", requeued, result.RecoveredCount, err)
	}
	result.Status = string(domain.EmbeddingReconciliationCompleted)
	err = s.reconciliation.CompleteEmbeddingReconciliationRun(finalizeCtx, repository.CompleteEmbeddingReconciliationRunInput{
		RunID: run.RunID, WorkerID: s.workerID, LeaseToken: run.LeaseToken,
		Status: string(domain.EmbeddingReconciliationCompleted), CanaryOutcome: "succeeded",
		RequeuedCount: requeued, RecoveredCount: result.RecoveredCount,
	})
	if err == nil {
		if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
			metrics.ObserveEmbeddingReconciliationRun("completed")
			metrics.ObserveEmbeddingReconciliationDuration(s.reconciliationElapsedSeconds(run, s.now().UTC()), "completed")
		}
		run.Status = result.Status
		s.logInfo(ctx, "embedding_reconciliation_completed", run, map[string]any{
			"canary_outcome": "succeeded", "requeued_count": requeued, "recovered_count": result.RecoveredCount,
		})
	}
	return err
}

func isExpectedStaleCanary(err error) bool {
	return errors.Is(err, repository.ErrSearchStaleVersion) ||
		errors.Is(err, repository.ErrSearchContractMismatch) ||
		errors.Is(err, repository.ErrTeamInactive)
}

func (s *embeddingReconciliationService) finishFailedCanary(ctx context.Context, run *repository.EmbeddingReconciliationRun, job *repository.EmbeddingJob, failureClass, failureCode, message string, canaryAttempted bool) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reconciliationCleanupTimeout)
	defer cancel()
	workerID := repository.EmbeddingReconciliationWorkerIDPrefix + run.RunID
	_, failErr := s.search.FailEmbeddingJob(cleanupCtx, repository.FailEmbeddingJobInput{
		TeamID: job.TeamID, EmbeddingJobID: job.EmbeddingJobID, WorkerID: workerID,
		ExpectedAttempts: 1, Error: message,
		FailureClass: failureClass, FailureCode: failureCode, Terminal: true, SpaceID: job.SpaceID,
	})
	if failErr != nil && !errors.Is(failErr, repository.ErrTeamInactive) {
		return s.deferRun(cleanupCtx, run, string(domain.EmbeddingReconciliationAmbiguous), failureClass, failureCode, "daily embedding canary failure persistence was ambiguous", canaryAttempted, failErr)
	}
	if failErr == nil {
		logEmbeddingFailureRecorded(s.logger, job.TeamID, job.SourceKind, failureClass, failureCode, "canary", 1)
	}
	if err := s.reconciliation.CompleteEmbeddingReconciliationCanary(cleanupCtx, repository.CompleteEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, CanaryJobID: job.EmbeddingJobID, WorkerID: s.workerID,
		LeaseToken: run.LeaseToken, Succeeded: false, FailureClass: failureClass, FailureCode: failureCode,
	}); err != nil {
		return s.deferRun(cleanupCtx, run, string(domain.EmbeddingReconciliationAmbiguous), failureClass, failureCode, "daily embedding canary completion was ambiguous", canaryAttempted, err)
	}
	return s.deferRun(cleanupCtx, run, string(domain.EmbeddingReconciliationDeferred), failureClass, failureCode, message, canaryAttempted, nil)
}

func (s *embeddingReconciliationService) deferRun(ctx context.Context, run *repository.EmbeddingReconciliationRun, status, failureClass, failureCode, message string, canaryAttempted bool, cause error) error {
	canaryOutcome := ""
	if canaryAttempted {
		canaryOutcome = "failed"
		if status == string(domain.EmbeddingReconciliationAmbiguous) {
			canaryOutcome = "ambiguous"
		}
	}
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reconciliationCleanupTimeout)
	defer cancel()
	err := s.reconciliation.CompleteEmbeddingReconciliationRun(finalizeCtx, repository.CompleteEmbeddingReconciliationRunInput{
		RunID: run.RunID, WorkerID: s.workerID, LeaseToken: run.LeaseToken,
		Status: status, CanaryOutcome: canaryOutcome,
		FailureClass: failureClass, FailureCode: failureCode, LastError: message,
	})
	if err == nil {
		run.Status = status
		if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
			if canaryAttempted {
				metrics.ObserveEmbeddingReconciliationCanary(canaryOutcome)
			}
			metrics.ObserveEmbeddingReconciliationRun(status)
			metrics.ObserveEmbeddingReconciliationDuration(s.reconciliationElapsedSeconds(run, s.now().UTC()), status)
		}
		s.logWarn(ctx, "embedding_reconciliation_deferred", run, map[string]any{
			"canary_outcome": canaryOutcome, "failure_code": failureCode,
		})
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
		LastError: message,
	})
	if err == nil {
		run.Status = string(domain.EmbeddingReconciliationDeferred)
		if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
			metrics.ObserveEmbeddingReconciliationRun(string(domain.EmbeddingReconciliationDeferred))
			metrics.ObserveEmbeddingReconciliationDuration(s.reconciliationElapsedSeconds(run, s.now().UTC()), string(domain.EmbeddingReconciliationDeferred))
		}
		s.logWarn(ctx, "embedding_reconciliation_deferred", run, map[string]any{
			"canary_outcome": "succeeded", "requeued_count": requeuedCount, "recovered_count": recoveredCount,
		})
	}
	if cause != nil {
		return errors.Join(cause, err)
	}
	return err
}

func (s *embeddingReconciliationService) reconciliationElapsedSeconds(run *repository.EmbeddingReconciliationRun, now time.Time) float64 {
	if run == nil || run.StartedAt == nil {
		return 0
	}
	seconds := now.Sub(*run.StartedAt).Seconds()
	if seconds < 0 {
		if s.logger != nil {
			skew := run.StartedAt.Sub(now)
			s.logger.Warn("embedding_reconciliation_clock_skew",
				observability.String("reconciliation_run_id", run.RunID),
				observability.Int("clock_skew_milliseconds", int(skew.Milliseconds())),
			)
		}
		return 0
	}
	return seconds
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
	attrs := []observability.LogAttr{observability.String("reconciliation_run_id", run.RunID), observability.String("status", run.Status)}
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
