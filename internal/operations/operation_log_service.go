package operations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	settings "github.com/markhuangai/dense-mem/internal/settings"
)

const (
	operationLogQueueSize            = 4096
	operationLogBatchSize            = 100
	operationLogFlushInterval        = 2 * time.Second
	operationLogPruneInterval        = time.Hour
	operationLogShutdownFlushTimeout = 5 * time.Second
	operationLogPersistTimeout       = 5 * time.Second
	operationLogRetryInterval        = 250 * time.Millisecond
)

var (
	ErrOperationLogQueueFull         = errors.New("operation log queue full")
	ErrOperationLogAdmissionCanceled = errors.New("operation log admission canceled")
	ErrOperationLogShutdown          = errors.New("operation log service is shutting down")
	ErrOperationLogSinkUnavailable   = errors.New("operation log sink unavailable")
)

type OperationLogReader interface {
	ListOperationLogs(ctx context.Context, filter domain.OperationLogFilter) (*domain.OperationLogPage, error)
}

type OperationLogService interface {
	OperationLogReader
	WriteLog(ctx context.Context, record observability.LogRecord) error
	CheckReadiness(ctx context.Context) error
	Flush(ctx context.Context) error
	Prune(ctx context.Context) error
	Start(ctx context.Context)
	Shutdown(ctx context.Context) error
}

type OperationLogRetentionProvider interface {
	OperationLogRuntimeConfig(ctx context.Context) (domain.OperationLogRuntimeConfig, error)
}

type OperationLogServiceImpl struct {
	repo      operationscontract.OperationLogRepository
	retention OperationLogRetentionProvider

	queue         chan domain.OperationLog
	failed        []domain.OperationLog
	flushRequests chan struct{}
	stop          chan struct{}
	stopOnce      sync.Once

	lifecycleMu    sync.Mutex
	cancel         context.CancelFunc
	done           chan struct{}
	admissionMu    sync.Mutex
	admissionWG    sync.WaitGroup
	shuttingDown   bool
	flushMu        sync.Mutex
	stateMu        sync.RWMutex
	started        bool
	healthy        bool
	lastError      error
	retryAfter     time.Time
	shutdownErr    error
	gapPending     int64
	dropped        int64
	gapMarker      *domain.OperationLog
	gapMarkerCount int64
	probeMu        sync.Mutex
	probeID        uuid.UUID
	minimumLevel   slog.Level
}

var _ OperationLogService = (*OperationLogServiceImpl)(nil)
var _ observability.LogSink = (*OperationLogServiceImpl)(nil)

func NewOperationLogService(repo operationscontract.OperationLogRepository, retention OperationLogRetentionProvider) *OperationLogServiceImpl {
	return &OperationLogServiceImpl{
		repo:          repo,
		retention:     retention,
		queue:         make(chan domain.OperationLog, operationLogQueueSize),
		flushRequests: make(chan struct{}, 1),
		stop:          make(chan struct{}),
		minimumLevel:  observability.LevelTrace,
	}
}

// SetMinimumLevel keeps service-generated readiness records aligned with the
// root's shared console and operation-log level policy.
func (s *OperationLogServiceImpl) SetMinimumLevel(level slog.Level) {
	if s == nil {
		return
	}
	s.stateMu.Lock()
	s.minimumLevel = level
	s.stateMu.Unlock()
}

