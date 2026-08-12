package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

type activityBatchRepo struct {
	repository.APIKeyRepository
	mu      sync.Mutex
	updates []repository.LastUsedUpdate
	err     error
}

func (r *activityBatchRepo) TouchLastUsedBatch(_ context.Context, updates []repository.LastUsedUpdate) error {
	if r.err != nil {
		return r.err
	}
	r.mu.Lock()
	r.updates = append(r.updates, updates...)
	r.mu.Unlock()
	return nil
}

type activityLogger struct {
	mu       sync.Mutex
	warnings []struct {
		message string
		attrs   []observability.LogAttr
	}
}

func (*activityLogger) Info(string, ...observability.LogAttr)         {}
func (*activityLogger) Error(string, error, ...observability.LogAttr) {}
func (l *activityLogger) Warn(message string, attrs ...observability.LogAttr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnings = append(l.warnings, struct {
		message string
		attrs   []observability.LogAttr
	}{message: message, attrs: attrs})
}
func (*activityLogger) Debug(string, ...observability.LogAttr)                    {}
func (l *activityLogger) With(...observability.LogAttr) observability.LogProvider { return l }

func TestAPIKeyActivityWriterCoalescesAndFlushesNewestTimestamp(t *testing.T) {
	repo := &activityBatchRepo{}
	writer := NewAPIKeyActivityWriter(repo)

	id := uuid.New()
	older := time.Now().UTC().Add(-time.Minute)
	newer := older.Add(time.Second)
	writer.RecordLastUsed(id, newer)
	writer.RecordLastUsed(id, older)
	require.NoError(t, writer.flush(context.Background()))

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.updates, 1)
	require.Equal(t, id, repo.updates[0].ID)
	require.True(t, repo.updates[0].At.Equal(newer))
}

func TestAPIKeyActivityWriterFlushesOnContextCancellation(t *testing.T) {
	repo := &activityBatchRepo{}
	writer := NewAPIKeyActivityWriter(repo)
	ctx, cancel := context.WithCancel(context.Background())
	writer.Start(ctx)

	id := uuid.New()
	writer.RecordLastUsed(id, time.Now().UTC())
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	require.NoError(t, writer.Shutdown(shutdownCtx))
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.updates, 1)
	require.Equal(t, id, repo.updates[0].ID)
}

func TestAPIKeyActivityWriterReportsFlushFailureAndRequeues(t *testing.T) {
	repo := &activityBatchRepo{err: errors.New("activity persistence failed")}
	logger := &activityLogger{}
	writer := NewAPIKeyActivityWriter(repo, logger)
	id := uuid.New()
	writer.RecordLastUsed(id, time.Now().UTC())

	writer.flushAndReport(context.Background())

	writer.mu.Lock()
	_, requeued := writer.pending[id]
	writer.mu.Unlock()
	require.True(t, requeued)
	logger.mu.Lock()
	defer logger.mu.Unlock()
	require.Len(t, logger.warnings, 1)
	require.Equal(t, "api_key_activity_flush_failed", logger.warnings[0].message)
	require.Equal(t, 1, activityLogAttrValue(logger.warnings[0].attrs, "update_count"))
}

func TestAPIKeyActivityWriterReportsQueueDrops(t *testing.T) {
	repo := &activityBatchRepo{}
	logger := &activityLogger{}
	writer := NewAPIKeyActivityWriter(repo, logger)
	at := time.Now().UTC()
	for range apiKeyActivityMaxPending {
		writer.RecordLastUsed(uuid.New(), at)
	}
	writer.RecordLastUsed(uuid.New(), at)

	writer.flushAndReport(context.Background())

	logger.mu.Lock()
	defer logger.mu.Unlock()
	require.Len(t, logger.warnings, 1)
	require.Equal(t, "api_key_activity_dropped", logger.warnings[0].message)
	require.Equal(t, 1, activityLogAttrValue(logger.warnings[0].attrs, "event_count"))
}

func activityLogAttrValue(attrs []observability.LogAttr, key string) any {
	for _, attr := range attrs {
		if attr.Key == key {
			return attr.Value
		}
	}
	return nil
}
