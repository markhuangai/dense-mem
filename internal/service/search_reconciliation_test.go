package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

type searchRepairConfigStub struct {
	value domain.GeneralRuntimeConfig
	err   error
}

func (s searchRepairConfigStub) GeneralRuntimeConfig(context.Context) (domain.GeneralRuntimeConfig, error) {
	return s.value, s.err
}

type searchRepairExecutorStub struct {
	result        semanticwrite.Result
	err           error
	plan          semanticwrite.Plan
	beforeExecute func()
}

func (s *searchRepairExecutorStub) Execute(_ context.Context, plan semanticwrite.Plan) (semanticwrite.Result, error) {
	s.plan = plan
	if s.beforeExecute != nil {
		s.beforeExecute()
	}
	return s.result, s.err
}

type searchRepairRepositoryStub struct {
	contract    *repository.ActiveSearchContract
	run         *repository.SearchRepairRun
	documents   []repository.SearchRepairDocument
	selectErr   error
	count       int64
	countCalls  int
	countErr    error
	hasMore     bool
	beforeApply func()
	reserve     repository.SearchRepairRunInput
	now         time.Time
	timeErr     error
	apply       *repository.ApplySearchRepairInput
	applyResult *repository.SearchRepairApplyResult
	applyErr    error
	finish      *repository.FinishSearchRepairRunInput
}

type searchRepairLoggerStub struct{ warnings []string }

func (*searchRepairLoggerStub) Info(string, ...observability.LogAttr)         {}
func (*searchRepairLoggerStub) Error(string, error, ...observability.LogAttr) {}
func (s *searchRepairLoggerStub) Warn(message string, _ ...observability.LogAttr) {
	s.warnings = append(s.warnings, message)
}
func (*searchRepairLoggerStub) Debug(string, ...observability.LogAttr) {}
func (s *searchRepairLoggerStub) With(...observability.LogAttr) observability.LogProvider {
	return s
}

func (s *searchRepairRepositoryStub) GetActiveSearchContract(context.Context) (*repository.ActiveSearchContract, error) {
	return s.contract, nil
}
func (s *searchRepairRepositoryStub) GetSearchRepairTime(context.Context) (time.Time, error) {
	if s.timeErr != nil {
		return time.Time{}, s.timeErr
	}
	if !s.now.IsZero() {
		return s.now, nil
	}
	return time.Now().UTC(), nil
}
func (s *searchRepairRepositoryStub) ReserveSearchRepairRun(_ context.Context, input repository.SearchRepairRunInput) (*repository.SearchRepairRun, bool, error) {
	s.reserve = input
	return s.run, s.run != nil, nil
}
func (s *searchRepairRepositoryStub) SelectSearchRepairDocuments(context.Context, repository.SearchRepairSelectionInput) ([]repository.SearchRepairDocument, bool, error) {
	return s.documents, s.hasMore, s.selectErr
}
func (s *searchRepairRepositoryStub) CountSearchRepairDocuments(context.Context, repository.SearchRepairSelectionInput) (int64, error) {
	s.countCalls++
	return s.count, s.countErr
}
func (s *searchRepairRepositoryStub) ApplySearchRepair(_ context.Context, input repository.ApplySearchRepairInput) (*repository.SearchRepairApplyResult, error) {
	s.apply = &input
	if s.beforeApply != nil {
		s.beforeApply()
	}
	if s.applyErr != nil {
		return s.applyResult, s.applyErr
	}
	if s.applyResult != nil {
		return s.applyResult, nil
	}
	return &repository.SearchRepairApplyResult{UpdatedCount: int64(len(input.Documents))}, nil
}
func (s *searchRepairRepositoryStub) FinishSearchRepairRun(_ context.Context, input repository.FinishSearchRepairRunInput) error {
	s.finish = &input
	return nil
}