func (s *OperationLogServiceImpl) WriteLog(ctx context.Context, record observability.LogRecord) error {
	if s == nil || s.repo == nil {
		return nil
	}
	if !s.beginAdmission() {
		return ErrOperationLogShutdown
	}
	defer s.admissionWG.Done()
	if ctx == nil {
		ctx = context.Background()
	}
	entry := domain.OperationLog{
		ID:            uuid.New(),
		Timestamp:     record.Timestamp,
		Severity:      strings.ToUpper(strings.TrimSpace(record.Severity)),
		SeverityRank:  record.SeverityRank,
		Message:       record.Message,
		Source:        record.Source,
		CorrelationID: record.CorrelationID,
		Error:         record.Error,
		Attrs:         cloneOperationLogAttrs(record.Attrs),
	}
	if function := strings.TrimSpace(record.Function); function != "" {
		if entry.Attrs == nil {
			entry.Attrs = make(map[string]any)
		}
		entry.Attrs["caller_function"] = function
	} else if entry.Attrs != nil {
		delete(entry.Attrs, "caller_function")
	}
	entry.Attrs = observability.BoundOperationAttrs(entry.Attrs)
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if entry.Severity == "" {
		entry.Severity = "INFO"
	}
	if entry.SeverityRank == 0 {
		entry.SeverityRank = operationLogSeverityRank(entry.Severity)
	}
	entry.TeamID = parseLogUUID(record.TeamID)
	entry.ProfileID = parseLogUUID(record.ProfileID)

	requestFlush := func() {
		select {
		case s.flushRequests <- struct{}{}:
		default:
		}
	}
	if len(s.queue) >= operationLogBatchSize {
		requestFlush()
	}
	// Context-free legacy callers retain their existing nonblocking contract
	// until their capability migrations adopt the contextual methods.
	if !record.Contextual {
		select {
		case <-s.stop:
			return ErrOperationLogShutdown
		case s.queue <- entry:
			if s.shutdownRequested() {
				return ErrOperationLogShutdown
			}
			if len(s.queue) >= operationLogBatchSize {
				requestFlush()
			}
			return nil
		default:
			s.markGap("queue_full")
			return ErrOperationLogQueueFull
		}
	}
	if err := ctx.Err(); err != nil {
		s.markGap("admission_canceled")
		return fmt.Errorf("%w: %v", ErrOperationLogAdmissionCanceled, err)
	}
	select {
	case <-s.stop:
		return ErrOperationLogShutdown
	case <-ctx.Done():
		s.markGap("admission_canceled")
		return fmt.Errorf("%w: %v", ErrOperationLogAdmissionCanceled, ctx.Err())
	case s.queue <- entry:
		if s.shutdownRequested() {
			return ErrOperationLogShutdown
		}
		if len(s.queue) >= operationLogBatchSize {
			requestFlush()
		}
		return nil
	default:
		// Request-path diagnostics must never hold a business operation open
		// while a failed sink leaves the bounded queue full.
		s.markGap("queue_full")
		return ErrOperationLogQueueFull
	}
}

func cloneOperationLogAttrs(attrs map[string]any) map[string]any {
	if len(attrs) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(attrs))
	for key, value := range attrs {
		cloned[key] = value
	}
	return cloned
}

func (s *OperationLogServiceImpl) shutdownRequested() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *OperationLogServiceImpl) beginAdmission() bool {
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	if s.shuttingDown {
		return false
	}
	s.admissionWG.Add(1)
	return true
}

func (s *OperationLogServiceImpl) requestShutdown() {
	s.admissionMu.Lock()
	if !s.shuttingDown {
		s.shuttingDown = true
		s.stopOnce.Do(func() { close(s.stop) })
	}
	s.admissionMu.Unlock()
}

func operationLogSeverityRank(severity string) int {
	switch strings.ToUpper(strings.TrimSpace(severity)) {
	case "FATAL":
		return 50
	case "ERROR":
		return 40
	case "WARN":
		return 30
	case "DEBUG":
		return 10
	case "TRACE":
		return 0
	default:
		return 20
	}
}

func operationLogGapSeverity(level slog.Level) (string, int) {
	switch {
	case level >= observability.LevelFatal:
		return "FATAL", 50
	case level >= slog.LevelError:
		return "ERROR", 40
	default:
		return "WARN", 30
	}
}

func (s *OperationLogServiceImpl) ListOperationLogs(ctx context.Context, filter domain.OperationLogFilter) (*domain.OperationLogPage, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("operation log service unavailable")
	}
	if err := s.Flush(ctx); err != nil {
		return nil, err
	}
	return s.repo.List(observability.WithSinkSuppressed(ctx), filter)
}

func (s *OperationLogServiceImpl) Flush(ctx context.Context) error {
	return s.flush(ctx, false)
}

