//go:build integration

package postgres

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type finalizationFixture struct {
	f         *ontologyFixture
	window    *ontology.MaintenanceWindow
	claim     *ontology.MaintenanceClaim
	organizer *organization.Service
	calls     *atomic.Int32
	result    ontology.OrganizationResult
}

type finalizationAccounting struct {
	store *Store
	claim ontology.MaintenanceClaim
}

func (a finalizationAccounting) BeforeAttempt(ctx context.Context, id string, number, input, output int) (context.Context, error) {
	return modelprovider.WithAdmissionCallback(ctx, func(dispatch context.Context) error {
		return a.store.AdmitMaintenanceAttempt(dispatch, a.claim, id, number, input, output, time.Now().UTC())
	}), nil
}

func (a finalizationAccounting) AfterAttempt(ctx context.Context, id string, attempt ontology.AssessmentAttempt, _ bool) error {
	return a.store.ReconcileMaintenanceAttempt(ctx, a.claim, id, attempt)
}

func newFinalizationFixture(t *testing.T, texts ...string) *finalizationFixture {
	t.Helper()
	return newFinalizationFixtureWithConcurrency(t, 4, 4, texts...)
}

func newFinalizationFixtureWithConcurrency(t *testing.T, global, team int, texts ...string) *finalizationFixture {
	t.Helper()
	f := newOrganizationFixture(t)
	if len(texts) == 0 {
		texts = []string{"Atlas uses PostgreSQL for storage.", "Atlas stores data in PostgreSQL."}
	}
	for i, text := range texts {
		f.organizationEvidence(t, i%len(f.owners), text, nil)
	}
	config := concurrencyConfig(t, f, global, team)
	window := maintenanceWindow(t, f, config)
	return claimFinalizationFixture(t, f, window)
}

func claimFinalizationFixture(t *testing.T, f *ontologyFixture, window *ontology.MaintenanceWindow) *finalizationFixture {
	t.Helper()
	_, err := f.store.MaintenanceCommand(context.Background(), domain.OntologyMaintenanceCommand{Action: "run", OperationKey: uuid.NewString(), MaxBatches: 1}, time.Now().UTC(), nil)
	require.NoError(t, err)
	_, claim := maintenanceClaim(t, f, window)
	organizer, calls := organizationFixtureServiceWithAccounting(t, f, nil, nil, "fixture-model", maintenanceFixtureLimits(), finalizationAccounting{f.store, *claim}, 300000, map[string]int{"prompt_tokens": 17, "completion_tokens": 9, "total_tokens": 26})
	return &finalizationFixture{f: f, window: window, claim: claim, organizer: organizer, calls: calls}
}

func (x *finalizationFixture) stage(t *testing.T) {
	t.Helper()
	var err error
	x.result, err = x.organizer.Organize(ontology.WithMaintenanceClaim(context.Background(), *x.claim), x.f.team,
		ontology.OrganizationInput{OperationKey: "maintenance:" + x.claim.ID, Sources: x.claim.Sources})
	require.NoError(t, err)
	require.True(t, x.result.Current)
}

func (x *finalizationFixture) stageOutcome(t *testing.T, mode string) {
	t.Helper()
	if mode == "reported" {
		x.stage(t)
		return
	}
	usage := []map[string]int{{"prompt_tokens": 17, "completion_tokens": 9, "total_tokens": 26}}
	var edit func(assessment.Request, *assessment.Response)
	expectedFailure := ""
	switch mode {
	case "unknown_usage":
		usage = nil
	case "provider_response_invalid", "failed_unknown_usage":
		expectedFailure = "provider_response_invalid"
		edit = func(_ assessment.Request, response *assessment.Response) { response.RequestID = "invalid" }
		if mode == "failed_unknown_usage" {
			usage = nil
		}
	case "stale_input":
		expectedFailure = mode
		source := x.claim.Sources[0]
		var owner string
		require.NoError(t, x.f.admin.Raw(`SELECT owner_profile_id::text FROM evidence_fragments WHERE team_id=?::uuid AND fragment_id=?::uuid`, x.f.team, source.ID).Row().Scan(&owner))
		ownerIndex := slices.Index(x.f.owners, owner)
		require.NotEqual(t, -1, ownerIndex)
		edit = func(_ assessment.Request, _ *assessment.Response) {
			_, err := x.f.knowledge.RetractEvidence(x.f.actor(ownerIndex, "member"), knowledge.RetractEvidenceInput{TeamID: x.f.team, OwnerProfileID: owner, EvidenceIDs: []string{source.ID}, Reason: "withdrawn during assessment", IdempotencyKey: "finalization-withdraw", RequestHash: testHash("finalization-withdraw")})
			if err != nil {
				t.Error(err)
			}
		}
	case "maintenance_paused":
		expectedFailure = mode
		_, err := x.f.store.MaintenanceCommand(context.Background(), domain.OntologyMaintenanceCommand{Action: "pause", OperationKey: uuid.NewString()}, time.Now(), nil)
		require.NoError(t, err)
	default:
		t.Fatalf("unsupported finalization outcome %q", mode)
	}
	x.organizer, x.calls = organizationFixtureServiceWithAccounting(t, x.f, nil, edit, "fixture-model", maintenanceFixtureLimits(), finalizationAccounting{x.f.store, *x.claim}, 300000, usage...)
	var err error
	x.result, err = x.organizer.Organize(ontology.WithMaintenanceClaim(context.Background(), *x.claim), x.f.team,
		ontology.OrganizationInput{OperationKey: "maintenance:" + x.claim.ID, Sources: x.claim.Sources})
	if expectedFailure == "" {
		require.NoError(t, err)
		require.True(t, x.result.Current)
	} else {
		require.Error(t, err)
		require.Equal(t, expectedFailure, x.result.FailureCode)
		require.False(t, x.result.Current)
		require.Nil(t, x.result.Publication)
	}
	if mode == "maintenance_paused" {
		_, err := x.f.store.MaintenanceCommand(context.Background(), domain.OntologyMaintenanceCommand{Action: "resume", OperationKey: uuid.NewString()}, time.Now(), nil)
		require.NoError(t, err)
	}
}

