package service

import (
	"context"
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
	mu      sync.Mutex
	buckets map[fakeUsageMetricKey]domain.UsageMetricBucket
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
