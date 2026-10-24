//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	"github.com/markhuangai/dense-mem/internal/settings"
	"github.com/stretchr/testify/require"
)

func maintenanceServiceFixture(t *testing.T, f *ontologyFixture, config *settings.AppConfigServiceImpl, edit func(assessment.Request, *assessment.Response), audits ...organization.MaintenanceAuditPreparer) (*organization.MaintenanceService, func() int32) {
	t.Helper()
	var audit organization.MaintenanceAuditPreparer
	if len(audits) > 0 {
		audit = audits[0]
	}
	var counters []*atomic.Int32
	var countersMu sync.Mutex
	service := organization.NewMaintenanceService(organization.MaintenanceDependencies{Repository: f.store, Config: config, Audit: audit, DefaultModel: "fixture-model", ProviderTimeout: time.Minute, Organizer: func(model string, contextWindowTokens int64, accounting assessment.AttemptAccounting) *organization.Service {
		if accounting == nil {
			return organization.NewService(nil, assessment.NewProvider(nil, model, maintenanceFixtureLimits(), int(contextWindowTokens)))
		}
		organizer, calls := organizationFixtureServiceWithAccounting(t, f, nil, edit, model, maintenanceFixtureLimits(), accounting, int(contextWindowTokens))
		countersMu.Lock()
		counters = append(counters, calls)
		countersMu.Unlock()
		return organizer
	}})
	return service, func() int32 {
		countersMu.Lock()
		defer countersMu.Unlock()
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
	decode(request(http.MethodGet, "/ontology/status", ""), &status)
	require.Equal(t, http.StatusNotFound, request(http.MethodPost, "/ontology/allowances", `{"operation_key":"retired"}`).Code)
	require.Equal(t, http.StatusUnprocessableEntity, request(http.MethodPatch, "/config/ontology-maintenance", `{"items":[{"key":"ONTOLOGY_MAINTENANCE_INPUT_TOKENS","value":"1"}]}`).Code)

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
	require.Greater(t, status.Usage.UnavailableUsageAttempts, int64(0))
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

func TestOntologyMaintenanceCommandAuditIsAtomic(t *testing.T) {
	f := newOrganizationFixture(t)
	config := maintenanceSettings(t, f)
	service, _ := maintenanceServiceFixture(t, f, config, nil, auditapp.New(auditpostgres.NewStore(f.app, f.rls)))
	ctx := context.Background()
	assertRolledBack := func(t *testing.T, operationKey string, paused bool) {
		t.Helper()
		var storedPaused bool
		require.NoError(t, f.admin.Raw(`SELECT paused FROM ontology_maintenance_state WHERE singleton`).Row().Scan(&storedPaused))
		require.Equal(t, paused, storedPaused)
		var commands int
		require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_runs WHERE operation_key=?`, operationKey).Row().Scan(&commands))
		require.Zero(t, commands, "failed audit must not leave a durable operator command")
	}
	t.Run("database_failure", func(t *testing.T) {
		require.NoError(t, f.admin.Exec(`CREATE FUNCTION reject_maintenance_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.operation='ONTOLOGY_MAINTENANCE_COMMAND' THEN RAISE EXCEPTION 'maintenance audit fixture failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER reject_maintenance_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_maintenance_audit()`).Error)
		defer func() {
			require.NoError(t, f.admin.Exec(`DROP TRIGGER reject_maintenance_audit ON audit_log; DROP FUNCTION reject_maintenance_audit()`).Error)
		}()
		maintenanceWindow(t, f, config)
		for _, action := range []string{"pause", "resume", "run"} {
			paused := action == "resume"
			require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_state SET paused=? WHERE singleton`, paused).Error)
			key := "audit-failure-" + action
			command := domain.OntologyMaintenanceCommand{Action: action, OperationKey: key}
			_, err := service.Command(ctx, command, "192.0.2.20", "atomic-audit")
			require.Error(t, err)
			var auditFailure *organization.OrganizationError
			require.ErrorAs(t, err, &auditFailure)
			require.Equal(t, "audit_unavailable", auditFailure.Code)
			assertRolledBack(t, key, paused)
			status, err := service.Status(ctx)
			require.NoError(t, err)
			require.Zero(t, status.Usage.Attempts)
		}
	})
	t.Run("cancellation_during_audit", func(t *testing.T) {
		require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_state SET paused=false WHERE singleton`).Error)
		require.NoError(t, f.admin.Exec(`CREATE FUNCTION block_maintenance_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.operation='ONTOLOGY_MAINTENANCE_COMMAND' THEN PERFORM pg_advisory_xact_lock(243541); END IF; RETURN NEW; END $$;
		CREATE TRIGGER block_maintenance_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION block_maintenance_audit()`).Error)
		defer func() {
			require.NoError(t, f.admin.Exec(`DROP TRIGGER block_maintenance_audit ON audit_log; DROP FUNCTION block_maintenance_audit()`).Error)
		}()
		db, err := f.admin.DB()
		require.NoError(t, err)
		blocker, err := db.Conn(ctx)
		require.NoError(t, err)
		defer blocker.Close()
		_, err = blocker.ExecContext(ctx, `SELECT pg_advisory_lock(243541)`)
		require.NoError(t, err)
		defer func() {
			_, err := blocker.ExecContext(ctx, `SELECT pg_advisory_unlock(243541)`)
			require.NoError(t, err)
		}()
		commandCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		finished := make(chan error, 1)
		go func() {
			_, err := service.Command(commandCtx, domain.OntologyMaintenanceCommand{Action: "pause", OperationKey: "cancel-audit"}, "192.0.2.20", "atomic-audit")
			finished <- err
		}()
		require.Eventually(t, func() bool {
			var blocked bool
			err := f.admin.Raw(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%INSERT INTO audit_log%')`).Row().Scan(&blocked)
			return err == nil && blocked
		}, 5*time.Second, 10*time.Millisecond)
		cancel()
		select {
		case err := <-finished:
			require.Error(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("command did not cancel while its audit insert was blocked")
		}
		assertRolledBack(t, "cancel-audit", false)
	})
	require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_state SET paused=false WHERE singleton`).Error)
	command := domain.OntologyMaintenanceCommand{Action: "pause", OperationKey: "successful-atomic-audit"}
	first, err := service.Command(ctx, command, "192.0.2.20", "atomic-audit")
	require.NoError(t, err)
	replay, err := service.Command(ctx, command, "192.0.2.20", "atomic-audit")
	require.NoError(t, err)
	require.Equal(t, first, replay)
	var audits int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM audit_log WHERE operation='ONTOLOGY_MAINTENANCE_COMMAND' AND entity_id=? AND metadata->>'operation_key'=? AND actor_role='control' AND client_ip='192.0.2.20' AND correlation_id='atomic-audit'`, first.ID, command.OperationKey).Row().Scan(&audits))
	require.Equal(t, 2, audits, "both accepted requests retain their audit entry and one idempotent command result")
}

