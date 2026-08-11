package embeddingservice

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

type reconciliationConfigStub struct{ runtime domain.GeneralRuntimeConfig }

func (s reconciliationConfigStub) GeneralRuntimeConfig(context.Context) (domain.GeneralRuntimeConfig, error) {
	return s.runtime, nil
}

type reconciliationRepositoryStub struct {
	run                *repository.EmbeddingReconciliationRun
	job                *repository.EmbeddingJob
	jobs               []*repository.EmbeddingJob
	claimed            bool
	reserved           []repository.ReserveEmbeddingReconciliationRunInput
	marked             bool
	markInput          *repository.MarkEmbeddingReconciliationCanaryAttemptInput
	canaryOK           bool
	canaryContext      context.Context
	canaryContextErr   error
	markErr            error
	canaryErr          error
	requeued           int64
	requeueErr         error
	requeueInput       *repository.RequeueEmbeddingReconciliationJobsInput
	requeueContext     context.Context
	requeueContextErr  error
	completed          *repository.CompleteEmbeddingReconciliationRunInput
	completeContext    context.Context
	completeContextErr error
	resetInputs        []repository.ResetEmbeddingReconciliationCanaryInput
	resetErr           error
	markErrs           []error
}

func (s *reconciliationRepositoryStub) GetSearchConvergence(context.Context, repository.SearchConvergenceInput) (*repository.SearchConvergence, error) {
	return &repository.SearchConvergence{Status: "converged"}, nil
}
func (s *reconciliationRepositoryStub) ReserveEmbeddingReconciliationRun(_ context.Context, input repository.ReserveEmbeddingReconciliationRunInput) (*repository.EmbeddingReconciliationRun, bool, error) {
	s.reserved = append(s.reserved, input)
	if s.run == nil {
		s.run = &repository.EmbeddingReconciliationRun{RunID: "run-1", LeaseToken: "lease-1", Status: "running", CandidateCutoff: time.Now().UTC()}
	}
	if s.claimed {
		return s.run, false, nil
	}
	s.claimed = true
	return s.run, true, nil
}

func (s *reconciliationRepositoryStub) SelectEmbeddingReconciliationCanary(context.Context, repository.SelectEmbeddingReconciliationCanaryInput) (*repository.EmbeddingJob, error) {
	if len(s.jobs) > 0 {
		job := s.jobs[0]
		s.jobs = s.jobs[1:]
		return job, nil
	}
	return s.job, nil
}
func (s *reconciliationRepositoryStub) MarkEmbeddingReconciliationCanaryAttempt(_ context.Context, input repository.MarkEmbeddingReconciliationCanaryAttemptInput) error {
	s.marked = true
	s.markInput = &input
	if s.markErr != nil {
		return s.markErr
	}
	if len(s.markErrs) > 0 {
		err := s.markErrs[0]
		s.markErrs = s.markErrs[1:]
		if err != nil {
			return err
		}
	}
	if s.job != nil {
		s.job.Attempts = 1
	}
	return nil
}
func (s *reconciliationRepositoryStub) CompleteEmbeddingReconciliationCanary(ctx context.Context, _ repository.CompleteEmbeddingReconciliationCanaryInput) error {
	s.canaryContext = ctx
	s.canaryContextErr = ctx.Err()
	if s.canaryErr != nil {
		return s.canaryErr
	}
	s.canaryOK = true
	return nil
}
func (s *reconciliationRepositoryStub) ResetEmbeddingReconciliationCanary(_ context.Context, input repository.ResetEmbeddingReconciliationCanaryInput) error {
	s.resetInputs = append(s.resetInputs, input)
	return s.resetErr
}
func (s *reconciliationRepositoryStub) RequeueEmbeddingReconciliationJobs(ctx context.Context, input repository.RequeueEmbeddingReconciliationJobsInput) (int64, error) {
	s.requeueContext = ctx
	s.requeueContextErr = ctx.Err()
	s.requeueInput = &input
	if s.requeued == 0 {
		s.requeued = 3
	}
	return s.requeued, s.requeueErr
}

func (s *reconciliationRepositoryStub) CompleteEmbeddingReconciliationRun(ctx context.Context, input repository.CompleteEmbeddingReconciliationRunInput) error {
	s.completeContext = ctx
	s.completeContextErr = ctx.Err()
	s.completed = &input
	return nil
}