func TestSearchReconciliationRunUsesOneFencedBatchAndFinishesDocumentCounters(t *testing.T) {
	contract := &repository.ActiveSearchContract{
		EmbeddingContractID: "11111111-1111-4111-8111-111111111111", SearchIndexGenerationID: "22222222-2222-4222-8222-222222222222",
		EmbeddingDimensions: 2, EmbeddingModel: "model", IndexGeneration: 1,
	}
	repo := &searchRepairRepositoryStub{
		contract: contract,
		run:      &repository.SearchRepairRun{RunID: "33333333-3333-4333-8333-333333333333", LeaseToken: "44444444-4444-4444-8444-444444444444"},
		documents: []repository.SearchRepairDocument{{
			TeamID: "team", SearchDocumentID: "doc", OwnerProfileID: "owner", SourceKind: "evidence", SourceID: "source",
			SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, EmbeddingContractID: contract.EmbeddingContractID,
			EmbeddingDimensions: 2, DocumentText: "canonical document", DocumentHash: "hash", StoredDocumentHash: "hash",
		}},
	}
	executor := &searchRepairExecutorStub{result: semanticwrite.Result{Embeddings: []semanticwrite.Embedding{{DocumentHash: "hash", Vector: []float32{1, 2}}}}}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	svc := NewSearchRepairService(SearchRepairDependencies{Repository: repo, Executor: executor, Metrics: metrics, WorkerID: "worker"})

	result, err := svc.Run(context.Background(), time.Date(2026, 8, 28, 4, 30, 0, 0, time.UTC), true)

	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Len(t, executor.plan.Documents, 1)
	require.Equal(t, 1, len(repo.apply.Documents))
	require.Equal(t, repo.run.RunID, repo.apply.RunID)
	require.Equal(t, repo.run.LeaseToken, repo.apply.LeaseToken)
	require.Equal(t, int64(1), repo.finish.SelectedCount)
	require.Equal(t, int64(1), repo.finish.EmbeddedCount)
	require.Equal(t, int64(1), repo.finish.UpdatedCount)
	require.Zero(t, repo.finish.DriftedCount)
	require.Equal(t, 1, metrics.EmbeddingReconciliationRunCount("completed"))
}

func TestSearchReconciliationRunPersistsExactRemainingDriftCount(t *testing.T) {
	contract := searchRepairTestContract()
	repo := &searchRepairRepositoryStub{
		contract: contract,
		run:      &repository.SearchRepairRun{RunID: "33333333-3333-4333-8333-333333333333", LeaseToken: "44444444-4444-4444-8444-444444444444"},
		documents: []repository.SearchRepairDocument{{
			DocumentText: "canonical document", DocumentHash: "hash",
		}},
		applyResult: &repository.SearchRepairApplyResult{UpdatedCount: 1, RemainingDriftedCount: 7},
	}
	executor := &searchRepairExecutorStub{result: semanticwrite.Result{Embeddings: []semanticwrite.Embedding{{DocumentHash: "hash", Vector: []float32{1, 2}}}}}
	svc := NewSearchRepairService(SearchRepairDependencies{Repository: repo, Executor: executor, WorkerID: "worker"})

	result, err := svc.Run(context.Background(), time.Now().UTC(), true)

	require.NoError(t, err)
	require.EqualValues(t, 7, result.DriftedCount)
	require.EqualValues(t, 7, repo.finish.DriftedCount)
}

func TestSearchReconciliationFailureUsesAvailableRemainingDriftCount(t *testing.T) {
	contract := searchRepairTestContract()
	repo := &searchRepairRepositoryStub{
		contract: contract,
		run:      &repository.SearchRepairRun{RunID: "33333333-3333-4333-8333-333333333333", LeaseToken: "44444444-4444-4444-8444-444444444444"},
		documents: []repository.SearchRepairDocument{{
			DocumentText: "canonical document", DocumentHash: "hash",
		}},
		count: 9,
	}
	svc := NewSearchRepairService(SearchRepairDependencies{
		Repository: repo, Executor: &searchRepairExecutorStub{err: semanticwrite.ErrProviderUnavailable}, WorkerID: "worker",
	})

	result, err := svc.Run(context.Background(), time.Now().UTC(), true)

	require.ErrorIs(t, err, ErrSearchRepairFailed)
	require.EqualValues(t, 9, result.DriftedCount)
	require.EqualValues(t, 9, repo.finish.DriftedCount)
}

