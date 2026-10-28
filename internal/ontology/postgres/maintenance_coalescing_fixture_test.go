//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func coalescingDefinitions(t *testing.T, x *discoveryFixture, count int, shape string) []ontology.Record {
	t.Helper()
	definitions := make([]ontology.Record, count)
	changes := make([]ontology.Change, count)
	for i := range definitions {
		definitions[i] = testTopic(fmt.Sprintf("coalescing-bucket-%02d", i))
		switch shape {
		case "overlap":
			definitions[i].Definition.Description = "PostgreSQL"
		case "disjoint":
			var words []string
			for j := i * len(x.sources) / count; j < (i+1)*len(x.sources)/count; j++ {
				words = append(words, fmt.Sprintf("%04d", j+1))
			}
			definitions[i].Definition.Description = strings.Join(words, " ")
		case "empty":
		default:
			t.Fatalf("unknown marker shape %s", shape)
		}
		changes[i] = ontology.Change{Record: definitions[i]}
	}
	revalidationPublish(t, x.f, changes...)
	for i, definition := range definitions {
		expected := x.sources
		if shape == "disjoint" {
			expected = x.sources[i*len(x.sources)/count : (i+1)*len(x.sources)/count]
		}
		if shape == "empty" {
			expected = nil
		}
		require.NoError(t, x.f.store.withScope(context.Background(), x.f.team, false, func(tx *gorm.DB, fence scope) error {
			handles, err := maintenanceMarkerSources(tx, fence, maintenanceMarker{AnchorKind: "ontology_record_heads", AnchorID: definition.ID, TargetKind: "definition", TargetID: definition.ID}, ontology.MaintenancePageSize+1)
			require.NoError(t, err)
			require.ElementsMatch(t, expected, handles, "effective marker selection must match the declared workload")
			return nil
		}))
	}
	x.drain(t)
	for _, source := range x.queue(t) {
		require.Equal(t, "organized", source.Status)
	}
	return definitions
}

func enqueueCoalescingMarkers(t *testing.T, x *discoveryFixture, definitions []ontology.Record) {
	t.Helper()
	require.NoError(t, x.f.rls.WithTeamTx(context.Background(), x.f.app, x.f.team, func(tx *gorm.DB) error {
		for _, definition := range definitions {
			if err := tx.Exec(`SELECT dense_mem_enqueue_ontology_marker(?::uuid,?::uuid,?,'ontology_record_heads',?,'definition',?)`,
				x.f.team, x.f.space, x.f.generation, definition.ID, definition.ID).Error; err != nil {
				return err
			}
		}
		return nil
	}))
}

func coalescingMarkerCursors(t *testing.T, x *discoveryFixture) map[string]string {
	t.Helper()
	rows, err := x.f.admin.Raw(`SELECT anchor_id,cursor FROM ontology_maintenance_markers WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, x.f.team, x.f.space, x.f.generation).Rows()
	require.NoError(t, err)
	defer rows.Close()
	cursors := map[string]string{}
	for rows.Next() {
		var id, cursor string
		require.NoError(t, rows.Scan(&id, &cursor))
		cursors[id] = cursor
	}
	require.NoError(t, rows.Err())
	return cursors
}

func coalescingPage(t *testing.T, x *discoveryFixture, limit int) ontology.MaintenanceDiscoveryResult {
	t.Helper()
	result, err := x.f.store.DiscoverMaintenance(context.Background(), x.turn, limit)
	require.NoError(t, err)
	require.True(t, result.Committed)
	require.Equal(t, result.Attempted, result.Counts)
	require.Positive(t, result.Duration)
	require.Zero(t, result.Counts.InitialSources)
	return result
}

func enqueueDiscoverySupportMarker(t *testing.T, x *discoveryFixture) {
	t.Helper()
	require.NoError(t, x.f.store.withScope(context.Background(), x.f.team, false, func(tx *gorm.DB, fence scope) error {
		return enqueueMaintenanceDependencies(tx, fence, x.support)
	}))
}