type reconciliationRawProvider struct {
	available     bool
	model         string
	dims          int
	vector        []float32
	err           error
	errs          []error
	calls         int
	embedContexts []context.Context
}

func (p *reconciliationRawProvider) Embed(ctx context.Context, _ string) ([]float32, string, error) {
	p.embedContexts = append(p.embedContexts, ctx)
	p.calls++
	err := p.err
	if index := p.calls - 1; index < len(p.errs) {
		err = p.errs[index]
	}
	return p.vector, p.model, err
}
func (p *reconciliationRawProvider) EmbedBatch(context.Context, []string) ([][]float32, string, error) {
	return nil, "", errors.New("batch should not be used by canary")
}
func (p *reconciliationRawProvider) ModelName() string { return p.model }
func (p *reconciliationRawProvider) Dimensions() int   { return p.dims }
func (p *reconciliationRawProvider) IsAvailable() bool { return p.available }

type reconciliationMetricsStub struct {
	observability.DiscoverabilityMetrics
	runs             []string
	canaries         []string
	durationOutcomes []string
	durations        []float64
}

func (m *reconciliationMetricsStub) ObserveEmbeddingReconciliationRun(outcome string) {
	m.runs = append(m.runs, outcome)
}
func (m *reconciliationMetricsStub) ObserveEmbeddingReconciliationCanary(outcome string) {
	m.canaries = append(m.canaries, outcome)
}
func (*reconciliationMetricsStub) ObserveEmbeddingReconciliationJobs(string, string, string, string, int) {
}
func (m *reconciliationMetricsStub) ObserveEmbeddingReconciliationDuration(seconds float64, outcome string) {
	m.durations = append(m.durations, seconds)
	m.durationOutcomes = append(m.durationOutcomes, outcome)
}

type reconciliationLogRecord struct {
	message string
	attrs   []observability.LogAttr
}

type reconciliationLoggerStub struct {
	infos     []reconciliationLogRecord
	warnings  []string
	warnAttrs []observability.LogAttr
}

func (l *reconciliationLoggerStub) Info(message string, attrs ...observability.LogAttr) {
	l.infos = append(l.infos, reconciliationLogRecord{message: message, attrs: attrs})
}
func (*reconciliationLoggerStub) Error(string, error, ...observability.LogAttr) {}
func (l *reconciliationLoggerStub) Warn(message string, attrs ...observability.LogAttr) {
	l.warnings = append(l.warnings, message)
	l.warnAttrs = append(l.warnAttrs, attrs...)
}

func TestEmbeddingReconciliationLogsNumericRecoveryCounts(t *testing.T) {
	logger := &reconciliationLoggerStub{}
	service := &embeddingReconciliationService{logger: logger}
	run := &repository.EmbeddingReconciliationRun{RunID: "run-1", Status: "running"}

	service.logInfo(context.Background(), "completed", run, map[string]any{"recovered_count": 1, "requeued_count": int64(7)})
	service.logWarn(context.Background(), "deferred", run, map[string]any{"requeued_count": int64(5)})

	require.Len(t, logger.infos, 1)
	assert.Equal(t, 1, reconciliationLogAttrValue(logger.infos[0].attrs, "recovered_count"))
	assert.Equal(t, 7, reconciliationLogAttrValue(logger.infos[0].attrs, "requeued_count"))
	assert.Equal(t, 5, reconciliationLogAttrValue(logger.warnAttrs, "requeued_count"))
}

func reconciliationLogAttrValue(attrs []observability.LogAttr, key string) any {
	for _, attr := range attrs {
		if attr.Key == key {
			return attr.Value
		}
	}
	return nil
}
func (*reconciliationLoggerStub) Debug(string, ...observability.LogAttr) {}
func (l *reconciliationLoggerStub) With(...observability.LogAttr) observability.LogProvider {
	return l
}

func TestEmbeddingReconciliationProcessDueErrorsAreObservable(t *testing.T) {
	metrics := &reconciliationMetricsStub{}
	logger := &reconciliationLoggerStub{}
	svc := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Metrics: metrics, Logger: logger,
	}).(*embeddingReconciliationService)

	svc.recordProcessDueError()

	require.Equal(t, []string{"scheduler_error"}, metrics.runs)
	require.Equal(t, []string{"embedding_reconciliation_process_due_failed"}, logger.warnings)
}