func TestSearchReconciliationRunLeavesDocumentsUntouchedWhenProviderFails(t *testing.T) {
	contract := &repository.ActiveSearchContract{
		EmbeddingContractID: "11111111-1111-4111-8111-111111111111", SearchIndexGenerationID: "22222222-2222-4222-8222-222222222222",
		EmbeddingDimensions: 2, EmbeddingModel: "model", IndexGeneration: 1,
	}
	repo := &searchRepairRepositoryStub{
		contract:  contract,
		run:       &repository.SearchRepairRun{RunID: "33333333-3333-4333-8333-333333333333", LeaseToken: "44444444-4444-4444-8444-444444444444"},
		documents: []repository.SearchRepairDocument{{DocumentText: "canonical document", DocumentHash: "hash"}},
		countErr:  errors.New("count unavailable"),
	}
	svc := NewSearchRepairService(SearchRepairDependencies{
		Repository: repo, Executor: &searchRepairExecutorStub{err: errors.New("provider unavailable")}, WorkerID: "worker",
	})

	result, err := svc.Run(context.Background(), time.Now().UTC(), true)

	require.ErrorIs(t, err, ErrSearchRepairFailed)
	require.EqualValues(t, 1, result.DriftedCount)
	require.Nil(t, repo.apply)
	require.NotNil(t, repo.finish)
	require.Equal(t, "failed", repo.finish.Status)
	require.EqualValues(t, 1, repo.finish.DriftedCount)
}

func TestSearchReconciliationRunPreservesDriftWhenSelectionFails(t *testing.T) {
	contract := &repository.ActiveSearchContract{
		EmbeddingContractID: "11111111-1111-4111-8111-111111111111", SearchIndexGenerationID: "22222222-2222-4222-8222-222222222222",
		EmbeddingDimensions: 2, EmbeddingModel: "model", IndexGeneration: 1,
	}
	repo := &searchRepairRepositoryStub{
		contract:  contract,
		run:       &repository.SearchRepairRun{RunID: "33333333-3333-4333-8333-333333333333", LeaseToken: "44444444-4444-4444-8444-444444444444"},
		selectErr: errors.New("selection unavailable"),
		countErr:  errors.New("count unavailable"),
	}
	svc := NewSearchRepairService(SearchRepairDependencies{Repository: repo, Executor: &searchRepairExecutorStub{}, WorkerID: "worker"})

	result, err := svc.Run(context.Background(), time.Now().UTC(), true)

	require.ErrorIs(t, err, ErrSearchRepairFailed)
	require.EqualValues(t, 1, result.DriftedCount)
	require.NotNil(t, repo.finish)
	require.EqualValues(t, 1, repo.finish.DriftedCount)
}

func TestSearchReconciliationSelectionCancellationLeavesRunReclaimable(t *testing.T) {
	contract := searchRepairTestContract()
	repo := &searchRepairRepositoryStub{
		contract:  contract,
		run:       &repository.SearchRepairRun{RunID: "33333333-3333-4333-8333-333333333333", LeaseToken: "44444444-4444-4444-8444-444444444444"},
		selectErr: context.Canceled,
	}
	svc := NewSearchRepairService(SearchRepairDependencies{Repository: repo, Executor: &searchRepairExecutorStub{}, WorkerID: "worker"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := svc.Run(ctx, time.Now().UTC(), true)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, string(domain.EmbeddingReconciliationRunning), result.Status)
	require.Nil(t, repo.finish)
}

func TestSearchReconciliationProviderCancellationLeavesRunReclaimable(t *testing.T) {
	contract := searchRepairTestContract()
	repo := &searchRepairRepositoryStub{
		contract:  contract,
		run:       &repository.SearchRepairRun{RunID: "33333333-3333-4333-8333-333333333333", LeaseToken: "44444444-4444-4444-8444-444444444444"},
		documents: []repository.SearchRepairDocument{searchRepairTestDocument(contract)},
	}
	ctx, cancel := context.WithCancel(context.Background())
	executor := &searchRepairExecutorStub{
		err:           context.Canceled,
		beforeExecute: cancel,
	}
	svc := NewSearchRepairService(SearchRepairDependencies{Repository: repo, Executor: executor, WorkerID: "worker"})

	result, err := svc.Run(ctx, time.Now().UTC(), true)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, string(domain.EmbeddingReconciliationRunning), result.Status)
	require.Nil(t, repo.finish)
}

func TestSearchReconciliationApplyCancellationLeavesRunReclaimable(t *testing.T) {
	contract := searchRepairTestContract()
	repo := &searchRepairRepositoryStub{
		contract:  contract,
		run:       &repository.SearchRepairRun{RunID: "33333333-3333-4333-8333-333333333333", LeaseToken: "44444444-4444-4444-8444-444444444444"},
		documents: []repository.SearchRepairDocument{searchRepairTestDocument(contract)},
		applyErr:  context.Canceled,
	}
	ctx, cancel := context.WithCancel(context.Background())
	repo.beforeApply = cancel
	executor := &searchRepairExecutorStub{result: semanticwrite.Result{Embeddings: []semanticwrite.Embedding{{DocumentHash: "hash", Vector: []float32{1, 2}}}}}
	svc := NewSearchRepairService(SearchRepairDependencies{Repository: repo, Executor: executor, WorkerID: "worker"})

	result, err := svc.Run(ctx, time.Now().UTC(), true)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, string(domain.EmbeddingReconciliationRunning), result.Status)
	require.Nil(t, repo.finish)
}

