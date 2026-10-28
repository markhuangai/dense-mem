//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testMaintenanceCoalescingMeasurement(t *testing.T) {
	discoveryLog(t, "coalescing_protocol", map[string]any{
		"fixture": "coalescing-markers-v3", "sequential_base": "09d16398005f0a8ba3ca0fe6c31e08c4341eca9f",
		"source_files_sha256": discoverySourceHashes(t), "shapes": []string{"overlap", "disjoint"},
		"markers": 20, "sources": 100, "receipt_source_counts": []int{20, 20, 20, 20, 20},
		"pairs": 5, "fresh_database_per_sample": true, "warmup_drains": 0,
		"setup":    "drain source-creation notifications before real receipts; ANALYZE after receipts and after definition setup",
		"provider": "deterministic HTTP fixture", "run_id": os.Getenv("DENSE_MEM_CI_PRECHECK_RUN_ID"),
		"image_digest": os.Getenv("DENSE_MEM_CI_PRECHECK_IMAGE_DIGEST"),
		"timing":       "sum of discovery calls including commit; fixture setup and between-page completion queries excluded",
	})
	for _, shape := range []string{"overlap", "disjoint"} {
		t.Run(shape, func(t *testing.T) {
			samples := map[bool][]float64{}
			for pair := range 5 {
				order := []bool{false, true}
				if pair%2 == 1 {
					order = []bool{true, false}
				}
				for _, combined := range order {
					t.Run(fmt.Sprintf("pair-%d/combined-%t", pair+1, combined), func(t *testing.T) {
						report := map[string]any{"shape": shape, "pair": pair + 1, "combined": combined}
						defer func() {
							report["failed"] = t.Failed()
							discoveryLog(t, "coalescing_measurement", report)
						}()
						setup := time.Now()
						x := newDiscoveryFixtureWithSetup(t, []int{20, 20, 20, 20, 20}, nil, false, true)
						definitions := coalescingDefinitions(t, x, 20, shape)
						require.NoError(t, x.f.admin.Exec(`ANALYZE`).Error)
						report["fixture_setup_ms"] = float64(time.Since(setup)) / float64(time.Millisecond)
						canonical, history, queue, calls := schedulerCanonical(t, x.f), revalidationReceiptHistory(t, x.f), x.queue(t), x.providerCalls()
						enqueueCoalescingMarkers(t, x, definitions)
						store, ctx, recorder := x.recorder(context.Background(), true)
						var elapsed time.Duration
						pages := 0
						for ; pages < 20; pages++ {
							started := time.Now()
							var err error
							if combined {
								_, err = store.DiscoverMaintenance(ctx, x.turn, 100)
							} else {
								err = store.discoverSequentialMaintenance(ctx, x.turn, 100)
							}
							elapsed += time.Since(started)
							require.NoError(t, err)
							if len(coalescingMarkerCursors(t, x)) == 0 {
								pages++
								break
							}
						}
						require.Empty(t, coalescingMarkerCursors(t, x))
						metrics := recorder.metrics()
						expectedCalls := 100
						if !combined && shape == "overlap" {
							expectedCalls = 2000
						}
						report["discovery_ms"], report["pages"], report["freshness"] = float64(elapsed)/float64(time.Millisecond), pages, metrics
						report["sql_statements"], report["sql_ms"] = recorder.queries, float64(recorder.sqlTime)/float64(time.Millisecond)
						report["canonical_before"], report["canonical_after"] = canonical, schedulerCanonical(t, x.f)
						report["receipts_unchanged"], report["provider_calls_added"] = history == revalidationReceiptHistory(t, x.f), x.providerCalls()-calls
						require.Equal(t, expectedCalls, metrics["calls"].(map[string]int)["started"])
						require.Equal(t, expectedCalls, metrics["calls"].(map[string]int)["completed"])
						require.Equal(t, expectedCalls, metrics["calls"].(map[string]int)["current"])
						require.Equal(t, 5, metrics["distinct_receipts"])
						require.Equal(t, queue, x.queue(t))
						require.Equal(t, canonical, report["canonical_after"])
						require.Equal(t, true, report["receipts_unchanged"])
						require.EqualValues(t, 0, report["provider_calls_added"])
						samples[combined] = append(samples[combined], float64(elapsed)/float64(time.Millisecond))
					})
				}
			}
			medians := map[bool]float64{}
			for _, combined := range []bool{false, true} {
				require.Len(t, samples[combined], 5, "all predeclared pairs must succeed")
				sorted := append([]float64(nil), samples[combined]...)
				sort.Float64s(sorted)
				medians[combined] = sorted[2]
			}
			discoveryLog(t, "coalescing_comparison", map[string]any{"shape": shape, "sequential_ms": samples[false], "combined_ms": samples[true],
				"sequential_median_ms": medians[false], "combined_median_ms": medians[true], "change_percent": (medians[true]/medians[false] - 1) * 100})
			if shape == "overlap" {
				require.Less(t, medians[true], medians[false], "combined overlapping work must have lower median processing time")
			}
		})
	}
}
