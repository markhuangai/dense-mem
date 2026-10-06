//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpostgres "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	densehttp "github.com/markhuangai/dense-mem/internal/http"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	access "github.com/markhuangai/dense-mem/internal/service/access"
	"github.com/markhuangai/dense-mem/internal/settings"
	"github.com/stretchr/testify/require"
)

func maintenanceServiceFixture(t *testing.T, f *ontologyFixture, config *settings.AppConfigServiceImpl, edit func(assessment.Request, *assessment.Response), audits ...access.AuditService) (*organization.MaintenanceService, func() int32) {
	t.Helper()
	var audit access.AuditService
	if len(audits) > 0 {
		audit = audits[0]
	}
	var counters []*atomic.Int32
	service := organization.NewMaintenanceService(organization.MaintenanceDependencies{Repository: f.store, Config: config, Audit: audit, DefaultModel: "fixture-model", ProviderTimeout: time.Minute, Organizer: func(model string, accounting assessment.AttemptAccounting) *organization.Service {
		organizer, calls := organizationFixtureServiceWithAccounting(t, f, nil, edit, model, maintenanceFixtureLimits(), accounting)
		counters = append(counters, calls)
		return organizer
	}})
	return service, func() int32 {
		var count int32
		for _, counter := range counters {
			count += counter.Load()
		}
		return count
	}
}

