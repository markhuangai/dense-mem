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
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMaintenanceFinalizationCancellationAndDiscovery(t *testing.T) {
	for _, mode := range []string{"reported", "stale_input", "provider_response_invalid", "failed_unknown_usage"} {
		t.Run(mode, func(t *testing.T) {
			x := newFinalizationFixture(t)
			f, ctx := x.f, context.Background()
			discovery, err := f.store.ClaimMaintenanceTurn(ctx, x.window.ID, time.Now().UTC(), time.Minute)
			require.NoError(t, err)
			require.NotNil(t, discovery)
			x.stageOutcome(t, mode)
			before, history := f.canonicalSnapshot(t), x.history(t)
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
			if mode == "failed_unknown_usage" {
				cancel()
				completionCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
			}
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				finished <- f.store.CompleteMaintenanceBatch(completionCtx, *x.claim, x.result, x.result.FailureCode, time.Now().UTC())
			}()
			waitFor("transactionid", "%UPDATE ontology_maintenance_sources SET status=%")
			expectedError := context.Canceled
			if mode == "failed_unknown_usage" {
				expectedError = context.DeadlineExceeded
			} else {
				cancel()
			}
			err = <-finished
			require.ErrorIs(t, err, expectedError)
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
			var finalized int
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND assessment_id=?::uuid`, f.team, x.result.AssessmentID).Row().Scan(&finalized))
			require.Zero(t, finalized, "partial source locks cannot produce partial completion")
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
			x.assertCompletedOutcome(t)
			require.Equal(t, history, x.history(t))
			require.Equal(t, 1, maintenanceRunByID(t, f, x.claim.RunID).CompletedBatches)
			require.Equal(t, calls, x.calls.Load())
			require.Equal(t, receipts, receiptCount(t, f))
			statusAfter, err := f.store.MaintenanceStatus(ctx, time.Now())
			require.NoError(t, err)
			require.Equal(t, statusBefore.Window, statusAfter.Window)
			if x.result.FailureCode != "" {
				require.Equal(t, statusBefore.LastSuccessfulProgress, statusAfter.LastSuccessfulProgress)
			}
			require.Equal(t, before, f.canonicalSnapshot(t))
			t.Logf("finalization phase=%s class=%s duration=%s; recovery=%s; occupied batch slots 1->0; provider calls=%d receipts=%d", failure.Phase, failure.FailureClass(), failure.Duration, elapsed, calls, receipts)
		})
	}
}

func TestOntologyMaintenanceFinalizationReceiptsAndAccounting(t *testing.T) {
	for _, mode := range []string{"reported", "zero_attempts", "unknown_usage", "unreconciled", "missing_receipt", "provider_response_invalid", "failed_unknown_usage", "failed_unreconciled", "stale_input", "maintenance_paused"} {
		t.Run(mode, func(t *testing.T) {
			var texts []string
			if mode == "zero_attempts" {
				texts = []string{strings.Repeat("Atlas uses PostgreSQL. ", 50000)}
			}
			x := newFinalizationFixture(t, texts...)
			ctx := context.Background()
			if mode == "zero_attempts" {
				x.stage(t)
			} else if mode != "missing_receipt" {
				outcome := mode
				if mode == "unreconciled" {
					outcome = "reported"
				} else if mode == "failed_unreconciled" {
					outcome = "provider_response_invalid"
				}
				x.stageOutcome(t, outcome)
			}
			if mode == "unreconciled" || mode == "failed_unreconciled" {
				require.NoError(t, x.f.store.AdmitMaintenanceAttempt(ctx, *x.claim, uuid.NewString(), 1, 10, 10, time.Now()))
			}
			before, history := x.f.canonicalSnapshot(t), x.history(t)
			statusBefore, err := x.f.store.MaintenanceStatus(ctx, time.Now())
			require.NoError(t, err)
			calls, receipts := x.calls.Load(), receiptCount(t, x.f)
			x.recover(t)
			expected := "completed"
			if mode == "unreconciled" || mode == "missing_receipt" || mode == "failed_unreconciled" {
				expected = "running"
			}
			require.Equal(t, expected, x.batchStatus(t))
			if expected == "completed" {
				x.assertCompletedOutcome(t)
			}
			require.Equal(t, history, x.history(t))
			statusAfter, err := x.f.store.MaintenanceStatus(ctx, time.Now())
			require.NoError(t, err)
			require.Equal(t, statusBefore.Window, statusAfter.Window)
			if mode == "zero_attempts" {
				require.Zero(t, calls)
			} else if mode == "unknown_usage" || mode == "failed_unknown_usage" {
				require.Positive(t, statusAfter.Usage.UnavailableUsageAttempts)
				require.Zero(t, statusAfter.Usage.UnreconciledAttempts)
			} else if mode == "provider_response_invalid" {
				require.EqualValues(t, 51, statusAfter.Window.Usage.ReportedInputTokens)
				require.EqualValues(t, 27, statusAfter.Window.Usage.ReportedOutputTokens)
				require.EqualValues(t, 3, calls)
			} else if mode == "maintenance_paused" {
				require.Zero(t, calls)
				require.Zero(t, statusAfter.Usage.UnreconciledAttempts)
				require.EqualValues(t, len(x.claim.Sources), statusAfter.Counts.Pending)
			} else if mode == "reported" {
				require.EqualValues(t, 17, statusAfter.Window.Usage.ReportedInputTokens)
				require.EqualValues(t, 9, statusAfter.Window.Usage.ReportedOutputTokens)
				require.Zero(t, statusAfter.Usage.UnreconciledAttempts)
				require.EqualValues(t, len(x.claim.Sources), statusAfter.Counts.Organized)
			}
			require.Equal(t, calls, x.calls.Load())
			require.Equal(t, receipts, receiptCount(t, x.f))
			require.Equal(t, before, x.f.canonicalSnapshot(t))
		})
	}
}

func TestOntologyMaintenanceFinalizationRacesAndFences(t *testing.T) {
	for _, mode := range []string{"reported", "failed_unknown_usage"} {
		t.Run(mode, func(t *testing.T) {
			x := newFinalizationFixture(t)
			x.stageOutcome(t, mode)
			f, ctx := x.f, context.Background()
			history := x.history(t)
			before, receipts, calls := f.canonicalSnapshot(t), receiptCount(t, f), x.calls.Load()
			errs := make(chan error, 9)
			var workers sync.WaitGroup
			for i := range 8 {
				workers.Add(1)
				go func() {
					defer workers.Done()
					if i%2 == 0 {
						errs <- NewStore(f.app, f.rls).CompleteMaintenanceBatch(ctx, *x.claim, x.result, x.result.FailureCode, time.Now())
					} else {
						errs <- NewStore(f.app, f.rls).recoverMaintenanceBatches(ctx, time.Now())
					}
				}()
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				_, err := f.store.MaintenanceStatus(ctx, time.Now())
				errs <- err
			}()
			workers.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			x.recover(t)
			x.assertCompletedOutcome(t)
			require.Equal(t, history, x.history(t))
			require.Equal(t, 1, maintenanceRunByID(t, f, x.claim.RunID).CompletedBatches)
			require.Equal(t, "completed", x.batchStatus(t))
			require.ErrorIs(t, f.store.AdmitMaintenanceAttempt(ctx, *x.claim, uuid.NewString(), 1, 1, 1, time.Now()), ontology.ErrLeaseLost)
			_, err := f.store.SeedDefinitions(ontology.WithMaintenanceClaim(ctx, *x.claim), f.team, ontology.SeedInput{OperationKey: "stale-finalization", Limit: 1})
			require.ErrorIs(t, err, ontology.ErrLeaseLost)
			forged := *x.claim
			forged.LeaseToken = uuid.NewString()
			require.ErrorIs(t, f.store.CompleteMaintenanceBatch(ctx, forged, x.result, x.result.FailureCode, time.Now()), ontology.ErrLeaseLost)
			require.Equal(t, before, f.canonicalSnapshot(t))
			require.Equal(t, receipts, receiptCount(t, f))
			require.Equal(t, calls, x.calls.Load())
		})
	}
}

func TestOntologyMaintenanceFinalizationAdmissionAndIsolation(t *testing.T) {
	for _, global := range []int{1, 4} {
		t.Run(fmt.Sprintf("recovered_capacity_global_%d_team_1", global), func(t *testing.T) {
			x := newFinalizationFixtureWithConcurrency(t, global, 1)
			f, ctx := x.f, context.Background()
			next := f
			if global == 1 {
				next = maintenanceOtherTeam(t, f)
			}
			pending := next.organizationEvidence(t, 0, "Copper uses SQLite for archival storage.", nil)
			x.stageOutcome(t, "failed_unknown_usage")
			before, otherBefore, history := f.canonicalSnapshot(t), next.canonicalSnapshot(t), x.history(t)
			calls := x.calls.Load()
			blocker := f.admin.Begin()
			require.NoError(t, blocker.Error)
			defer blocker.Rollback()
			require.NoError(t, blocker.Exec(`SELECT source_id FROM ontology_maintenance_sources WHERE team_id=?::uuid AND source_id=? FOR UPDATE`, f.team, x.claim.Sources[0].ID).Error)
			blocked, err := f.store.ClaimMaintenanceTurn(ctx, x.window.ID, time.Now(), time.Minute)
			require.NoError(t, err)
			require.Nil(t, blocked, "the occupied global/team slot must prevent pending work admission")
			require.Equal(t, "running", x.batchStatus(t))
			statusBefore, err := f.store.MaintenanceStatus(ctx, time.Now())
			require.NoError(t, err)
			require.Equal(t, 1, statusBefore.ActiveWork)
			require.NoError(t, f.store.withScope(ctx, f.team, false, func(tx *gorm.DB, fence scope) error {
				busy, err := maintenanceBusySources(tx, fence)
				require.NoError(t, err)
				for _, source := range x.claim.Sources {
					require.True(t, busy[ontology.SourceKey(source)])
				}
				return nil
			}))
			require.NoError(t, blocker.Rollback().Error)
			started := time.Now()
			turn, err := NewStore(f.app, f.rls).ClaimMaintenanceTurn(ctx, x.window.ID, time.Now(), time.Minute)
			require.NoError(t, err)
			require.NotNil(t, turn, "recovered capacity must admit pending work before expiry")
			require.Equal(t, next.team, turn.TeamID)
			require.True(t, time.Now().Before(x.claim.LeaseUntil))
			x.assertCompletedOutcome(t)
			for range 8 {
				require.NoError(t, f.store.DiscoverMaintenance(ctx, *turn, ontology.MaintenancePageSize))
			}
			claim, err := f.store.ClaimMaintenanceBatch(ctx, *turn, x.window.ID, time.Now())
			require.NoError(t, err)
			require.NotNil(t, claim)
			require.Contains(t, claim.Sources, pending)
			assertDisjointClaims(t, x.claim, claim)
			statusAfter, err := f.store.MaintenanceStatus(ctx, time.Now())
			require.NoError(t, err)
			require.Equal(t, 1, statusAfter.ActiveWork, "new useful work replaces the terminal-held slot")
			require.Equal(t, statusBefore.Window, statusAfter.Window)
			require.Equal(t, statusBefore.LastSuccessfulProgress, statusAfter.LastSuccessfulProgress)
			require.Equal(t, history, x.history(t))
			require.Equal(t, calls, x.calls.Load())
			require.Equal(t, before, f.canonicalSnapshot(t))
			require.Equal(t, otherBefore, next.canonicalSnapshot(t))
			t.Logf("global=%d team=1 recovery=%s terminal-held slots 1->0; pending batches admitted=1; provider calls unchanged=%d", global, time.Since(started), calls)
		})
	}
	for _, receiptMode := range []string{"reported", "provider_response_invalid"} {
		t.Run(receiptMode, func(t *testing.T) {
			for _, mode := range []string{"paused", "disabled", "expired", "new_generation", "revision", "actor"} {
				t.Run(mode, func(t *testing.T) {
					x := newFinalizationFixture(t)
					x.stageOutcome(t, receiptMode)
					history := x.history(t)
					f, ctx := x.f, context.Background()
					var expected error
					var revisedSource string
					switch mode {
					case "paused":
						require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_state SET paused=true WHERE singleton`).Error)
						expected = ontology.ErrMaintenancePaused
					case "disabled":
						require.NoError(t, f.admin.Exec(`UPDATE app_config SET value='false' WHERE key=?`, domain.AppConfigOntologyEnabled).Error)
						expected = ontology.ErrMaintenanceDisabled
					case "expired":
						require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()-interval '1 second' WHERE batch_id=?::uuid`, x.claim.ID).Error)
					case "new_generation":
						require.NoError(t, f.admin.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE id=?::uuid`, f.space).Error)
					case "revision":
						source := x.claim.Sources[0]
						require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_sources SET revision=revision+1,status='pending',reason='new_revision' WHERE team_id=?::uuid AND source_kind=? AND source_id=?`, f.team, source.Kind, source.ID).Error)
						require.NoError(t, f.admin.Raw(`SELECT to_jsonb(source)::text FROM ontology_maintenance_sources AS source WHERE team_id=?::uuid AND source_id=?`, f.team, source.ID).Row().Scan(&revisedSource))
					case "actor":
						ctx = f.actor(0, "manager")
						expected = ontology.ErrUnauthorized
					}
					before, calls := f.canonicalSnapshot(t), x.calls.Load()
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
						require.ErrorIs(t, f.store.CompleteMaintenanceBatch(context.Background(), *x.claim, x.result, x.result.FailureCode, time.Now()), ontology.ErrLeaseLost)
					} else if mode == "new_generation" || mode == "actor" {
						expectedStatus = "running"
					}
					require.Equal(t, expectedStatus, x.batchStatus(t))
					require.Equal(t, history, x.history(t))
					require.Equal(t, before, f.canonicalSnapshot(t))
					require.Equal(t, calls, x.calls.Load())
					if expectedStatus == "completed" {
						x.assertCompletedOutcome(t)
					}
					if mode == "revision" {
						if receiptMode == "reported" {
							state := revalidationQueue(t, f, x.claim.Sources[0])
							require.Equal(t, "organized", state.Status)
							require.Equal(t, x.result.AssessmentID, state.AssessmentID)
							require.Equal(t, x.claim.Revisions[ontology.SourceKey(x.claim.Sources[0])]+1, state.Revision)
						} else {
							var after string
							require.NoError(t, f.admin.Raw(`SELECT to_jsonb(source)::text FROM ontology_maintenance_sources AS source WHERE team_id=?::uuid AND source_id=?`, f.team, x.claim.Sources[0].ID).Row().Scan(&after))
							require.Equal(t, revisedSource, after)
						}
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
		})
	}
}

func TestOntologyMaintenanceFinalizationRejectsMismatchedReceipt(t *testing.T) {
	for _, mode := range []string{"reported", "provider_response_invalid"} {
		t.Run(mode, func(t *testing.T) {
			x := newFinalizationFixture(t)
			x.stageOutcome(t, mode)
			var body []byte
			require.NoError(t, x.f.admin.Raw(`SELECT body FROM ontology_assessments WHERE operation_key=?`, "maintenance:"+x.claim.ID).Row().Scan(&body))
			receipt, err := decodeOrganization(body)
			require.NoError(t, err)
			claim := *x.claim
			claim.Sources = append([]ontology.SourceHandle(nil), claim.Sources...)
			claim.Sources[0].Version++
			require.ErrorIs(t, validateMaintenanceReceipt(claim, receipt), ontology.ErrInvalid)
			claim = *x.claim
			claim.Revisions = map[string]int64{}
			require.ErrorIs(t, validateMaintenanceReceipt(claim, receipt), ontology.ErrInvalid)
			receipt.OperationKey = "different-batch"
			encoded, err := json.Marshal(receipt)
			require.NoError(t, err)
			decoded, err := decodeOrganization(encoded)
			require.NoError(t, err)
			require.ErrorIs(t, validateMaintenanceReceipt(*x.claim, decoded), ontology.ErrInvalid)
		})
	}
}
