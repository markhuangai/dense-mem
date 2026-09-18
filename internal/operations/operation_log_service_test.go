package operations

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type operationLogRepoStub struct {
	appended        []domain.OperationLog
	filters         []domain.OperationLogFilter
	prunedAt        *time.Time
	appendErr       error
	listErr         error
	pruneErr        error
	appendCalls     int
	appendDeadlines []time.Time
}

type shutdownRetryRepo struct {
	operationLogRepoStub
	failFirstAppend bool
}

type ambiguousGapRepo struct {
	operationLogRepoStub
	failFirstAppend bool
}

type operationLogProbeRepo struct {
	operationLogRepoStub
	pingCalls int
}

func (r *operationLogProbeRepo) ProbeOperationLogSink(context.Context) error {
	r.pingCalls++
	return nil
}

func (s *operationLogRepoStub) AppendBatch(ctx context.Context, logs []domain.OperationLog) error {
	s.appendCalls++
	if s.appendErr != nil {
		return s.appendErr
	}
	if deadline, ok := ctx.Deadline(); ok {
		s.appendDeadlines = append(s.appendDeadlines, deadline)
	}
	s.appended = append(s.appended, logs...)
	return nil
}

func (s *shutdownRetryRepo) AppendBatch(ctx context.Context, logs []domain.OperationLog) error {
	if s.failFirstAppend {
		s.failFirstAppend = false
		return errors.New("transient shutdown append failure")
	}
	return s.operationLogRepoStub.AppendBatch(ctx, logs)
}

func (s *ambiguousGapRepo) AppendBatch(ctx context.Context, logs []domain.OperationLog) error {
	if s.failFirstAppend {
		s.failFirstAppend = false
		s.appended = append(s.appended, logs...)
		return errors.New("ambiguous commit")
	}
	return s.operationLogRepoStub.AppendBatch(ctx, logs)
}

func (s *operationLogRepoStub) List(_ context.Context, filter domain.OperationLogFilter) (*domain.OperationLogPage, error) {
	s.filters = append(s.filters, filter)
	if s.listErr != nil {
		return nil, s.listErr
	}
	return &domain.OperationLogPage{
		Items: s.appended,
		Total: int64(len(s.appended)),
	}, nil
}

func (s *operationLogRepoStub) PruneBefore(_ context.Context, cutoff time.Time) error {
	s.prunedAt = &cutoff
	return s.pruneErr
}

type operationLogRetentionStub struct {
	cfg domain.OperationLogRuntimeConfig
	err error
}

func (s operationLogRetentionStub) OperationLogRuntimeConfig(context.Context) (domain.OperationLogRuntimeConfig, error) {
	return s.cfg, s.err
}

func TestOperationLogServiceWriteFlushListAndPrune(t *testing.T) {
	ctx := context.Background()
	repo := &operationLogRepoStub{}
	retention := operationLogRetentionStub{cfg: domain.OperationLogRuntimeConfig{RetentionDays: 7}}
	svc := NewOperationLogService(repo, retention)
	teamID := uuid.New()
	profileID := uuid.New()

	require.NoError(t, svc.WriteLog(ctx, observability.LogRecord{
		Severity:      " warn ",
		Message:       "dream cycle completed",
		Source:        "scheduler",
		TeamID:        teamID.String(),
		ProfileID:     profileID.String(),
		CorrelationID: "corr-1",
		Error:         "boom",
		Attrs:         map[string]any{"phase": "dream"},
	}))

	require.NoError(t, svc.Flush(ctx))
	require.Len(t, repo.appended, 1)
	got := repo.appended[0]
	assert.False(t, got.Timestamp.IsZero())
	assert.Equal(t, "WARN", got.Severity)
	assert.Equal(t, 30, got.SeverityRank)
	assert.Equal(t, "dream cycle completed", got.Message)
	assert.Equal(t, "scheduler", got.Source)
	require.NotNil(t, got.TeamID)
	require.NotNil(t, got.ProfileID)
	assert.Equal(t, teamID, *got.TeamID)
	assert.Equal(t, profileID, *got.ProfileID)
	assert.Equal(t, "corr-1", got.CorrelationID)
	assert.Equal(t, "boom", got.Error)
	assert.Equal(t, "dream", got.Attrs["phase"])

	page, err := svc.ListOperationLogs(ctx, domain.OperationLogFilter{Limit: 5, Severity: "WARN"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), page.Total)
	require.Len(t, repo.filters, 1)
	assert.Equal(t, 5, repo.filters[0].Limit)
	assert.Equal(t, "WARN", repo.filters[0].Severity)

	before := time.Now().UTC().AddDate(0, 0, -7).Add(-time.Second)
	require.NoError(t, svc.Prune(ctx))
	require.NotNil(t, repo.prunedAt)
	after := time.Now().UTC().AddDate(0, 0, -7).Add(time.Second)
	assert.True(t, repo.prunedAt.After(before), "cutoff %s should be after %s", repo.prunedAt, before)
	assert.True(t, repo.prunedAt.Before(after), "cutoff %s should be before %s", repo.prunedAt, after)
}

