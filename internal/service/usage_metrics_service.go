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
	UsageMetricsBucketSeconds = 60
	UsageMetricsRetentionDays = 30
	defaultFlushInterval      = 10 * time.Second
	defaultPruneInterval      = time.Hour
)

type UsageMetricsRecorder interface {
	RecordRequest(ctx context.Context, event domain.UsageMetricEvent)
}

type UsageMetricsReader interface {
	Snapshot(ctx context.Context, filter domain.UsageMetricsFilter) (*domain.UsageMetricsSnapshot, error)
}

type UsageMetricsService interface {
	UsageMetricsRecorder
	UsageMetricsReader
	Flush(ctx context.Context) error
	Prune(ctx context.Context) error
	Start(ctx context.Context)
	Shutdown(ctx context.Context) error
}

type UsageMetricsServiceImpl struct {
	repo   repository.UsageMetricsRepository
	logger observability.LogProvider

	mu      sync.Mutex
	buckets map[usageBucketKey]domain.UsageMetricBucket

	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
}

type usageBucketKey struct {
	bucketStartUnix int64
	teamID          uuid.UUID
	keyID           uuid.UUID
	route           string
	method          string
	statusClass     int
}

var _ UsageMetricsService = (*UsageMetricsServiceImpl)(nil)

func NewUsageMetricsService(repo repository.UsageMetricsRepository, logger observability.LogProvider) *UsageMetricsServiceImpl {
	return &UsageMetricsServiceImpl{
		repo:    repo,
		logger:  logger,
		buckets: make(map[usageBucketKey]domain.UsageMetricBucket),
	}
}

func (s *UsageMetricsServiceImpl) RecordRequest(_ context.Context, event domain.UsageMetricEvent) {
	if s == nil || s.repo == nil || event.TeamID == uuid.Nil || event.KeyID == uuid.Nil {
		return
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	} else {
		event.Timestamp = event.Timestamp.UTC()
	}
	route := strings.TrimSpace(event.Route)
	if route == "" {
		route = "unknown"
	}
	method := strings.ToUpper(strings.TrimSpace(event.Method))
	if method == "" {
		method = "UNKNOWN"
	}
	status := normalizeHTTPStatus(event.Status)
	statusClass := status / 100
	latencyMS := event.Latency.Milliseconds()
	if latencyMS < 0 {
		latencyMS = 0
	}

	bucketStart := event.Timestamp.Truncate(time.Minute)
	key := usageBucketKey{
		bucketStartUnix: bucketStart.Unix(),
		teamID:          event.TeamID,
		keyID:           event.KeyID,
		route:           route,
		method:          method,
		statusClass:     statusClass,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	bucket := s.buckets[key]
	if bucket.RequestCount == 0 {
		bucket = domain.UsageMetricBucket{
			BucketStart: bucketStart,
			TeamID:      event.TeamID,
			KeyID:       event.KeyID,
			Route:       route,
			Method:      method,
			StatusClass: statusClass,
		}
	}
	bucket.RequestCount++
	if status >= 400 {
		bucket.ErrorCount++
	}
	bucket.TotalLatencyMS += latencyMS
	if latencyMS > bucket.MaxLatencyMS {
		bucket.MaxLatencyMS = latencyMS
	}
	if bucket.LastSeenAt.IsZero() || event.Timestamp.After(bucket.LastSeenAt) {
		bucket.LastSeenAt = event.Timestamp
	}
	s.buckets[key] = bucket
}

func (s *UsageMetricsServiceImpl) Snapshot(ctx context.Context, filter domain.UsageMetricsFilter) (*domain.UsageMetricsSnapshot, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("usage metrics service unavailable")
	}
	if err := s.Flush(ctx); err != nil {
		return nil, err
	}
	normalized := normalizeMetricsFilter(filter)
	snapshot, err := s.repo.Snapshot(ctx, normalized)
	if err != nil {
		return nil, err
	}
	snapshot.Window = domain.UsageMetricsWindow{
		From:          normalized.From,
		To:            normalized.To,
		BucketSeconds: UsageMetricsBucketSeconds,
		RetentionDays: UsageMetricsRetentionDays,
	}
	return snapshot, nil
}

