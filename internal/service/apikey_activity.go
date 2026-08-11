package service

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	apiKeyActivityFlushInterval = time.Second
	apiKeyActivityMaxPending    = 4096
)

// APIKeyActivityWriter coalesces admitted API-key activity before persisting
// it. Request handlers never perform database work or start goroutines.
type APIKeyActivityWriter struct {
	repo      repository.APIKeyRepository
	batchRepo repository.APIKeyLastUsedBatchRepository

	mu      sync.Mutex
	pending map[uuid.UUID]time.Time
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	start   sync.Once
	stopOne sync.Once
}

func NewAPIKeyActivityWriter(repo repository.APIKeyRepository) *APIKeyActivityWriter {
	var batchRepo repository.APIKeyLastUsedBatchRepository
	if candidate, ok := repo.(repository.APIKeyLastUsedBatchRepository); ok {
		batchRepo = candidate
	}
	return &APIKeyActivityWriter{
		repo:      repo,
		batchRepo: batchRepo,
		pending:   make(map[uuid.UUID]time.Time),
		wake:      make(chan struct{}, 1),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// RecordLastUsed records the newest timestamp for one credential. When the
// bounded queue is full, existing keys still coalesce and a new event is
// dropped rather than allowing unbounded memory growth.
func (w *APIKeyActivityWriter) RecordLastUsed(id uuid.UUID, at time.Time) {
	if w == nil || id == uuid.Nil || at.IsZero() {
		return
	}
	at = at.UTC()
	w.mu.Lock()
	if previous, ok := w.pending[id]; ok && !at.After(previous) {
		w.mu.Unlock()
		return
	}
	if _, exists := w.pending[id]; !exists && len(w.pending) >= apiKeyActivityMaxPending {
		w.mu.Unlock()
		return
	}
	w.pending[id] = at
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *APIKeyActivityWriter) Start(ctx context.Context) {
	if w == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	w.start.Do(func() {
		go w.run(ctx)
	})
}

func (w *APIKeyActivityWriter) run(ctx context.Context) {
	ticker := time.NewTicker(apiKeyActivityFlushInterval)
	defer ticker.Stop()
	defer close(w.done)
	for {
		select {
		case <-ticker.C:
			_ = w.flush(context.Background())
		case <-w.wake:
			_ = w.flush(context.Background())
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = w.flush(flushCtx)
			cancel()
			return
		case <-w.stop:
			flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = w.flush(flushCtx)
			cancel()
			return
		}
	}
}

func (w *APIKeyActivityWriter) flush(ctx context.Context) error {
	updates := w.takePending()
	if len(updates) == 0 || w.repo == nil {
		return nil
	}
	var err error
	if w.batchRepo != nil {
		err = w.batchRepo.TouchLastUsedBatch(ctx, updates)
	} else {
		for _, update := range updates {
			if err = w.repo.TouchLastUsed(ctx, update.ID); err != nil {
				break
			}
		}
	}
	if err != nil {
		w.requeue(updates)
	}
	return err
}

func (w *APIKeyActivityWriter) takePending() []repository.LastUsedUpdate {
	w.mu.Lock()
	defer w.mu.Unlock()
	updates := make([]repository.LastUsedUpdate, 0, len(w.pending))
	for id, at := range w.pending {
		updates = append(updates, repository.LastUsedUpdate{ID: id, At: at})
	}
	w.pending = make(map[uuid.UUID]time.Time)
	return updates
}

func (w *APIKeyActivityWriter) requeue(updates []repository.LastUsedUpdate) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, update := range updates {
		if previous, ok := w.pending[update.ID]; ok && !update.At.After(previous) {
			continue
		}
		if _, exists := w.pending[update.ID]; !exists && len(w.pending) >= apiKeyActivityMaxPending {
			continue
		}
		w.pending[update.ID] = update.At
	}
}

func (w *APIKeyActivityWriter) Shutdown(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.stopOne.Do(func() { close(w.stop) })
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
