package http

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunDependencyChecksPreservesOrderAndCapsConcurrency(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	checks := make([]HealthCheck, 7)
	for index := range checks {
		index := index
		checks[index] = HealthCheck{
			Name: "dependency-" + string(rune('a'+index)),
			Check: func(context.Context) error {
				current := active.Add(1)
				for {
					old := maximum.Load()
					if current <= old || maximum.CompareAndSwap(old, current) {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
				active.Add(-1)
				return nil
			},
		}
	}

	results := runDependencyChecks(context.Background(), checks)
	require.Len(t, results, len(checks))
	require.LessOrEqual(t, maximum.Load(), int32(dependencyCheckConcurrency))
	for index, result := range results {
		require.Equal(t, checks[index].Name, result.Check.Name)
		require.NoError(t, result.Err)
	}
}

func TestDependencyDisclosureBoundsFailureAndTimeout(t *testing.T) {
	report := observeDependencies(context.Background(), HealthConfig{Checks: []HealthCheck{
		{Name: "postgres", Check: func(context.Context) error { return errors.New("raw database password and SQL details") }},
		{Name: "redis", Optional: true, Check: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }},
	}})

	require.Len(t, report.Dependencies, 2)
	require.NotNil(t, report.Dependencies[0].ReasonCode)
	require.Equal(t, "check_failed", *report.Dependencies[0].ReasonCode)
	require.Equal(t, "Dependency check failed.", *report.Dependencies[0].Message)
	require.NotContains(t, *report.Dependencies[0].Message, "password")
	require.NotNil(t, report.Dependencies[1].ReasonCode)
	require.Equal(t, "check_timeout", *report.Dependencies[1].ReasonCode)
	require.Equal(t, "degraded", report.Dependencies[1].Status)
	require.NotEmpty(t, report.CheckedAt)

	payload, err := json.Marshal(report.Dependencies)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "raw database password")
}