func TestOperationLogServiceBatchesAndPropagatesErrors(t *testing.T) {
	ctx := context.Background()
	repo := &operationLogRepoStub{}
	svc := NewOperationLogService(repo, nil)
	for i := 0; i < operationLogBatchSize+1; i++ {
		require.NoError(t, svc.WriteLog(ctx, observability.LogRecord{
			Severity: "debug",
			Message:  "queued",
		}))
	}
	require.NoError(t, svc.Flush(ctx))
	assert.Equal(t, 2, repo.appendCalls)
	assert.Len(t, repo.appended, operationLogBatchSize+1)
	assert.Equal(t, "DEBUG", repo.appended[0].Severity)
	assert.Equal(t, 10, repo.appended[0].SeverityRank)

	repo = &operationLogRepoStub{appendErr: errors.New("append failed")}
	svc = NewOperationLogService(repo, nil)
	require.NoError(t, svc.WriteLog(ctx, observability.LogRecord{Severity: "error", Message: "queued"}))
	require.ErrorContains(t, svc.Flush(ctx), "append failed")
	assert.Empty(t, repo.appended)
	repo.appendErr = nil
	time.Sleep(operationLogRetryInterval)
	require.NoError(t, svc.Flush(ctx))
	require.Len(t, repo.appended, 1)
	assert.Equal(t, "queued", repo.appended[0].Message)

	repo = &operationLogRepoStub{listErr: errors.New("list failed")}
	svc = NewOperationLogService(repo, nil)
	_, err := svc.ListOperationLogs(ctx, domain.OperationLogFilter{})
	require.ErrorContains(t, err, "list failed")

	repo = &operationLogRepoStub{pruneErr: errors.New("prune failed")}
	svc = NewOperationLogService(repo, operationLogRetentionStub{err: errors.New("config unavailable")})
	require.ErrorContains(t, svc.Prune(ctx), "prune failed")
	require.NotNil(t, repo.prunedAt)
}

func TestOperationLogServiceReportsQueueBackpressure(t *testing.T) {
	ctx := context.Background()
	svc := NewOperationLogService(&operationLogRepoStub{}, nil)
	for i := 0; i < operationLogQueueSize; i++ {
		require.NoError(t, svc.WriteLog(ctx, observability.LogRecord{Message: "queued"}))
	}

	err := svc.WriteLog(ctx, observability.LogRecord{Message: "overflow"})

	require.ErrorIs(t, err, ErrOperationLogQueueFull)
}

func TestOperationLogServiceLifecycleAndUnavailableBranches(t *testing.T) {
	ctx := context.Background()
	var nilSvc *OperationLogServiceImpl
	require.NoError(t, nilSvc.WriteLog(ctx, observability.LogRecord{}))
	require.NoError(t, nilSvc.Flush(ctx))
	require.NoError(t, nilSvc.Prune(ctx))
	require.NoError(t, nilSvc.Shutdown(ctx))
	_, err := nilSvc.ListOperationLogs(ctx, domain.OperationLogFilter{})
	require.ErrorContains(t, err, "unavailable")

	svc := NewOperationLogService(nil, nil)
	require.NoError(t, svc.WriteLog(ctx, observability.LogRecord{TeamID: "not-a-uuid", ProfileID: " "}))
	require.NoError(t, svc.Flush(ctx))
	require.NoError(t, svc.Prune(ctx))
	svc.Start(ctx)
	require.NoError(t, svc.Shutdown(ctx))

	repo := &operationLogRepoStub{}
	svc = NewOperationLogService(repo, operationLogRetentionStub{cfg: domain.OperationLogRuntimeConfig{RetentionDays: 1}})
	svc.Start(ctx)
	svc.Start(ctx)
	require.NoError(t, svc.WriteLog(ctx, observability.LogRecord{Message: "started"}))
	require.NoError(t, svc.Shutdown(ctx))
	require.Len(t, repo.appended, 1)
	require.NotEmpty(t, repo.appendDeadlines)
	assert.WithinDuration(t, time.Now().UTC().Add(operationLogShutdownFlushTimeout), repo.appendDeadlines[0], time.Second)
}