func maintenanceFixtureLimits() assessor.SemanticAssessmentLimits {
	limits := assessor.DefaultSemanticAssessmentLimits()
	limits.MaxOutputTokens = 4096
	return limits
}

func TestOntologyMaintenanceProviderFailureReleasesCapacity(t *testing.T) {
	for _, mode := range []string{"throttled", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := newOrganizationFixture(t)
			f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
			configured := maintenanceSettings(t, f)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var failed atomic.Bool
			failed.Store(true)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if failed.Load() {
					if mode == "cancelled" {
						// HTTP/1 observes client disconnects after the request body has been consumed.
						_, err := io.Copy(io.Discard, r.Body)
						require.NoError(t, err)
						cancel()
						<-r.Context().Done()
						return
					}
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				var envelope struct {
					Messages []struct{ Role, Content string }
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&envelope))
				var input assessment.Request
				require.NoError(t, json.Unmarshal([]byte(envelope.Messages[1].Content), &input))
				body, err := json.Marshal(organizationFixtureResponse(input, nil))
				require.NoError(t, err)
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(body)}}}}))
			}))
			t.Cleanup(server.Close)
			limits := maintenanceFixtureLimits()
			cfg := &config.Config{AIVerifierAPIURL: server.URL, AIVerifierAPIKey: "synthetic-provider-key", AIVerifierModel: "fixture-model"}
			transport := assessorprovider.NewOpenAIAssessorWithAssessmentLimits(cfg, server.Client(), limits)
			service := organization.NewMaintenanceService(organization.MaintenanceDependencies{Repository: f.store, Config: configured, ProviderTimeout: time.Minute,
				Organizer: func(model string, capacity int64, accounting assessment.AttemptAccounting) *organization.Service {
					return organization.NewService(f.store, assessment.NewProviderWithAccounting(transport, model, limits, accounting, int(capacity)))
				}})
			before := f.canonicalSnapshot(t)
			var failure error
			for range 10 {
				_, failure = service.RunTurn(ctx)
				if failure != nil {
					break
				}
			}
			require.Error(t, failure)
			if mode == "cancelled" {
				require.ErrorIs(t, failure, context.Canceled)
			} else {
				require.Equal(t, modelprovider.ProviderFailureClassRateLimited, modelprovider.ProviderFailureDetails(failure).Class)
			}
			require.EqualValues(t, 1, calls.Load())
			status, err := service.Status(context.Background())
			require.NoError(t, err)
			require.Zero(t, status.ActiveWork)
			require.Zero(t, status.Usage.UnreconciledAttempts)
			require.EqualValues(t, 1, status.Usage.UnavailableUsageAttempts)
			require.EqualValues(t, 1, status.Counts.Failed)
			require.Equal(t, before, f.canonicalSnapshot(t))
			failed.Store(false)
			other := maintenanceOtherTeam(t, f)
			other.organizationEvidence(t, 0, "Beacon uses SQLite.", nil)
			otherBefore := other.canonicalSnapshot(t)
			drainMaintenance(t, service)
			status, err = service.Status(context.Background())
			require.NoError(t, err)
			require.EqualValues(t, 1, status.Counts.Failed)
			require.Positive(t, status.Counts.Organized)
			require.Greater(t, calls.Load(), int32(1))
			require.Equal(t, before, f.canonicalSnapshot(t))
			require.Equal(t, otherBefore, other.canonicalSnapshot(t))
		})
	}
}

