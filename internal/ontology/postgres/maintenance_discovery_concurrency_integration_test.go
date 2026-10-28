//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type pausedDiscovery struct {
	recorder *discoveryRecorder
	release  func()
	cancel   context.CancelFunc
	done     <-chan error
}

func pauseDiscovery(t *testing.T, x *discoveryFixture, phase string, limit int) pausedDiscovery {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	store, ctx, recorder := x.recorder(ctx, true)
	arrived, resume, stopped := make(chan maintenanceDiscoveryEvent, 1), make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	var reached atomic.Bool
	release := sync.OnceFunc(func() { close(resume) })
	recorder.after = func(event maintenanceDiscoveryEvent) {
		if event.Phase == phase && reached.CompareAndSwap(false, true) {
			arrived <- event
			select {
			case <-resume:
			case <-ctx.Done():
			}
		}
	}
	t.Cleanup(func() {
		cancel()
		release()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("discovery did not stop after cancellation")
		}
	})
	go func() {
		defer close(stopped)
		finished <- store.DiscoverMaintenance(ctx, x.turn, limit)
	}()
	select {
	case event := <-arrived:
		require.NoError(t, event.Err)
		if phase == "receipt_finished" {
			require.True(t, event.Current)
		}
	case err := <-finished:
		t.Fatalf("discovery missed %s boundary: %v", phase, err)
	case <-ctx.Done():
		t.Fatalf("discovery did not reach %s boundary: %v", phase, ctx.Err())
	}
	return pausedDiscovery{recorder: recorder, release: release, cancel: cancel, done: finished}
}