func (s *UsageMetricsServiceImpl) Flush(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return nil
	}
	buckets := s.drainBuckets()
	if len(buckets) == 0 {
		return nil
	}
	if err := s.repo.UpsertBuckets(ctx, buckets); err != nil {
		s.requeueBuckets(buckets)
		return err
	}
	return nil
}

func (s *UsageMetricsServiceImpl) Prune(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -UsageMetricsRetentionDays).Truncate(time.Minute)
	return s.repo.PruneBefore(ctx, cutoff)
}

func (s *UsageMetricsServiceImpl) Start(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.run(runCtx)
}

func (s *UsageMetricsServiceImpl) Shutdown(ctx context.Context) error {
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

func (s *UsageMetricsServiceImpl) run(ctx context.Context) {
	defer close(s.done)

	if err := s.Prune(ctx); err != nil && s.logger != nil {
		s.logger.Warn("usage metrics prune failed", observability.String("error", err.Error()))
	}

	flushTicker := time.NewTicker(defaultFlushInterval)
	defer flushTicker.Stop()
	pruneTicker := time.NewTicker(defaultPruneInterval)
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-flushTicker.C:
			if err := s.Flush(ctx); err != nil && s.logger != nil {
				s.logger.Warn("usage metrics flush failed", observability.String("error", err.Error()))
			}
		case <-pruneTicker.C:
			if err := s.Prune(ctx); err != nil && s.logger != nil {
				s.logger.Warn("usage metrics prune failed", observability.String("error", err.Error()))
			}
		}
	}
}

func (s *UsageMetricsServiceImpl) drainBuckets() []domain.UsageMetricBucket {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buckets) == 0 {
		return nil
	}
	buckets := make([]domain.UsageMetricBucket, 0, len(s.buckets))
	for _, bucket := range s.buckets {
		buckets = append(buckets, bucket)
	}
	s.buckets = make(map[usageBucketKey]domain.UsageMetricBucket)
	return buckets
}

func (s *UsageMetricsServiceImpl) requeueBuckets(buckets []domain.UsageMetricBucket) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, bucket := range buckets {
		key := usageBucketKey{
			bucketStartUnix: bucket.BucketStart.Unix(),
			teamID:          bucket.TeamID,
			keyID:           bucket.KeyID,
			route:           bucket.Route,
			method:          bucket.Method,
			statusClass:     bucket.StatusClass,
		}
		current := s.buckets[key]
		if current.RequestCount == 0 {
			s.buckets[key] = bucket
			continue
		}
		current.RequestCount += bucket.RequestCount
		current.ErrorCount += bucket.ErrorCount
		current.TotalLatencyMS += bucket.TotalLatencyMS
		if bucket.MaxLatencyMS > current.MaxLatencyMS {
			current.MaxLatencyMS = bucket.MaxLatencyMS
		}
		if bucket.LastSeenAt.After(current.LastSeenAt) {
			current.LastSeenAt = bucket.LastSeenAt
		}
		s.buckets[key] = current
	}
}

func normalizeMetricsFilter(filter domain.UsageMetricsFilter) domain.UsageMetricsFilter {
	now := time.Now().UTC()
	if filter.To.IsZero() {
		filter.To = now
	} else {
		filter.To = filter.To.UTC()
	}
	if filter.From.IsZero() {
		filter.From = filter.To.Add(-time.Hour)
	} else {
		filter.From = filter.From.UTC()
	}
	if !filter.From.Before(filter.To) {
		filter.From = filter.To.Add(-time.Hour)
	}
	return filter
}

func normalizeHTTPStatus(status int) int {
	if status == 0 {
		return 200
	}
	if status < 100 || status > 599 {
		return 500
	}
	return status
}