func TestEmbeddingReconciliationDoesNotCompleteReclaimedCanaryAsSkipped(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	attemptedAt := time.Date(2026, 8, 10, 4, 31, 0, 0, time.UTC)
	startedAt := attemptedAt.Add(-time.Minute)
	reconciliation := &reconciliationRepositoryStub{
		run: &repository.EmbeddingReconciliationRun{
			RunID: "run-1", LeaseToken: "lease-1", Status: string(domain.EmbeddingReconciliationRunning),
			CandidateCutoff: attemptedAt, CanaryAttemptedAt: &attemptedAt, StartedAt: &startedAt,
		},
		job: &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "job-1", EmbeddingDimensions: 3, DocumentText: "canary"},
	}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3, vector: []float32{1, 0, 0}}
	metrics := &reconciliationMetricsStub{}
	svc := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
		Metrics:   metrics,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return attemptedAt.Add(time.Minute) },
	})

	result, err := svc.ProcessDue(context.Background())
	require.NoError(t, err)
	assert.Equal(t, string(domain.EmbeddingReconciliationAmbiguous), result.Status)
	assert.Equal(t, 0, provider.calls)
	assert.Zero(t, reconciliation.requeued)
	require.NotNil(t, reconciliation.completed)
	assert.Equal(t, string(domain.EmbeddingReconciliationAmbiguous), reconciliation.completed.Status)
	assert.Equal(t, "ambiguous", reconciliation.completed.CanaryOutcome)
	assert.Equal(t, []string{string(domain.EmbeddingReconciliationAmbiguous)}, metrics.durationOutcomes)
	assert.Greater(t, metrics.durations[0], float64(0))
}

func TestEmbeddingReconciliationReportsDatabaseClockSkew(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	now := time.Date(2026, 8, 10, 4, 31, 0, 0, time.UTC)
	startedAt := now.Add(time.Minute)
	reconciliation := &reconciliationRepositoryStub{
		run: &repository.EmbeddingReconciliationRun{
			RunID: "run-1", LeaseToken: "lease-1", Status: string(domain.EmbeddingReconciliationRunning),
			CandidateCutoff: now, StartedAt: &startedAt,
		},
	}
	metrics := &reconciliationMetricsStub{}
	logger := &reconciliationLoggerStub{}
	service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation,
		Provider: &reconciliationRawProvider{available: true, model: "model", dims: 3, vector: []float32{1, 0, 0}},
		Metrics:  metrics, Logger: logger,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	result, err := service.ProcessDue(context.Background())
	require.NoError(t, err)
	require.Equal(t, string(domain.EmbeddingReconciliationCompleted), result.Status)
	require.Equal(t, []float64{0}, metrics.durations)
	require.Contains(t, logger.warnings, "embedding_reconciliation_clock_skew")
}

func TestEmbeddingReconciliationUsesOneRawCanaryThenRequeuesBacklog(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	reconciliation := &reconciliationRepositoryStub{job: &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "job-1", Attempts: 20, EmbeddingDimensions: 3, DocumentText: "canary"}}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3, vector: []float32{1, 0, 0}}
	logger := &reconciliationLoggerStub{}
	now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
	service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
		Logger:    logger,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	result, err := service.ProcessDue(context.Background())
	require.NoError(t, err)
	if result.Status != string(domain.EmbeddingReconciliationCompleted) || !result.CanarySucceeded {
		t.Fatalf("result = %#v", result)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want one raw canary", provider.calls)
	}
	if !reconciliation.marked || !reconciliation.canaryOK || reconciliation.requeued != 3 {
		t.Fatalf("reconciliation state = %#v", reconciliation)
	}
	require.NotNil(t, reconciliation.markInput)
	require.NotNil(t, reconciliation.requeueInput)
	assert.Equal(t, reconciliationLease, reconciliation.markInput.Lease)
	assert.Equal(t, reconciliationLease, reconciliation.requeueInput.Lease)
	require.NotNil(t, reconciliation.completed)
	if reconciliation.completed.RequeuedCount != 3 || reconciliation.completed.RecoveredCount != 1 {
		t.Fatalf("completed run = %#v", reconciliation.completed)
	}
	require.Len(t, logger.infos, 2)
	assert.Equal(t, "embedding_reconciliation_completed", logger.infos[1].message)
	assert.Equal(t, string(domain.EmbeddingReconciliationCompleted), reconciliationLogAttrValue(logger.infos[1].attrs, "status"))
}

