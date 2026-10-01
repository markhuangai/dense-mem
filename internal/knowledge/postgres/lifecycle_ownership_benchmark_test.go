//go:build integration

package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func BenchmarkLifecycleOwnership(b *testing.B) {
	ctx := context.Background()
	for _, workload := range []string{"support_recomputation", "evidence_retraction", "correction_preview"} {
		b.Run(workload, func(b *testing.B) {
			f := newLifecycleOwnershipFixture(b)
			counters := &predicateOwnershipBenchmarkCounters{}
			store := NewStore(newPredicateOwnershipCountedDB(f.appDB, counters), f.rls, ConflictRuntimeConfig{})
			run := func() (string, error) {
				switch workload {
				case "support_recomputation":
					result, err := store.ApplyRelationshipSupportDecision(ctx, f.supportInput())
					if err != nil {
						return "", err
					}
					return fmt.Sprintf("%s:%s:support=%d:groups=%d", result.FromStatus, result.ToStatus, result.SupportCount, result.SourceGroupCount), nil
				case "evidence_retraction":
					result, err := store.RetractEvidence(ctx, f.retractInput())
					if err != nil {
						return "", err
					}
					return fmt.Sprintf("%s:evidence=%d:affected=%d:pending=%d:active=%d:replay=%t", result.ProcessingState, len(result.RetractedEvidenceIDs), result.AffectedRelationshipCount, result.PendingRelationshipCount, result.RetainedActiveRelationshipCount, result.Existing), nil
				default:
					plan, err := store.PlanRelationshipCorrectionEmbeddings(ctx, f.correctionInput())
					if err != nil {
						return "", err
					}
					if len(plan.Documents) != 2 {
						return "", fmt.Errorf("correction preview returned %d documents, want 2", len(plan.Documents))
					}
					hashes := []string{plan.Documents[0].DocumentHash, plan.Documents[1].DocumentHash}
					return strings.Join(hashes, ","), nil
				}
			}
			verify := func(before lifecycleOwnershipState, result string) string {
				after := f.state(b)
				want := before
				if workload != "correction_preview" {
					want.Status, want.SupportCount = "pending_evidence", 0
					want.Version++
					want.SupportDecisions++
					want.Transitions++
				}
				switch workload {
				case "support_recomputation":
					require.Equal(b, "active:pending_evidence:support=0:groups=0", result)
				case "evidence_retraction":
					require.Equal(b, "completed:evidence=1:affected=1:pending=1:active=0:replay=false", result)
					want.LifecycleEvents++
				}
				require.Equal(b, want, after, "the workload must exercise fresh authoritative state")
				return fmt.Sprintf("%s|%+v", result, after)
			}
			for range 20 {
				f.reset(b)
				before := f.state(b)
				result, err := run()
				require.NoError(b, err)
				verify(before, result)
			}
			f.reset(b)
			before := f.state(b)
			capture := newPredicateOwnershipQueryCapture()
			counters.reset()
			counters.capture = capture
			result, err := run()
			require.NoError(b, err)
			counters.capture = nil
			operationCounts := counters.snapshot()
			wantSignature := verify(before, result)
			durations := make([]time.Duration, b.N)
			var totals predicateOwnershipBenchmarkCount
			b.ReportAllocs()
			b.ResetTimer()
			b.StopTimer()
			for index := range b.N {
				f.reset(b)
				before := f.state(b)
				counters.reset()
				b.StartTimer()
				started := time.Now()
				result, err := run()
				durations[index] = time.Since(started)
				b.StopTimer()
				require.NoError(b, err)
				counts := counters.snapshot()
				require.Equal(b, operationCounts, counts)
				require.Equal(b, wantSignature, verify(before, result))
				totals.statements += counts.statements
				totals.transactions += counts.transactions
				totals.commits += counts.commits
				totals.rollbacks += counts.rollbacks
			}
			require.Positive(b, totals.statements)
			require.Positive(b, totals.transactions)
			require.Equal(b, totals.transactions, totals.commits+totals.rollbacks)
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			b.ReportMetric(float64(durations[(len(durations)-1)/2].Nanoseconds()), "p50-ns/op")
			b.ReportMetric(float64(durations[(len(durations)*95+99)/100-1].Nanoseconds()), "p95-ns/op")
			b.ReportMetric(float64(totals.statements)/float64(b.N), "sql-statements/op")
			b.ReportMetric(float64(totals.transactions)/float64(b.N), "transactions/op")
			b.ReportMetric(float64(totals.commits+totals.rollbacks)/float64(b.N), "transaction-completions/op")
			b.ReportMetric(0, "provider-calls/op")
			b.Logf("result_signature=%s", wantSignature)
			b.Logf("query_contract_sha256=%s statements=%d", capture.digest(), capture.statements)
		})
	}
}
