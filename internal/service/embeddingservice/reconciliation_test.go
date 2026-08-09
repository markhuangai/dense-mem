package embeddingservice

import (
	"context"
	"errors"
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
	run        *repository.EmbeddingReconciliationRun
	job        *repository.EmbeddingJob
	claimed    bool
	reserved   []repository.ReserveEmbeddingReconciliationRunInput
	marked     bool
	canaryOK   bool
	markErr    error
	canaryErr  error
	requeued   int64
	requeueErr error
	completed  *repository.CompleteEmbeddingReconciliationRunInput
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
	return s.job, nil
}
func (s *reconciliationRepositoryStub) MarkEmbeddingReconciliationCanaryAttempt(context.Context, repository.MarkEmbeddingReconciliationCanaryAttemptInput) error {
	s.marked = true
	if s.markErr != nil {
		return s.markErr
	}
	if s.job != nil {
		s.job.Attempts = 1
	}
	return nil
}
func (s *reconciliationRepositoryStub) CompleteEmbeddingReconciliationCanary(_ context.Context, _ repository.CompleteEmbeddingReconciliationCanaryInput) error {
	if s.canaryErr != nil {
		return s.canaryErr
	}
	s.canaryOK = true
	return nil
}
func (s *reconciliationRepositoryStub) RequeueEmbeddingReconciliationJobs(context.Context, repository.RequeueEmbeddingReconciliationJobsInput) (int64, error) {
	if s.requeued == 0 {
		s.requeued = 3
	}
	return s.requeued, s.requeueErr
}
func (s *reconciliationRepositoryStub) CompleteEmbeddingReconciliationRun(_ context.Context, input repository.CompleteEmbeddingReconciliationRunInput) error {
	s.completed = &input
	return nil
}

type reconciliationRawProvider struct {
	available bool
	model     string
	dims      int
	vector    []float32
	err       error
	calls     int
}

func (p *reconciliationRawProvider) Embed(_ context.Context, _ string) ([]float32, string, error) {
	p.calls++
	return p.vector, p.model, p.err
}
func (p *reconciliationRawProvider) EmbedBatch(context.Context, []string) ([][]float32, string, error) {
	return nil, "", errors.New("batch should not be used by canary")
}
func (p *reconciliationRawProvider) ModelName() string { return p.model }
func (p *reconciliationRawProvider) Dimensions() int   { return p.dims }
func (p *reconciliationRawProvider) IsAvailable() bool { return p.available }

type reconciliationMetricsStub struct {
	observability.DiscoverabilityMetrics
	runs []string
}

func (m *reconciliationMetricsStub) ObserveEmbeddingReconciliationRun(outcome string) {
	m.runs = append(m.runs, outcome)
}
func (*reconciliationMetricsStub) ObserveEmbeddingReconciliationCanary(string) {}
func (*reconciliationMetricsStub) ObserveEmbeddingReconciliationJobs(string, string, string, string, int) {
}
func (*reconciliationMetricsStub) ObserveEmbeddingReconciliationDuration(float64, string) {}

type reconciliationLoggerStub struct{ warnings []string }

func (*reconciliationLoggerStub) Info(string, ...observability.LogAttr)         {}
func (*reconciliationLoggerStub) Error(string, error, ...observability.LogAttr) {}
func (l *reconciliationLoggerStub) Warn(message string, _ ...observability.LogAttr) {
	l.warnings = append(l.warnings, message)
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
	reconciliation := &reconciliationRepositoryStub{
		run: &repository.EmbeddingReconciliationRun{
			RunID: "run-1", LeaseToken: "lease-1", Status: string(domain.EmbeddingReconciliationRunning),
			CandidateCutoff: attemptedAt, CanaryAttemptedAt: &attemptedAt,
		},
		job: &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "job-1", EmbeddingDimensions: 3, DocumentText: "canary"},
	}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3, vector: []float32{1, 0, 0}}
	svc := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
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
}

func TestEmbeddingReconciliationUsesOneRawCanaryThenRequeuesBacklog(t *testing.T) {
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
	require.NotNil(t, reconciliation.completed)
	if reconciliation.completed.RequeuedCount != 3 || reconciliation.completed.RecoveredCount != 1 {
		t.Fatalf("completed run = %#v", reconciliation.completed)
	}
}

func TestEmbeddingReconciliationPersistsPartialRequeueProgress(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	reconciliation := &reconciliationRepositoryStub{
		job:        &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "job-1", Attempts: 20, EmbeddingDimensions: 3, DocumentText: "canary"},
		requeued:   500,
		requeueErr: errors.New("later batch failed"),
	}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3, vector: []float32{1, 0, 0}}
	now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
	service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
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
}

func TestEmbeddingReconciliationFailedCanaryDoesNotReleaseBacklog(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	reconciliation := &reconciliationRepositoryStub{job: &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "job-1", Attempts: 20, EmbeddingDimensions: 3, DocumentText: "canary"}}
	provider := &reconciliationRawProvider{available: true, model: "model", dims: 3, err: &embedding.ProviderHTTPError{Status: 429, Code: "insufficient_quota"}}
	now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
	service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
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
	require.NotNil(t, reconciliation.completed)
	assert.Equal(t, string(domain.EmbeddingReconciliationAmbiguous), reconciliation.completed.Status)
	assert.Equal(t, "ambiguous", reconciliation.completed.CanaryOutcome)
	assert.Zero(t, reconciliation.requeued)
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
	assert.Equal(t, "embedding provider quota exhausted", embeddingFailureMessage(string(domain.EmbeddingFailureProviderQuotaExhausted)))
	assert.Equal(t, "embedding processing failed", embeddingFailureMessage(string(domain.EmbeddingFailureUnknown)))
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
