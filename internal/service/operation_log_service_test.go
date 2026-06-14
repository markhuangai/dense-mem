package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type operationLogRepoStub struct {
	appended    []domain.OperationLog
	filters     []domain.OperationLogFilter
	prunedAt    *time.Time
	appendErr   error
	listErr     error
	pruneErr    error
	appendCalls int
}

func (s *operationLogRepoStub) AppendBatch(_ context.Context, logs []domain.OperationLog) error {
	s.appendCalls++
	if s.appendErr != nil {
		return s.appendErr
	}
	s.appended = append(s.appended, logs...)
	return nil
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

	repo = &operationLogRepoStub{listErr: errors.New("list failed")}
	svc = NewOperationLogService(repo, nil)
	_, err := svc.ListOperationLogs(ctx, domain.OperationLogFilter{})
	require.ErrorContains(t, err, "list failed")

	repo = &operationLogRepoStub{pruneErr: errors.New("prune failed")}
	svc = NewOperationLogService(repo, operationLogRetentionStub{err: errors.New("config unavailable")})
	require.ErrorContains(t, svc.Prune(ctx), "prune failed")
	require.NotNil(t, repo.prunedAt)
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
}