func (x *finalizationFixture) history(t *testing.T) map[string]string {
	t.Helper()
	result := map[string]string{}
	for name, query := range map[string]string{
		"receipt":  `SELECT COALESCE(jsonb_agg(to_jsonb(receipt) ORDER BY assessment_id)::text,'[]') FROM ontology_assessments AS receipt WHERE operation_key=?`,
		"attempts": `SELECT COALESCE(jsonb_agg(to_jsonb(attempt_record) ORDER BY assessment_id,attempt)::text,'[]') FROM ontology_maintenance_attempts AS attempt_record WHERE batch_id=?::uuid`,
		"window":   `SELECT to_jsonb(window_row)::text FROM ontology_maintenance_windows AS window_row WHERE window_id=?::uuid`,
	} {
		id := x.claim.ID
		if name == "receipt" {
			id = "maintenance:" + id
		} else if name == "window" {
			id = x.window.ID
		}
		var body string
		require.NoError(t, x.f.admin.Raw(query, id).Row().Scan(&body))
		result[name] = body
	}
	return result
}

func (x *finalizationFixture) assertCompletedOutcome(t *testing.T) {
	t.Helper()
	var failure string
	require.NoError(t, x.f.admin.Raw(`SELECT failure_code FROM ontology_maintenance_batches WHERE batch_id=?::uuid AND status='completed'`, x.claim.ID).Row().Scan(&failure))
	require.Equal(t, x.result.FailureCode, failure)
	run := maintenanceRunByID(t, x.f, x.claim.RunID)
	require.Equal(t, 1, run.CompletedBatches)
	require.Equal(t, x.result.FailureCode, run.FailureCode)
	if x.result.FailureCode != "" {
		require.Equal(t, "incomplete", run.Status)
		for _, source := range x.claim.Sources {
			var row struct{ Status, Reason, AssessmentID string }
			found := x.f.admin.Raw(`SELECT status,reason,assessment_id::text FROM ontology_maintenance_sources WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_kind=? AND source_id=? AND revision=?`, x.claim.TeamID, x.claim.SpaceID, x.claim.Generation, source.Kind, source.ID, x.claim.Revisions[ontology.SourceKey(source)]).Scan(&row)
			require.NoError(t, found.Error)
			if found.RowsAffected > 0 {
				state := "failed"
				if failure == "maintenance_paused" {
					state = "pending"
				}
				require.Equal(t, state, row.Status)
				require.Equal(t, failure, row.Reason)
				require.Equal(t, x.result.AssessmentID, row.AssessmentID)
			}
		}
	}
}

func (x *finalizationFixture) batchStatus(t *testing.T) string {
	t.Helper()
	var status string
	require.NoError(t, x.f.admin.Raw(`SELECT status FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, x.claim.ID).Row().Scan(&status))
	return status
}

func (x *finalizationFixture) recover(t *testing.T) {
	t.Helper()
	turn, err := NewStore(x.f.app, x.f.rls).ClaimMaintenanceTurn(context.Background(), x.window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	if turn != nil {
		require.NoError(t, x.f.store.ReleaseMaintenanceTurn(context.Background(), *turn))
	}
}

func newFinalizationRecoveryCohort(t *testing.T, mode string, healthyFirst bool) (*finalizationFixture, *finalizationFixture, *ontologyFixture) {
	t.Helper()
	expiring := newFinalizationFixtureWithConcurrency(t, 2, 1)
	other := maintenanceOtherTeam(t, expiring.f)
	other.organizationEvidence(t, 0, "Copper uses SQLite for archival storage.", nil)
	healthy := claimFinalizationFixture(t, other, expiring.window)
	require.Equal(t, other.team, healthy.claim.TeamID)
	pending := maintenanceOtherTeam(t, expiring.f)
	pending.organizationEvidence(t, 0, "Cobalt uses Redis for coordination.", nil)
	expiring.stageOutcome(t, mode)
	healthy.stageOutcome(t, mode)
	if healthyFirst {
		require.NoError(t, expiring.f.admin.Exec(`UPDATE ontology_maintenance_batches SET created_at=(SELECT created_at-interval '1 second' FROM ontology_maintenance_batches WHERE batch_id=?::uuid) WHERE batch_id=?::uuid`, expiring.claim.ID, healthy.claim.ID).Error)
	}
	return expiring, healthy, pending
}

func (x *finalizationFixture) queuedSources(t *testing.T) string {
	t.Helper()
	var rows string
	require.NoError(t, x.f.admin.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(source) ORDER BY source_kind,source_id)::text,'[]') FROM ontology_maintenance_sources AS source WHERE team_id=?::uuid`, x.f.team).Row().Scan(&rows))
	require.NotEqual(t, "[]", rows)
	return rows
}

