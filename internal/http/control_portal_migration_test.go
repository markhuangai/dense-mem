package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/migrationcontrol"
	"github.com/markhuangai/dense-mem/internal/service/migrationexecutor"
)

func TestControlPortalV2MigrationStatusAndActions(t *testing.T) {
	migration := &controlMigrationSvc{
		status: &domain.V2MigrationControlStatus{
			State:            domain.V2MigrationStateRequired,
			Required:         true,
			ReadinessMessage: "legacy migration is required",
		},
	}
	executor := &controlMigrationExec{
		result: &migrationexecutor.RunOnceResult{
			RunID:     "run-1",
			Fetched:   1,
			Submitted: 1,
		},
	}
	server, err := NewControlPortalServerWithMetricsAndTelemetry(
		&config.Config{ControlPortalToken: "secret"},
		&controlProfileSvc{},
		&controlKeySvc{},
		nil,
		ControlPortalTelemetry{Migration: migration, MigrationExec: executor},
		HealthConfig{},
		nil,
	)
	require.NoError(t, err)

	rec := controlMigrationRequest(server, http.MethodGet, "/control/api/v2/migration", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, domain.V2MigrationStateRequired, body["data"]["state"])

	rec = controlMigrationRequest(server, http.MethodPost, "/control/api/v2/migration/preflight", `{
		"actor": "operator",
		"backup_reference": "backup-20260717",
		"preflight_checks": {
			"postgres_restore_verified": true,
			"neo4j_snapshot_verified": true
		}
	}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "operator", migration.lastReq.Actor)
	require.Equal(t, "backup-20260717", migration.lastReq.BackupReference)
	require.NotEmpty(t, migration.lastReq.RemoteIP)

	rec = controlMigrationRequest(server, http.MethodPost, "/control/api/v2/migration/start", `{"reason":"begin"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "begin", migration.lastReq.Reason)

	rec = controlMigrationRequest(server, http.MethodPost, "/control/api/v2/migration/finalize-gates", `{
		"actor": "operator",
		"corpus_hash": "sha256:corpus",
		"gate_report_hash": "sha256:gates",
		"gates": [{
			"gate_name": "backup_restore",
			"outcome": "pass",
			"evidence_ref": "local://gate/backup_restore",
			"evidence_hash": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"gate_version": "dense-mem.gate.test.v1"
		}]
	}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "sha256:gates", migration.lastGateReq.GateReportHash)
	require.NotEmpty(t, migration.lastGateReq.RemoteIP)

	rec = controlMigrationRequest(server, http.MethodPost, "/control/api/v2/migration/cutover", `{
		"actor": "operator",
		"corpus_hash": "sha256:corpus",
		"gate_report_hash": "sha256:gates"
	}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "sha256:gates", migration.lastCutoverReq.GateReportHash)
	require.NotEmpty(t, migration.lastCutoverReq.RemoteIP)

	rec = controlMigrationRequest(server, http.MethodPost, "/control/api/v2/migration/run-once", `{}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, executor.calls)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "run-1", body["data"]["run_id"])
}

func TestControlPortalV2MigrationValidationAndUnavailable(t *testing.T) {
	server, err := NewControlPortalServerWithMetricsAndTelemetry(
		&config.Config{ControlPortalToken: "secret"},
		&controlProfileSvc{},
		&controlKeySvc{},
		nil,
		ControlPortalTelemetry{},
		HealthConfig{},
		nil,
	)
	require.NoError(t, err)
	rec := controlMigrationRequest(server, http.MethodGet, "/control/api/v2/migration", "")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	rec = controlMigrationRequest(server, http.MethodPost, "/control/api/v2/migration/run-once", `{}`)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	migration := &controlMigrationSvc{err: migrationcontrol.ErrPreflightRequired}
	executor := &controlMigrationExec{err: migrationexecutor.ErrMigrationNotRunning}
	server, err = NewControlPortalServerWithMetricsAndTelemetry(
		&config.Config{ControlPortalToken: "secret"},
		&controlProfileSvc{},
		&controlKeySvc{},
		nil,
		ControlPortalTelemetry{Migration: migration, MigrationExec: executor},
		HealthConfig{},
		nil,
	)
	require.NoError(t, err)
	rec = controlMigrationRequest(server, http.MethodPost, "/control/api/v2/migration/start", `{}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	migration.err = errors.New("store failed")
	rec = controlMigrationRequest(server, http.MethodPost, "/control/api/v2/migration/start", `{}`)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	rec = controlMigrationRequest(server, http.MethodPost, "/control/api/v2/migration/start", `{`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	rec = controlMigrationRequest(server, http.MethodPost, "/control/api/v2/migration/run-once", `{}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func controlMigrationRequest(server http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

type controlMigrationSvc struct {
	status         *domain.V2MigrationControlStatus
	lastReq        migrationcontrol.OperatorRequest
	lastGateReq    migrationcontrol.GateReportRequest
	lastCutoverReq migrationcontrol.CutoverRequest
	err            error
}

func (s *controlMigrationSvc) Status(context.Context) (*domain.V2MigrationControlStatus, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.status != nil {
		return s.status, nil
	}
	return &domain.V2MigrationControlStatus{State: domain.V2MigrationStateReady}, nil
}

func (s *controlMigrationSvc) ApprovePreflight(_ context.Context, req migrationcontrol.OperatorRequest) (*domain.V2MigrationControlStatus, error) {
	return s.action(req)
}

func (s *controlMigrationSvc) Start(_ context.Context, req migrationcontrol.OperatorRequest) (*domain.V2MigrationControlStatus, error) {
	return s.action(req)
}

func (s *controlMigrationSvc) Pause(_ context.Context, req migrationcontrol.OperatorRequest) (*domain.V2MigrationControlStatus, error) {
	return s.action(req)
}

func (s *controlMigrationSvc) Resume(_ context.Context, req migrationcontrol.OperatorRequest) (*domain.V2MigrationControlStatus, error) {
	return s.action(req)
}

func (s *controlMigrationSvc) FinalizeGates(_ context.Context, req migrationcontrol.GateReportRequest) (*domain.V2MigrationControlStatus, error) {
	s.lastGateReq = req
	if s.err != nil {
		return nil, s.err
	}
	return &domain.V2MigrationControlStatus{State: domain.V2MigrationStateReadyCutover}, nil
}

func (s *controlMigrationSvc) CommitCutover(_ context.Context, req migrationcontrol.CutoverRequest) (*domain.V2MigrationControlStatus, error) {
	s.lastCutoverReq = req
	if s.err != nil {
		return nil, s.err
	}
	return &domain.V2MigrationControlStatus{State: domain.V2MigrationStateCutOver}, nil
}

func (s *controlMigrationSvc) action(req migrationcontrol.OperatorRequest) (*domain.V2MigrationControlStatus, error) {
	s.lastReq = req
	if s.err != nil {
		return nil, s.err
	}
	return &domain.V2MigrationControlStatus{State: domain.V2MigrationStateRunning}, nil
}

type controlMigrationExec struct {
	result *migrationexecutor.RunOnceResult
	err    error
	calls  int
}

func (s *controlMigrationExec) RunOnce(context.Context) (*migrationexecutor.RunOnceResult, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}
