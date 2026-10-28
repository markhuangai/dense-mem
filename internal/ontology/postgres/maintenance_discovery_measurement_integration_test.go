//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

type discoveryWorkload struct {
	Name     string   `json:"name"`
	Groups   []int    `json:"receipt_source_counts"`
	Outcomes []string `json:"receipt_outcomes"`
}

func discoveryWorkloads() []discoveryWorkload {
	unique := discoveryWorkload{Name: "unique"}
	overlap := discoveryWorkload{Name: "overlap-64"}
	shared := discoveryWorkload{Name: "shared-5"}
	add := func(w *discoveryWorkload, count, times int, status string) {
		for range times {
			w.Groups, w.Outcomes = append(w.Groups, count), append(w.Outcomes, status)
		}
	}
	add(&unique, 1, 100, "organized")
	for _, size := range []int{9, 8, 5} {
		add(&overlap, size, 1, "organized")
	}
	add(&overlap, 3, 6, "organized")
	add(&overlap, 1, 35, "organized")
	add(&overlap, 3, 1, "ambiguous")
	add(&overlap, 2, 1, "ambiguous")
	add(&overlap, 1, 5, "ambiguous")
	add(&overlap, 3, 1, "failed")
	add(&overlap, 1, 12, "failed")
	add(&shared, 20, 5, "organized")
	return []discoveryWorkload{unique, overlap, shared}
}

func discoveryLog(t *testing.T, kind string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	t.Logf("ontology_discovery_%s %s", kind, encoded)
}

func discoverySourceHashes(t *testing.T) map[string]string {
	t.Helper()
	hashes := map[string]string{}
	for _, name := range []string{"maintenance_discovery.go", "maintenance_discovery_observation.go", "organization.go", "source_queries.go",
		"maintenance_discovery_fixture_test.go", "maintenance_discovery_measurement_integration_test.go", "maintenance_discovery_concurrency_integration_test.go", "maintenance_discovery_isolation_integration_test.go",
		"maintenance_markers.go", "maintenance_discovery_stats.go", "maintenance_coalescing_reference_test.go",
		"maintenance_coalescing_fixture_test.go", "maintenance_coalescing_measurement_integration_test.go"} {
		body, err := os.ReadFile(name)
		require.NoError(t, err)
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256(body))
	}
	return hashes
}

func (r *discoveryRecorder) metrics() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := map[string]int{"selected": 0, "refreshed": 0, "source_errors": 0, "started": 0, "completed": 0, "current": 0, "stale": 0, "errors": 0}
	identities := map[string]bool{}
	widths := map[string]int{}
	var duration, repeated time.Duration
	for _, event := range r.events {
		switch event.Phase {
		case "source_selected":
			counts["selected"]++
		case "source_finished":
			if event.Err == nil {
				counts["refreshed"]++
			} else {
				counts["source_errors"]++
			}
		case "receipt_started":
			counts["started"]++
		case "receipt_finished":
			counts["completed"]++
			if event.Err != nil {
				counts["errors"]++
			} else if event.Current {
				counts["current"]++
			} else {
				counts["stale"]++
			}
			identity := fmt.Sprintf("%s/%s/%d/%s/%s", event.Scope.TeamID, event.Scope.SpaceID, event.Scope.Generation, event.ReceiptID, event.OperationKey)
			if identities[identity] {
				repeated += event.Duration
			} else {
				widths[fmt.Sprintf("sources=%d,dependencies=%d", event.Sources, event.Dependencies)]++
			}
			identities[identity] = true
			duration += event.Duration
		}
	}
	return map[string]any{"calls": counts, "distinct_receipts": len(identities), "distinct_receipt_widths": widths,
		"validation_ms": float64(duration.Microseconds()) / 1000, "repeated_validation_ms": float64(repeated.Microseconds()) / 1000,
		"receipt_sql_statements": r.receiptQueries, "receipt_sql_ms": float64(r.receiptSQLTime.Microseconds()) / 1000}
}

