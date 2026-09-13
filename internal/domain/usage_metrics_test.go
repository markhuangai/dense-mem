package domain

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPToolMetricsContextAndSnapshot(t *testing.T) {
	ctx, metrics := WithMCPToolMetrics(context.Background())
	require.Same(t, metrics, MCPToolMetricsFromContext(ctx))
	require.Nil(t, MCPToolMetricsFromContext(context.Background()))
	require.Nil(t, MCPToolMetricsFromContext(nilUsageMetricsContext()))

	RecordMCPToolCall(ctx)
	RecordMCPToolCall(ctx)
	RecordMCPToolFailure(ctx)
	require.EqualValues(t, 2, metrics.calls.Load())
	require.EqualValues(t, 1, metrics.failures.Load())
	calls, failures := metrics.Snapshot()
	require.EqualValues(t, 2, calls)
	require.EqualValues(t, 1, failures)

	RecordMCPToolCall(context.Background())
	RecordMCPToolFailure(nilUsageMetricsContext())
	var nilMetrics *MCPToolMetrics
	calls, failures = nilMetrics.Snapshot()
	require.Zero(t, calls)
	require.Zero(t, failures)
}

func nilUsageMetricsContext() context.Context {
	return nil
}
