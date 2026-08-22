package access

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/observability"
)

const (
	apiKeyActivityFlushInterval = time.Second
	apiKeyActivityMaxPending    = 4096
)

// CredentialActivityWriter coalesces admitted API-key activity before persisting
// it. Request handlers never perform database work or start goroutines.
type CredentialActivityWriter struct {
	repo      CredentialActivityStore
	batchRepo CredentialLastUsedBatchStore
	logger    observability.LogProvider

	mu       sync.Mutex
	pending  map[uuid.UUID]time.Time
	dropped  int
	wake     chan struct{}
	lastWake time.Time
	stop     chan struct{}
	done     chan struct{}
	start    sync.Once
	stopOne  sync.Once
}

func NewCredentialActivityWriter(repo CredentialActivityStore, loggers ...observability.LogProvider) *CredentialActivityWriter {
	var batchRepo CredentialLastUsedBatchStore
	if candidate, ok := repo.(CredentialLastUsedBatchStore); ok {
		batchRepo = candidate
	}
	var logger observability.LogProvider
	if len(loggers) > 0 {
		logger = loggers[0]
	}
	return &CredentialActivityWriter{
		repo:      repo,
		batchRepo: batchRepo,
		logger:    logger,
		pending:   make(map[uuid.UUID]time.Time),
		wake:      make(chan struct{}, 1),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// RecordLastUsed records the newest timestamp for one credential. When the
// bounded queue is full, existing keys still coalesce and a new event is
// dropped rather than allowing unbounded memory growth.
func (w *CredentialActivityWriter) RecordLastUsed(id uuid.UUID, at time.Time) {
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
		w.dropped++
		w.mu.Unlock()
		return
	}
	w.pending[id] = at
	shouldWake := w.lastWake.IsZero() || time.Since(w.lastWake) >= apiKeyActivityFlushInterval
	if shouldWake {
		w.lastWake = time.Now()
	}
	w.mu.Unlock()
	if !shouldWake {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *CredentialActivityWriter) Start(ctx context.Context) {
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

func (w *CredentialActivityWriter) run(ctx context.Context) {
	ticker := time.NewTicker(apiKeyActivityFlushInterval)
	defer ticker.Stop()
	defer close(w.done)
	for {
		select {
		case <-ticker.C:
			w.flushAndReport(context.Background())
		case <-w.wake:
			w.flushAndReport(context.Background())
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			w.flushAndReport(flushCtx)
			cancel()
			return
		case <-w.stop:
			flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			w.flushAndReport(flushCtx)
			cancel()
			return
		}
	}
}

func (w *CredentialActivityWriter) flush(ctx context.Context) error {
	_, err := w.flushWithCount(ctx)
	return err
}

func (w *CredentialActivityWriter) flushAndReport(ctx context.Context) {
	count, err := w.flushWithCount(ctx)
	dropped := w.takeDropped()
	if err != nil && w.logger != nil {
		w.logger.Warn("api_key_activity_flush_failed", observability.Int("update_count", count))
	}
	if dropped > 0 && w.logger != nil {
		w.logger.Warn("api_key_activity_dropped", observability.Int("event_count", dropped))
	}
}

func (w *CredentialActivityWriter) flushWithCount(ctx context.Context) (int, error) {
	updates := w.takePending()
	if len(updates) == 0 || w.repo == nil {
		return len(updates), nil
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
	return len(updates), err
}

func (w *CredentialActivityWriter) takePending() []LastUsedUpdate {
	w.mu.Lock()
	defer w.mu.Unlock()
	updates := make([]LastUsedUpdate, 0, len(w.pending))
	for id, at := range w.pending {
		updates = append(updates, LastUsedUpdate{ID: id, At: at})
	}
	w.pending = make(map[uuid.UUID]time.Time)
	return updates
}

func (w *CredentialActivityWriter) requeue(updates []LastUsedUpdate) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, update := range updates {
		if previous, ok := w.pending[update.ID]; ok && !update.At.After(previous) {
			continue
		}
		if _, exists := w.pending[update.ID]; !exists && len(w.pending) >= apiKeyActivityMaxPending {
			w.dropped++
			continue
		}
		w.pending[update.ID] = update.At
	}
}

func (w *CredentialActivityWriter) takeDropped() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	dropped := w.dropped
	w.dropped = 0
	return dropped
}

func (w *CredentialActivityWriter) Shutdown(ctx context.Context) error {
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
