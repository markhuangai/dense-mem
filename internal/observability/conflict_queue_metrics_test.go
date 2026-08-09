package observability

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestConflictQueueCountersUseClosedLabels(t *testing.T) {
	metrics := NewPrometheusMetrics()
	metrics.ObserveConflictAssessment("team-1", "selected", "database-error")
	metrics.ObserveConflictResolution("team-1", "last_write_wins", "resolved")

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	require.Contains(t, body, `densemem_conflict_assessments_total{decision="selected",failure_class="unknown",team_id="team-1"} 1`)
	require.Contains(t, body, `densemem_conflict_resolutions_total{method="last_write_wins",outcome="resolved",team_id="team-1"} 1`)
	require.NotContains(t, body, "database-error")
}

func TestConflictQueueCollectorSuppressesGaugesOnFailure(t *testing.T) {
	collector := NewConflictQueueCollector(func(context.Context) (domain.ConflictQueueMetricsSnapshot, error) {
		return domain.ConflictQueueMetricsSnapshot{}, errors.New("database detail")
	})
	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(collector))
	families, err := registry.Gather()
	require.NoError(t, err)
	require.Len(t, families, 1)
	require.Equal(t, "densemem_conflict_queue_collection_success", families[0].GetName())
	require.Equal(t, float64(0), families[0].GetMetric()[0].GetGauge().GetValue())
}

func TestConflictQueueCollectorPublishesZeroAndNonZeroState(t *testing.T) {
	collector := NewConflictQueueCollector(func(context.Context) (domain.ConflictQueueMetricsSnapshot, error) {
		return domain.ConflictQueueMetricsSnapshot{
			Cases:        []domain.ConflictQueueMetricCase{{TeamID: "team-1", Status: "open", Value: 0}},
			OldestAges:   []domain.ConflictQueueMetricOldestAge{{TeamID: "team-1", Status: "open", Value: 42}},
			Leases:       []domain.ConflictQueueMetricLease{{TeamID: "team-1", State: "idle", Value: 1}},
			DerivedTasks: []domain.ConflictQueueMetricDerivedTask{{TeamID: "team-1", Status: "pending", Value: 2}},
		}, nil
	})
	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(collector))
	families, err := registry.Gather()
	require.NoError(t, err)
	text := make([]string, 0, len(families))
	for _, family := range families {
		text = append(text, family.GetName())
	}
	require.ElementsMatch(t, []string{
		"densemem_conflict_queue_cases",
		"densemem_conflict_queue_oldest_age_seconds",
		"densemem_conflict_queue_leases",
		"densemem_conflict_queue_derived_tasks",
		"densemem_conflict_queue_collection_success",
	}, text)
}

func TestConflictQueueCollectorNormalizesGaugeLabels(t *testing.T) {
	collector := NewConflictQueueCollector(func(context.Context) (domain.ConflictQueueMetricsSnapshot, error) {
		return domain.ConflictQueueMetricsSnapshot{
			Cases:        []domain.ConflictQueueMetricCase{{TeamID: "team-1", Status: "database-error", Value: 1}},
			OldestAges:   []domain.ConflictQueueMetricOldestAge{{TeamID: "team-1", Status: "database-error", Value: 1}},
			Leases:       []domain.ConflictQueueMetricLease{{TeamID: "team-1", State: "database-error", Value: 1}},
			DerivedTasks: []domain.ConflictQueueMetricDerivedTask{{TeamID: "team-1", Status: "database-error", Value: 1}},
		}, nil
	})
	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(collector))
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		require.NotContains(t, family.String(), "database-error")
	}
}

func TestConflictQueueCollectorHonorsTimeout(t *testing.T) {
	collector := NewConflictQueueCollector(func(ctx context.Context) (domain.ConflictQueueMetricsSnapshot, error) {
		<-ctx.Done()
		return domain.ConflictQueueMetricsSnapshot{}, ctx.Err()
	})
	collector.timeout = 5 * time.Millisecond
	start := time.Now()
	collector.Collect(make(chan prometheus.Metric, 1))
	require.Less(t, time.Since(start), time.Second)
}

func TestConflictQueueCollectorDoesNotExposeErrors(t *testing.T) {
	collector := NewConflictQueueCollector(func(context.Context) (domain.ConflictQueueMetricsSnapshot, error) {
		return domain.ConflictQueueMetricsSnapshot{}, errors.New("secret provider response")
	})
	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(collector))
	families, err := registry.Gather()
	require.NoError(t, err)
	require.False(t, strings.Contains(families[0].String(), "secret provider response"))
}