func TestSearchReconciliationPreservesCommittedCountWhenDriftProbeFails(t *testing.T) {
	contract := searchRepairTestContract()
	repo := &searchRepairRepositoryStub{
		contract:    contract,
		run:         &repository.SearchRepairRun{RunID: "33333333-3333-4333-8333-333333333333", LeaseToken: "44444444-4444-4444-8444-444444444444"},
		documents:   []repository.SearchRepairDocument{searchRepairTestDocument(contract)},
		applyResult: &repository.SearchRepairApplyResult{UpdatedCount: 1},
		applyErr:    errors.New("drift probe unavailable"),
		countErr:    errors.New("drift count unavailable"),
	}
	executor := &searchRepairExecutorStub{result: semanticwrite.Result{Embeddings: []semanticwrite.Embedding{{DocumentHash: "hash", Vector: []float32{1, 2}}}}}
	svc := NewSearchRepairService(SearchRepairDependencies{Repository: repo, Executor: executor, WorkerID: "worker"})

	result, err := svc.Run(context.Background(), time.Now().UTC(), true)

	require.ErrorIs(t, err, ErrSearchRepairFailed)
	require.Equal(t, int64(1), result.UpdatedCount)
	require.Equal(t, "deferred", result.Status)
	require.Equal(t, "reconciliation_count_failed", result.ErrorCode)
	require.NotNil(t, repo.finish)
	require.Equal(t, int64(1), repo.finish.UpdatedCount)
	require.Equal(t, "deferred", repo.finish.Status)
	require.Equal(t, "reconciliation_count_failed", repo.finish.LastError)
}

func TestSearchReconciliationRunClearsDriftWhenSelectionConverged(t *testing.T) {
	contract := searchRepairTestContract()
	repo := &searchRepairRepositoryStub{
		contract: contract,
		run:      &repository.SearchRepairRun{RunID: "33333333-3333-4333-8333-333333333333", LeaseToken: "44444444-4444-4444-8444-444444444444"},
	}
	svc := NewSearchRepairService(SearchRepairDependencies{Repository: repo, Executor: &searchRepairExecutorStub{}, WorkerID: "worker"})

	result, err := svc.Run(context.Background(), time.Now().UTC(), true)

	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Zero(t, result.SelectedCount)
	require.Zero(t, result.DriftedCount)
	require.NotNil(t, repo.finish)
	require.Zero(t, repo.finish.DriftedCount)
}

func TestSearchReconciliationEmptyPartialScanUsesAuthoritativeDriftCount(t *testing.T) {
	contract := searchRepairTestContract()
	repo := &searchRepairRepositoryStub{
		contract: contract,
		run:      &repository.SearchRepairRun{RunID: "33333333-3333-4333-8333-333333333333", LeaseToken: "44444444-4444-4444-8444-444444444444"},
		hasMore:  true,
		count:    0,
	}
	svc := NewSearchRepairService(SearchRepairDependencies{Repository: repo, Executor: &searchRepairExecutorStub{}, WorkerID: "worker"})

	result, err := svc.Run(context.Background(), time.Now().UTC(), true)

	require.NoError(t, err)
	require.Zero(t, result.DriftedCount)
	require.Equal(t, 1, repo.countCalls)
	require.NotNil(t, repo.finish)
	require.Zero(t, repo.finish.DriftedCount)
}

func TestSearchReconciliationSchedulerErrorsAreVisible(t *testing.T) {
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	logger := &searchRepairLoggerStub{}
	svc := NewSearchRepairService(SearchRepairDependencies{Metrics: metrics, Logger: logger})

	svc.(*searchRepairService).recordProcessDueError()

	require.Equal(t, 1, metrics.EmbeddingReconciliationRunCount("scheduler_error"))
	require.Equal(t, []string{"search_reconciliation_process_due_failed"}, logger.warnings)
}

func searchRepairTestContract() *repository.ActiveSearchContract {
	return &repository.ActiveSearchContract{
		EmbeddingContractID: "11111111-1111-4111-8111-111111111111", SearchIndexGenerationID: "22222222-2222-4222-8222-222222222222",
		EmbeddingDimensions: 2, EmbeddingModel: "model", IndexGeneration: 1,
	}
}