func TestOperationLogServiceRetainsFailedBatchUntilRetrySucceeds(t *testing.T) {
	repo := &operationLogRepoStub{appendErr: errors.New("append failed")}
	svc := NewOperationLogService(repo, nil)
	require.NoError(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "retry"}))
	require.Error(t, svc.Flush(context.Background()))
	require.Error(t, svc.Flush(context.Background()))
	repo.appendErr = nil
	time.Sleep(operationLogRetryInterval)
	require.NoError(t, svc.Flush(context.Background()))
	require.Len(t, repo.appended, 1)
}

func TestOperationLogServiceShutdownDoesNotMarkRecoveredBatchAsDropped(t *testing.T) {
	repo := &shutdownRetryRepo{failFirstAppend: true}
	svc := NewOperationLogService(repo, nil)
	svc.Start(context.Background())
	require.NoError(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "shutdown retry", Contextual: true}))

	require.NoError(t, svc.Shutdown(context.Background()))
	require.Len(t, repo.appended, 1)
	assert.Equal(t, "shutdown retry", repo.appended[0].Message)
	assert.Zero(t, svc.DroppedEvents())
	for _, entry := range repo.appended {
		assert.NotEqual(t, "operation log gap recovered", entry.Message)
	}
}

func TestOperationLogServiceShutdownDropsRetainedBatchAfterFinalFailure(t *testing.T) {
	repo := &operationLogRepoStub{appendErr: errors.New("persistent shutdown append failure")}
	svc := NewOperationLogService(repo, nil)
	svc.Start(context.Background())
	require.NoError(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "shutdown drop", Contextual: true}))

	require.Error(t, svc.Shutdown(context.Background()))
	assert.EqualValues(t, 1, svc.DroppedEvents())
	assert.Empty(t, repo.appended)
}

func TestOperationLogServiceTriggersFlushAtBatchCapacity(t *testing.T) {
	svc := NewOperationLogService(&operationLogRepoStub{}, nil)
	for i := 0; i < operationLogBatchSize; i++ {
		require.NoError(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "capacity"}))
	}
	select {
	case <-svc.flushRequests:
	default:
		t.Fatal("batch capacity did not request an immediate flush")
	}
}

func TestOperationLogServiceContextualAdmissionHonorsCancellation(t *testing.T) {
	svc := NewOperationLogService(&operationLogRepoStub{}, nil)
	for i := 0; i < operationLogQueueSize; i++ {
		require.NoError(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "queued"}))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := svc.WriteLog(ctx, observability.LogRecord{Message: "canceled", Contextual: true})
	require.ErrorIs(t, err, ErrOperationLogAdmissionCanceled)
	assert.EqualValues(t, 1, svc.DroppedEvents())
}

func TestOperationLogServiceBoundsServiceAddedCallerMetadata(t *testing.T) {
	repo := &operationLogRepoStub{}
	svc := NewOperationLogService(repo, nil)
	teamID, profileID := uuid.New(), uuid.New()
	attrs := map[string]any{
		"ordinary":        strings.Repeat("x", observability.MaxOperationMetadataBytes*2),
		"team_id":         teamID.String(),
		"profile_id":      profileID.String(),
		"caller_function": "forged.Function",
	}
	require.NoError(t, svc.WriteLog(context.Background(), observability.LogRecord{
		Message: "bounded", TeamID: teamID.String(), ProfileID: profileID.String(), Function: "producer.Function", Attrs: attrs,
	}))
	require.NoError(t, svc.Flush(context.Background()))
	require.Len(t, repo.appended, 1)
	encoded, err := json.Marshal(repo.appended[0].Attrs)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(encoded), observability.MaxOperationMetadataBytes)
	assert.Equal(t, teamID, *repo.appended[0].TeamID)
	assert.Equal(t, profileID, *repo.appended[0].ProfileID)
	assert.Equal(t, "producer.Function", repo.appended[0].Attrs["caller_function"])
	assert.Equal(t, "forged.Function", attrs["caller_function"], "the service must not mutate the caller's metadata")
}