func TestEmbeddingReconciliationLogsCompletedStatusWhenCanaryIsSkipped(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	reconciliation := &reconciliationRepositoryStub{}
	logger := &reconciliationLoggerStub{}
	now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
	service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: &reconciliationRawProvider{available: true, model: "model", dims: 3},
		Logger:    logger,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	result, err := service.ProcessDue(context.Background())
	require.NoError(t, err)
	assert.Equal(t, string(domain.EmbeddingReconciliationCompleted), result.Status)
	require.Len(t, logger.infos, 2)
	assert.Equal(t, "embedding_reconciliation_completed", logger.infos[1].message)
	assert.Equal(t, string(domain.EmbeddingReconciliationCompleted), reconciliationLogAttrValue(logger.infos[1].attrs, "status"))
}

func TestEmbeddingReconciliationFinalizesSuccessfulCanaryAfterContextCancellation(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	reconciliation := &reconciliationRepositoryStub{job: &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "job-1", Attempts: 20, EmbeddingDimensions: 3, DocumentText: "canary"}}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3, vector: []float32{1, 0, 0}}
	now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
	service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := service.ProcessDue(ctx)
	require.NoError(t, err)
	assert.Equal(t, string(domain.EmbeddingReconciliationCompleted), result.Status)
	assert.True(t, result.CanarySucceeded)
	assert.NoError(t, search.completeContextErr)
	assert.NoError(t, reconciliation.canaryContextErr)
	assert.NoError(t, reconciliation.requeueContextErr)
	assert.NoError(t, reconciliation.completeContextErr)
	assert.NotNil(t, reconciliation.completed)
	assert.Equal(t, "succeeded", reconciliation.completed.CanaryOutcome)
}

func TestEmbeddingReconciliationSkipsStaleCanaryAndReleasesBacklog(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	search.completeErrs["job-1"] = repository.ErrSearchStaleVersion
	reconciliation := &reconciliationRepositoryStub{job: &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "job-1", Attempts: 20, EmbeddingDimensions: 3, DocumentText: "stale canary"}}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3, vector: []float32{1, 0, 0}}
	now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
	service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	result, err := service.ProcessDue(context.Background())
	require.NoError(t, err)
	assert.Equal(t, string(domain.EmbeddingReconciliationCompleted), result.Status)
	assert.True(t, result.CanarySucceeded)
	assert.Zero(t, result.RecoveredCount, "a stale canary proves provider availability but does not recover a job")
	assert.EqualValues(t, 3, result.RequeuedCount)
	require.NotNil(t, reconciliation.completed)
	assert.Equal(t, "succeeded", reconciliation.completed.CanaryOutcome)
	assert.Zero(t, reconciliation.completed.RecoveredCount)
}

func TestEmbeddingReconciliationPersistsPartialRequeueProgress(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
	startedAt := now.Add(-time.Minute)
	reconciliation := &reconciliationRepositoryStub{
		run:        &repository.EmbeddingReconciliationRun{StartedAt: &startedAt},
		job:        &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "job-1", Attempts: 20, EmbeddingDimensions: 3, DocumentText: "canary"},
		requeued:   500,
		requeueErr: errors.New("later batch failed"),
	}
	metrics := &reconciliationMetricsStub{}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3, vector: []float32{1, 0, 0}}
	service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
		Metrics:   metrics,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	result, err := service.ProcessDue(context.Background())
	require.ErrorContains(t, err, "later batch failed")
	assert.Equal(t, string(domain.EmbeddingReconciliationDeferred), result.Status)
	assert.True(t, result.CanarySucceeded)
	assert.EqualValues(t, 500, result.RequeuedCount)
	assert.EqualValues(t, 1, result.RecoveredCount)
	require.NotNil(t, reconciliation.completed)
	assert.Equal(t, string(domain.EmbeddingReconciliationDeferred), reconciliation.completed.Status)
	assert.Equal(t, "succeeded", reconciliation.completed.CanaryOutcome)
	assert.EqualValues(t, 500, reconciliation.completed.RequeuedCount)
	assert.EqualValues(t, 1, reconciliation.completed.RecoveredCount)
	assert.NotSame(t, reconciliation.requeueContext, reconciliation.completeContext)
	assert.Equal(t, []string{string(domain.EmbeddingReconciliationDeferred)}, metrics.durationOutcomes)
	assert.Len(t, metrics.durations, 1)
	assert.GreaterOrEqual(t, metrics.durations[0], float64(0))
}