func discoveryWriterState(t *testing.T, f *ontologyFixture, done <-chan error) (committed bool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case err := <-done:
			require.NoError(t, err)
			return true
		case <-ticker.C:
			var blocked bool
			require.NoError(t, f.admin.Raw(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			 WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND cardinality(pg_blocking_pids(pid))>0)`).Row().Scan(&blocked))
			if blocked {
				return false
			}
		case <-deadline.C:
			t.Fatal("writer neither committed nor reached a PostgreSQL lock boundary")
			return false
		}
	}
}

func TestOntologyMaintenanceRevalidationDiscoveryConcurrency(t *testing.T) {
	for _, change := range []string{"definition", "new_vocabulary", "source", "support", "override", "generation", "unrelated_vocabulary"} {
		t.Run(change, func(t *testing.T) {
			outcomes := []string{"organized"}
			if change == "new_vocabulary" {
				outcomes[0] = "ambiguous"
			}
			x := newDiscoveryFixture(t, []int{2}, outcomes, change == "support")
			f := x.f
			canonical, history, queue, calls := schedulerCanonical(t, f), revalidationReceiptHistory(t, f), x.queue(t), x.providerCalls()
			page, err := f.store.ListRecords(context.Background(), f.team, "", "", 1)
			require.NoError(t, err)
			var write func(context.Context) error
			writerParent := f.actor(0, "manager")
			switch change {
			case "definition", "new_vocabulary", "unrelated_vocabulary", "override":
				var changes []ontology.Change
				if change == "definition" {
					topic := x.topic
					topic.Definition = &ontology.Definition{Key: topic.Definition.Key, Label: topic.Definition.Label, Description: "A revised required PostgreSQL definition"}
					changes = []ontology.Change{{ExpectedVersion: 1, Record: topic}}
				} else {
					topic := testTopic("postgresql-concurrent-alternative")
					changes = []ontology.Change{{Record: topic}}
					if change == "override" {
						override := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind,
							Override: &ontology.Override{Action: ontology.SetClassification, DefinitionID: topic.ID, Members: []ontology.SourceHandle{x.sources[0]}},
							Sources:  []ontology.SourceDependency{f.source(t, x.sources[0])}}
						changes = append(changes, ontology.Change{Record: override})
					}
				}
				input := testPublication("concurrent-"+change, page.Revision, changes...)
				write = func(ctx context.Context) error { _, err := f.store.PublishManager(ctx, f.team, input); return err }
			case "source", "support":
				source := x.sources[0]
				if change == "support" {
					source = x.support
				}
				writerParent = f.actor(1, "member")
				write = func(ctx context.Context) error {
					_, err := f.knowledge.RetractEvidence(ctx, knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[1], EvidenceIDs: []string{source.ID},
						Reason: "concurrent discovery withdrawal", IdempotencyKey: "discovery-withdrawal", RequestHash: testHash("discovery-withdrawal")})
					return err
				}
			case "generation":
				writerParent = context.Background()
				write = func(ctx context.Context) error {
					return f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
						return tx.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE team_id=?::uuid AND id=?::uuid`, f.team, f.space).Error
					})
				}
			}
			x.marker(t)
			paused := pauseDiscovery(t, x, "receipt_finished", 2)
			writerCtx, cancelWriter := context.WithTimeout(writerParent, 15*time.Second)
			defer cancelWriter()
			written, writerStopped := make(chan error, 1), make(chan struct{})
			go func() { defer close(writerStopped); written <- write(writerCtx) }()
			defer func() { cancelWriter(); paused.release(); <-writerStopped }()
			committed := discoveryWriterState(t, f, written)
			var afterWriter string
			if committed {
				afterWriter = schedulerCanonical(t, f)
			}
			paused.release()
			pageErr := <-paused.done
			if !committed {
				require.NoError(t, <-written)
			}
			if change == "generation" && committed {
				require.Error(t, pageErr)
				var postgresError *pgconn.PgError
				require.True(t, errors.As(pageErr, &postgresError), "generation fencing must remain a visible PostgreSQL rejection: %v", pageErr)
				require.Equal(t, "42501", postgresError.Code)
				require.Equal(t, queue, x.queue(t), "failed discovery must roll back source updates")
			} else {
				require.NoError(t, pageErr)
			}
			checks := paused.recorder.completed()
			if committed && change != "generation" {
				require.Len(t, checks, 2)
				require.True(t, checks[0].Current)
				require.Equal(t, change == "unrelated_vocabulary", checks[1].Current,
					"a later check of the same receipt must see committed relevant changes")
			}
			if afterWriter != "" {
				require.Equal(t, afterWriter, schedulerCanonical(t, f), "discovery must not add canonical writes")
			}
			if change == "generation" {
				require.ErrorIs(t, f.store.DiscoverMaintenance(context.Background(), x.turn, 100), ontology.ErrLeaseLost)
				var visible int
				require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
					return tx.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND space_generation=?`, f.team, f.generation).Row().Scan(&visible)
				}))
				require.Zero(t, visible)
			} else {
				beforeDrain := schedulerCanonical(t, f)
				x.drain(t)
				require.Equal(t, beforeDrain, schedulerCanonical(t, f))
				for i, state := range x.queue(t) {
					want := "pending"
					if change == "unrelated_vocabulary" {
						want = "organized"
					} else if change == "source" && i == 0 {
						want = "unavailable"
					}
					require.Equal(t, want, state.Status)
				}
			}
			if change != "source" && change != "support" {
				require.Equal(t, canonical, schedulerCanonical(t, f))
			}
			require.Equal(t, history, revalidationReceiptHistory(t, f))
			require.Equal(t, calls, x.providerCalls())
			discoveryLog(t, "concurrency", map[string]any{"change": change, "writer_committed_between_checks": committed,
				"writer_waited_for_lock": !committed, "page_rejected": pageErr != nil, "freshness": paused.recorder.metrics(),
				"receipts_unchanged": true, "provider_calls_added": 0})
		})
	}
}

func TestOntologyMaintenanceRevalidationDiscoveryCancellation(t *testing.T) {
	for _, boundary := range []string{"receipt_started", "receipt_finished", "source_finished"} {
		t.Run(boundary, func(t *testing.T) {
			x := newDiscoveryFixture(t, []int{2}, nil, false)
			canonical, history, queue := schedulerCanonical(t, x.f), revalidationReceiptHistory(t, x.f), x.queue(t)
			x.marker(t)
			marker := discoveryMarkerState(t, x)
			paused := pauseDiscovery(t, x, boundary, 100)
			paused.cancel()
			paused.release()
			err := <-paused.done
			require.Error(t, err)
			require.True(t, errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded), fmt.Sprintf("unexpected cancellation error: %v", err))
			require.Equal(t, queue, x.queue(t))
			require.Equal(t, marker, discoveryMarkerState(t, x))
			require.Equal(t, canonical, schedulerCanonical(t, x.f))
			require.Equal(t, history, revalidationReceiptHistory(t, x.f))
			require.NoError(t, x.f.store.DiscoverMaintenance(context.Background(), x.turn, 100))
			require.EqualValues(t, 0, discoveryMarkerState(t, x)["markers"])
			discoveryLog(t, "cancellation", map[string]any{"boundary": boundary, "rolled_back": true, "resumed": true, "freshness": paused.recorder.metrics()})
		})
	}
}