func TestOntologyMaintenanceOperatorHTTPAndScheduler(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
	before := f.canonicalSnapshot(t)
	appConfig := maintenanceSettings(t, f)
	service, calls := maintenanceServiceFixture(t, f, appConfig, nil, auditapp.New(auditpostgres.NewStore(f.app, f.rls)))
	const token = "synthetic-maintenance-control"
	portal, err := densehttp.NewControlPortalServerWithCapabilityBindings(&config.Config{ControlPortalToken: token}, nil, nil, nil,
		densehttp.ControlPortalBindings{Telemetry: densehttp.ControlPortalTelemetry{Ontology: service, Config: appConfig}}, densehttp.HealthConfig{}, nil)
	require.NoError(t, err)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/control/api"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		portal.ServeHTTP(rec, req)
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder, value any) {
		t.Helper()
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var envelope struct{ Data json.RawMessage }
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
		require.NoError(t, json.Unmarshal(envelope.Data, value))
	}
	var configured domain.OntologyMaintenanceSettings
	decode(request(http.MethodGet, "/config/ontology-maintenance", ""), &configured)
	require.True(t, configured.Effective.Enabled)
	decode(request(http.MethodPatch, "/config/ontology-maintenance", `{"items":[{"key":"ONTOLOGY_MAINTENANCE_CADENCE_HOURS","value":"24"}]}`), &configured)
	require.Equal(t, 24, configured.Effective.CadenceHours)
	for _, item := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodPost, "/ontology/runs", `{"operation_key":"invalid-bound","max_batches":101}`, http.StatusUnprocessableEntity},
		{http.MethodPost, "/ontology/runs", `{"operation_key":"unknown-field","team_id":"untrusted"}`, http.StatusUnprocessableEntity},
		{http.MethodGet, "/ontology/runs?limit=invalid", "", http.StatusUnprocessableEntity},
		{http.MethodGet, "/ontology/runs?unexpected=1", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/ontology/runs/invalid/retry", `{"operation_key":"invalid-run"}`, http.StatusBadRequest},
		{http.MethodPatch, "/config/ontology-maintenance", `{"items":[{"key":"ONTOLOGY_MAINTENANCE_MODEL","value":"a"},{"key":"ONTOLOGY_MAINTENANCE_MODEL","value":"b"}]}`, http.StatusUnprocessableEntity},
	} {
		rec := request(item.method, item.path, item.body)
		require.Equal(t, item.status, rec.Code, "%s %s: %s", item.method, item.path, rec.Body.String())
	}
	var command ontology.MaintenanceRun
	decode(request(http.MethodPost, "/ontology/pause", `{"operation_key":"http-pause"}`), &command)
	var status ontology.MaintenanceStatus
	decode(request(http.MethodGet, "/ontology/status", ""), &status)
	require.True(t, status.Paused)
	require.Equal(t, http.StatusConflict, request(http.MethodPost, "/ontology/runs", `{"operation_key":"paused-run"}`).Code)
	require.Zero(t, calls())
	decode(request(http.MethodPost, "/ontology/resume", `{"operation_key":"http-resume"}`), &command)
	decode(request(http.MethodPost, "/ontology/runs", `{"operation_key":"http-run","max_batches":1}`), &command)
	var replay ontology.MaintenanceRun
	decode(request(http.MethodPost, "/ontology/runs", `{"operation_key":"http-run","max_batches":1}`), &replay)
	require.Equal(t, command, replay)
	require.Equal(t, http.StatusNotFound, request(http.MethodPost, "/ontology/runs/"+uuid.NewString()+"/retry", `{"operation_key":"missing-run"}`).Code)
	var runs ontology.MaintenanceRunPage
	decode(request(http.MethodGet, "/ontology/runs?limit=1", ""), &runs)
	require.Len(t, runs.Runs, 1)
	require.NotEmpty(t, runs.NextCursor)
	decode(request(http.MethodGet, "/ontology/runs?cursor="+runs.NextCursor, ""), &runs)
	require.NotEmpty(t, runs.Runs)
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		organization.NewMaintenanceScheduler(service, nil).Start(ctx)
	}()
	defer func() { cancel(); <-finished }()
	require.Eventually(t, func() bool {
		current, err := service.Status(context.Background())
		return err == nil && current.CoverageComplete && current.Counts.Organized == 1
	}, 10*time.Second, 20*time.Millisecond)
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance scheduler failed to release its workers after cancellation")
	}
	require.Positive(t, calls())
	decode(request(http.MethodGet, "/ontology/status", ""), &status)
	require.Greater(t, status.Window.ChargedInput, int64(0))
	require.Equal(t, before, f.canonicalSnapshot(t))
	var auditCount int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM audit_log WHERE operation='ONTOLOGY_MAINTENANCE_COMMAND'`).Row().Scan(&auditCount))
	require.GreaterOrEqual(t, auditCount, 4)
	var diagnostic strings.Builder
	organization.NewMaintenanceScheduler(service, slog.New(slog.NewTextHandler(&diagnostic, nil))).Start(ctx)
	require.Contains(t, diagnostic.String(), "configuration_unavailable")
	failed := httptest.NewRequest(http.MethodGet, "/control/api/ontology/status", nil).WithContext(ctx)
	failed.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	portal.ServeHTTP(rec, failed)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func maintenanceFixtureLimits() assessor.SemanticAssessmentLimits {
	limits := assessor.DefaultSemanticAssessmentLimits()
	limits.MaxOutputTokens = 4096
	return limits
}

func drainMaintenance(t *testing.T, service *organization.MaintenanceService) {
	t.Helper()
	for range 30 {
		progress, err := service.RunTurn(context.Background())
		require.NoError(t, err)
		if !progress {
			return
		}
	}
	t.Fatal("maintenance failed to quiesce within bounded turns")
}

func TestOntologyMaintenanceRegenerationFailureRetry(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
	config := maintenanceSettings(t, f)
	broken := true
	service, calls := maintenanceServiceFixture(t, f, config, func(_ assessment.Request, response *assessment.Response) {
		if broken {
			response.Items = nil
		}
	})
	ctx := context.Background()
	var failed bool
	for range 10 {
		_, err := service.RunTurn(ctx)
		if err != nil {
			failed = true
			break
		}
	}
	require.True(t, failed)
	require.EqualValues(t, 3, calls())
	status, err := service.Status(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.Failed)
	require.True(t, status.LatestRun.Retryable)
	var count int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_attempts`).Row().Scan(&count))
	require.Equal(t, 3, count)
	require.Greater(t, status.Window.ReservedOutput, int64(0))
	for range 8 {
		_, err := service.RunTurn(ctx)
		require.NoError(t, err)
	}
	require.EqualValues(t, 3, calls(), "unchanged failed sources require explicit retry")
	broken = false
	var previousRun string
	require.NoError(t, f.admin.Raw(`SELECT last_run_id::text FROM ontology_maintenance_sources WHERE team_id=?::uuid AND status='failed'`, f.team).Row().Scan(&previousRun))
	command := domain.OntologyMaintenanceCommand{Action: "retry", OperationKey: "retry-failed", RetryRunID: previousRun, MaxBatches: 1}
	run, err := f.store.MaintenanceCommand(ctx, command, time.Now().UTC())
	require.NoError(t, err)
	replayed, err := f.store.MaintenanceCommand(ctx, command, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, run.ID, replayed.ID)
	drainMaintenance(t, service)
	status, err = service.Status(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.Organized)
	require.Zero(t, status.Counts.Failed)
	require.Equal(t, run.WindowID, status.Window.ID)
	require.Greater(t, status.Window.ChargedOutput, int64(3*maintenanceFixtureLimits().MaxOutputTokens))
}

