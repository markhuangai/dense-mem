//go:build integration

package postgres

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func BenchmarkOperationLogOwnership(b *testing.B) {
	f := newOperationLogOwnershipFixture(b)
	for _, workload := range []struct {
		name   string
		filter domain.OperationLogFilter
		total  int64
		items  int
	}{
		{"list_default", domain.OperationLogFilter{}, 2000, 100},
		{"list_maximum", domain.OperationLogFilter{Limit: 999}, 2000, 500},
		{"team_invocation_default", domain.OperationLogFilter{TeamID: &f.teamA, InvocationID: " invocation-1 "}, 600, 100},
		{"team_invocation_maximum", domain.OperationLogFilter{TeamID: &f.teamA, InvocationID: " invocation-1 ", Limit: 999}, 600, 500},
	} {
		b.Run(workload.name, func(b *testing.B) {
			var signature string
			for range 20 {
				page, err := f.service.ListOperationLogs(context.Background(), workload.filter)
				require.NoError(b, err)
				require.Equal(b, workload.total, page.Total)
				require.Len(b, page.Items, workload.items)
				signature = operationLogOwnershipSignature(page)
			}
			f.counters.reset()
			durations := make([]time.Duration, b.N)
			var querySignature string
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				started := time.Now()
				page, err := f.service.ListOperationLogs(context.Background(), workload.filter)
				durations[i] = time.Since(started)
				b.StopTimer()
				require.NoError(b, err)
				require.Equal(b, signature, operationLogOwnershipSignature(page))
				if i == 0 {
					querySignature = f.counters.querySignature()
					f.counters.capture = nil
					f.counters.queries = nil
				}
				b.StartTimer()
			}
			b.StopTimer()
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			require.Equal(b, b.N, f.counters.transactions)
			require.Equal(b, b.N, f.counters.commits)
			require.Zero(b, f.counters.rollbacks)
			require.Positive(b, f.counters.statements)
			b.ReportMetric(float64(durations[(len(durations)-1)/2].Nanoseconds()), "p50-ns/op")
			b.ReportMetric(float64(durations[(len(durations)*95+99)/100-1].Nanoseconds()), "p95-ns/op")
			b.ReportMetric(float64(f.counters.statements)/float64(b.N), "sql-statements/op")
			b.ReportMetric(float64(f.counters.transactions)/float64(b.N), "transactions/op")
			b.ReportMetric(float64(f.counters.commits+f.counters.rollbacks)/float64(b.N), "transaction-completions/op")
			b.ReportMetric(0, "provider-calls/op")
			b.Logf("result_signature_sha256=%s", signature)
			b.Logf("query_contract_sha256=%s", querySignature)
		})
	}
}