func TestEmbeddingReconciliationFailedCanaryDoesNotReleaseBacklog(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	reconciliation := &reconciliationRepositoryStub{job: &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "job-1", Attempts: 20, EmbeddingDimensions: 3, DocumentText: "canary"}}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3, err: &embedding.ProviderHTTPError{Status: 429, Code: "insufficient_quota"}}
	metrics := &reconciliationMetricsStub{}
	now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
	service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
		Metrics:   metrics,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	_, err := service.ProcessDue(context.Background())
	require.NoError(t, err)
	if provider.calls != 1 || reconciliation.requeued != 0 {
		t.Fatalf("provider calls=%d requeued=%d", provider.calls, reconciliation.requeued)
	}
	require.NotNil(t, reconciliation.completed)
	if reconciliation.completed.Status != string(domain.EmbeddingReconciliationDeferred) || reconciliation.completed.FailureCode != string(domain.EmbeddingFailureProviderQuotaExhausted) {
		t.Fatalf("completed run = %#v", reconciliation.completed)
	}
	assert.Equal(t, []string{string(domain.EmbeddingReconciliationDeferred)}, metrics.durationOutcomes)
}

func TestEmbeddingReconciliationSkipsInputRejectedCanary(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	reconciliation := &reconciliationRepositoryStub{
		jobs: []*repository.EmbeddingJob{
			{TeamID: "team-1", EmbeddingJobID: "bad-job", Attempts: 20, EmbeddingDimensions: 3, DocumentText: "bad input"},
			{TeamID: "team-1", EmbeddingJobID: "good-job", Attempts: 20, EmbeddingDimensions: 3, DocumentText: "healthy input"},
		},
	}
	provider := &reconciliationRawProvider{
		available: true, model: "model", dims: 3, vector: []float32{1, 0, 0},
		errs: []error{&embedding.ProviderHTTPError{Status: 413}, nil},
	}
	now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
	service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	result, err := service.ProcessDue(context.Background())
	require.NoError(t, err)
	assert.Equal(t, string(domain.EmbeddingReconciliationCompleted), result.Status)
	assert.True(t, result.CanarySucceeded)
	assert.Equal(t, 2, provider.calls)
	require.Len(t, search.failInputs, 1)
	assert.Equal(t, string(domain.EmbeddingFailureInputRejected), search.failInputs[0].FailureCode)
	require.Len(t, reconciliation.resetInputs, 1)
	assert.Equal(t, "bad-job", reconciliation.resetInputs[0].CanaryJobID)
	assert.EqualValues(t, 3, result.RequeuedCount)
}

func TestEmbeddingReconciliationDefersAfterInputRejectedCanaryLimit(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	jobs := make([]*repository.EmbeddingJob, reconciliationCanaryAttemptLimit)
	errs := make([]error, reconciliationCanaryAttemptLimit)
	for i := range jobs {
		jobs[i] = &repository.EmbeddingJob{
			TeamID: "team-1", EmbeddingJobID: fmt.Sprintf("rejected-job-%d", i), Attempts: 20,
			EmbeddingDimensions: 3, DocumentText: "rejected input",
		}
		errs[i] = &embedding.ProviderHTTPError{Status: 413}
	}
	reconciliation := &reconciliationRepositoryStub{jobs: jobs}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3, errs: errs}
	now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
	service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	result, err := service.ProcessDue(context.Background())
	require.NoError(t, err)
	assert.Equal(t, string(domain.EmbeddingReconciliationDeferred), result.Status)
	assert.Equal(t, reconciliationCanaryAttemptLimit, provider.calls)
	assert.Len(t, search.failInputs, reconciliationCanaryAttemptLimit)
	assert.Len(t, reconciliation.resetInputs, reconciliationCanaryAttemptLimit)
	require.NotNil(t, reconciliation.completed)
	assert.Equal(t, string(domain.EmbeddingReconciliationDeferred), reconciliation.completed.Status)
	assert.Equal(t, string(domain.EmbeddingFailurePermanent), reconciliation.completed.FailureClass)
	assert.Equal(t, string(domain.EmbeddingFailureInputRejected), reconciliation.completed.FailureCode)
	for _, embedContext := range provider.embedContexts {
		assert.ErrorIs(t, embedContext.Err(), context.Canceled)
	}
}