func TestOntologyMaintenanceProviderPauseDrain(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
	config := maintenanceSettings(t, f)
	service, calls := maintenanceServiceFixture(t, f, config, func(_ assessment.Request, _ *assessment.Response) {
		_, err := f.store.MaintenanceCommand(context.Background(), domain.OntologyMaintenanceCommand{Action: "pause", OperationKey: "pause-during-provider"}, time.Now().UTC())
		require.NoError(t, err)
	})
	for range 10 {
		progress, err := service.RunTurn(context.Background())
		require.NoError(t, err)
		if calls() > 0 {
			require.True(t, progress)
			break
		}
	}
	status, err := service.Status(context.Background())
	require.NoError(t, err)
	require.True(t, status.Paused)
	require.EqualValues(t, 1, status.Counts.Organized)
	_, err = service.RunTurn(context.Background())
	require.ErrorIs(t, err, ontology.ErrMaintenancePaused)
	require.EqualValues(t, 1, calls())
	page, err := f.store.ListRecords(context.Background(), f.team, ontology.AssignmentKind, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Records, 1)
	require.True(t, page.Records[0].Current)
}

func TestOntologyMaintenanceAccountingFailurePreventsPublication(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
	config := maintenanceSettings(t, f)
	require.NoError(t, f.admin.Exec(`CREATE FUNCTION maintenance_test_reconcile_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture accounting failure'; END $$;
	 CREATE TRIGGER maintenance_test_reconcile_failure BEFORE UPDATE ON ontology_maintenance_attempts FOR EACH ROW EXECUTE FUNCTION maintenance_test_reconcile_failure()`).Error)
	service, calls := maintenanceServiceFixture(t, f, config, nil)
	var failure error
	for range 10 {
		_, err := service.RunTurn(context.Background())
		if err != nil {
			failure = err
			break
		}
	}
	require.ErrorIs(t, failure, ontology.ErrAccounting)
	require.EqualValues(t, 1, calls())
	page, err := f.store.ListRecords(context.Background(), f.team, ontology.AssignmentKind, "", 20)
	require.NoError(t, err)
	require.Empty(t, page.Records)
	status, err := service.Status(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.Failed)
	require.Greater(t, status.Window.ReservedInput, int64(0))
	require.False(t, status.CoverageComplete)
}

func TestOntologyMaintenanceBudgetDeferralAndManualReuse(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
	config := maintenanceSettings(t, f)
	_, err := config.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{domain.AppConfigOntologyOutputTokens: "1"}, "control", "", "")
	require.NoError(t, err)
	window := maintenanceWindow(t, f, config)
	manual, err := f.store.MaintenanceCommand(context.Background(), domain.OntologyMaintenanceCommand{Action: "run", OperationKey: uuid.NewString()}, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, window.ID, manual.WindowID)
	service, calls := maintenanceServiceFixture(t, f, config, nil)
	for range 10 {
		_, err := service.RunTurn(context.Background())
		if err != nil {
			require.ErrorIs(t, err, ontology.ErrBudgetDeferred)
			break
		}
	}
	status, err := service.Status(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.BudgetDeferred)
	require.False(t, status.LatestRun.Retryable)
	require.Zero(t, calls())
	require.False(t, status.CoverageComplete)
	for range 8 {
		_, err := service.RunTurn(context.Background())
		require.NoError(t, err)
	}
	require.Zero(t, calls())
}
