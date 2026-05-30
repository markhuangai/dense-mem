package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestUsageMetricsService_PersistsUsageAcrossServiceInstances(t *testing.T) {
	repo := newFakeUsageMetricsRepo()
	teamID := uuid.New()
	keyID := uuid.New()
	now := time.Now().UTC()

	svc := NewUsageMetricsService(repo, nil)
	svc.RecordRequest(context.Background(), domain.UsageMetricEvent{
		Timestamp: now,
		TeamID:    teamID,
		KeyID:     keyID,
		Method:    "GET",
		Route:     "/api/v1/fragments/:id",
		Status:    200,
		Latency:   15 * time.Millisecond,
	})
	svc.RecordRequest(context.Background(), domain.UsageMetricEvent{
		Timestamp: now,
		TeamID:    teamID,
		KeyID:     keyID,
		Method:    "GET",
		Route:     "/api/v1/fragments/:id",
		Status:    503,
		Latency:   25 * time.Millisecond,
	})
	require.NoError(t, svc.Flush(context.Background()))

	recreated := NewUsageMetricsService(repo, nil)
	snapshot, err := recreated.Snapshot(context.Background(), domain.UsageMetricsFilter{
		From: now.Add(-time.Minute),
		To:   now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), snapshot.System.Requests)
	require.Equal(t, int64(1), snapshot.System.Errors)
	require.Equal(t, 20.0, snapshot.System.AvgLatencyMS)
	require.Equal(t, int64(25), snapshot.System.MaxLatencyMS)
	require.Len(t, snapshot.Teams, 1)
	require.Equal(t, teamID, snapshot.Teams[0].TeamID)
	require.Len(t, snapshot.Keys, 1)
	require.Equal(t, keyID, snapshot.Keys[0].KeyID)
	require.NotEmpty(t, snapshot.Routes)
}

