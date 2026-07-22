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
)

func TestControlPortalV2MigrationStatusAndActions(t *testing.T) {
	migration := &controlMigrationSvc{
		status: &domain.V2MigrationControlStatus{
			State:            domain.V2MigrationStateRequired,
			Required:         true,
			ReadinessMessage: "legacy migration is required",
		},
	}
	server, err := NewControlPortalServerWithMetricsAndTelemetry(
		&config.Config{ControlPortalToken: "secret"},
		&controlProfileSvc{},
		&controlKeySvc{},
		nil,
		ControlPortalTelemetry{Migration: migration, PortalMode: "migration", LegacyConfig: true},
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
		"remote_ip": "198.51.100.99",
		"postgres_backup_reference": "pg-backup-20260717",
		"postgres_backup_created": true,
		"neo4j_snapshot_reference": "neo4j-snapshot-20260717",
		"neo4j_snapshot_created": true
	}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "control_portal:authorization-bearer", migration.lastReq.Actor)
	require.Equal(t, "pg-backup-20260717", migration.lastReq.PostgresBackupReference)
	require.Equal(t, "192.0.2.1", migration.lastReq.RemoteIP)

	rec = controlMigrationRequest(server, http.MethodPost, "/control/api/v2/migration/start", `{"reason":"begin"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "begin", migration.lastReq.Reason)

	rec = controlMigrationRequest(server, http.MethodPost, "/control/api/v2/migration/run-once", `{}`)
	require.Equal(t, http.StatusNotFound, rec.Code)

	rec = controlMigrationRequest(server, http.MethodPost, "/control/api/v2/migration/cutover", `{}`)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestControlPortalV2MigrationValidationAndUnavailable(t *testing.T) {
	server, err := NewControlPortalServerWithMetricsAndTelemetry(
		&config.Config{ControlPortalToken: "secret"},
		&controlProfileSvc{},
		&controlKeySvc{},
		nil,
		ControlPortalTelemetry{PortalMode: "migration"},
		HealthConfig{},
		nil,
	)
	require.NoError(t, err)
	rec := controlMigrationRequest(server, http.MethodGet, "/control/api/v2/migration", "")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	rec = controlMigrationRequest(server, http.MethodPost, "/control/api/v2/migration/run-once", `{}`)
	require.Equal(t, http.StatusNotFound, rec.Code)

	migration := &controlMigrationSvc{err: migrationcontrol.ErrPreflightRequired}
	server, err = NewControlPortalServerWithMetricsAndTelemetry(
		&config.Config{ControlPortalToken: "secret"},
		&controlProfileSvc{},
		&controlKeySvc{},
		nil,
		ControlPortalTelemetry{Migration: migration, PortalMode: "migration"},
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
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestControlPortalV2CleanupModeOnlyExposesSessionAndMigrationStatus(t *testing.T) {
	migration := &controlMigrationSvc{
		status: &domain.V2MigrationControlStatus{
			State:            domain.V2MigrationStateCutOver,
			Required:         false,
			DataPlaneAllowed: true,
			ReadinessMessage: "compatible V2 authority marker present",
		},
	}
	server, err := NewControlPortalServerWithMetricsAndTelemetry(
		&config.Config{ControlPortalToken: "secret"},
		&controlProfileSvc{},
		&controlKeySvc{},
		nil,
		ControlPortalTelemetry{Migration: migration, PortalMode: "cleanup", LegacyConfig: true},
		HealthConfig{},
		nil,
	)
	require.NoError(t, err)

	rec := controlMigrationRequest(server, http.MethodGet, "/control/api/session", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var session map[string]map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &session))
	require.Equal(t, "cleanup", session["data"]["portal_mode"])
	require.Equal(t, true, session["data"]["legacy_config_present"])

	rec = controlMigrationRequest(server, http.MethodGet, "/control/api/v2/migration", "")
	require.Equal(t, http.StatusOK, rec.Code)
	rec = controlMigrationRequest(server, http.MethodPost, "/control/api/v2/migration/start", `{}`)
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec = controlMigrationRequest(server, http.MethodGet, "/control/api/teams", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func controlMigrationRequest(server http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Real-IP", "203.0.113.7")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

type controlMigrationSvc struct {
	status      *domain.V2MigrationControlStatus
	lastReq     migrationcontrol.OperatorRequest
	lastCutover migrationcontrol.CutoverRequest
	err         error
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

func (s *controlMigrationSvc) Cutover(_ context.Context, req migrationcontrol.CutoverRequest) (*domain.V2MigrationControlStatus, error) {
	s.lastCutover = req
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