func TestDefaultSlogBridgeRetainsNonblockingLegacyAdmission(t *testing.T) {
	svc := NewOperationLogService(&operationLogRepoStub{}, nil)
	root := observability.New(observability.LevelTrace)
	require.NoError(t, root.AttachSink(svc))
	for i := 0; i < operationLogQueueSize; i++ {
		require.NoError(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "queued"}))
	}
	started := time.Now()
	root.Slog().Info("legacy overflow")
	assert.Less(t, time.Since(started), 250*time.Millisecond)
	assert.EqualValues(t, 1, svc.DroppedEvents())
}

func TestOperationLogServiceReadinessRecoversAfterPersistenceFailure(t *testing.T) {
	repo := &operationLogRepoStub{appendErr: errors.New("database unavailable")}
	svc := NewOperationLogService(repo, nil)
	require.NoError(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "pending"}))
	require.ErrorIs(t, svc.CheckReadiness(context.Background()), ErrOperationLogSinkUnavailable)
	repo.appendErr = nil
	time.Sleep(operationLogRetryInterval)
	require.NoError(t, svc.CheckReadiness(context.Background()))
	require.Len(t, repo.appended, 2)
	assert.NotEqual(t, uuid.Nil, repo.appended[0].ID)
	assert.Equal(t, "operation log sink readiness probe", repo.appended[1].Message)
	repo.appendErr = errors.New("sink outage")
	require.ErrorIs(t, svc.CheckReadiness(context.Background()), ErrOperationLogSinkUnavailable)
	repo.appendErr = nil
	time.Sleep(operationLogRetryInterval)
	require.NoError(t, svc.CheckReadiness(context.Background()))
}

func TestOperationLogServiceReadinessProbeUsesDirectPingWhenInfoIsFiltered(t *testing.T) {
	repo := &operationLogProbeRepo{}
	svc := NewOperationLogService(repo, nil)
	svc.SetMinimumLevel(slog.LevelError)
	require.NoError(t, svc.CheckReadiness(context.Background()))
	assert.Equal(t, 1, repo.pingCalls)
	assert.Empty(t, repo.appended)
}

func TestOperationLogServicePersistsGapRecoveryMarker(t *testing.T) {
	repo := &operationLogRepoStub{}
	svc := NewOperationLogService(repo, nil)
	for i := 0; i < operationLogQueueSize; i++ {
		require.NoError(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "queued"}))
	}
	require.ErrorIs(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "overflow"}), ErrOperationLogQueueFull)
	require.NoError(t, svc.Flush(context.Background()))
	found := false
	for _, entry := range repo.appended {
		if entry.Message == "operation log gap recovered" {
			found = true
			assert.EqualValues(t, 1, entry.Attrs["dropped_events"])
		}
	}
	assert.True(t, found)
	require.NoError(t, svc.CheckReadiness(context.Background()))
}

func TestOperationLogServiceGapRecoveryUsesEnabledSeverity(t *testing.T) {
	repo := &operationLogRepoStub{}
	svc := NewOperationLogService(repo, nil)
	svc.SetMinimumLevel(slog.LevelError)
	for i := 0; i < operationLogQueueSize; i++ {
		require.NoError(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "queued"}))
	}
	require.ErrorIs(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "overflow"}), ErrOperationLogQueueFull)
	require.NoError(t, svc.Flush(context.Background()))
	for _, entry := range repo.appended {
		if entry.Message == "operation log gap recovered" {
			assert.Equal(t, "ERROR", entry.Severity)
			assert.Equal(t, 40, entry.SeverityRank)
			return
		}
	}
	t.Fatal("missing recovery marker")
}

