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

	"github.com/google/uuid"
	assignment "github.com/markhuangai/dense-mem/internal/topicassignment/contract"
	"gorm.io/gorm"

	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
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
	require.NoError(t, x.f.admin.Raw(`SELECT count(*),COALESCE(min(marker_sequence),0),COALESCE(min(cursor),'') FROM topic_maintenance_markers
	 WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, x.f.team, x.f.space, x.f.generation).Row().Scan(&count, &sequence, &cursor))
	return map[string]any{"markers": count, "oldest_sequence": sequence, "cursor": cursor}
}

// The fixture begins after foreground publication and the scheduler's first successful assignment.
func incrementalDiscovery(t *testing.T, count int, supported bool) *discoveryFixture {
	t.Helper()
	x := &discoveryFixture{f: newOrganizationFixture(t), topic: testTopic("postgresql")}
	f := x.f
	if supported {
		x.support = discoveryEvidence(t, f, 9999)
		x.topic.Sources = []ontology.SourceDependency{f.source(t, x.support)}
	}
	revalidationPublish(t, f, ontology.Change{Record: x.topic})
	x.addSources(t, count)
	x.claimTurn(t)
	ctx := context.Background()
	for _, handle := range x.sources {
		snapshots, err := f.store.ReadSources(ctx, f.team, []ontology.SourceHandle{handle})
		require.NoError(t, err)
		text := ontology.SourceDisplayText(snapshots[0])
		input := assignment.Scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation, OperationID: uuid.NewString()}
		item := assignment.Publication{EvidenceID: handle.ID, Outcome: assignment.Outcome{Method: "generation", InputHash: testHash(text), Text: text,
			Matches: []assignment.Match{{ID: x.topic.ID, Version: 1, Quote: text}}}}
		require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
			return f.store.PublishAssignmentsTx(ctx, tx, input, []assignment.Publication{item})
		}))
	}
	x.drain(t)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
		result := tx.Exec(`UPDATE topic_maintenance_sources s SET status='organized', last_assignment_input_hash=o.input_hash
			FROM topic_assignment_outcomes o WHERE s.team_id=? AND s.shared_space_id=? AND s.space_generation=?
			AND (s.team_id,s.shared_space_id,s.space_generation,s.source_id)=(o.team_id,o.shared_space_id,o.space_generation,o.evidence_id::text)
			AND o.status='assigned'`, f.team, f.space, f.generation)
		if result.Error != nil {
			return result.Error
		}
		require.EqualValues(t, count, result.RowsAffected)
		return nil
	}))
	return x
}

func assignmentHistory(t *testing.T, f *ontologyFixture) string {
	t.Helper()
	var history string
	require.NoError(t, f.admin.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(o) ORDER BY outcome_id)::text,'[]')
		FROM topic_assignment_outcomes o WHERE team_id=? AND shared_space_id=? AND space_generation=?`, f.team, f.space, f.generation).Row().Scan(&history))
	return history
}

func TestOntologyMaintenanceRevalidationDiscoveryMeasurement(t *testing.T) {
	for _, width := range []int{1, 8, 20} {
		t.Run(fmt.Sprintf("sources-%d", width), func(t *testing.T) {
			x := incrementalDiscovery(t, width, false)
			f := x.f
			unchanged := x.queue(t)
			history, canonical := assignmentHistory(t, f), schedulerCanonical(t, f)
			require.NoError(t, f.admin.Exec(`UPDATE topic_maintenance_teams SET next_discovery_at=now()-interval '1 second' WHERE team_id=? AND shared_space_id=? AND space_generation=?`, f.team, f.space, f.generation).Error)
			store, ctx, recorder := x.recorder(context.Background(), true)
			visited := 0
			for visited < width {
				page, err := store.DiscoverMaintenance(ctx, x.turn, 3)
				require.NoError(t, err)
				require.True(t, page.Committed)
				require.Positive(t, page.Counts.InitialSources)
				require.LessOrEqual(t, page.Counts.InitialSources, 3)
				require.Zero(t, page.Counts.RefreshedSources)
				require.Zero(t, page.Counts.FreshnessInvocations)
				require.Zero(t, page.Counts.Requeues)
				visited += page.Counts.InitialSources
			}
			require.Equal(t, width, visited)
			require.Equal(t, unchanged, x.queue(t))
			require.Empty(t, recorder.completed())
			x.marker(t)
			seen := 0
			for seen < width {
				page, err := store.DiscoverMaintenance(ctx, x.turn, 3)
				require.NoError(t, err)
				require.True(t, page.Committed)
				require.Positive(t, page.Counts.RefreshedSources)
				require.LessOrEqual(t, page.Counts.RefreshedSources, 3)
				require.Zero(t, page.Counts.FreshnessInvocations)
				require.Equal(t, page.Counts.RefreshedSources, page.Counts.Requeues)
				seen += page.Counts.RefreshedSources
			}
			for i, row := range x.queue(t) {
				require.Equal(t, "pending", row.Status)
				require.Equal(t, unchanged[i].Revision+1, row.Revision)
			}
			require.Equal(t, history, assignmentHistory(t, f))
			require.Equal(t, canonical, schedulerCanonical(t, f))
			require.Empty(t, recorder.completed())
			discoveryLog(t, "measurement", map[string]any{"sources": width, "periodic_sources": visited, "requeued_revisions": seen, "receipt_checks": 0, "sql_statements": recorder.queries, "sql_ms": float64(recorder.sqlTime.Microseconds()) / 1000})
		})
	}
}
