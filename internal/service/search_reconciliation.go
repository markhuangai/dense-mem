package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

const (
	searchRepairTickInterval = time.Minute
	searchRepairLease        = 15 * time.Minute
	searchRepairLeaseGrace   = 30 * time.Second
	searchRepairTimeout      = 30 * time.Second
	searchRepairFinalizeCap  = 5 * time.Second
	searchRepairLimit        = semanticwrite.MaxDocuments
)

var ErrSearchRepairFailed = errors.New("search repair failed")

type searchRepairRuntimeConfig interface {
	GeneralRuntimeConfig(context.Context) (domain.GeneralRuntimeConfig, error)
}

type searchRepairExecutor interface {
	Execute(context.Context, semanticwrite.Plan) (semanticwrite.Result, error)
}

type SearchRepairDependencies struct {
	Repository                      repository.SearchRepairRepository
	Executor                        searchRepairExecutor
	AppConfig                       searchRepairRuntimeConfig
	Logger                          observability.LogProvider
	Metrics                         observability.DiscoverabilityMetrics
	WorkerID                        string
	ProviderTimeout                 time.Duration
	Now                             func() time.Time
	DistributedCoordinationRequired bool
}

type SearchRepairResult struct {
	RunID         string
	LeaseToken    string
	Status        string
	SelectedCount int64
	EmbeddedCount int64
	UpdatedCount  int64
	DriftedCount  int64
	Skipped       bool
	ErrorCode     string
}

type SearchRepairService interface {
	ProcessDue(context.Context) (SearchRepairResult, error)
	Run(context.Context, time.Time, bool) (SearchRepairResult, error)
	Start(context.Context)
	Stop()
}

type searchRepairService struct {
	repository      repository.SearchRepairRepository
	executor        searchRepairExecutor
	appConfig       searchRepairRuntimeConfig
	logger          observability.LogProvider
	metrics         observability.DiscoverabilityMetrics
	workerID        string
	now             func() time.Time
	distributed     bool
	providerTimeout time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewSearchRepairService(deps SearchRepairDependencies) SearchRepairService {
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	providerTimeout := deps.ProviderTimeout
	if providerTimeout <= 0 {
		providerTimeout = searchRepairTimeout
	}
	return &searchRepairService{
		repository:      deps.Repository,
		executor:        deps.Executor,
		appConfig:       deps.AppConfig,
		logger:          deps.Logger,
		metrics:         metrics,
		workerID:        strings.TrimSpace(deps.WorkerID),
		now:             now,
		distributed:     deps.DistributedCoordinationRequired,
		providerTimeout: providerTimeout,
	}
}

func (s *searchRepairService) ProcessDue(ctx context.Context) (SearchRepairResult, error) {
	if s == nil || s.repository == nil || s.executor == nil || s.appConfig == nil || s.workerID == "" {
		return SearchRepairResult{}, fmt.Errorf("%w: service unavailable", ErrSearchRepairFailed)
	}
	runtime, err := s.appConfig.GeneralRuntimeConfig(ctx)
	if err != nil {
		return SearchRepairResult{}, fmt.Errorf("%w: runtime configuration unavailable", ErrSearchRepairFailed)
	}
	if s.distributed && (strings.TrimSpace(runtime.Timezone) == "" || strings.TrimSpace(runtime.Timezone) == "Local") {
		return SearchRepairResult{}, fmt.Errorf("%w: APP_TIMEZONE must be an explicit IANA timezone", ErrSearchRepairFailed)
	}
	location, err := time.LoadLocation(strings.TrimSpace(runtime.Timezone))
	if err != nil {
		return SearchRepairResult{}, fmt.Errorf("%w: configured timezone is invalid", ErrSearchRepairFailed)
	}
	start, err := time.Parse("15:04", strings.TrimSpace(runtime.EmbeddingReconciliationStartTimeLocal))
	if err != nil {
		return SearchRepairResult{}, fmt.Errorf("%w: configured start time is invalid", ErrSearchRepairFailed)
	}
	now, err := s.repository.GetSearchRepairTime(ctx)
	if err != nil {
		return SearchRepairResult{}, fmt.Errorf("%w: database clock unavailable", ErrSearchRepairFailed)
	}
	localNow := now.In(location)
	scheduled := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), start.Hour(), start.Minute(), 0, 0, localNow.Location())
	create := !localNow.Before(scheduled)
	return s.Run(ctx, localNow, create)
}