func TestUsageMetricsService_PrunesExpiredBuckets(t *testing.T) {
	repo := newFakeUsageMetricsRepo()
	oldBucket := domain.UsageMetricBucket{
		BucketStart:  time.Now().UTC().AddDate(0, 0, -UsageMetricsRetentionDays-1).Truncate(time.Minute),
		TeamID:       uuid.New(),
		KeyID:        uuid.New(),
		Route:        "/api/v1/recall",
		Method:       "GET",
		StatusClass:  2,
		RequestCount: 1,
		LastSeenAt:   time.Now().UTC(),
	}
	require.NoError(t, repo.UpsertBuckets(context.Background(), []domain.UsageMetricBucket{oldBucket}))

	svc := NewUsageMetricsService(repo, nil)
	require.NoError(t, svc.Prune(context.Background()))
	snapshot, err := svc.Snapshot(context.Background(), domain.UsageMetricsFilter{
		From: time.Now().UTC().AddDate(0, 0, -UsageMetricsRetentionDays-2),
		To:   time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, int64(0), snapshot.System.Requests)
}

type fakeUsageMetricsRepo struct {
	mu          sync.Mutex
	buckets     map[fakeUsageMetricKey]domain.UsageMetricBucket
	upsertErr   error
	snapshotErr error
}

type fakeUsageMetricKey struct {
	bucketStart time.Time
	teamID      uuid.UUID
	keyID       uuid.UUID
	route       string
	method      string
	statusClass int
}

func newFakeUsageMetricsRepo() *fakeUsageMetricsRepo {
	return &fakeUsageMetricsRepo{buckets: make(map[fakeUsageMetricKey]domain.UsageMetricBucket)}
}

func (r *fakeUsageMetricsRepo) UpsertBuckets(_ context.Context, buckets []domain.UsageMetricBucket) error {
	if r.upsertErr != nil {
		return r.upsertErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, bucket := range buckets {
		key := fakeUsageMetricKey{
			bucketStart: bucket.BucketStart,
			teamID:      bucket.TeamID,
			keyID:       bucket.KeyID,
			route:       bucket.Route,
			method:      bucket.Method,
			statusClass: bucket.StatusClass,
		}
		current := r.buckets[key]
		if current.RequestCount == 0 {
			r.buckets[key] = bucket
			continue
		}
		current.RequestCount += bucket.RequestCount
		current.ErrorCount += bucket.ErrorCount
		current.TotalLatencyMS += bucket.TotalLatencyMS
		if bucket.MaxLatencyMS > current.MaxLatencyMS {
			current.MaxLatencyMS = bucket.MaxLatencyMS
		}
		r.buckets[key] = current
	}
	return nil
}

func (r *fakeUsageMetricsRepo) PruneBefore(_ context.Context, cutoff time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.buckets {
		if key.bucketStart.Before(cutoff) {
			delete(r.buckets, key)
		}
	}
	return nil
}

func (r *fakeUsageMetricsRepo) Snapshot(_ context.Context, filter domain.UsageMetricsFilter) (*domain.UsageMetricsSnapshot, error) {
	if r.snapshotErr != nil {
		return nil, r.snapshotErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	teamTotals := map[uuid.UUID]domain.UsageMetricTotal{}
	keyTotals := map[uuid.UUID]domain.UsageMetricTotal{}
	routeTotals := map[string]domain.UsageMetricTotal{}
	var system domain.UsageMetricTotal

	for _, bucket := range r.buckets {
		if bucket.BucketStart.Before(filter.From) || !bucket.BucketStart.Before(filter.To) {
			continue
		}
		if filter.TeamID != nil && bucket.TeamID != *filter.TeamID {
			continue
		}
		total := domain.UsageMetricTotal{
			Requests:     bucket.RequestCount,
			Errors:       bucket.ErrorCount,
			AvgLatencyMS: float64(bucket.TotalLatencyMS),
			MaxLatencyMS: bucket.MaxLatencyMS,
		}
		system = addFakeTotal(system, total)
		teamTotals[bucket.TeamID] = addFakeTotal(teamTotals[bucket.TeamID], total)
		keyTotals[bucket.KeyID] = addFakeTotal(keyTotals[bucket.KeyID], total)
		routeKey := bucket.Method + " " + bucket.Route
		routeTotals[routeKey] = addFakeTotal(routeTotals[routeKey], total)
	}
	system = finalizeFakeTotal(system)

	snapshot := &domain.UsageMetricsSnapshot{
		System: system,
		Teams:  []domain.UsageTeamMetric{},
		Keys:   []domain.UsageKeyMetric{},
		Routes: []domain.UsageRouteMetric{},
	}
	for teamID, total := range teamTotals {
		snapshot.Teams = append(snapshot.Teams, domain.UsageTeamMetric{TeamID: teamID, UsageMetricTotal: finalizeFakeTotal(total)})
	}
	for keyID, total := range keyTotals {
		snapshot.Keys = append(snapshot.Keys, domain.UsageKeyMetric{KeyID: keyID, UsageMetricTotal: finalizeFakeTotal(total)})
	}
	for route, total := range routeTotals {
		snapshot.Routes = append(snapshot.Routes, domain.UsageRouteMetric{Route: route, UsageMetricTotal: finalizeFakeTotal(total)})
	}
	return snapshot, nil
}

func addFakeTotal(a, b domain.UsageMetricTotal) domain.UsageMetricTotal {
	a.Requests += b.Requests
	a.Errors += b.Errors
	a.AvgLatencyMS += b.AvgLatencyMS
	if b.MaxLatencyMS > a.MaxLatencyMS {
		a.MaxLatencyMS = b.MaxLatencyMS
	}
	return a
}

func finalizeFakeTotal(total domain.UsageMetricTotal) domain.UsageMetricTotal {
	if total.Requests > 0 {
		total.AvgLatencyMS = total.AvgLatencyMS / float64(total.Requests)
	}
	return total
}

func TestUsageMetricsService_RequeuesBucketsAfterFlushError(t *testing.T) {
	repo := newFakeUsageMetricsRepo()
	repo.upsertErr = errors.New("upsert failed")
	svc := NewUsageMetricsService(repo, nil)
	teamID := uuid.New()
	keyID := uuid.New()

	svc.RecordRequest(context.Background(), domain.UsageMetricEvent{
		Timestamp: time.Now().UTC(),
		TeamID:    teamID,
		KeyID:     keyID,
		Route:     " ",
		Method:    " ",
		Status:    700,
		Latency:   -time.Second,
	})

	require.ErrorContains(t, svc.Flush(context.Background()), "upsert failed")
	repo.upsertErr = nil
	require.NoError(t, svc.Flush(context.Background()))

	snapshot, err := svc.Snapshot(context.Background(), domain.UsageMetricsFilter{
		From: time.Now().UTC().Add(-time.Hour),
		To:   time.Now().UTC().Add(time.Hour),
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), snapshot.System.Requests)
	require.Equal(t, int64(1), snapshot.System.Errors)
	require.Equal(t, int64(0), snapshot.System.MaxLatencyMS)
}

func TestUsageMetricsService_NilSnapshotAndLifecycle(t *testing.T) {
	var nilSvc *UsageMetricsServiceImpl
	nilSvc.RecordRequest(context.Background(), domain.UsageMetricEvent{})
	require.NoError(t, nilSvc.Flush(context.Background()))
	require.NoError(t, nilSvc.Prune(context.Background()))
	nilSvc.Start(context.Background())
	require.NoError(t, nilSvc.Shutdown(context.Background()))
	_, err := nilSvc.Snapshot(context.Background(), domain.UsageMetricsFilter{})
	require.ErrorContains(t, err, "usage metrics service unavailable")

	repo := newFakeUsageMetricsRepo()
	svc := NewUsageMetricsService(repo, nil)
	ctx, cancel := context.WithCancel(context.Background())
	svc.Start(ctx)
	svc.Start(ctx)
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	require.NoError(t, svc.Shutdown(shutdownCtx))

	repo.snapshotErr = errors.New("snapshot failed")
	_, err = svc.Snapshot(context.Background(), domain.UsageMetricsFilter{})
	require.ErrorContains(t, err, "snapshot failed")
}

func TestUsageMetricsNormalizationHelpers(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	filter := normalizeMetricsFilter(domain.UsageMetricsFilter{
		From: now,
		To:   now.Add(-time.Minute),
	})
	require.True(t, filter.From.Before(filter.To))
	require.Equal(t, 200, normalizeHTTPStatus(0))
	require.Equal(t, 500, normalizeHTTPStatus(99))
	require.Equal(t, 500, normalizeHTTPStatus(600))
	require.Equal(t, 204, normalizeHTTPStatus(204))
}