func TestEmbeddingReconciliationUsesFreshContextAfterInputRejectedCleanupFails(t *testing.T) {
	for _, test := range []struct {
		name      string
		canaryErr error
		resetErr  error
	}{
		{name: "outcome", canaryErr: errors.New("outcome persistence failed")},
		{name: "reset", resetErr: errors.New("reset persistence failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			search := newEmbeddingSearchStub()
			search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
			reconciliation := &reconciliationRepositoryStub{
				job:       &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "bad-job", Attempts: 20, EmbeddingDimensions: 3, DocumentText: "bad input"},
				canaryErr: test.canaryErr,
				resetErr:  test.resetErr,
			}
			provider := &reconciliationRawProvider{available: true, model: "model", dims: 3, errs: []error{&embedding.ProviderHTTPError{Status: 413}}}
			now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
			service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
				Search: search, Reconciliation: reconciliation, Provider: provider,
				AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
				WorkerID:  "worker-1", Now: func() time.Time { return now },
			})

			result, err := service.ProcessDue(context.Background())
			require.Error(t, err)
			assert.Equal(t, string(domain.EmbeddingReconciliationRunning), result.Status)
			require.NotNil(t, reconciliation.canaryContext)
			require.NotNil(t, reconciliation.completeContext)
			require.False(t, reconciliation.canaryContext == reconciliation.completeContext)
			assert.NoError(t, reconciliation.completeContextErr)
			require.NotNil(t, reconciliation.completed)
			assert.Equal(t, string(domain.EmbeddingReconciliationAmbiguous), reconciliation.completed.Status)
			assert.Equal(t, string(domain.EmbeddingFailurePermanent), reconciliation.completed.FailureClass)
			assert.Equal(t, string(domain.EmbeddingFailureInputRejected), reconciliation.completed.FailureCode)
		})
	}
}

func TestEmbeddingReconciliationSkipsStaleCanaryClaim(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	reconciliation := &reconciliationRepositoryStub{
		jobs: []*repository.EmbeddingJob{
			{TeamID: "team-1", EmbeddingJobID: "stale-job", EmbeddingDimensions: 3, DocumentText: "stale canary"},
			{TeamID: "team-1", EmbeddingJobID: "healthy-job", EmbeddingDimensions: 3, DocumentText: "healthy canary"},
		},
		markErrs: []error{repository.ErrEmbeddingReconciliationCanarySkipped, nil},
	}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3, vector: []float32{1, 0, 0}}
	now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
	service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	result, err := service.ProcessDue(context.Background())
	require.NoError(t, err)
	assert.Equal(t, string(domain.EmbeddingReconciliationCompleted), result.Status)
	assert.True(t, result.CanaryAttempted)
	assert.True(t, result.CanarySucceeded)
	assert.Equal(t, 1, provider.calls)
	assert.Equal(t, "healthy-job", reconciliation.markInput.CanaryJobID)
}

func TestEmbeddingReconciliationFinalizesFailedCanaryAfterContextCancellation(t *testing.T) {
	search := newEmbeddingSearchStub()
	reconciliation := &reconciliationRepositoryStub{}
	service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, WorkerID: "worker-1",
	}).(*embeddingReconciliationService)
	run := &repository.EmbeddingReconciliationRun{RunID: "run-1", LeaseToken: "lease-1", Status: "running"}
	job := &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "job-1"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := service.finishFailedCanary(ctx, run, job,
		string(domain.EmbeddingFailureTransient), string(domain.EmbeddingFailureProviderTimeout), "provider timed out", true)
	require.NoError(t, err)
	require.NotNil(t, search.failContext)
	require.NotNil(t, reconciliation.canaryContext)
	require.NotNil(t, reconciliation.completeContext)
	assert.NotSame(t, search.failContext, reconciliation.completeContext)
	assert.NotSame(t, reconciliation.canaryContext, reconciliation.completeContext)
	assert.NoError(t, search.failContextErr)
	assert.NoError(t, reconciliation.canaryContextErr)
	assert.NoError(t, reconciliation.completeContextErr)
}

