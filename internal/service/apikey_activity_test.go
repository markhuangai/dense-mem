package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
)

type activityBatchRepo struct {
	repository.APIKeyRepository
	mu      sync.Mutex
	updates []repository.LastUsedUpdate
}

func (r *activityBatchRepo) TouchLastUsedBatch(_ context.Context, updates []repository.LastUsedUpdate) error {
	r.mu.Lock()
	r.updates = append(r.updates, updates...)
	r.mu.Unlock()
	return nil
}

func TestAPIKeyActivityWriterCoalescesAndFlushesNewestTimestamp(t *testing.T) {
	repo := &activityBatchRepo{}
	writer := NewAPIKeyActivityWriter(repo)
	writer.Start(context.Background())

	id := uuid.New()
	older := time.Now().UTC().Add(-time.Minute)
	newer := older.Add(time.Second)
	writer.RecordLastUsed(id, newer)
	writer.RecordLastUsed(id, older)

	require.Eventually(t, func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return len(repo.updates) == 1 && repo.updates[0].ID == id && repo.updates[0].At.Equal(newer)
	}, time.Second, 5*time.Millisecond)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, writer.Shutdown(shutdownCtx))
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
