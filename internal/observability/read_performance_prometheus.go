package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type readPerformancePrometheusMetrics struct {
	stageDuration *prometheus.HistogramVec
	sqlStatements *prometheus.CounterVec
	stageItems    *prometheus.HistogramVec
}

func newReadPerformancePrometheusMetrics() *readPerformancePrometheusMetrics {
	return &readPerformancePrometheusMetrics{
		stageDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_read_stage_duration_seconds",
			Help:    "Complete Search and Recall read-stage duration, including row decoding and error checks.",
			Buckets: durationBuckets([]float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}),
		}, []string{"operation", "stage", "outcome"}),
		sqlStatements: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_read_sql_statements_total",
			Help: "GORM SQL statements executed by an allowlisted Search or Recall read stage.",
		}, []string{"operation", "stage"}),
		stageItems: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_read_stage_items",
			Help:    "Candidates or results produced by one executed Search or Recall read stage.",
			Buckets: []float64{0, 1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000},
		}, []string{"operation", "stage"}),
	}
}

func (m *readPerformancePrometheusMetrics) collectors() []prometheus.Collector {
	if m == nil {
		return nil
	}
	return []prometheus.Collector{m.stageDuration, m.sqlStatements, m.stageItems}
}

func (m *PrometheusMetrics) ObserveReadStage(operation ReadOperation, stage ReadStage, outcome ReadOutcome, duration time.Duration, items int) {
	if m == nil || m.readPerformance == nil || !validReadOperation(operation) ||
		!validReadStage(stage) || !validReadOutcome(outcome) || duration < 0 {
		return
	}
	if items < 0 {
		items = 0
	}
	m.readPerformance.stageDuration.WithLabelValues(string(operation), string(stage), string(outcome)).Observe(duration.Seconds())
	m.readPerformance.stageItems.WithLabelValues(string(operation), string(stage)).Observe(float64(items))
}

func (m *PrometheusMetrics) IncReadSQLStatement(operation ReadOperation, stage ReadStage) {
	if m == nil || m.readPerformance == nil || !validReadOperation(operation) || !validReadStage(stage) {
		return
	}
	m.readPerformance.sqlStatements.WithLabelValues(string(operation), string(stage)).Inc()
}