func TestOntologyMaintenanceDirectOrganizationRejectsMissingContext(t *testing.T) {
	f := newOrganizationFixture(t)
	source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
	before := f.canonicalSnapshot(t)
	for _, capacity := range []int{0, 1} {
		service := organization.NewService(f.store, assessment.NewProvider(nil, "fixture-model", maintenanceFixtureLimits(), capacity))
		_, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: uuid.NewString(), Sources: []ontology.SourceHandle{source}})
		require.ErrorIs(t, err, ontology.ErrConfiguration)
		require.Zero(t, receiptCount(t, f))
		require.Equal(t, before, f.canonicalSnapshot(t))
	}
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
	require.Greater(t, status.Usage.UnavailableUsageAttempts, int64(0))
	for range 8 {
		_, err := service.RunTurn(ctx)
		require.NoError(t, err)
	}
	require.EqualValues(t, 3, calls(), "unchanged failed sources require explicit retry")
	broken = false
	var previousRun string
	require.NoError(t, f.admin.Raw(`SELECT last_run_id::text FROM ontology_maintenance_sources WHERE team_id=?::uuid AND status='failed'`, f.team).Row().Scan(&previousRun))
	command := domain.OntologyMaintenanceCommand{Action: "retry", OperationKey: "retry-failed", RetryRunID: previousRun, MaxBatches: 1}
	run, err := f.store.MaintenanceCommand(ctx, command, time.Now().UTC(), nil)
	require.NoError(t, err)
	replayed, err := f.store.MaintenanceCommand(ctx, command, time.Now().UTC(), nil)
	require.NoError(t, err)
	require.Equal(t, run.ID, replayed.ID)
	drainMaintenance(t, service)
	status, err = service.Status(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.Organized)
	require.Zero(t, status.Counts.Failed)
	require.Equal(t, run.WindowID, status.Window.ID)
	require.EqualValues(t, 4, status.Usage.UnavailableUsageAttempts)
}

func TestOntologyMaintenanceProviderPauseDrain(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
	config := maintenanceSettings(t, f)
	service, calls := maintenanceServiceFixture(t, f, config, func(_ assessment.Request, _ *assessment.Response) {
		_, err := f.store.MaintenanceCommand(context.Background(), domain.OntologyMaintenanceCommand{Action: "pause", OperationKey: "pause-during-provider"}, time.Now().UTC(), nil)
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

func TestOntologyMaintenanceProviderDisableDrain(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
	config := maintenanceSettings(t, f)
	service, calls := maintenanceServiceFixture(t, f, config, func(_ assessment.Request, _ *assessment.Response) {
		_, err := config.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{domain.AppConfigOntologyEnabled: "false"}, "control", "", "")
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
	require.False(t, status.PendingPolicy.Enabled)
	require.EqualValues(t, 1, status.Counts.Organized)
	progress, err := service.RunTurn(context.Background())
	require.NoError(t, err)
	require.False(t, progress)
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
	require.Greater(t, status.Usage.UnreconciledAttempts, int64(0))
	require.False(t, status.CoverageComplete)
}

func TestOntologyMaintenanceContextConfigurationAndManualReuse(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
	config := maintenanceSettings(t, f)
	ctx := context.Background()
	_, err := config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyContextWindow: ""}, "control", "", "")
	require.NoError(t, err)
	service, calls := maintenanceServiceFixture(t, f, config, nil, auditapp.New(auditpostgres.NewStore(f.app, f.rls)))
	for _, value := range []string{"", "1"} {
		_, err := config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyContextWindow: value}, "control", "", "")
		require.NoError(t, err)
		progress, err := service.RunTurn(ctx)
		require.ErrorIs(t, err, ontology.ErrConfiguration)
		require.False(t, progress)
		_, err = service.Command(ctx, domain.OntologyMaintenanceCommand{OperationKey: uuid.NewString(), Action: "run"}, "", "")
		require.ErrorIs(t, err, ontology.ErrConfiguration)
		status, err := service.Status(ctx)
		require.NoError(t, err)
		require.Equal(t, "configuration_invalid", status.ConfigurationError)
		require.Nil(t, status.Window)
		require.Zero(t, status.Counts.Failed)
		require.Zero(t, status.Usage.Attempts)
		require.Zero(t, calls())
	}
	_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyContextWindow: "300000"}, "control", "", "")
	require.NoError(t, err)
	manual, err := service.Command(ctx, domain.OntologyMaintenanceCommand{OperationKey: uuid.NewString(), Action: "run"}, "", "")
	require.NoError(t, err)
	drainMaintenance(t, service)
	status, err := service.Status(ctx)
	require.NoError(t, err)
	require.Empty(t, status.ConfigurationError)
	require.Equal(t, manual.WindowID, status.Window.ID)
	require.EqualValues(t, 1, status.Counts.Organized)
	require.Positive(t, calls())
}
