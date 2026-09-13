package domain

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// UsageMetricEvent is one authenticated runtime request observed by HTTP middleware.
type UsageMetricEvent struct {
	Timestamp       time.Time
	TeamID          uuid.UUID
	KeyID           uuid.UUID
	Method          string
	Route           string
	Status          int
	Latency         time.Duration
	MCPToolCalls    int64
	MCPToolFailures int64
}

// UsageMetricBucket is the persisted aggregate for a bounded metric dimension set.
type UsageMetricBucket struct {
	BucketStart     time.Time
	TeamID          uuid.UUID
	KeyID           uuid.UUID
	Route           string
	Method          string
	StatusClass     int
	RequestCount    int64
	ErrorCount      int64
	MCPToolCalls    int64
	MCPToolFailures int64
	TotalLatencyMS  int64
	MaxLatencyMS    int64
	LastSeenAt      time.Time
}

type UsageMetricsFilter struct {
	From time.Time
	To   time.Time
	// TeamID filters the admin metrics view. It does not come from request-supplied
	// runtime identity and is only used on the control API.
	TeamID *uuid.UUID
}

type UsageMetricsWindow struct {
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	BucketSeconds int       `json:"bucket_seconds"`
	RetentionDays int       `json:"retention_days"`
}

type UsageMetricTotal struct {
	Requests        int64   `json:"requests"`
	Errors          int64   `json:"errors"`
	MCPToolCalls    *int64  `json:"mcp_tool_calls,omitempty"`
	MCPToolFailures *int64  `json:"mcp_tool_failures,omitempty"`
	AvgLatencyMS    float64 `json:"avg_latency_ms"`
	MaxLatencyMS    int64   `json:"max_latency_ms"`
}

type UsageTeamMetric struct {
	TeamID   uuid.UUID `json:"team_id"`
	TeamName string    `json:"team_name"`
	UsageMetricTotal
}

type UsageKeyMetric struct {
	TeamID    uuid.UUID `json:"team_id"`
	TeamName  string    `json:"team_name"`
	KeyID     uuid.UUID `json:"key_id"`
	KeyName   string    `json:"key_name"`
	KeySuffix string    `json:"key_suffix"`
	UsageMetricTotal
}

type UsageRouteMetric struct {
	Route       string `json:"route"`
	Method      string `json:"method"`
	StatusClass string `json:"status_class"`
	UsageMetricTotal
}

type UsageMetricsSnapshot struct {
	Window UsageMetricsWindow `json:"window"`
	System UsageMetricTotal   `json:"system"`
	Teams  []UsageTeamMetric  `json:"teams"`
	Keys   []UsageKeyMetric   `json:"keys"`
	Routes []UsageRouteMetric `json:"routes"`
}

type mcpToolMetricsContextKey struct{}

// MCPToolMetrics records server-owned tool outcomes for one HTTP request.
type MCPToolMetrics struct {
	calls    atomic.Int64
	failures atomic.Int64
}

func WithMCPToolMetrics(ctx context.Context) (context.Context, *MCPToolMetrics) {
	metrics := &MCPToolMetrics{}
	return context.WithValue(ctx, mcpToolMetricsContextKey{}, metrics), metrics
}

func MCPToolMetricsFromContext(ctx context.Context) *MCPToolMetrics {
	if ctx == nil {
		return nil
	}
	metrics, _ := ctx.Value(mcpToolMetricsContextKey{}).(*MCPToolMetrics)
	return metrics
}

func RecordMCPToolCall(ctx context.Context) {
	if metrics := MCPToolMetricsFromContext(ctx); metrics != nil {
		metrics.calls.Add(1)
	}
}

func RecordMCPToolFailure(ctx context.Context) {
	if metrics := MCPToolMetricsFromContext(ctx); metrics != nil {
		metrics.failures.Add(1)
	}
}

func (metrics *MCPToolMetrics) Snapshot() (int64, int64) {
	if metrics == nil {
		return 0, 0
	}
	return metrics.calls.Load(), metrics.failures.Load()
}
