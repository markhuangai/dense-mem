//go:build integration

package postgres

import (
	"context"
	"slices"
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