func TestEmbeddingReconciliationClassifiesUnavailableProviderAsTransient(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	reconciliation := &reconciliationRepositoryStub{job: &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "job-1", Attempts: 20, EmbeddingDimensions: 3, DocumentText: "canary"}}
	metrics := &reconciliationMetricsStub{}
	provider := &reconciliationRawProvider{available: false, model: "model", dims: 3}
	now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
	service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider, Metrics: metrics,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	_, err := service.ProcessDue(context.Background())
	require.NoError(t, err)
	require.Len(t, search.failInputs, 1)
	assert.Equal(t, string(domain.EmbeddingFailureTransient), search.failInputs[0].FailureClass)
	assert.Equal(t, string(domain.EmbeddingFailureProviderNetworkError), search.failInputs[0].FailureCode)
	require.NotNil(t, reconciliation.completed)
	assert.Equal(t, string(domain.EmbeddingFailureProviderNetworkError), reconciliation.completed.FailureCode)
	assert.Empty(t, reconciliation.completed.CanaryOutcome)
	assert.Empty(t, metrics.canaries)
}

func TestEmbeddingReconciliationDefersWhenCanaryOutcomePersistenceFails(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	reconciliation := &reconciliationRepositoryStub{
		job:       &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "job-1", EmbeddingDimensions: 3, DocumentText: "canary"},
		canaryErr: errors.New("canary outcome persistence failed"),
	}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3, vector: []float32{1, 0, 0}}
	now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
	svc := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	_, err := svc.ProcessDue(context.Background())
	require.Error(t, err)
	require.NotNil(t, reconciliation.canaryContext)
	require.NotNil(t, reconciliation.completeContext)
	assert.NotSame(t, reconciliation.canaryContext, reconciliation.completeContext)
	assert.NoError(t, reconciliation.completeContextErr)
	require.NotNil(t, reconciliation.completed)
	assert.Equal(t, string(domain.EmbeddingReconciliationAmbiguous), reconciliation.completed.Status)
	assert.Equal(t, "ambiguous", reconciliation.completed.CanaryOutcome)
	assert.Zero(t, reconciliation.requeued)
}

func TestEmbeddingReconciliationDoesNotRecordCanaryBeforeProviderAttempt(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	reconciliation := &reconciliationRepositoryStub{
		job:     &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "job-1", EmbeddingDimensions: 3, DocumentText: "canary"},
		markErr: errors.New("canary claim persistence failed"),
	}
	metrics := &reconciliationMetricsStub{}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3, vector: []float32{1, 0, 0}}
	now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
	svc := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider, Metrics: metrics,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	_, err := svc.ProcessDue(context.Background())
	require.ErrorContains(t, err, "canary claim persistence failed")
	require.NotNil(t, reconciliation.completed)
	assert.Empty(t, reconciliation.completed.CanaryOutcome)
	assert.Empty(t, metrics.canaries)
	assert.Zero(t, provider.calls)
}

func TestEmbeddingReconciliationDefersFailedCanaryOutcomePersistence(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	reconciliation := &reconciliationRepositoryStub{
		job:       &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "job-1", EmbeddingDimensions: 3, DocumentText: "canary"},
		canaryErr: errors.New("canary outcome persistence failed"),
	}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3, err: &embedding.ProviderHTTPError{Status: 429, Code: "insufficient_quota"}}
	now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
	svc := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	_, err := svc.ProcessDue(context.Background())
	require.Error(t, err)
	require.NotNil(t, reconciliation.completed)
	assert.Equal(t, string(domain.EmbeddingReconciliationAmbiguous), reconciliation.completed.Status)
	assert.Equal(t, "ambiguous", reconciliation.completed.CanaryOutcome)
	assert.Equal(t, string(domain.EmbeddingFailureProviderQuotaExhausted), reconciliation.completed.FailureCode)
}

func TestEmbeddingReconciliationUsesConfiguredLocalCalendarDate(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	reconciliation := &reconciliationRepositoryStub{}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3}
	now := time.Date(2026, 8, 10, 11, 30, 20, 0, time.UTC) // 04:30 in America/Los_Angeles.
	svc := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "America/Los_Angeles", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	_, err := svc.ProcessDue(context.Background())
	require.NoError(t, err)
	require.Len(t, reconciliation.reserved, 1)
	assert.Equal(t, 2026, reconciliation.reserved[0].LocalRunDate.Year())
	assert.Equal(t, time.August, reconciliation.reserved[0].LocalRunDate.Month())
	assert.Equal(t, 10, reconciliation.reserved[0].LocalRunDate.Day())
	assert.Equal(t, "America/Los_Angeles", reconciliation.reserved[0].LocalRunDate.Location().String())
}

