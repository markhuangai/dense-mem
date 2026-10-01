package operations

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func BenchmarkTelemetryTransportOwnership(b *testing.B) {
	for _, mode := range []string{"success", "partial_failure"} {
		b.Run(mode, func(b *testing.B) {
			f := newTelemetryTransportOwnershipFixture(b, mode)
			filter := TelemetryFilter{Window: "15m", Scope: "system", Audience: TelemetryAudienceOperator}
			var signature string
			for range 20 {
				snapshot, err := f.snapshot(context.Background(), filter)
				require.NoError(b, err)
				actual := f.snapshotSignature(b, snapshot, err)
				if signature != "" {
					require.Equal(b, signature, actual)
				}
				signature = actual
			}
			requestSignature, providerCalls := f.requestContract(b, 20)
			f.resetRequests()
			durations := make([]time.Duration, b.N)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				started := time.Now()
				snapshot, err := f.snapshot(context.Background(), filter)
				durations[i] = time.Since(started)
				b.StopTimer()
				require.NoError(b, err)
				require.Equal(b, signature, f.snapshotSignature(b, snapshot, err))
				b.StartTimer()
			}
			b.StopTimer()
			measuredRequestSignature, measuredProviderCalls := f.requestContract(b, b.N)
			require.Equal(b, requestSignature, measuredRequestSignature)
			require.Equal(b, providerCalls, measuredProviderCalls)
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			b.ReportMetric(float64(durations[(len(durations)-1)/2].Nanoseconds()), "p50-ns/op")
			b.ReportMetric(float64(durations[(len(durations)*95+99)/100-1].Nanoseconds()), "p95-ns/op")
			b.ReportMetric(0, "sql-statements/op")
			b.ReportMetric(0, "transactions/op")
			b.ReportMetric(float64(providerCalls), "provider-calls/op")
			b.Logf("result_signature_sha256=%s request_contract_sha256=%s", signature, requestSignature)
		})
	}
}
