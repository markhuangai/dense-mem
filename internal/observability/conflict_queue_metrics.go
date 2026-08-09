package observability

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// ConflictQueueMetrics records only outcomes that have crossed a durable
// conflict-review commit boundary.
type ConflictQueueMetrics interface {
	ObserveConflictAssessment(teamID, decision, failureClass string)
	ObserveConflictResolution(teamID, method, outcome string)
}

var _ ConflictQueueMetrics = (*PrometheusMetrics)(nil)

func (m *PrometheusMetrics) ObserveConflictAssessment(teamID, decision, failureClass string) {
	if m == nil || m.conflictAssessments == nil {
		return
	}
	m.conflictAssessments.WithLabelValues(
		normalizeTeamMetricLabel(teamID),
		domain.NormalizeConflictQueueAssessmentDecision(decision),
		domain.NormalizeConflictQueueFailureClass(failureClass),
	).Inc()
}

func (m *PrometheusMetrics) ObserveConflictResolution(teamID, method, outcome string) {
	if m == nil || m.conflictResolutions == nil {
		return
	}
	m.conflictResolutions.WithLabelValues(
		normalizeTeamMetricLabel(teamID),
		domain.NormalizeConflictQueueResolutionMethod(method),
		domain.NormalizeConflictQueueResolutionOutcome(outcome),
	).Inc()
}

func normalizeTeamMetricLabel(teamID string) string {
	return normalizeLabel(teamID)
}

// ConflictQueueSnapshotReader is intentionally a function-shaped port so the
// PostgreSQL repository remains independent from Prometheus implementation
// details.
type ConflictQueueSnapshotReader func(context.Context) (domain.ConflictQueueMetricsSnapshot, error)

type ConflictQueueCollector struct {
	read         ConflictQueueSnapshotReader
	timeout      time.Duration
	cases        *prometheus.Desc
	oldestAges   *prometheus.Desc
	leases       *prometheus.Desc
	derivedTasks *prometheus.Desc
	collectionOK *prometheus.Desc
}

func NewConflictQueueCollector(read ConflictQueueSnapshotReader) *ConflictQueueCollector {
	return &ConflictQueueCollector{
		read:         read,
		timeout:      2 * time.Second,
		cases:        prometheus.NewDesc("densemem_conflict_queue_cases", "Open and overdue conflict cases.", []string{"team_id", "status"}, nil),
		oldestAges:   prometheus.NewDesc("densemem_conflict_queue_oldest_age_seconds", "Age of the oldest active conflict case.", []string{"team_id", "status"}, nil),
		leases:       prometheus.NewDesc("densemem_conflict_queue_leases", "Conflict case leases by state.", []string{"team_id", "state"}, nil),
		derivedTasks: prometheus.NewDesc("densemem_conflict_queue_derived_tasks", "Incomplete derived conflict tasks by state.", []string{"team_id", "status"}, nil),
		collectionOK: prometheus.NewDesc("densemem_conflict_queue_collection_success", "Whether the latest conflict queue collection succeeded.", nil, nil),
	}
}

func (c *ConflictQueueCollector) Describe(ch chan<- *prometheus.Desc) {
	if c == nil {
		return
	}
	ch <- c.cases
	ch <- c.oldestAges
	ch <- c.leases
	ch <- c.derivedTasks
	ch <- c.collectionOK
}

func (c *ConflictQueueCollector) Collect(ch chan<- prometheus.Metric) {
	if c == nil || c.read == nil {
		if c != nil {
			ch <- prometheus.MustNewConstMetric(c.collectionOK, prometheus.GaugeValue, 0)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	result := make(chan conflictQueueCollectionResult, 1)
	go func() {
		snapshot, err := c.read(ctx)
		result <- conflictQueueCollectionResult{snapshot: snapshot, err: err}
	}()
	select {
	case <-ctx.Done():
		ch <- prometheus.MustNewConstMetric(c.collectionOK, prometheus.GaugeValue, 0)
		return
	case collected := <-result:
		if collected.err != nil {
			ch <- prometheus.MustNewConstMetric(c.collectionOK, prometheus.GaugeValue, 0)
			return
		}
		for _, value := range collected.snapshot.Cases {
			ch <- prometheus.MustNewConstMetric(c.cases, prometheus.GaugeValue, value.Value, normalizeTeamMetricLabel(value.TeamID), normalizeConflictQueueStatusLabel(value.Status))
		}
		for _, value := range collected.snapshot.OldestAges {
			ch <- prometheus.MustNewConstMetric(c.oldestAges, prometheus.GaugeValue, value.Value, normalizeTeamMetricLabel(value.TeamID), normalizeConflictQueueStatusLabel(value.Status))
		}
		for _, value := range collected.snapshot.Leases {
			ch <- prometheus.MustNewConstMetric(c.leases, prometheus.GaugeValue, value.Value, normalizeTeamMetricLabel(value.TeamID), normalizeConflictQueueLeaseLabel(value.State))
		}
		for _, value := range collected.snapshot.DerivedTasks {
			ch <- prometheus.MustNewConstMetric(c.derivedTasks, prometheus.GaugeValue, value.Value, normalizeTeamMetricLabel(value.TeamID), normalizeConflictQueueDerivedTaskLabel(value.Status))
		}
		ch <- prometheus.MustNewConstMetric(c.collectionOK, prometheus.GaugeValue, 1)
	}
}

func normalizeConflictQueueStatusLabel(value string) string {
	if value == "open" || value == "overdue" {
		return value
	}
	return "unknown"
}

func normalizeConflictQueueLeaseLabel(value string) string {
	if value == "idle" || value == "active" || value == "expired" {
		return value
	}
	return "unknown"
}

func normalizeConflictQueueDerivedTaskLabel(value string) string {
	if value == "pending" || value == "failed" {
		return value
	}
	return "unknown"
}

type conflictQueueCollectionResult struct {
	snapshot domain.ConflictQueueMetricsSnapshot
	err      error
}

func (m *PrometheusMetrics) RegisterConflictQueueCollector(collector prometheus.Collector) error {
	if m == nil || m.registry == nil {
		return nil
	}
	return m.registry.Register(collector)
}
