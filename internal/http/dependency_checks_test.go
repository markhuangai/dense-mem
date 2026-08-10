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

func TestHealthConfigWithSharedDependencyChecksSurvivesCopies(t *testing.T) {
	shared := (HealthConfig{}).WithSharedDependencyChecks()
	copy := shared

	require.Same(t, shared.dependencyCheckRegistry(), copy.dependencyCheckRegistry())
}

func TestRunDependencyChecksPreservesOrderAndCapsConcurrency(t *testing.T) {
	registry := newDependencyCheckFlightRegistry()
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

	results := runDependencyChecks(context.Background(), registry, checks)
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

func TestRunDependencyChecksReturnsWhenCheckIgnoresCancellation(t *testing.T) {
	registry := newDependencyCheckFlightRegistry()
	started := make(chan struct{})
	release := make(chan struct{})
	checks := []HealthCheck{{
		Name: "blocking",
		Check: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	}}

	done := make(chan []dependencyCheckResult, 1)
	go func() { done <- runDependencyChecks(context.Background(), registry, checks) }()
	<-started

	select {
	case results := <-done:
		require.Len(t, results, 1)
		require.ErrorIs(t, results[0].Err, context.DeadlineExceeded)
		require.True(t, results[0].TimedOut)
	case <-time.After(dependencyCheckTimeout + 500*time.Millisecond):
		t.Fatal("dependency checks did not return after the timeout")
	}
	close(release)
}

func TestRunDependencyChecksDoesNotRelaunchTimedOutCheckWhileInFlight(t *testing.T) {
	registry := newDependencyCheckFlightRegistry()
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var calls atomic.Int32
	checks := []HealthCheck{{
		Name: "blocking-shared",
		Check: func(context.Context) error {
			if calls.Add(1) == 1 {
				close(started)
				<-release
				close(finished)
			}
			return nil
		},
	}}

	firstDone := make(chan []dependencyCheckResult, 1)
	go func() { firstDone <- runDependencyChecks(context.Background(), registry, checks) }()
	<-started
	first := <-firstDone
	require.Len(t, first, 1)
	require.ErrorIs(t, first[0].Err, context.DeadlineExceeded)
	require.Equal(t, int32(1), calls.Load())

	secondCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	second := runDependencyChecks(secondCtx, registry, checks)
	require.Len(t, second, 1)
	require.ErrorIs(t, second[0].Err, context.DeadlineExceeded)
	require.Equal(t, int32(1), calls.Load())

	close(release)
	<-finished
	deadline := time.Now().Add(time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		third := runDependencyChecks(context.Background(), registry, checks)
		if len(third) == 1 && third[0].Err == nil && !third[0].TimedOut && calls.Load() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, int32(2), calls.Load())
}

func TestRunDependencyChecksSharesInFlightResult(t *testing.T) {
	registry := newDependencyCheckFlightRegistry()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	checks := []HealthCheck{{
		Name: "shared-success",
		Check: func(context.Context) error {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-release
			return nil
		},
	}}

	firstDone := make(chan []dependencyCheckResult, 1)
	go func() { firstDone <- runDependencyChecks(context.Background(), registry, checks) }()
	<-started

	go func() {
		time.Sleep(25 * time.Millisecond)
		close(release)
	}()
	second := runDependencyChecks(context.Background(), registry, checks)
	first := <-firstDone
	require.Len(t, first, 1)
	require.NoError(t, first[0].Err)
	require.Len(t, second, 1)
	require.NoError(t, second[0].Err)
	require.Equal(t, int32(1), calls.Load())
}

func TestRunDependencyChecksSharedFlightOutlivesFirstCaller(t *testing.T) {
	registry := newDependencyCheckFlightRegistry()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	checks := []HealthCheck{{
		Name: "shared-independent-context",
		Check: func(ctx context.Context) error {
			if calls.Add(1) == 1 {
				close(started)
			}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}}

	firstCtx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan []dependencyCheckResult, 1)
	go func() { firstDone <- runDependencyChecks(firstCtx, registry, checks) }()
	<-started
	cancel()
	first := <-firstDone
	require.Len(t, first, 1)
	require.ErrorIs(t, first[0].Err, context.Canceled)

	secondDone := make(chan []dependencyCheckResult, 1)
	go func() { secondDone <- runDependencyChecks(context.Background(), registry, checks) }()
	time.Sleep(10 * time.Millisecond)
	close(release)
	second := <-secondDone
	require.Len(t, second, 1)
	require.NoError(t, second[0].Err)
	require.False(t, second[0].TimedOut)
	require.Equal(t, int32(1), calls.Load())
}