func searchRepairTestDocument(contract *repository.ActiveSearchContract) repository.SearchRepairDocument {
	return repository.SearchRepairDocument{
		TeamID: "team", SearchDocumentID: "doc", OwnerProfileID: "owner", SourceKind: "evidence", SourceID: "source",
		SourceVersion: 1, ProjectionFormat: 1, DocumentVersion: 1, EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions, DocumentText: "canonical document", DocumentHash: "hash", StoredDocumentHash: "hash",
	}
}

func TestSearchReconciliationProcessDueRunsAtConfiguredMinute(t *testing.T) {
	contract := searchRepairTestContract()
	repo := &searchRepairRepositoryStub{
		contract:  contract,
		run:       &repository.SearchRepairRun{RunID: "33333333-3333-4333-8333-333333333333", LeaseToken: "44444444-4444-4444-8444-444444444444"},
		documents: []repository.SearchRepairDocument{searchRepairTestDocument(contract)},
		now:       time.Date(2026, 8, 28, 4, 30, 20, 0, time.UTC),
	}
	executor := &searchRepairExecutorStub{result: semanticwrite.Result{Embeddings: []semanticwrite.Embedding{{DocumentHash: "hash", Vector: []float32{1, 2}}}}}
	svc := NewSearchRepairService(SearchRepairDependencies{
		Repository: repo, Executor: executor, AppConfig: searchRepairConfigStub{value: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}}, WorkerID: "worker", ProviderTimeout: 45 * time.Second,
	})

	result, err := svc.ProcessDue(context.Background())

	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, int64(1), result.SelectedCount)
	require.Equal(t, 45*time.Second, executor.plan.Timeout)
	require.NotNil(t, repo.apply)
	require.NotNil(t, repo.finish)
}

func TestSearchReconciliationProcessDueCreatesOverdueRun(t *testing.T) {
	contract := searchRepairTestContract()
	repo := &searchRepairRepositoryStub{
		contract: contract,
		run:      &repository.SearchRepairRun{RunID: "33333333-3333-4333-8333-333333333333", LeaseToken: "44444444-4444-4444-8444-444444444444"},
		now:      time.Date(2026, 8, 28, 4, 31, 20, 0, time.UTC),
	}
	svc := NewSearchRepairService(SearchRepairDependencies{
		Repository: repo,
		Executor:   &searchRepairExecutorStub{},
		AppConfig:  searchRepairConfigStub{value: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:   "worker",
	})

	result, err := svc.ProcessDue(context.Background())

	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.True(t, repo.reserve.CreateIfMissing)
}

func TestSearchReconciliationLeaseCoversConfiguredProviderTimeout(t *testing.T) {
	contract := searchRepairTestContract()
	repo := &searchRepairRepositoryStub{
		contract: contract,
		run:      &repository.SearchRepairRun{RunID: "33333333-3333-4333-8333-333333333333", LeaseToken: "44444444-4444-4444-8444-444444444444"},
	}
	svc := NewSearchRepairService(SearchRepairDependencies{
		Repository: repo, Executor: &searchRepairExecutorStub{}, WorkerID: "worker", ProviderTimeout: 16 * time.Minute,
	})

	result, err := svc.Run(context.Background(), time.Now().UTC(), true)

	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, 16*time.Minute+searchRepairLeaseGrace, repo.reserve.Lease)
}

func TestSearchReconciliationProcessDueRejectsInvalidRuntime(t *testing.T) {
	cases := []struct {
		name        string
		config      searchRepairConfigStub
		distributed bool
		timeErr     error
	}{
		{name: "config error", config: searchRepairConfigStub{err: errors.New("config unavailable")}},
		{name: "local timezone in distributed mode", config: searchRepairConfigStub{value: domain.GeneralRuntimeConfig{Timezone: "Local", EmbeddingReconciliationStartTimeLocal: "04:30"}}, distributed: true},
		{name: "invalid timezone", config: searchRepairConfigStub{value: domain.GeneralRuntimeConfig{Timezone: "No/Such", EmbeddingReconciliationStartTimeLocal: "04:30"}}},
		{name: "invalid start time", config: searchRepairConfigStub{value: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "25:00"}}},
		{name: "database clock error", config: searchRepairConfigStub{value: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}}, timeErr: errors.New("clock unavailable")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &searchRepairRepositoryStub{contract: searchRepairTestContract(), timeErr: tc.timeErr}
			svc := NewSearchRepairService(SearchRepairDependencies{
				Repository: repo, Executor: &searchRepairExecutorStub{}, AppConfig: tc.config, WorkerID: "worker", DistributedCoordinationRequired: tc.distributed,
			})
			_, err := svc.ProcessDue(context.Background())
			require.ErrorIs(t, err, ErrSearchRepairFailed)
		})
	}
}

func TestSearchReconciliationStartStopIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := NewSearchRepairService(SearchRepairDependencies{
		Repository: &searchRepairRepositoryStub{contract: searchRepairTestContract(), now: time.Date(2026, 8, 28, 4, 29, 0, 0, time.UTC)},
		Executor:   &searchRepairExecutorStub{},
		AppConfig:  searchRepairConfigStub{value: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:   "worker",
	})

	svc.Start(ctx)
	svc.Start(ctx)
	cancel()
	svc.Stop()
	svc.Stop()
}

func TestSearchReconciliationExecutorFailureClassification(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status string
		code   string
	}{
		{name: "timeout", err: context.DeadlineExceeded, status: "deferred", code: "embedding_timeout"},
		{name: "provider timeout", err: semanticwrite.ErrProviderTimeout, status: "deferred", code: "embedding_timeout"},
		{name: "canceled", err: context.Canceled, status: "deferred", code: "embedding_cancelled"},
		{name: "unavailable", err: semanticwrite.ErrProviderUnavailable, status: "deferred", code: "embedding_unavailable"},
		{name: "invalid response", err: semanticwrite.ErrProviderResponseInvalid, status: "failed", code: "embedding_response_invalid"},
		{name: "unknown", err: errors.New("unknown"), status: "failed", code: "reconciliation_snapshot_invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contract := searchRepairTestContract()
			repo := &searchRepairRepositoryStub{contract: contract, run: &repository.SearchRepairRun{RunID: "33333333-3333-4333-8333-333333333333", LeaseToken: "44444444-4444-4444-8444-444444444444"}, documents: []repository.SearchRepairDocument{searchRepairTestDocument(contract)}, countErr: errors.New("count unavailable")}
			svc := NewSearchRepairService(SearchRepairDependencies{Repository: repo, Executor: &searchRepairExecutorStub{err: tc.err}, WorkerID: "worker"})
			result, err := svc.Run(context.Background(), time.Now().UTC(), true)
			require.ErrorIs(t, err, ErrSearchRepairFailed)
			require.Equal(t, tc.status, result.Status)
			require.Equal(t, tc.code, result.ErrorCode)
			require.Equal(t, tc.status, repo.finish.Status)
			require.EqualValues(t, 1, result.DriftedCount)
			require.EqualValues(t, 1, repo.finish.DriftedCount)
		})
	}
}

func TestSearchReconciliationPlanHandlesRetiredAndDuplicateDocuments(t *testing.T) {
	contract := searchRepairTestContract()
	retired := searchRepairTestDocument(contract)
	retired.Retired = true
	duplicate := searchRepairTestDocument(contract)
	duplicate.SearchDocumentID = "doc-2"

	plan, err := searchRepairPlan([]repository.SearchRepairDocument{retired, searchRepairTestDocument(contract), duplicate}, contract, time.Second)

	require.NoError(t, err)
	require.Len(t, plan.Documents, 1)
}

func TestSearchReconciliationPlanRejectsInvalidDocuments(t *testing.T) {
	contract := searchRepairTestContract()
	cases := []struct {
		name string
		docs []repository.SearchRepairDocument
	}{
		{name: "missing hash", docs: []repository.SearchRepairDocument{{DocumentText: "text"}}},
		{name: "missing text", docs: []repository.SearchRepairDocument{{DocumentHash: "hash"}}},
		{name: "conflicting duplicate hash", docs: []repository.SearchRepairDocument{{DocumentHash: "hash", DocumentText: "one"}, {DocumentHash: "hash", DocumentText: "two"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := searchRepairPlan(tc.docs, contract, time.Second)
			require.Error(t, err)
		})
	}
	tooMany := make([]repository.SearchRepairDocument, semanticwrite.MaxDocuments+1)
	for index := range tooMany {
		tooMany[index] = repository.SearchRepairDocument{DocumentHash: fmt.Sprintf("hash-%d", index), DocumentText: "text"}
	}
	_, err := searchRepairPlan(tooMany, contract, time.Second)
	require.Error(t, err)
}
