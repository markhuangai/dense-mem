//go:build integration

package postgres

import (
	"context"
	"testing"

	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
)

func TestOntologyMaintenanceCoalescingPages(t *testing.T) {
	for _, shape := range []string{"overlap", "disjoint", "empty"} {
		t.Run(shape, func(t *testing.T) {
			x := incrementalDiscovery(t, 100, false)
			definitions := coalescingDefinitions(t, x, 20, shape)
			canonical, history, queue, calls := schedulerCanonical(t, x.f), assignmentHistory(t, x.f), x.queue(t), coalescingDispatchCount(t, x)
			enqueueCoalescingMarkers(t, x, definitions)
			result := coalescingPage(t, x, 100)
			want := 100
			if shape == "empty" {
				want = 0
			}
			require.Equal(t, want, result.Counts.RefreshedSources)
			matches := want
			if shape == "overlap" {
				matches *= 20
			}
			require.Equal(t, matches, result.Counts.EnumeratedMatches)
			require.Equal(t, 20, result.Counts.CompletedMarkers)
			require.Zero(t, result.Counts.Requeues)
			require.Empty(t, coalescingMarkerCursors(t, x))
			require.Equal(t, queue, x.queue(t))
			require.Equal(t, canonical, schedulerCanonical(t, x.f))
			require.Equal(t, history, assignmentHistory(t, x.f))
			require.Equal(t, calls, coalescingDispatchCount(t, x))
		})
	}
}

func TestOntologyMaintenanceCoalescingCursorAndGroupBounds(t *testing.T) {
	x := incrementalDiscovery(t, 100, false)
	definitions := coalescingDefinitions(t, x, 21, "overlap")
	canonical, history, queue := schedulerCanonical(t, x.f), assignmentHistory(t, x.f), x.queue(t)
	enqueueCoalescingMarkers(t, x, definitions)
	ordered := discoverySortedSources(x.sources)
	ahead := ontology.SourceKey(ordered[49])
	require.NoError(t, x.f.admin.Exec(`UPDATE topic_maintenance_markers SET cursor=? WHERE team_id=?::uuid AND anchor_id=?`, ahead, x.f.team, definitions[0].ID).Error)
	first := coalescingPage(t, x, 25)
	require.Equal(t, 25, first.Counts.RefreshedSources)
	require.Zero(t, first.Counts.CompletedMarkers)
	cursors := coalescingMarkerCursors(t, x)
	require.Len(t, cursors, 21)
	require.Equal(t, ahead, cursors[definitions[0].ID], "a marker beyond the covered prefix cannot move backward")
	for _, definition := range definitions[1:20] {
		require.Equal(t, ontology.SourceKey(ordered[24]), cursors[definition.ID])
	}
	require.Empty(t, cursors[definitions[20].ID], "the twenty-first marker is outside this group")
	restarted := NewStore(x.f.app, x.f.rls)
	second, err := restarted.DiscoverMaintenance(context.Background(), x.turn, 100)
	require.NoError(t, err)
	require.Equal(t, 75, second.Counts.RefreshedSources)
	require.Zero(t, second.Counts.FreshnessInvocations)
	require.Zero(t, second.Counts.Requeues)
	require.Equal(t, 20, second.Counts.CompletedMarkers)
	require.Equal(t, map[string]string{definitions[20].ID: ""}, coalescingMarkerCursors(t, x))
	last := coalescingPage(t, x, 100)
	require.Equal(t, 100, last.Counts.RefreshedSources)
	require.Equal(t, 1, last.Counts.CompletedMarkers)
	require.Empty(t, coalescingMarkerCursors(t, x))
	require.Equal(t, queue, x.queue(t))
	require.Equal(t, canonical, schedulerCanonical(t, x.f))
	require.Equal(t, history, assignmentHistory(t, x.f))
}

func TestOntologyMaintenanceCoalescingPreservesNonDefinitionOrder(t *testing.T) {
	x := incrementalDiscovery(t, 2, false)
	definitions := coalescingDefinitions(t, x, 3, "overlap")
	enqueueCoalescingMarkers(t, x, definitions[:1])
	source := maintenanceEvidence(t, x.f, "Non-definition boundary")
	enqueueCoalescingMarkers(t, x, definitions[1:])
	first := coalescingPage(t, x, 100)
	require.Equal(t, 1, first.Counts.CompletedMarkers)
	require.Equal(t, 2, first.Counts.RefreshedSources)
	cursors := coalescingMarkerCursors(t, x)
	require.Contains(t, cursors, source.ID)
	for _, definition := range definitions[1:] {
		require.Contains(t, cursors, definition.ID)
		require.Empty(t, cursors[definition.ID])
	}
	var remainingMarkers int
	require.NoError(t, x.f.admin.Raw(`SELECT count(*) FROM topic_maintenance_markers WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, x.f.team, x.f.space, x.f.generation).Row().Scan(&remainingMarkers))
	second := coalescingPage(t, x, 100)
	require.Equal(t, remainingMarkers, second.Counts.CompletedMarkers)
	require.Equal(t, 3, second.Counts.RefreshedSources)
	require.Zero(t, second.Counts.FreshnessInvocations)
	require.Empty(t, coalescingMarkerCursors(t, x))
	state := revalidationQueue(t, x.f, source)
	require.Equal(t, "pending", state.Status)
}
