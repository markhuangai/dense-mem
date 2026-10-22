//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMaintenanceFinalizationCancellationAndDiscovery(t *testing.T) {
	x := newFinalizationFixture(t)
	f, ctx := x.f, context.Background()
	before := f.canonicalSnapshot(t)
	discovery, err := f.store.ClaimMaintenanceTurn(ctx, x.window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, discovery)
	x.stage(t)
	calls, receipts := x.calls.Load(), receiptCount(t, f)
	statusBefore, err := f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_teams SET discovery_kind=2,discovery_cursor='' WHERE team_id=?::uuid`, f.team).Error)
	second := uuid.MustParse(x.claim.Sources[1].ID).String()
	require.NoError(t, f.admin.Exec(fmt.Sprintf(`CREATE FUNCTION finalization_discovery_barrier() RETURNS trigger LANGUAGE plpgsql AS $$
	 BEGIN IF NEW.source_id='%s' THEN PERFORM pg_advisory_xact_lock(603001); END IF; RETURN NEW; END $$;
	 CREATE TRIGGER finalization_discovery_barrier BEFORE INSERT ON ontology_maintenance_sources FOR EACH ROW EXECUTE FUNCTION finalization_discovery_barrier()`, second)).Error)
	pool, err := f.admin.DB()
	require.NoError(t, err)
	blocker, err := pool.Conn(ctx)
	require.NoError(t, err)
	defer blocker.Close()
	_, err = blocker.ExecContext(ctx, `SELECT pg_advisory_lock(603001)`)
	require.NoError(t, err)
	defer func() { _, _ = blocker.ExecContext(ctx, `SELECT pg_advisory_unlock(603001)`) }()
	discovered := make(chan error, 1)
	go func() { discovered <- f.store.DiscoverMaintenance(ctx, *discovery, ontology.MaintenancePageSize) }()
	waitFor := func(event, statement string) {
		t.Helper()
		require.Eventually(t, func() bool {
			var waiting bool
			err := f.admin.Raw(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND wait_event=? AND query LIKE ?)`, event, statement).Row().Scan(&waiting)
			return err == nil && waiting
		}, 5*time.Second, 10*time.Millisecond)
	}
	waitFor("advisory", "%INSERT INTO ontology_maintenance_sources%")
	completionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- f.store.CompleteMaintenanceBatch(completionCtx, *x.claim, x.result, "", time.Now().UTC())
	}()
	waitFor("transactionid", "%UPDATE ontology_maintenance_sources SET status=%")
	cancel()
	err = <-finished
	require.ErrorIs(t, err, context.Canceled)
	var failure *ontology.MaintenanceFinalizationError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "source_updates", failure.Phase)
	require.Equal(t, x.claim.ID, failure.BatchID)
	require.Equal(t, x.claim.RunID, failure.RunID)
	require.Equal(t, "running", x.batchStatus(t))
	started := time.Now()
	x.recover(t)
	require.Less(t, time.Since(started), time.Second, "recovery must skip the complete locked batch")
	require.Equal(t, "running", x.batchStatus(t))
	require.Zero(t, maintenanceRunByID(t, f, x.claim.RunID).CompletedBatches)
	var organized int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND status='organized'`, f.team).Row().Scan(&organized))
	require.Zero(t, organized, "partial source locks cannot produce partial completion")
	_, err = blocker.ExecContext(ctx, `SELECT pg_advisory_unlock(603001)`)
	require.NoError(t, err)
	require.NoError(t, <-discovered)
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *discovery))
	started = time.Now()
	x.recover(t)
	elapsed := time.Since(started)
	require.Less(t, elapsed, time.Second)
	require.True(t, time.Now().Before(x.claim.LeaseUntil))
	require.Equal(t, "completed", x.batchStatus(t))
	require.Equal(t, 1, maintenanceRunByID(t, f, x.claim.RunID).CompletedBatches)
	require.Equal(t, calls, x.calls.Load())
	require.Equal(t, receipts, receiptCount(t, f))
	statusAfter, err := f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.Equal(t, statusBefore.Window, statusAfter.Window)
	require.Equal(t, before, f.canonicalSnapshot(t))
	t.Logf("finalization phase=%s class=%s duration=%s; recovery=%s; occupied batch slots 1->0; provider calls=%d receipts=%d", failure.Phase, failure.FailureClass(), failure.Duration, elapsed, calls, receipts)
}

func TestOntologyMaintenanceFinalizationReceiptsAndAccounting(t *testing.T) {
	for _, mode := range []string{"reported", "zero_attempts", "unknown_usage", "unreconciled", "missing_receipt", "failed_receipt"} {
		t.Run(mode, func(t *testing.T) {
			var texts []string
			if mode == "zero_attempts" {
				texts = []string{strings.Repeat("Atlas uses PostgreSQL. ", 50000)}
			}
			x := newFinalizationFixture(t, texts...)
			ctx := context.Background()
			if mode == "unknown_usage" || mode == "failed_receipt" {
				var edit func(assessment.Request, *assessment.Response)
				if mode == "failed_receipt" {
					edit = func(_ assessment.Request, result *assessment.Response) { result.RequestID = "invalid" }
				}
				x.organizer, x.calls = organizationFixtureServiceWithAccounting(t, x.f, nil, edit, "fixture-model", maintenanceFixtureLimits(), finalizationAccounting{x.f.store, *x.claim})
			}
			if mode == "failed_receipt" {
				_, err := x.organizer.Organize(ontology.WithMaintenanceClaim(ctx, *x.claim), x.f.team, ontology.OrganizationInput{OperationKey: "maintenance:" + x.claim.ID, Sources: x.claim.Sources})
				require.Error(t, err)
			} else if mode != "missing_receipt" {
				x.stage(t)
			}
			if mode == "unreconciled" {
				require.NoError(t, x.f.store.ReserveMaintenanceAttempt(ctx, *x.claim, uuid.NewString(), 1, 10, 10, time.Now()))
			}
			before := x.f.canonicalSnapshot(t)
			statusBefore, err := x.f.store.MaintenanceStatus(ctx, time.Now())
			require.NoError(t, err)
			calls, receipts := x.calls.Load(), receiptCount(t, x.f)
			x.recover(t)
			expected := "completed"
			if mode == "unreconciled" || mode == "missing_receipt" || mode == "failed_receipt" {
				expected = "running"
			}
			require.Equal(t, expected, x.batchStatus(t))
			statusAfter, err := x.f.store.MaintenanceStatus(ctx, time.Now())
			require.NoError(t, err)
			require.Equal(t, statusBefore.Window, statusAfter.Window)
			if mode == "zero_attempts" {
				require.Zero(t, calls)
			} else if mode == "unknown_usage" {
				require.Positive(t, statusAfter.Window.ReservedInput)
				require.Positive(t, statusAfter.Window.ChargedInput)
			} else if mode == "reported" {
				require.EqualValues(t, 17, statusAfter.Window.ChargedInput)
				require.EqualValues(t, 9, statusAfter.Window.ChargedOutput)
				require.Zero(t, statusAfter.Window.ReservedInput)
				require.EqualValues(t, len(x.claim.Sources), statusAfter.Counts.Organized)
			}
			require.Equal(t, calls, x.calls.Load())
			require.Equal(t, receipts, receiptCount(t, x.f))
			require.Equal(t, before, x.f.canonicalSnapshot(t))
		})
	}
}

func TestOntologyMaintenanceFinalizationRacesAndFences(t *testing.T) {
	x := newFinalizationFixture(t)
	x.stage(t)
	f, ctx := x.f, context.Background()
	before, receipts, calls := f.canonicalSnapshot(t), receiptCount(t, f), x.calls.Load()
	errs := make(chan error, 9)
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if i%2 == 0 {
				errs <- NewStore(f.app, f.rls).CompleteMaintenanceBatch(ctx, *x.claim, x.result, "", time.Now())
			} else {
				errs <- NewStore(f.app, f.rls).recoverMaintenanceBatches(ctx, time.Now())
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		_, err := f.store.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{Action: "allowance", OperationKey: "finalization-allowance", WindowID: x.window.ID, InputTokens: 100, OutputTokens: 100}, time.Now(), nil)
		errs <- err
	}()
	workers.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	x.recover(t)
	require.Equal(t, 1, maintenanceRunByID(t, f, x.claim.RunID).CompletedBatches)
	require.Equal(t, "completed", x.batchStatus(t))
	require.ErrorIs(t, f.store.ReserveMaintenanceAttempt(ctx, *x.claim, uuid.NewString(), 1, 1, 1, time.Now()), ontology.ErrLeaseLost)
	_, err := f.store.SeedDefinitions(ontology.WithMaintenanceClaim(ctx, *x.claim), f.team, ontology.SeedInput{OperationKey: "stale-finalization", Limit: 1})
	require.ErrorIs(t, err, ontology.ErrLeaseLost)
	forged := *x.claim
	forged.LeaseToken = uuid.NewString()
	require.ErrorIs(t, f.store.CompleteMaintenanceBatch(ctx, forged, x.result, "", time.Now()), ontology.ErrLeaseLost)
	require.Equal(t, before, f.canonicalSnapshot(t))
	require.Equal(t, receipts, receiptCount(t, f))
	require.Equal(t, calls, x.calls.Load())
}

func TestOntologyMaintenanceFinalizationAdmissionAndIsolation(t *testing.T) {
	for _, mode := range []string{"paused", "disabled", "overrun", "expired", "new_generation", "revision", "actor"} {
		t.Run(mode, func(t *testing.T) {
			x := newFinalizationFixture(t)
			x.stage(t)
			f, ctx := x.f, context.Background()
			var expected error
			switch mode {
			case "paused":
				require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_state SET paused=true WHERE singleton`).Error)
				expected = ontology.ErrMaintenancePaused
			case "disabled":
				require.NoError(t, f.admin.Exec(`UPDATE app_config SET value='false' WHERE key=?`, domain.AppConfigOntologyEnabled).Error)
				expected = ontology.ErrMaintenanceDisabled
			case "overrun":
				require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_windows SET overrun=true WHERE window_id=?::uuid`, x.window.ID).Error)
				expected = ontology.ErrBudgetDeferred
			case "expired":
				require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()-interval '1 second' WHERE batch_id=?::uuid`, x.claim.ID).Error)
			case "new_generation":
				require.NoError(t, f.admin.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE id=?::uuid`, f.space).Error)
			case "revision":
				source := x.claim.Sources[0]
				require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_sources SET revision=revision+1,status='pending',reason='new_revision' WHERE team_id=?::uuid AND source_kind=? AND source_id=?`, f.team, source.Kind, source.ID).Error)
			case "actor":
				ctx = f.actor(0, "manager")
				expected = ontology.ErrUnauthorized
			}
			turn, err := f.store.ClaimMaintenanceTurn(ctx, x.window.ID, time.Now(), time.Minute)
			if expected != nil {
				require.ErrorIs(t, err, expected)
			} else {
				require.NoError(t, err)
			}
			if turn != nil {
				require.NoError(t, f.store.ReleaseMaintenanceTurn(context.Background(), *turn))
			}
			expectedStatus := "completed"
			if mode == "expired" {
				expectedStatus = "lost"
				require.ErrorIs(t, f.store.CompleteMaintenanceBatch(context.Background(), *x.claim, x.result, "", time.Now()), ontology.ErrLeaseLost)
			} else if mode == "new_generation" || mode == "actor" {
				expectedStatus = "running"
			}
			require.Equal(t, expectedStatus, x.batchStatus(t))
			if mode == "revision" {
				var reason string
				require.NoError(t, f.admin.Raw(`SELECT reason FROM ontology_maintenance_sources WHERE team_id=?::uuid AND source_id=?`, f.team, x.claim.Sources[0].ID).Row().Scan(&reason))
				require.Equal(t, "new_revision", reason)
			}
			if mode == "actor" {
				other := maintenanceOtherTeam(t, f)
				for _, reader := range []struct {
					ctx     context.Context
					team    string
					visible int
				}{{f.actor(0, "member"), f.team, 1}, {f.actor(1, "member"), f.team, 1}, {other.actor(0, "manager"), other.team, 0}} {
					require.NoError(t, f.rls.WithTeamTx(reader.ctx, f.app, reader.team, func(tx *gorm.DB) error {
						var count int
						err := tx.Raw(`SELECT count(*) FROM ontology_maintenance_batches WHERE team_id=?::uuid`, f.team).Row().Scan(&count)
						require.Equal(t, reader.visible, count)
						return err
					}))
				}
			}
		})
	}
}

func TestOntologyMaintenanceFinalizationRejectsMismatchedReceipt(t *testing.T) {
	x := newFinalizationFixture(t)
	x.stage(t)
	var body []byte
	require.NoError(t, x.f.admin.Raw(`SELECT body FROM ontology_assessments WHERE operation_key=?`, "maintenance:"+x.claim.ID).Row().Scan(&body))
	receipt, err := decodeOrganization(body)
	require.NoError(t, err)
	claim := *x.claim
	claim.Sources = append([]ontology.SourceHandle(nil), claim.Sources...)
	claim.Sources[0].Version++
	require.ErrorIs(t, validateMaintenanceRecovery(claim, receipt), ontology.ErrInvalid)
	claim = *x.claim
	claim.Revisions = map[string]int64{}
	require.ErrorIs(t, validateMaintenanceRecovery(claim, receipt), ontology.ErrInvalid)
	receipt.OperationKey = "different-batch"
	encoded, err := json.Marshal(receipt)
	require.NoError(t, err)
	decoded, err := decodeOrganization(encoded)
	require.NoError(t, err)
	require.ErrorIs(t, validateMaintenanceRecovery(*x.claim, decoded), ontology.ErrInvalid)
}