func (x *finalizationFixture) recoverAfterExpiry(t *testing.T, boundary, rollbackFailure string) (*ontology.MaintenanceTurn, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reached, resume := make(chan error, 1), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	var blocked, rollbackReached atomic.Bool
	barrier := func(tx *gorm.DB) {
		if tx.Error != nil || !blocked.CompareAndSwap(false, true) {
			return
		}
		var count int
		var err error
		if boundary == "source_updates" {
			err = tx.Statement.ConnPool.QueryRowContext(ctx, `SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=$1::uuid AND assessment_id=$2::uuid`, x.f.team, x.result.AssessmentID).Scan(&count)
			if err == nil && count != len(x.claim.Sources) {
				err = fmt.Errorf("completion barrier reached with %d of %d source writes", count, len(x.claim.Sources))
			}
		}
		reached <- err
		select {
		case <-resume:
		case <-ctx.Done():
		}
	}
	name := "finalization_expiry_" + uuid.NewString()
	require.NoError(t, x.f.app.Callback().Row().After("gorm:row").Register(name, func(tx *gorm.DB) {
		if boundary == "selection" && strings.Contains(tx.Statement.SQL.String(), "FROM ontology_maintenance_batches AS batch JOIN ontology_assessments AS assessment") {
			barrier(tx)
		}
	}))
	defer func() { require.NoError(t, x.f.app.Callback().Row().Remove(name)) }()
	require.NoError(t, x.f.app.Callback().Raw().Before("gorm:raw").Register(name, func(tx *gorm.DB) {
		query := tx.Statement.SQL.String()
		if boundary == "source_updates" && strings.HasPrefix(query, "UPDATE ontology_maintenance_batches SET status='completed'") && slices.Contains(tx.Statement.Vars, any(x.claim.ID)) {
			barrier(tx)
		}
		if query == "ROLLBACK TO SAVEPOINT maintenance_recovery_candidate" {
			rollbackReached.Store(true)
			if rollbackFailure == "canceled" {
				cancel()
			} else if rollbackFailure == "sql" {
				_, err := tx.Statement.ConnPool.ExecContext(ctx, "RELEASE SAVEPOINT maintenance_recovery_candidate")
				tx.AddError(err)
			}
		}
	}))
	defer func() { require.NoError(t, x.f.app.Callback().Raw().Remove(name)) }()
	type result struct {
		turn *ontology.MaintenanceTurn
		err  error
	}
	finished, stopped := make(chan result, 1), make(chan struct{})
	require.NoError(t, x.f.admin.Raw(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()+interval '2 seconds' WHERE batch_id=?::uuid RETURNING lease_until`, x.claim.ID).Row().Scan(&x.claim.LeaseUntil))
	now := time.Now().UTC()
	go func() {
		defer close(stopped)
		turn, err := x.f.store.ClaimMaintenanceTurn(ctx, x.window.ID, now, time.Minute)
		finished <- result{turn, err}
	}()
	defer func() {
		cancel()
		release()
		<-stopped
	}()
	select {
	case err := <-reached:
		require.NoError(t, err)
	case outcome := <-finished:
		t.Fatalf("recovery did not reach %s barrier: %v", boundary, outcome.err)
	case <-time.After(5 * time.Second):
		t.Fatalf("recovery did not reach %s barrier", boundary)
	}
	var live bool
	require.NoError(t, x.f.admin.Raw(`SELECT lease_until>clock_timestamp() FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, x.claim.ID).Row().Scan(&live))
	require.True(t, live, "the candidate must still be live at the barrier")
	require.Eventually(t, func() bool {
		var expired bool
		err := x.f.admin.Raw(`SELECT lease_until<=clock_timestamp() FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, x.claim.ID).Row().Scan(&expired)
		return err == nil && expired
	}, 3*time.Second, 10*time.Millisecond)
	release()
	select {
	case outcome := <-finished:
		require.True(t, rollbackReached.Load(), "the expired candidate must reach its rollback boundary")
		return outcome.turn, outcome.err
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not finish after lease expiry")
		return nil, nil
	}
}