func (s *searchRepairService) Run(ctx context.Context, localNow time.Time, createIfMissing bool) (SearchRepairResult, error) {
	result := SearchRepairResult{}
	if s == nil || s.repository == nil || s.executor == nil || s.workerID == "" {
		return result, fmt.Errorf("%w: service unavailable", ErrSearchRepairFailed)
	}
	contract, err := s.repository.GetActiveSearchContract(ctx)
	if err != nil {
		return result, fmt.Errorf("%w: active search contract unavailable", ErrSearchRepairFailed)
	}
	run, claimed, err := s.repository.ReserveSearchRepairRun(ctx, repository.SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate:        localNow,
		CreateIfMissing:     createIfMissing,
		WorkerID:            s.workerID,
		Lease:               searchRepairLeaseForTimeout(s.providerTimeout),
	})
	if err != nil {
		return result, fmt.Errorf("%w: run reservation failed", ErrSearchRepairFailed)
	}
	if !claimed || run == nil {
		result.Status = "skipped"
		result.Skipped = true
		return result, nil
	}
	result.RunID = run.RunID
	result.LeaseToken = run.LeaseToken
	result.Status = string(domain.EmbeddingReconciliationRunning)
	if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
		metrics.ObserveEmbeddingReconciliationRun("reserved")
	}

	// A reserved run cannot claim convergence when selection itself fails.
	result.DriftedCount = 1
	documents, hasMore, err := s.repository.SelectSearchRepairDocuments(ctx, repository.SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions,
		Limit:               searchRepairLimit,
	})
	if err != nil {
		return s.finishFailure(ctx, result, contract, "failed", "reconciliation_selection_failed")
	}
	result.SelectedCount = int64(len(documents))
	if len(documents) == 0 {
		result.DriftedCount = 0
		if hasMore {
			result.DriftedCount = 1
		}
		return s.finish(ctx, result, "completed", "")
	}
	plan, err := searchRepairPlan(documents, contract, s.providerTimeout)
	if err != nil {
		return s.finishFailure(ctx, result, contract, "failed", "reconciliation_snapshot_invalid")
	}
	metricCtx := observability.WithMetricIdentity(ctx, "", "")
	metricCtx = observability.WithAIOperation(metricCtx, observability.AIOperationBackgroundEmbedding, len(plan.Documents))
	embedded, err := s.executor.Execute(metricCtx, plan)
	if err != nil {
		return s.finishExecutorFailure(ctx, result, contract, err)
	}
	result.EmbeddedCount = int64(len(embedded.Embeddings))
	vectors := make(map[string][]float32, len(embedded.Embeddings))
	for _, item := range embedded.Embeddings {
		vectors[item.DocumentHash] = item.Vector
	}
	applyDocuments := make([]repository.SearchRepairEmbedding, 0, len(documents))
	for _, document := range documents {
		entry := repository.SearchRepairEmbedding{SearchRepairDocument: document}
		if !document.Retired {
			vector, ok := vectors[document.DocumentHash]
			if !ok {
				return s.finishFailure(ctx, result, contract, "failed", "embedding_response_invalid")
			}
			entry.Embedding = vector
		}
		applyDocuments = append(applyDocuments, entry)
	}
	apply, err := s.repository.ApplySearchRepair(ctx, repository.ApplySearchRepairInput{
		RunID:                   result.RunID,
		LeaseToken:              result.LeaseToken,
		EmbeddingContractID:     contract.EmbeddingContractID,
		EmbeddingDimensions:     contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID,
		IndexGeneration:         contract.IndexGeneration,
		Documents:               applyDocuments,
	})
	if err != nil {
		return s.finishFailure(ctx, result, contract, "ambiguous", "reconciliation_commit_failed")
	}
	result.UpdatedCount = apply.UpdatedCount
	result.DriftedCount = apply.RemainingDriftedCount
	return s.finish(ctx, result, "completed", "")
}

func searchRepairLeaseForTimeout(providerTimeout time.Duration) time.Duration {
	lease := searchRepairLease
	if providerTimeout > 0 && providerTimeout+searchRepairLeaseGrace > lease {
		lease = providerTimeout + searchRepairLeaseGrace
	}
	return lease
}

func searchRepairPlan(documents []repository.SearchRepairDocument, contract *repository.ActiveSearchContract, timeout time.Duration) (semanticwrite.Plan, error) {
	if contract == nil {
		return semanticwrite.Plan{}, errors.New("active contract is required")
	}
	if timeout <= 0 {
		timeout = searchRepairTimeout
	}
	plan := semanticwrite.Plan{Fence: semanticwrite.Fence{
		Model:                   contract.EmbeddingModel,
		Dimensions:              contract.EmbeddingDimensions,
		EmbeddingContractID:     contract.EmbeddingContractID,
		SearchGenerationID:      contract.SearchIndexGenerationID,
		SearchGenerationVersion: int64(contract.IndexGeneration),
	}, Timeout: timeout}
	seen := make(map[string]string, len(documents))
	for _, document := range documents {
		if document.Retired {
			continue
		}
		hash := strings.TrimSpace(document.DocumentHash)
		text := strings.TrimSpace(document.DocumentText)
		if hash == "" || text == "" {
			return semanticwrite.Plan{}, errors.New("document text and hash are required")
		}
		if prior, ok := seen[hash]; ok {
			if prior != text {
				return semanticwrite.Plan{}, errors.New("document hash maps to conflicting text")
			}
			continue
		}
		seen[hash] = text
		plan.Documents = append(plan.Documents, semanticwrite.Document{Hash: hash, Text: text})
	}
	if len(plan.Documents) > searchRepairLimit {
		return semanticwrite.Plan{}, errors.New("document limit exceeded")
	}
	return plan, nil
}