func (s *OperationLogServiceImpl) flush(ctx context.Context, force bool) error {
	if s == nil || s.repo == nil {
		return nil
	}
	if !force {
		if delay, deferred := s.retryDelay(); deferred {
			return s.retryDeferredError(delay)
		}
	}
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	if !force {
		if delay, deferred := s.retryDelay(); deferred {
			return s.retryDeferredError(delay)
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if err := s.flushFailed(ctx); err != nil {
		return err
	}
	for {
		batch := make([]domain.OperationLog, 0, operationLogBatchSize)
		for len(batch) < operationLogBatchSize {
			select {
			case entry := <-s.queue:
				batch = append(batch, entry)
			default:
				if len(batch) == 0 {
					return s.flushGap(ctx)
				}
				if err := s.appendOrRetain(ctx, batch); err != nil {
					return err
				}
				return s.flushGap(ctx)
			}
		}
		if err := s.appendOrRetain(ctx, batch); err != nil {
			return err
		}
		if err := s.flushGap(ctx); err != nil {
			return err
		}
	}
}

func (s *OperationLogServiceImpl) retryDeferredError(delay time.Duration) error {
	s.stateMu.RLock()
	err := s.lastError
	s.stateMu.RUnlock()
	if err == nil {
		err = ErrOperationLogSinkUnavailable
	}
	return fmt.Errorf("operation log retry deferred for %s: %w", delay.Round(time.Millisecond), err)
}

func (s *OperationLogServiceImpl) flushFailed(ctx context.Context) error {
	if len(s.failed) == 0 {
		return nil
	}
	if err := s.persist(ctx, s.failed); err != nil {
		s.markFailure(err)
		return err
	}
	s.failed = nil
	s.markHealthy()
	return nil
}

func (s *OperationLogServiceImpl) appendOrRetain(ctx context.Context, batch []domain.OperationLog) error {
	if len(batch) == 0 {
		return nil
	}
	if err := s.persist(ctx, batch); err != nil {
		s.failed = append([]domain.OperationLog(nil), batch...)
		s.markFailure(err)
		return err
	}
	s.markHealthy()
	return nil
}

func (s *OperationLogServiceImpl) discardRetainedAfterShutdownFailure() {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	count := len(s.failed)
	s.failed = nil
	for {
		select {
		case <-s.queue:
			count++
		default:
			s.markGapCount(count)
			return
		}
	}
}

func (s *OperationLogServiceImpl) flushGap(ctx context.Context) error {
	s.stateMu.Lock()
	if s.gapPending == 0 {
		s.stateMu.Unlock()
		return nil
	}
	markerSeverity, markerRank := operationLogGapSeverity(s.minimumLevel)
	if s.gapMarker == nil {
		s.gapMarkerCount = s.gapPending
		s.gapMarker = &domain.OperationLog{
			ID:           uuid.New(),
			Timestamp:    time.Now().UTC(),
			Severity:     markerSeverity,
			SeverityRank: markerRank,
			Message:      "operation log gap recovered",
			Attrs: map[string]any{
				"event": "operation_log_gap_recovered",
			},
		}
	}
	marker := *s.gapMarker
	marker.Severity = markerSeverity
	marker.SeverityRank = markerRank
	marker.Attrs = map[string]any{
		"dropped_events": s.gapMarkerCount,
		"event":          "operation_log_gap_recovered",
	}
	markerCount := s.gapMarkerCount
	s.stateMu.Unlock()
	if err := s.persist(ctx, []domain.OperationLog{marker}); err != nil {
		s.markFailure(err)
		return err
	}
	s.stateMu.Lock()
	if s.gapPending <= markerCount {
		s.gapPending = 0
		s.gapMarker = nil
		s.gapMarkerCount = 0
	} else {
		s.gapPending -= markerCount
		s.gapMarker = nil
		s.gapMarkerCount = 0
	}
	s.stateMu.Unlock()
	return nil
}

func (s *OperationLogServiceImpl) persist(ctx context.Context, logs []domain.OperationLog) error {
	if ctx == nil {
		ctx = context.Background()
	}
	persistCtx, cancel := context.WithTimeout(ctx, operationLogPersistTimeout)
	defer cancel()
	return s.repo.AppendBatch(observability.WithSinkSuppressed(persistCtx), logs)
}

func (s *OperationLogServiceImpl) markGap(_ string) {
	s.markGapCount(1)
}

func (s *OperationLogServiceImpl) markGapCount(count int) {
	if count <= 0 {
		return
	}
	s.stateMu.Lock()
	s.gapPending += int64(count)
	s.dropped += int64(count)
	s.healthy = false
	s.stateMu.Unlock()
}

func (s *OperationLogServiceImpl) markFailure(err error) {
	s.stateMu.Lock()
	s.healthy = false
	s.lastError = err
	s.retryAfter = time.Now().Add(operationLogRetryInterval)
	s.stateMu.Unlock()
}

func (s *OperationLogServiceImpl) markHealthy() {
	s.stateMu.Lock()
	s.healthy = true
	s.lastError = nil
	s.retryAfter = time.Time{}
	s.stateMu.Unlock()
}

func (s *OperationLogServiceImpl) retryDelay() (time.Duration, bool) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if s.retryAfter.IsZero() {
		return 0, false
	}
	remaining := time.Until(s.retryAfter)
	if remaining <= 0 {
		return 0, false
	}
	return remaining, true
}

func (s *OperationLogServiceImpl) waitForRetry(ctx context.Context) bool {
	delay, deferred := s.retryDelay()
	if !deferred {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// CheckReadiness verifies that the required sink can flush and that no
// unreported admission gap remains.
func (s *OperationLogServiceImpl) CheckReadiness(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return ErrOperationLogSinkUnavailable
	}
	if err := s.Flush(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrOperationLogSinkUnavailable, err)
	}
	if err := s.probe(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrOperationLogSinkUnavailable, err)
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if s.gapPending > 0 {
		return fmt.Errorf("%w: %d events awaiting gap marker", ErrOperationLogSinkUnavailable, s.gapPending)
	}
	if s.lastError != nil {
		return fmt.Errorf("%w: %v", ErrOperationLogSinkUnavailable, s.lastError)
	}
	return nil
}

func (s *OperationLogServiceImpl) probe(ctx context.Context) error {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if s.probeID == uuid.Nil {
		s.probeID = uuid.New()
	}
	s.stateMu.RLock()
	minimumLevel := s.minimumLevel
	s.stateMu.RUnlock()
	if minimumLevel > slog.LevelInfo {
		if prober, ok := s.repo.(operationscontract.OperationLogSinkProber); ok {
			if err := prober.ProbeOperationLogSink(ctx); err != nil {
				s.markFailure(err)
				return err
			}
			s.markHealthy()
		}
		return nil
	}
	probe := domain.OperationLog{
		ID:           s.probeID,
		Timestamp:    time.Now().UTC(),
		Severity:     "INFO",
		SeverityRank: 20,
		Message:      "operation log sink readiness probe",
		Attrs: map[string]any{
			"event": "operation_log_sink_readiness_probe",
		},
	}
	if err := s.persist(ctx, []domain.OperationLog{probe}); err != nil {
		s.markFailure(err)
		return err
	}
	s.markHealthy()
	return nil
}

// DroppedEvents returns events rejected by admission or discarded after a
// failed final shutdown flush. The count stays visible for diagnostics.
func (s *OperationLogServiceImpl) DroppedEvents() int64 {
	if s == nil {
		return 0
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.dropped
}

func (s *OperationLogServiceImpl) Prune(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return nil
	}
	days := settings.DefaultOperationLogRetentionDays
	if s.retention != nil {
		cfg, err := s.retention.OperationLogRuntimeConfig(ctx)
		if err == nil && cfg.RetentionDays > 0 {
			days = cfg.RetentionDays
		}
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	return s.repo.PruneBefore(observability.WithSinkSuppressed(ctx), cutoff)
}

func (s *OperationLogServiceImpl) Start(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	shuttingDown := s.shuttingDown
	if shuttingDown {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.cancel != nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	s.stateMu.Lock()
	s.started = true
	s.healthy = true
	s.stateMu.Unlock()
	go s.run(runCtx, done)
}

func (s *OperationLogServiceImpl) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.requestShutdown()
	s.lifecycleMu.Lock()
	cancel := s.cancel
	done := s.done
	s.cancel = nil
	s.done = nil
	s.lifecycleMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.admissionWG.Wait()
	flushErr := s.flush(ctx, true)
	if flushErr != nil {
		s.discardRetainedAfterShutdownFailure()
	} else {
		s.stateMu.Lock()
		s.shutdownErr = nil
		s.stateMu.Unlock()
	}
	s.stateMu.RLock()
	shutdownErr := s.shutdownErr
	s.stateMu.RUnlock()
	return errors.Join(shutdownErr, flushErr)
}

func (s *OperationLogServiceImpl) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	_ = s.Prune(ctx)
	flushTicker := time.NewTicker(operationLogFlushInterval)
	pruneTicker := time.NewTicker(operationLogPruneInterval)
	defer flushTicker.Stop()
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.Background(), operationLogShutdownFlushTimeout)
			if err := s.flush(flushCtx, true); err != nil {
				s.stateMu.Lock()
				s.shutdownErr = err
				s.stateMu.Unlock()
			}
			cancel()
			return
		case <-flushTicker.C:
			if s.waitForRetry(ctx) {
				_ = s.flush(ctx, false)
			}
		case <-s.flushRequests:
			if s.waitForRetry(ctx) {
				_ = s.flush(ctx, false)
			}
		case <-pruneTicker.C:
			_ = s.Prune(ctx)
		}
	}
}

func parseLogUUID(value string) *uuid.UUID {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return nil
	}
	return &parsed
}
