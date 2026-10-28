//go:build integration

package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func holdCoalescingMarker(t *testing.T, x *discoveryFixture, id string) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	locked, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	unlock := sync.OnceFunc(func() { close(release) })
	go func() {
		defer close(stopped)
		finished <- x.f.rls.WithTeamTx(ctx, x.f.app, x.f.team, func(tx *gorm.DB) error {
			if err := tx.Exec(`SELECT 1 FROM ontology_maintenance_markers WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND anchor_id=? FOR UPDATE`, x.f.team, x.f.space, x.f.generation, id).Error; err != nil {
				return err
			}
			close(locked)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	t.Cleanup(func() {
		cancel()
		unlock()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("marker lock holder did not stop")
		}
	})
	select {
	case <-locked:
	case err := <-finished:
		t.Fatalf("marker lock failed: %v", err)
	case <-ctx.Done():
		t.Fatal("marker lock not acquired")
	}
	return func() { unlock(); require.NoError(t, <-finished) }
}

func TestOntologyMaintenanceCoalescingLockedPrefix(t *testing.T) {
	for _, blocked := range []int{0, 1} {
		t.Run(fmt.Sprint(blocked), func(t *testing.T) {
			x := newDiscoveryFixture(t, []int{2}, nil, false)
			definitions := coalescingDefinitions(t, x, 3, "overlap")
			canonical, history, queue := schedulerCanonical(t, x.f), revalidationReceiptHistory(t, x.f), x.queue(t)
			enqueueCoalescingMarkers(t, x, definitions)
			unlock := holdCoalescingMarker(t, x, definitions[blocked].ID)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result, err := x.f.store.DiscoverMaintenance(ctx, x.turn, 100)
			require.NoError(t, err, "a held marker must not stall a claimed prefix")
			require.True(t, result.Committed)
			require.Equal(t, blocked, result.Counts.CompletedMarkers)
			require.Equal(t, blocked*2, result.Counts.FreshnessInvocations)
			cursors := coalescingMarkerCursors(t, x)
			require.Len(t, cursors, 3-blocked)
			for _, definition := range definitions[blocked:] {
				require.Contains(t, cursors, definition.ID)
				require.Empty(t, cursors[definition.ID])
			}
			unlock()
			x.drain(t)
			require.Empty(t, coalescingMarkerCursors(t, x))
			require.Equal(t, canonical, schedulerCanonical(t, x.f))
			require.Equal(t, history, revalidationReceiptHistory(t, x.f))
			require.Equal(t, queue, x.queue(t))
		})
	}
}

func TestOntologyMaintenanceCoalescingConcurrentPublicationRetained(t *testing.T) {
	x := newDiscoveryFixture(t, []int{2}, nil, false)
	definitions := coalescingDefinitions(t, x, 3, "overlap")
	canonical, history := schedulerCanonical(t, x.f), revalidationReceiptHistory(t, x.f)
	enqueueCoalescingMarkers(t, x, definitions)
	page, err := x.f.store.ListRecords(context.Background(), x.f.team, "", "", 1)
	require.NoError(t, err)
	paused := pauseDiscovery(t, x, "source_finished", 100)
	updated := definitions[1]
	updated.Definition.Description = "PostgreSQL revised definition"
	input := testPublication("coalesced-concurrent-publication", page.Revision, ontology.Change{ExpectedVersion: 1, Record: updated})
	ctx, cancel := context.WithTimeout(x.f.actor(0, "manager"), 15*time.Second)
	defer cancel()
	finished, stopped := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(stopped)
		_, err := x.f.store.PublishManager(ctx, x.f.team, input)
		finished <- err
	}()
	t.Cleanup(func() { cancel(); paused.release(); <-stopped })
	require.False(t, discoveryWriterState(t, x.f, finished), "publication must encounter the captured marker claim")
	paused.release()
	require.NoError(t, <-paused.done)
	require.NoError(t, <-finished)
	result := <-paused.result
	require.Equal(t, 3, result.Counts.CompletedMarkers)
	cursors := coalescingMarkerCursors(t, x)
	require.Contains(t, cursors, updated.ID, "publication waiting behind deletion must recreate work")
	require.Empty(t, cursors[updated.ID])
	x.drain(t)
	require.Empty(t, coalescingMarkerCursors(t, x))
	require.Equal(t, canonical, schedulerCanonical(t, x.f))
	require.Equal(t, history, revalidationReceiptHistory(t, x.f))
}
