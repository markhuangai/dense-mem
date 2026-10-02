//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type usageFlushSample struct {
	operationNS, repositoryNS, coordinationNS int64
	flushID                                   uuid.UUID
	counters                                  usageFlushCounters
	recordNS                                  []int64
	state                                     usageFlushState
}

func BenchmarkUsageFlushScaling(b *testing.B) {
	b.StopTimer()
	f := newUsageFlushFixture(b)
	for _, buckets := range []int{1, 100, 1000} {
		for _, shape := range []string{"no_credentials", "one_per_owner", "ten_per_owner"} {
			for _, mode := range []string{"insert", "update", "replay", "concurrent_update", "record_only"} {
				b.Run(fmt.Sprintf("B%d/%s/%s", buckets, shape, mode), func(b *testing.B) {
					benchmarkUsageFlushWorkload(b, f, buckets, shape, mode)
				})
			}
		}
	}
}

func benchmarkUsageFlushWorkload(b *testing.B, f *usageFlushFixture, buckets int, shape, mode string) {
	b.StopTimer()
	events := f.reset(b, buckets, shape)
	for range 20 {
		usageFlushRun(b, f, events, mode, false)
	}
	operations, repositories, shares := make([]int64, b.N), make([]int64, b.N), make([]int64, b.N)
	coordination := make([]int64, b.N)
	records := make([]int64, 0, b.N*buckets)
	var first, last usageFlushSample
	seenIDs := make(map[uuid.UUID]bool, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	b.StopTimer()
	for i := 0; i < b.N; i++ {
		sample := usageFlushRun(b, f, events, mode, true)
		operations[i], repositories[i] = sample.operationNS, sample.repositoryNS
		coordination[i] = sample.coordinationNS
		shares[i] = (sample.counters.OwnersNS + sample.counters.CredentialsNS) * 1_000_000 / sample.operationNS
		records = append(records, sample.recordNS...)
		if i == 0 {
			first = sample
		}
		require.Equal(b, usageFlushCounts(first.counters), usageFlushCounts(sample.counters))
		if mode != "record_only" {
			id := sample.flushID
			require.False(b, seenIDs[id], "write priming must give every sample a fresh flush ID")
			seenIDs[id] = true
		}
		last = sample
	}
	metrics := map[string]float64{
		"operation-p50-ns/op":            float64(usageFlushPercentile(operations, 50)),
		"operation-p95-ns/op":            float64(usageFlushPercentile(operations, 95)),
		"repository-p50-ns/op":           float64(usageFlushPercentile(repositories, 50)),
		"repository-p95-ns/op":           float64(usageFlushPercentile(repositories, 95)),
		"owner-credential-share-percent": float64(usageFlushPercentile(shares, 50)) / 10000,
		"coordination-p50-ns/op":         float64(usageFlushPercentile(coordination, 50)),
		"record-p50-ns/call":             float64(usageFlushPercentile(records, 50)),
		"record-p95-ns/call":             float64(usageFlushPercentile(records, 95)),
		"ledger-statements/op":           float64(first.counters.Ledger),
		"owner-statements/op":            float64(first.counters.Owners),
		"credential-statements/op":       float64(first.counters.Credentials),
		"rls-statements/op":              float64(first.counters.Setup),
		"other-statements/op":            float64(first.counters.Other),
		"transactions/op":                float64(first.counters.Transactions),
		"commits/op":                     float64(first.counters.Commits),
		"rollbacks/op":                   float64(first.counters.Rollbacks),
	}
	data := first.counters.Ledger + first.counters.Owners + first.counters.Credentials
	expected := int64(1 + buckets)
	if shape != "no_credentials" {
		expected += int64(buckets)
	}
	if mode == "replay" {
		expected = 1
	} else if mode == "record_only" {
		expected = 0
	}
	metrics["data-statements/op"] = float64(data)
	metrics["data-model-difference/op"] = float64(data - expected)
	metrics["sql-statements/op"] = float64(data + first.counters.Setup + first.counters.Other)
	for name, value := range metrics {
		b.ReportMetric(value, name)
	}
	result := map[string]any{
		"workload": b.Name(), "iterations": b.N, "warmups": 20,
		"buckets": buckets, "shape": shape, "mode": mode,
		"postgresql": f.version, "metrics": metrics,
		"owners": usageFlushTotals(last.state.Owners), "credentials": usageFlushTotals(last.state.Credentials),
		"result_signature_sha256": usageFlushSignature(last.state),
	}
	encoded, err := json.Marshal(result)
	require.NoError(b, err)
	b.Logf("usage_flush_scaling_result=%s", encoded)
}

func usageFlushRun(b *testing.B, f *usageFlushFixture, events []domain.UsageMetricEvent, mode string, timed bool) usageFlushSample {
	f.resetUsage(b)
	f.record(events)
	newer := usageFlushLater(events, 30, 20)
	var replayID uuid.UUID
	expected := append([]domain.UsageMetricEvent(nil), events...)
	if mode == "update" || mode == "concurrent_update" {
		require.NoError(b, f.service.Flush(context.Background()))
		f.record(newer)
		expected = append(expected, newer...)
	} else if mode == "replay" {
		f.probe.loseReply = true
		require.ErrorIs(b, f.service.Flush(context.Background()), errUsageFlushReplyLost)
		replayID = f.probe.lastID
	}
	*f.counters = usageFlushCounters{}
	f.probe.repositoryNS = 0
	sample := usageFlushSample{}
	var done chan struct{}
	var concurrent []domain.UsageMetricEvent
	if mode == "concurrent_update" {
		concurrent = usageFlushLater(events, 35, 50)
		sample.recordNS = make([]int64, len(concurrent))
		start, first, proceed := make(chan struct{}), make(chan struct{}), make(chan struct{})
		done = make(chan struct{})
		go func() {
			defer close(done)
			<-start
			for i, event := range concurrent {
				started := time.Now()
				f.service.RecordRequest(context.Background(), event)
				sample.recordNS[i] = time.Since(started).Nanoseconds()
				if i == 0 {
					close(first)
					<-proceed
				}
			}
		}()
		f.probe.before = func() {
			started := time.Now()
			if timed {
				b.StopTimer()
			}
			close(start)
			<-first
			if timed {
				b.StartTimer()
			}
			close(proceed)
			sample.coordinationNS = time.Since(started).Nanoseconds()
		}
	} else if mode == "record_only" {
		sample.recordNS = make([]int64, len(newer))
	}
	if timed {
		b.StartTimer()
	}
	started := time.Now()
	var err error
	if mode == "record_only" {
		for i, event := range newer {
			recordStarted := time.Now()
			f.service.RecordRequest(context.Background(), event)
			sample.recordNS[i] = time.Since(recordStarted).Nanoseconds()
		}
	} else {
		err = f.service.Flush(context.Background())
	}
	sample.operationNS = time.Since(started).Nanoseconds() - sample.coordinationNS
	if timed {
		b.StopTimer()
	}
	require.NoError(b, err)
	sample.repositoryNS, sample.counters = f.probe.repositoryNS, *f.counters
	sample.flushID = f.probe.lastID
	if mode != "record_only" {
		require.Equal(b, len(events), f.probe.bucketCount)
		require.NotEqual(b, uuid.Nil, f.probe.lastID)
	}
	if mode == "replay" {
		require.Equal(b, replayID, f.probe.lastID)
	}
	if done != nil {
		<-done
	}
	if mode == "record_only" {
		require.NoError(b, f.service.Flush(context.Background()))
		expected = append(expected, newer...)
	}
	sample.state = f.verify(b, expected)
	if concurrent != nil {
		require.NoError(b, f.service.Flush(context.Background()))
		sample.state = f.verify(b, append(expected, concurrent...))
	}
	return sample
}

func usageFlushCounts(c usageFlushCounters) [8]int64 {
	return [8]int64{c.Ledger, c.Owners, c.Credentials, c.Setup, c.Other, c.Transactions, c.Commits, c.Rollbacks}
}

func usageFlushPercentile(values []int64, percentile int) int64 {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values[(len(values)*percentile+99)/100-1]
}

func usageFlushTotals(rows []domain.UsageMetricBucket) map[string]int64 {
	result := map[string]int64{"rows": int64(len(rows))}
	for _, row := range rows {
		result["requests"] += row.RequestCount
		result["errors"] += row.ErrorCount
		result["mcp_tool_calls"] += row.MCPToolCalls
		result["mcp_tool_failures"] += row.MCPToolFailures
		result["total_latency_ms"] += row.TotalLatencyMS
		result["max_latency_ms"] = max(result["max_latency_ms"], row.MaxLatencyMS)
	}
	return result
}