func TestOperationLogServiceRetriesFailedGapRecoveryMarkerWithoutNewTraffic(t *testing.T) {
	repo := &operationLogRepoStub{appendErr: errors.New("sink unavailable")}
	svc := NewOperationLogService(repo, nil)
	for i := 0; i < operationLogQueueSize; i++ {
		require.NoError(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "queued"}))
	}
	require.ErrorIs(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "overflow"}), ErrOperationLogQueueFull)
	require.Error(t, svc.Flush(context.Background()))
	repo.appendErr = nil
	time.Sleep(operationLogRetryInterval)
	require.NoError(t, svc.Flush(context.Background()))
	var markers []domain.OperationLog
	for _, entry := range repo.appended {
		if entry.Message == "operation log gap recovered" {
			markers = append(markers, entry)
		}
	}
	require.Len(t, markers, 1)
	assert.EqualValues(t, 1, markers[0].Attrs["dropped_events"])
}

func TestOperationLogServicePreservesAmbiguousGapMarkerCount(t *testing.T) {
	repo := &ambiguousGapRepo{failFirstAppend: true}
	svc := NewOperationLogService(repo, nil)
	svc.markGapCount(1)
	require.Error(t, svc.Flush(context.Background()))
	svc.markGapCount(2)
	time.Sleep(operationLogRetryInterval)
	require.NoError(t, svc.Flush(context.Background()))
	require.Len(t, repo.appended, 2)
	assert.EqualValues(t, 1, repo.appended[0].Attrs["dropped_events"])
	assert.EqualValues(t, 1, repo.appended[1].Attrs["dropped_events"])
	assert.EqualValues(t, 2, svc.gapPending)

	require.NoError(t, svc.Flush(context.Background()))
	require.Len(t, repo.appended, 3)
	assert.EqualValues(t, 2, repo.appended[2].Attrs["dropped_events"])
}

func TestOperationLogServiceSeverityAndContextualAdmission(t *testing.T) {
	for _, test := range []struct {
		severity string
		want     int
	}{
		{severity: "fatal", want: 50},
		{severity: "error", want: 40},
		{severity: "warn", want: 30},
		{severity: "debug", want: 10},
		{severity: "trace", want: 0},
		{severity: "info", want: 20},
	} {
		assert.Equal(t, test.want, operationLogSeverityRank(test.severity))
	}

	repo := &operationLogRepoStub{}
	svc := NewOperationLogService(repo, nil)
	var nilCtx context.Context
	require.NoError(t, svc.WriteLog(nilCtx, observability.LogRecord{Message: "contextual", Contextual: true}))
	require.NoError(t, svc.Flush(nilCtx))
	require.Len(t, repo.appended, 1)

	var nilSvc *OperationLogServiceImpl
	assert.Zero(t, nilSvc.DroppedEvents())
	svc.markGapCount(0)
	require.NoError(t, svc.Shutdown(nilCtx))
	assert.ErrorIs(t, svc.WriteLog(context.Background(), observability.LogRecord{}), ErrOperationLogShutdown)
}

func TestParseLogUUIDBoundsInvalidAndValidValues(t *testing.T) {
	assert.Nil(t, parseLogUUID(""))
	assert.Nil(t, parseLogUUID("not-a-uuid"))
	want := uuid.New()
	got := parseLogUUID("  " + want.String() + " ")
	require.NotNil(t, got)
	assert.Equal(t, want, *got)
}

func TestOperationLogContextualWriterBoundsQueueAdmissionAndShutdownClosesAdmission(t *testing.T) {
	svc := NewOperationLogService(&operationLogRepoStub{}, nil)
	for i := 0; i < operationLogQueueSize; i++ {
		require.NoError(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "queued"}))
	}

	started := time.Now()
	require.ErrorIs(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "bounded", Contextual: true}), ErrOperationLogQueueFull)
	assert.Less(t, time.Since(started), 250*time.Millisecond)
	assert.EqualValues(t, 1, svc.DroppedEvents())

	repo := &operationLogRepoStub{}
	svc = NewOperationLogService(repo, nil)
	for i := 0; i < operationLogQueueSize; i++ {
		require.NoError(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "queued"}))
	}
	require.NoError(t, svc.Shutdown(context.Background()))
	require.ErrorIs(t, svc.WriteLog(context.Background(), observability.LogRecord{Message: "shutdown", Contextual: true}), ErrOperationLogShutdown)
	assert.Len(t, repo.appended, operationLogQueueSize)
}