func (s *searchRepairService) finishExecutorFailure(ctx context.Context, result SearchRepairResult, contract *repository.ActiveSearchContract, err error) (SearchRepairResult, error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return s.finishFailure(ctx, result, contract, "deferred", "embedding_timeout")
	case errors.Is(err, semanticwrite.ErrProviderTimeout):
		return s.finishFailure(ctx, result, contract, "deferred", "embedding_timeout")
	case errors.Is(err, context.Canceled):
		return s.finishFailure(ctx, result, contract, "deferred", "embedding_cancelled")
	case errors.Is(err, semanticwrite.ErrProviderUnavailable):
		return s.finishFailure(ctx, result, contract, "deferred", "embedding_unavailable")
	case errors.Is(err, semanticwrite.ErrProviderResponseInvalid):
		return s.finishFailure(ctx, result, contract, "failed", "embedding_response_invalid")
	default:
		return s.finishFailure(ctx, result, contract, "failed", "reconciliation_snapshot_invalid")
	}
}

func (s *searchRepairService) finishFailure(ctx context.Context, result SearchRepairResult, contract *repository.ActiveSearchContract, status, code string) (SearchRepairResult, error) {
	counted := false
	if contract != nil {
		if remaining, err := s.repository.CountSearchRepairDocuments(ctx, repository.SearchRepairSelectionInput{
			EmbeddingContractID: contract.EmbeddingContractID,
			EmbeddingDimensions: contract.EmbeddingDimensions,
		}); err == nil {
			result.DriftedCount = remaining
			counted = true
		}
	}
	if !counted && result.DriftedCount == 0 {
		result.DriftedCount = 1
	}
	result.ErrorCode = code
	result, finishErr := s.finish(ctx, result, status, code)
	if finishErr != nil {
		return result, finishErr
	}
	return result, fmt.Errorf("%w: %s", ErrSearchRepairFailed, code)
}

func (s *searchRepairService) finish(ctx context.Context, result SearchRepairResult, status, code string) (SearchRepairResult, error) {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), searchRepairFinalizeCap)
	defer cancel()
	err := s.repository.FinishSearchRepairRun(finishCtx, repository.FinishSearchRepairRunInput{
		RunID: result.RunID, LeaseToken: result.LeaseToken, Status: status,
		SelectedCount: result.SelectedCount, EmbeddedCount: result.EmbeddedCount,
		UpdatedCount: result.UpdatedCount, DriftedCount: result.DriftedCount, LastError: code,
	})
	if err != nil {
		return result, fmt.Errorf("%w: run finalization failed", ErrSearchRepairFailed)
	}
	result.Status = status
	if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
		metrics.ObserveEmbeddingReconciliationRun(status)
	}
	if s.logger != nil {
		attrs := []observability.LogAttr{
			observability.String("reconciliation_run_id", result.RunID),
			observability.String("status", status),
			{Key: "selected_count", Value: result.SelectedCount},
			{Key: "embedded_count", Value: result.EmbeddedCount},
			{Key: "updated_count", Value: result.UpdatedCount},
		}
		if status == "completed" {
			s.logger.Info("search_reconciliation_completed", attrs...)
		} else {
			s.logger.Warn("search_reconciliation_failed", append(attrs, observability.String("error_code", code))...)
		}
	}
	return result, nil
}

func (s *searchRepairService) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(searchRepairTickInterval)
		defer ticker.Stop()
		for {
			if _, err := s.ProcessDue(workerCtx); err != nil {
				s.recordProcessDueError()
			}
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *searchRepairService) recordProcessDueError() {
	if metrics, ok := s.metrics.(observability.EmbeddingReconciliationMetrics); ok {
		metrics.ObserveEmbeddingReconciliationRun("scheduler_error")
	}
	if s.logger != nil {
		s.logger.Warn("search_reconciliation_process_due_failed", observability.String("error_code", "process_due"))
	}
}

func (s *searchRepairService) Stop() {
	if s == nil {
		return
	}
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