func discoveryMarkerState(t *testing.T, x *discoveryFixture) map[string]any {
	t.Helper()
	var count, sequence int64
	var cursor string
	require.NoError(t, x.f.admin.Raw(`SELECT count(*),COALESCE(min(marker_sequence),0),COALESCE(min(cursor),'') FROM ontology_maintenance_markers
	 WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, x.f.team, x.f.space, x.f.generation).Row().Scan(&count, &sequence, &cursor))
	return map[string]any{"markers": count, "oldest_sequence": sequence, "cursor": cursor}
}

func TestOntologyMaintenanceRevalidationDiscoveryMeasurement(t *testing.T) {
	t.Run("coalescing-markers", testMaintenanceCoalescingMeasurement)
	workloads := discoveryWorkloads()
	encoded, err := json.Marshal(workloads)
	require.NoError(t, err)
	discoveryLog(t, "protocol", map[string]any{"fixture": "discovery-receipts-v1", "workloads_sha256": fmt.Sprintf("%x", sha256.Sum256(encoded)),
		"workloads": workloads, "source_files_sha256": discoverySourceHashes(t), "run_id": os.Getenv("DENSE_MEM_CI_PRECHECK_RUN_ID"),
		"image_digest": os.Getenv("DENSE_MEM_CI_PRECHECK_IMAGE_DIGEST"), "fresh_repetitions": 3, "warmup_pages": 2, "measured_pairs": 5,
		"provider": "deterministic HTTP fixture", "sql_timing": "GORM Trace excludes row decoding and transaction begin/commit; page and validator timers include their complete calls"})
	for _, workload := range workloads {
		for repeat := range 3 {
			t.Run(fmt.Sprintf("%s/repetition-%d", workload.Name, repeat+1), func(t *testing.T) {
				x := newDiscoveryFixture(t, workload.Groups, workload.Outcomes, false)
				require.Len(t, x.sources, 100)
				canonical, history := schedulerCanonical(t, x.f), revalidationReceiptHistory(t, x.f)
				queue, calls := x.queue(t), x.providerCalls()
				expected := map[string]int{}
				for i, size := range workload.Groups {
					expected[workload.Outcomes[i]] += size
				}
				actual := map[string]int{}
				for _, source := range queue {
					actual[source.Status]++
				}
				require.Equal(t, expected, actual)
				discoveryLog(t, "coverage", map[string]any{"workload": workload.Name, "repetition": repeat + 1, "phase": "before",
					"state": revalidationMeasurement(t, x.f, time.Now()), "provider_calls": calls, "canonical_sha256": canonical})
				page := func(observe bool) (map[string]any, int) {
					x.marker(t)
					before := discoveryMarkerState(t, x)
					store, ctx, recorder := x.recorder(context.Background(), observe)
					started := time.Now()
					err := store.discoverMaintenanceError(ctx, x.turn, ontology.MaintenancePageSize)
					elapsed := time.Since(started)
					require.NoError(t, err)
					after := discoveryMarkerState(t, x)
					require.EqualValues(t, 1, before["markers"])
					require.EqualValues(t, 0, after["markers"])
					require.Equal(t, queue, x.queue(t))
					var freshness map[string]any
					if observe {
						freshness = recorder.metrics()
						require.Equal(t, len(workload.Groups), freshness["distinct_receipts"])
						require.Equal(t, map[string]int{"selected": 100, "refreshed": 100, "source_errors": 0, "started": 100, "completed": 100, "current": 100, "stale": 0, "errors": 0}, freshness["calls"])
					} else {
						require.Empty(t, recorder.completed())
					}
					return map[string]any{"workload": workload.Name, "repetition": repeat + 1, "observation_enabled": observe,
						"page_ms": float64(elapsed.Microseconds()) / 1000, "sql_statements": recorder.queries, "sql_ms": float64(recorder.sqlTime.Microseconds()) / 1000,
						"freshness": freshness, "marker_before": before, "marker_after": after}, recorder.queries
				}
				page(false)
				page(true)
				for sample := range 5 {
					order := []bool{false, true}
					if (sample+repeat)%2 == 1 {
						order = []bool{true, false}
					}
					queries := 0
					for _, observe := range order {
						result, count := page(observe)
						if queries != 0 {
							require.Equal(t, queries, count, "observation must not add or omit SQL work")
						}
						queries = count
						result["sample"] = sample + 1
						discoveryLog(t, "measurement", result)
					}
				}
				require.Equal(t, calls, x.providerCalls())
				require.Equal(t, canonical, schedulerCanonical(t, x.f))
				require.Equal(t, history, revalidationReceiptHistory(t, x.f))
				discoveryLog(t, "coverage", map[string]any{"workload": workload.Name, "repetition": repeat + 1, "phase": "after",
					"state": revalidationMeasurement(t, x.f, time.Now()), "provider_calls": x.providerCalls(), "canonical_sha256": canonical, "receipts_unchanged": true})
			})
		}
	}
}
