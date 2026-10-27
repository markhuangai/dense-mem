//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMaintenanceRevalidationDiscoveryIsolation(t *testing.T) {
	x := newDiscoveryFixture(t, []int{2}, nil, false)
	f := x.f
	other := maintenanceOtherTeam(t, f)
	foreign := other.organizationEvidence(t, 0, "PostgreSQL discovery foreign evidence.", nil)
	privateIDs := []string{}
	for _, credential := range f.credentials {
		ctx := requestctx.WithAllowedSpaces(context.Background(), []domain.MemorySpaceAccess{{ID: credential.MemorySpaceID, Kind: domain.MemorySpacePrivate, Generation: credential.MemorySpaceGeneration}})
		result, err := f.knowledge.CreateIngestForTest(ctx, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: credential.ID.String(), SpaceID: credential.MemorySpaceID.String(),
			SpaceGeneration: credential.MemorySpaceGeneration, Evidence: []knowledge.EvidenceInput{{Content: "PostgreSQL discovery private evidence."}}})
		require.NoError(t, err)
		privateIDs = append(privateIDs, result.Evidence[0].FragmentID)
	}
	canonical, history, queue := schedulerCanonical(t, f, other), revalidationReceiptHistory(t, f), x.queue(t)
	x.marker(t)
	marker := discoveryMarkerState(t, x)
	for _, owner := range []int{0, 1} {
		require.NoError(t, f.store.withScope(f.actor(owner, "member"), f.team, true, func(tx *gorm.DB, fence scope) error {
			handles, err := maintenanceMarkerSources(tx, fence, maintenanceMarker{AnchorKind: "ontology_record_heads", AnchorID: x.topic.ID, TargetKind: "definition", TargetID: x.topic.ID}, 100)
			require.NoError(t, err)
			require.ElementsMatch(t, x.sources, handles)
			return nil
		}))
	}
	require.ErrorIs(t, f.store.DiscoverMaintenance(other.actor(0, "manager"), x.turn, 100), ontology.ErrUnauthorized)
	wrong := x.turn
	wrong.TeamID, wrong.SpaceID, wrong.Generation = other.team, other.space, other.generation
	require.ErrorIs(t, f.store.DiscoverMaintenance(context.Background(), wrong, 100), ontology.ErrLeaseLost)
	require.Equal(t, marker, discoveryMarkerState(t, x))
	var leaked int
	require.NoError(t, f.rls.WithTeamProfileTx(other.actor(0, "member"), f.app, other.team, other.owners[0], func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM ontology_assessments WHERE team_id=?::uuid`, f.team).Row().Scan(&leaked)
	}))
	require.Zero(t, leaked)
	store, ctx, recorder := x.recorder(context.Background(), true)
	ordered := discoverySortedSources(x.sources)
	require.NoError(t, store.DiscoverMaintenance(ctx, x.turn, 1))
	afterFirst := discoveryMarkerState(t, x)
	require.EqualValues(t, 1, afterFirst["markers"])
	require.Equal(t, ontology.SourceKey(ordered[0]), afterFirst["cursor"])
	require.NoError(t, store.DiscoverMaintenance(ctx, x.turn, 1))
	require.EqualValues(t, 0, discoveryMarkerState(t, x)["markers"])
	checks := recorder.completed()
	require.Len(t, checks, 2)
	for i, check := range checks {
		require.Equal(t, ordered[i], check.Source)
		require.Equal(t, scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation}, check.Scope)
		require.True(t, check.Current)
		require.NoError(t, check.Err)
	}
	for _, id := range append(privateIDs, foreign.ID) {
		var count int
		require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND source_id=?`, f.team, id).Row().Scan(&count))
		require.Zero(t, count)
	}
	require.Equal(t, queue, x.queue(t))
	require.Equal(t, canonical, schedulerCanonical(t, f, other))
	require.Equal(t, history, revalidationReceiptHistory(t, f))
	discoveryLog(t, "isolation", map[string]any{"abc_owners": true, "private_and_foreign_excluded": true, "pagination_pages": 2,
		"canonical_unchanged": true, "receipts_unchanged": true, "freshness": recorder.metrics()})
}

func TestOntologyMaintenanceRevalidationDiscoveryLockCancellation(t *testing.T) {
	x := newDiscoveryFixture(t, []int{2}, nil, false)
	x.marker(t)
	marker, queue := discoveryMarkerState(t, x), x.queue(t)
	canonical, history := schedulerCanonical(t, x.f), revalidationReceiptHistory(t, x.f)
	locked, release, stopped := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	finished := make(chan struct{})
	releaseLock := sync.OnceFunc(func() { close(release) })
	lockCtx, cancelLock := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(func() {
		cancelLock()
		releaseLock()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("lock holder did not stop")
		}
	})
	go func() {
		defer close(finished)
		stopped <- x.f.rls.WithTeamTx(lockCtx, x.f.app, x.f.team, func(tx *gorm.DB) error {
			if err := tx.Exec(`SELECT 1 FROM ontology_maintenance_teams WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? FOR UPDATE`, x.f.team, x.f.space, x.f.generation).Error; err != nil {
				return err
			}
			close(locked)
			select {
			case <-release:
				return nil
			case <-lockCtx.Done():
				return lockCtx.Err()
			}
		})
	}()
	select {
	case <-locked:
	case err := <-stopped:
		t.Fatalf("lock holder failed: %v", err)
	case <-lockCtx.Done():
		t.Fatal("lock holder did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- x.f.store.DiscoverMaintenance(ctx, x.turn, 100) }()
	require.Eventually(t, func() bool {
		var waiting bool
		err := x.f.admin.Raw(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
		 AND wait_event_type='Lock' AND query LIKE '%discovery_kind,discovery_cursor%' AND cardinality(pg_blocking_pids(pid))>0)`).Row().Scan(&waiting)
		return err == nil && waiting
	}, 3*time.Second, 10*time.Millisecond)
	cancel()
	err := <-done
	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded), "lock wait must honor cancellation: %v", err)
	require.Equal(t, marker, discoveryMarkerState(t, x))
	require.Equal(t, queue, x.queue(t))
	require.Equal(t, canonical, schedulerCanonical(t, x.f))
	require.Equal(t, history, revalidationReceiptHistory(t, x.f))
	// Release the transaction before proving that a later discovery call can acquire the same row.
	cancelLock()
	require.Error(t, <-stopped)
	require.NoError(t, x.f.store.DiscoverMaintenance(context.Background(), x.turn, 100))
	require.EqualValues(t, 0, discoveryMarkerState(t, x)["markers"])
	discoveryLog(t, "lock_cancellation", map[string]any{"lock_wait_observed": true, "rolled_back": true, "resumed": true})
}