func TestEmbeddingReconciliationRunsAfterMissedConfiguredMinute(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	reconciliation := &reconciliationRepositoryStub{}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3}
	now := time.Date(2026, 8, 10, 4, 31, 20, 0, time.UTC)
	svc := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	result, err := svc.ProcessDue(context.Background())
	require.NoError(t, err)
	require.Len(t, reconciliation.reserved, 1)
	assert.Equal(t, string(domain.EmbeddingReconciliationCompleted), result.Status)
	require.NotNil(t, reconciliation.completed)
	assert.Empty(t, reconciliation.completed.CanaryOutcome, "a run without candidates must not claim a successful canary")
}

func TestEmbeddingReconciliationDoesNotRunBeforeScheduledMinute(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	reconciliation := &reconciliationRepositoryStub{}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3}
	now := time.Date(2026, 8, 10, 4, 29, 59, 0, time.UTC)
	svc := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	result, err := svc.ProcessDue(context.Background())
	require.NoError(t, err)
	assert.Empty(t, result.RunID)
	assert.Empty(t, reconciliation.reserved)
	assert.Zero(t, provider.calls)
}

func TestEmbeddingFailureMessageIsClosedAndDoesNotPersistProviderText(t *testing.T) {
	assert.Equal(t, "embedding provider quota exhausted", domain.EmbeddingFailureMessage(string(domain.EmbeddingFailureProviderQuotaExhausted)))
	assert.Equal(t, "embedding processing failed", domain.EmbeddingFailureMessage(string(domain.EmbeddingFailureUnknown)))
}

func TestEmbeddingReconciliationStartStopIsIdempotent(t *testing.T) {
	svc := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{WorkerID: "worker-1"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)
	svc.Start(ctx)
	svc.Stop()
	svc.Stop()
}

func TestEmbeddingReconciliationValidatesDependenciesAndTimezone(t *testing.T) {
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3}
	config := reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}}
	repositoryStub := &reconciliationRepositoryStub{}
	search := newEmbeddingSearchStub()
	for _, test := range []struct {
		name string
		deps EmbeddingReconciliationDependencies
		want string
	}{
		{name: "search", deps: EmbeddingReconciliationDependencies{Reconciliation: repositoryStub, Provider: provider, AppConfig: config, WorkerID: "worker"}, want: "search repository"},
		{name: "reconciliation", deps: EmbeddingReconciliationDependencies{Search: search, Provider: provider, AppConfig: config, WorkerID: "worker"}, want: "reconciliation repository"},
		{name: "provider", deps: EmbeddingReconciliationDependencies{Search: search, Reconciliation: repositoryStub, AppConfig: config, WorkerID: "worker"}, want: "embedding provider"},
		{name: "config", deps: EmbeddingReconciliationDependencies{Search: search, Reconciliation: repositoryStub, Provider: provider, WorkerID: "worker"}, want: "app config"},
		{name: "worker", deps: EmbeddingReconciliationDependencies{Search: search, Reconciliation: repositoryStub, Provider: provider, AppConfig: config}, want: "worker_id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc := NewEmbeddingReconciliationService(test.deps).(*embeddingReconciliationService)
			require.ErrorContains(t, svc.validate(), test.want)
		})
	}
	valid := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{Search: search, Reconciliation: repositoryStub, Provider: provider, AppConfig: config, WorkerID: "worker"}).(*embeddingReconciliationService)
	require.NoError(t, valid.validate())
	_, err := loadReconciliationLocation("Invalid/Timezone")
	require.Error(t, err)
}

func TestEmbeddingReconciliationRequiresSharedTimezoneForDistributedCoordination(t *testing.T) {
	search := newEmbeddingSearchStub()
	repositoryStub := &reconciliationRepositoryStub{}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3}
	svc := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: repositoryStub, Provider: provider,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{
			Timezone: "Local", EmbeddingReconciliationStartTimeLocal: "04:30",
		}},
		WorkerID: "worker", DistributedCoordinationRequired: true,
	})

	_, err := svc.ProcessDue(context.Background())
	require.ErrorContains(t, err, "APP_TIMEZONE must be an explicit IANA timezone")
	assert.Empty(t, repositoryStub.reserved)
}
