package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	operationLogQueueSize     = 4096
	operationLogBatchSize     = 100
	operationLogFlushInterval = 2 * time.Second
	operationLogPruneInterval = time.Hour
)

type OperationLogReader interface {
	ListOperationLogs(ctx context.Context, filter domain.OperationLogFilter) (*domain.OperationLogPage, error)
}

type OperationLogService interface {
	OperationLogReader
	WriteLog(ctx context.Context, record observability.LogRecord) error
	Flush(ctx context.Context) error
	Prune(ctx context.Context) error
	Start(ctx context.Context)
	Shutdown(ctx context.Context) error
}

type OperationLogRetentionProvider interface {
	OperationLogRuntimeConfig(ctx context.Context) (domain.OperationLogRuntimeConfig, error)
}

type OperationLogServiceImpl struct {
	repo      repository.OperationLogRepository
	retention OperationLogRetentionProvider

	queue chan domain.OperationLog

	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
}

var _ OperationLogService = (*OperationLogServiceImpl)(nil)
var _ observability.LogSink = (*OperationLogServiceImpl)(nil)

func NewOperationLogService(repo repository.OperationLogRepository, retention OperationLogRetentionProvider) *OperationLogServiceImpl {
	return &OperationLogServiceImpl{
		repo:      repo,
		retention: retention,
		queue:     make(chan domain.OperationLog, operationLogQueueSize),
	}
}

func (s *OperationLogServiceImpl) WriteLog(_ context.Context, record observability.LogRecord) error {
	if s == nil || s.repo == nil {
		return nil
	}
	entry := domain.OperationLog{
		Timestamp:     record.Timestamp,
		Severity:      strings.ToUpper(strings.TrimSpace(record.Severity)),
		SeverityRank:  record.SeverityRank,
		Message:       record.Message,
		Source:        record.Source,
		CorrelationID: record.CorrelationID,
		Error:         record.Error,
		Attrs:         record.Attrs,
	}
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

	select {
	case s.queue <- entry:
	default:
		return nil
	}
	return nil
}

func operationLogSeverityRank(severity string) int {
	switch strings.ToUpper(strings.TrimSpace(severity)) {
	case "ERROR":
		return 40
	case "WARN":
		return 30
	case "DEBUG":
		return 10
	default:
		return 20
	}
}

func (s *OperationLogServiceImpl) ListOperationLogs(ctx context.Context, filter domain.OperationLogFilter) (*domain.OperationLogPage, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("operation log service unavailable")
	}
	if err := s.Flush(ctx); err != nil {
		return nil, err
	}
	return s.repo.List(ctx, filter)
}

func (s *OperationLogServiceImpl) Flush(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return nil
	}
	for {
		batch := make([]domain.OperationLog, 0, operationLogBatchSize)
		for len(batch) < operationLogBatchSize {
			select {
			case entry := <-s.queue:
				batch = append(batch, entry)
			default:
				if len(batch) == 0 {
					return nil
				}
				if err := s.repo.AppendBatch(ctx, batch); err != nil {
					return err
				}
				return nil
			}
		}
		if err := s.repo.AppendBatch(ctx, batch); err != nil {
			return err
		}
	}
}

func (s *OperationLogServiceImpl) Prune(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return nil
	}
	days := DefaultOperationLogRetentionDays
	if s.retention != nil {
		cfg, err := s.retention.OperationLogRuntimeConfig(ctx)
		if err == nil && cfg.RetentionDays > 0 {
			days = cfg.RetentionDays
		}
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	return s.repo.PruneBefore(ctx, cutoff)
}

func (s *OperationLogServiceImpl) Start(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	go s.run(runCtx, done)
}

func (s *OperationLogServiceImpl) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
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
	return s.Flush(ctx)
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
			_ = s.Flush(context.Background())
			return
		case <-flushTicker.C:
			_ = s.Flush(ctx)
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
