package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service"
)

type controlProfileSvc struct {
	profiles  []*domain.Profile
	deleted   uuid.UUID
	listErr   error
	countErr  error
	createErr error
	updateErr error
	deleteErr error
}

func (s *controlProfileSvc) Create(_ context.Context, req service.CreateProfileRequest, _ *string, _ string, _ string, _ string) (*domain.Profile, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	profile := &domain.Profile{
		ID:          uuid.New(),
		Name:        req.Name,
		Description: req.Description,
		Metadata:    req.Metadata,
		Config:      req.Config,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	s.profiles = append(s.profiles, profile)
	return profile, nil
}

func (s *controlProfileSvc) Get(_ context.Context, id uuid.UUID) (*domain.Profile, error) {
	for _, profile := range s.profiles {
		if profile.ID == id {
			return profile, nil
		}
	}
	return nil, nil
}

func (s *controlProfileSvc) GetByID(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
	return s.Get(ctx, id)
}

func (s *controlProfileSvc) List(context.Context, int, int) ([]*domain.Profile, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.profiles, nil
}

func (s *controlProfileSvc) Count(context.Context) (int64, error) {
	if s.countErr != nil {
		return 0, s.countErr
	}
	return int64(len(s.profiles)), nil
}

func (s *controlProfileSvc) Update(_ context.Context, id uuid.UUID, req service.UpdateProfileRequest, _ *string, _ string, _ string, _ string) (*domain.Profile, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	for _, profile := range s.profiles {
		if profile.ID == id {
			if req.Name != nil {
				profile.Name = *req.Name
			}
			if req.Description != nil {
				profile.Description = *req.Description
			}
			profile.Metadata = req.Metadata
			profile.Config = req.Config
			profile.UpdatedAt = time.Now().UTC()
			return profile, nil
		}
	}
	return nil, nil
}

func (s *controlProfileSvc) Delete(_ context.Context, id uuid.UUID, _ *string, _ string, _ string, _ string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deleted = id
	return nil
}

type controlKeySvc struct {
	keys          []*domain.APIKey
	rawKey        string
	deletedKey    uuid.UUID
	lastCreateReq service.CreateAPIKeyRequest
	listErr       error
	countErr      error
	createErr     error
	updateErr     error
	rotateErr     error
	deleteErr     error
}

func (s *controlKeySvc) CreateStandardKey(_ context.Context, profileID uuid.UUID, req service.CreateAPIKeyRequest, _ *string, _ string, _ string, _ string) (*domain.APIKey, string, error) {
	if s.createErr != nil {
		return nil, "", s.createErr
	}
	scopes, err := service.NormalizeAPIKeyScopes(req.Scopes)
	if err != nil {
		return nil, "", err
	}
	s.lastCreateReq = req
	key := &domain.APIKey{
		ID:        uuid.New(),
		ProfileID: profileID,
		TeamID:    profileID,
		Label:     req.Name,
		Name:      req.Name,
		KeyPrefix: "dm_test",
		KeySuffix: "intext",
		Scopes:    scopes,
		RateLimit: req.RateLimit,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: req.ExpiresAt,
	}
	s.rawKey = "dm_test_plaintext"
	s.keys = append(s.keys, key)
	return key, s.rawKey, nil
}

func (s *controlKeySvc) RotateForProfile(_ context.Context, profileID, id uuid.UUID, req service.CreateAPIKeyRequest, _ *string, _ string, _ string, _ string) (*domain.APIKey, string, error) {
	if s.rotateErr != nil {
		return nil, "", s.rotateErr
	}
	for _, key := range s.keys {
		if key.ProfileID == profileID && key.ID == id {
			key.Name = req.Name
			key.Label = req.Name
			key.KeySuffix = "rot8ed"
			key.ExpiresAt = req.ExpiresAt
			return key, "dm_rotated_plaintext", nil
		}
	}
	return nil, "", nil
}

func (s *controlKeySvc) UpdateNameForProfile(_ context.Context, profileID, id uuid.UUID, name string, _ *string, _ string, _ string, _ string) (*domain.APIKey, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	for _, key := range s.keys {
		if key.ProfileID == profileID && key.ID == id {
			key.Name = name
			key.Label = name
			return key, nil
		}
	}
	return nil, nil
}

func (s *controlKeySvc) UpdateRoleForProfile(_ context.Context, profileID, id uuid.UUID, role string, _ *string, _ string, _ string, _ string) (*domain.APIKey, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	for _, key := range s.keys {
		if key.ProfileID == profileID && key.ID == id {
			key.Role = role
			return key, nil
		}
	}
	return nil, nil
}

func (s *controlKeySvc) ListByProfile(_ context.Context, profileID uuid.UUID, _ int, _ int) ([]*domain.APIKey, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]*domain.APIKey, 0, len(s.keys))
	for _, key := range s.keys {
		if key.ProfileID == profileID {
			out = append(out, key)
		}
	}
	return out, nil
}

func (s *controlKeySvc) CountByProfile(ctx context.Context, profileID uuid.UUID) (int64, error) {
	if s.countErr != nil {
		return 0, s.countErr
	}
	keys, err := s.ListByProfile(ctx, profileID, 0, 0)
	return int64(len(keys)), err
}

func (s *controlKeySvc) GetByIDForProfile(_ context.Context, profileID, id uuid.UUID) (*domain.APIKey, error) {
	for _, key := range s.keys {
		if key.ProfileID == profileID && key.ID == id {
			return key, nil
		}
	}
	return nil, nil
}

func (s *controlKeySvc) RevokeForProfile(_ context.Context, _ uuid.UUID, id uuid.UUID, _ *string, _ string, _ string, _ string) error {
	return nil
}

func (s *controlKeySvc) DeleteForProfile(_ context.Context, profileID uuid.UUID, id uuid.UUID, _ *string, _ string, _ string, _ string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deletedKey = id
	next := make([]*domain.APIKey, 0, len(s.keys))
	for _, key := range s.keys {
		if key.ProfileID == profileID && key.ID == id {
			continue
		}
		next = append(next, key)
	}
	s.keys = next
	return nil
}

func testControlServer(t *testing.T) (*controlProfileSvc, *controlKeySvc, http.Handler) {
	t.Helper()
	profiles := &controlProfileSvc{profiles: []*domain.Profile{{
		ID:        uuid.New(),
		Name:      "Default",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}}
	keys := &controlKeySvc{}
	e, err := NewControlPortalServer(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, profiles, keys, nil)
	require.NoError(t, err)
	return profiles, keys, e
}

type controlMetricsSvc struct {
	snapshot *domain.UsageMetricsSnapshot
	filter   domain.UsageMetricsFilter
	err      error
}

func (s *controlMetricsSvc) Snapshot(_ context.Context, filter domain.UsageMetricsFilter) (*domain.UsageMetricsSnapshot, error) {
	s.filter = filter
	if s.err != nil {
		return nil, s.err
	}
	if s.snapshot != nil {
		return s.snapshot, nil
	}
	return &domain.UsageMetricsSnapshot{}, nil
}

type controlTelemetrySvc struct {
	snapshot *service.TelemetrySnapshot
	filter   service.TelemetryFilter
	err      error
}

func (s *controlTelemetrySvc) Snapshot(_ context.Context, filter service.TelemetryFilter) (*service.TelemetrySnapshot, error) {
	s.filter = filter
	if s.err != nil {
		return nil, s.err
	}
	if s.snapshot != nil {
		return s.snapshot, nil
	}
	return &service.TelemetrySnapshot{Available: true}, nil
}

type controlSecuritySvc struct {
	settings       domain.SecuritySettings
	bans           []domain.SecurityIPBan
	includeExpired bool
	limit          int
	offset         int
	createdIP      string
	createdReason  string
	createdExpiry  *time.Time
	deletedIP      string
	authFailures   int
	settingsErr    error
	updateErr      error
	listErr        error
	createErr      error
	deleteErr      error
}

func newControlSecuritySvc(now time.Time) *controlSecuritySvc {
	expiresAt := now.Add(time.Hour)
	lastFailedAt := now.Add(-time.Minute)
	return &controlSecuritySvc{
		settings: domain.SecuritySettings{
			Enabled:              true,
			FailureThreshold:     5,
			FailureWindowSeconds: 300,
			BanDurationSeconds:   600,
			UpdatedAt:            now,
		},
		bans: []domain.SecurityIPBan{{
			IP:           "203.0.113.10",
			Reason:       "manual",
			Source:       domain.SecurityBanSourceManual,
			FailureCount: 0,
			BannedAt:     now,
			ExpiresAt:    &expiresAt,
			LastFailedAt: &lastFailedAt,
			Metadata:     map[string]any{"actor": "control"},
		}},
	}
}

func (s *controlSecuritySvc) CheckBan(context.Context, string) (*domain.SecurityIPBan, error) {
	return nil, nil
}

func (s *controlSecuritySvc) RecordAuthFailure(context.Context, string, string, string) (*domain.SecurityIPBan, error) {
	s.authFailures++
	return nil, nil
}

func (s *controlSecuritySvc) GetSecuritySettings(context.Context) (*domain.SecuritySettings, error) {
	if s.settingsErr != nil {
		return nil, s.settingsErr
	}
	settings := s.settings
	return &settings, nil
}

func (s *controlSecuritySvc) UpdateSecuritySettings(_ context.Context, settings domain.SecuritySettings, _, _, _ string) (*domain.SecuritySettings, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	if settings.FailureThreshold <= 0 {
		return nil, service.ErrInvalidSecuritySettings
	}
	s.settings = settings
	return &s.settings, nil
}

func (s *controlSecuritySvc) ListSecurityBans(_ context.Context, includeExpired bool, limit, offset int) ([]domain.SecurityIPBan, int64, error) {
	if s.listErr != nil {
		return nil, 0, s.listErr
	}
	s.includeExpired = includeExpired
	s.limit = limit
	s.offset = offset
	return s.bans, int64(len(s.bans)), nil
}

func (s *controlSecuritySvc) CreateManualSecurityBan(_ context.Context, ip, reason string, expiresAt *time.Time, _, _, _ string) (*domain.SecurityIPBan, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	s.createdIP = ip
	s.createdReason = reason
	s.createdExpiry = expiresAt
	ban := domain.SecurityIPBan{
		IP:        ip,
		Reason:    reason,
		Source:    domain.SecurityBanSourceManual,
		BannedAt:  time.Now().UTC(),
		ExpiresAt: expiresAt,
		Metadata:  map[string]any{"actor": "control"},
	}
	s.bans = append(s.bans, ban)
	return &ban, nil
}

func (s *controlSecuritySvc) DeleteSecurityBan(_ context.Context, ip, _, _, _ string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deletedIP = ip
	return nil
}

func TestControlPortalRejectsUnsafeConfig(t *testing.T) {
	_, err := NewControlPortalServer(&config.Config{
		ControlHTTPAddr: "127.0.0.1:8090",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil)
	require.ErrorContains(t, err, "token")
}

func TestControlPortalAuthAndOrigin(t *testing.T) {
	_, _, server := testControlServer(t)

	req := httptest.NewRequest(http.MethodGet, "/control/api/session", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	req = httptest.NewRequest(http.MethodOptions, "/control/api/session", nil)
	req.Header.Set("Origin", "http://192.168.1.253:8090")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, "http://192.168.1.253:8090", rec.Header().Get("Access-Control-Allow-Origin"))

	req = httptest.NewRequest(http.MethodGet, "/control/api/session", nil)
	req.Header.Set("X-Control-Portal-Token", "secret")
	req.Header.Set("Origin", "https://example.com")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "https://example.com", rec.Header().Get("Access-Control-Allow-Origin"))
}

func TestControlPortalMetrics(t *testing.T) {
	profiles := &controlProfileSvc{}
	keys := &controlKeySvc{}
	metrics := &controlMetricsSvc{snapshot: &domain.UsageMetricsSnapshot{
		System: domain.UsageMetricTotal{Requests: 3, Errors: 1, AvgLatencyMS: 12.5, MaxLatencyMS: 40},
		Teams:  []domain.UsageTeamMetric{{TeamID: uuid.New(), TeamName: "Default", UsageMetricTotal: domain.UsageMetricTotal{Requests: 3}}},
	}}
	health := HealthConfig{Checks: []HealthCheck{{
		Name: "postgres",
		Check: func(context.Context) error {
			return nil
		},
	}}}
	e, err := NewControlPortalServerWithMetrics(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, profiles, keys, metrics, health, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/control/api/metrics?window_minutes=30", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"requests":3`)
	require.Contains(t, rec.Body.String(), `"dependencies"`)
	require.Contains(t, rec.Body.String(), `"postgres"`)
	require.True(t, metrics.filter.To.After(metrics.filter.From))
}

func TestControlPortalTelemetry(t *testing.T) {
	teamID := uuid.New()
	telemetry := &controlTelemetrySvc{snapshot: &service.TelemetrySnapshot{
		Available: true,
		Window:    service.TelemetryWindow{Key: "1h"},
		Scope:     service.TelemetryScope{Type: "team", TeamID: &teamID},
		Cards:     []service.TelemetryCard{{ID: "http_requests", Label: "HTTP requests", Unit: "requests", Value: 3}},
	}}
	scrapeHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# HELP densemem_test metric\n"))
	})
	e, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{
		Reader:        telemetry,
		ScrapeHandler: scrapeHandler,
		ScrapeToken:   "scrape-secret",
	}, HealthConfig{}, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/control/api/telemetry?window=1h&scope=team&team_id="+teamID.String(), nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"available":true`)
	require.Equal(t, "1h", telemetry.filter.Window)
	require.Equal(t, "team", telemetry.filter.Scope)
	require.Equal(t, teamID, *telemetry.filter.TeamID)

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("X-Telemetry-Scrape-Token", "scrape-secret")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "densemem_test")

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer scrape-secret")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	_, err = NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{
		ScrapeHandler: scrapeHandler,
	}, HealthConfig{}, nil)
	require.ErrorContains(t, err, "telemetry scrape token is required")
}

func TestControlPortalProfileAndKeyFlows(t *testing.T) {
	profiles, keys, server := testControlServer(t)

	req := httptest.NewRequest(http.MethodGet, "/control/api/profiles", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"Default"`)

	body := `{"name":"Work Profile","description":"for work"}`
	req = httptest.NewRequest(http.MethodPost, "/control/api/profiles", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)
	require.Len(t, profiles.profiles, 2)

	profileID := profiles.profiles[1].ID
	keyBody := `{"rate_limit":120,"scopes":["read"]}`
	req = httptest.NewRequest(http.MethodPost, "/control/api/profiles/"+profileID.String()+"/api-keys", strings.NewReader(keyBody))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)
	require.Contains(t, rec.Body.String(), `"api_key":"dm_test_plaintext"`)
	require.NotContains(t, rec.Body.String(), `"key_prefix"`)
	require.Contains(t, rec.Body.String(), `"key_suffix":"intext"`)
	require.NotContains(t, rec.Body.String(), `"label"`)
	require.Contains(t, rec.Body.String(), `"scopes":["read"]`)
	require.NotContains(t, rec.Body.String(), `"revoked_at"`)
	require.Equal(t, []string{"read"}, keys.lastCreateReq.Scopes)
	require.Empty(t, keys.keys[len(keys.keys)-1].Label)

	keyID := keys.keys[len(keys.keys)-1].ID
	renameBody := `{"name":"Research Profile"}`
	req = httptest.NewRequest(http.MethodPatch, "/control/api/teams/"+profileID.String()+"/profiles/"+keyID.String(), strings.NewReader(renameBody))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"name":"Research Profile"`)
	require.Equal(t, "Research Profile", keys.keys[len(keys.keys)-1].Name)

	rotateBody := `{"name":"Research Profile","rate_limit":120}`
	req = httptest.NewRequest(http.MethodPost, "/control/api/teams/"+profileID.String()+"/profiles/"+keyID.String()+"/rotate", strings.NewReader(rotateBody))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"api_key":"dm_rotated_plaintext"`)
	require.Contains(t, rec.Body.String(), `"key_suffix":"rot8ed"`)

	req = httptest.NewRequest(http.MethodDelete, "/control/api/profiles/"+profileID.String()+"/api-keys/"+keyID.String(), nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, keyID, keys.deletedKey)
	require.Len(t, keys.keys, 0)

	req = httptest.NewRequest(http.MethodDelete, "/control/api/profiles/"+profileID.String(), nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, profileID, profiles.deleted)
}

func TestControlPortalAdditionalRouteBranches(t *testing.T) {
	profiles, keys, server := testControlServer(t)
	profileID := profiles.profiles[0].ID

	req := httptest.NewRequest(http.MethodPatch, "/control/api/teams/"+profileID.String(), strings.NewReader(`{"name":"Renamed","description":"updated"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "Renamed", profiles.profiles[0].Name)

	req = httptest.NewRequest(http.MethodPost, "/control/api/teams/"+profileID.String()+"/profiles", strings.NewReader(`{"name":"Reader","rate_limit":25}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)
	require.Len(t, keys.keys, 1)

	req = httptest.NewRequest(http.MethodGet, "/control/api/teams/"+profileID.String()+"/profiles?limit=1000&offset=0", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"name":"Reader"`)

	req = httptest.NewRequest(http.MethodGet, "/control/api/teams/not-a-uuid/profiles", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	req = httptest.NewRequest(http.MethodPost, "/control/api/teams/"+profileID.String()+"/profiles", strings.NewReader(`{"scopes":[]}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	req = httptest.NewRequest(http.MethodPost, "/control/api/teams/"+profileID.String()+"/profiles", strings.NewReader(`{"expires_at":"bad-date"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	keyID := keys.keys[0].ID
	req = httptest.NewRequest(http.MethodPost, "/control/api/teams/"+profileID.String()+"/profiles/"+keyID.String()+"/rotate", strings.NewReader(`{"scopes":["read"]}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestControlPortalMetricsErrors(t *testing.T) {
	_, _, server := testControlServer(t)
	req := httptest.NewRequest(http.MethodGet, "/control/api/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	metrics := &controlMetricsSvc{}
	e, err := NewControlPortalServerWithMetrics(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, metrics, HealthConfig{Degraded: true}, nil)
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodGet, "/control/api/metrics?window_minutes=0", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/control/api/metrics?team_id=bad", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/control/api/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"status":"degraded"`)
}

func TestControlPortalTelemetryErrors(t *testing.T) {
	_, _, server := testControlServer(t)
	req := httptest.NewRequest(http.MethodGet, "/control/api/telemetry", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	telemetry := &controlTelemetrySvc{}
	e, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{
		Reader: telemetry,
	}, HealthConfig{}, nil)
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodGet, "/control/api/telemetry?scope=bad", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/control/api/telemetry?team_id=bad", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/control/api/telemetry?profile_id=bad", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	telemetry.err = errors.New("telemetry backend failed")
	req = httptest.NewRequest(http.MethodGet, "/control/api/telemetry", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestControlPortalSecurityFlows(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	securitySvc := newControlSecuritySvc(now)
	e, err := NewControlPortalServer(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, securitySvc)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/control/api/security/settings", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"failure_threshold":5`)
	require.Contains(t, rec.Body.String(), `"updated_at":"2026-05-30T12:00:00Z"`)

	req = httptest.NewRequest(http.MethodPatch, "/control/api/security/settings", strings.NewReader(`{"enabled":false,"failure_threshold":7}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, securitySvc.settings.Enabled)
	require.Equal(t, 7, securitySvc.settings.FailureThreshold)

	req = httptest.NewRequest(http.MethodGet, "/control/api/security/bans?include_expired=yes&limit=200&offset=3", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, securitySvc.includeExpired)
	require.Equal(t, 100, securitySvc.limit)
	require.Equal(t, 3, securitySvc.offset)
	require.Contains(t, rec.Body.String(), `"ip":"203.0.113.10"`)
	require.Contains(t, rec.Body.String(), `"expires_at":"2026-05-30T13:00:00Z"`)

	expires := now.Add(2 * time.Hour).Format(time.RFC3339)
	req = httptest.NewRequest(http.MethodPost, "/control/api/security/bans", strings.NewReader(`{"ip":"203.0.113.11","reason":"manual review","expires_at":"`+expires+`"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)
	require.Equal(t, "203.0.113.11", securitySvc.createdIP)
	require.Equal(t, "manual review", securitySvc.createdReason)
	require.NotNil(t, securitySvc.createdExpiry)

	req = httptest.NewRequest(http.MethodDelete, "/control/api/security/bans/203.0.113.11", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "203.0.113.11", securitySvc.deletedIP)
}

func TestControlPortalSecurityErrorsAndAuthFailureRecording(t *testing.T) {
	securitySvc := newControlSecuritySvc(time.Now().UTC())
	e, err := NewControlPortalServer(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, securitySvc)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/control/api/security/settings", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, 1, securitySvc.authFailures)

	req = httptest.NewRequest(http.MethodPatch, "/control/api/security/settings", strings.NewReader(`{"failure_threshold":0}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	req = httptest.NewRequest(http.MethodPost, "/control/api/security/bans", strings.NewReader(`{"ip":"203.0.113.12","expires_at":"not-a-date"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	withoutSecurity, err := NewControlPortalServer(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil)
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodGet, "/control/api/security/settings", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	withoutSecurity.ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestControlPortalServiceAndValidationErrorBranches(t *testing.T) {
	_, err := NewControlPortalServerWithMetrics(nil, &controlProfileSvc{}, &controlKeySvc{}, nil, HealthConfig{}, nil)
	require.ErrorContains(t, err, "config is required")

	profileID := uuid.New()
	keyID := uuid.New()
	profiles := &controlProfileSvc{profiles: []*domain.Profile{{
		ID:        profileID,
		Name:      "Default",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}}
	keys := &controlKeySvc{keys: []*domain.APIKey{{
		ID:        keyID,
		ProfileID: profileID,
		TeamID:    profileID,
		Name:      "Key",
		KeySuffix: "suffix",
		Scopes:    []string{"read"},
		CreatedAt: time.Now().UTC(),
	}}}
	metrics := &controlMetricsSvc{err: errors.New("metrics failed")}
	securitySvc := newControlSecuritySvc(time.Now().UTC())
	server, err := NewControlPortalServerWithMetrics(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, profiles, keys, metrics, HealthConfig{Checks: []HealthCheck{{
		Name: "bad",
		Check: func(context.Context) error {
			return errors.New("dependency failed")
		},
	}, {
		Name: "nil",
	}}}, nil, securitySvc)
	require.NoError(t, err)

	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}

	require.Equal(t, http.StatusInternalServerError, do(http.MethodGet, "/control/api/metrics", "").Code)
	require.Contains(t, controlDependencySnapshot(context.Background(), HealthConfig{Checks: []HealthCheck{{
		Name: "bad",
		Check: func(context.Context) error {
			return errors.New("dependency failed")
		},
	}}})[0].Status, "error")

	require.Equal(t, http.StatusUnprocessableEntity, do(http.MethodPost, "/control/api/teams", "{").Code)
	profiles.createErr = errors.New("create failed")
	require.Equal(t, http.StatusInternalServerError, do(http.MethodPost, "/control/api/teams", `{"name":"New"}`).Code)
	profiles.createErr = nil

	profiles.listErr = errors.New("list failed")
	require.Equal(t, http.StatusInternalServerError, do(http.MethodGet, "/control/api/teams", "").Code)
	profiles.listErr = nil
	profiles.countErr = errors.New("count failed")
	require.Equal(t, http.StatusInternalServerError, do(http.MethodGet, "/control/api/teams", "").Code)
	profiles.countErr = nil

	require.Equal(t, http.StatusUnprocessableEntity, do(http.MethodPatch, "/control/api/teams/"+profileID.String(), "{").Code)
	profiles.updateErr = errors.New("update failed")
	require.Equal(t, http.StatusInternalServerError, do(http.MethodPatch, "/control/api/teams/"+profileID.String(), `{"name":"Renamed"}`).Code)
	profiles.updateErr = nil

	profiles.deleteErr = errors.New("delete failed")
	require.Equal(t, http.StatusInternalServerError, do(http.MethodDelete, "/control/api/teams/"+profileID.String(), "").Code)
	profiles.deleteErr = nil

	keys.listErr = errors.New("key list failed")
	require.Equal(t, http.StatusInternalServerError, do(http.MethodGet, "/control/api/teams/"+profileID.String()+"/profiles", "").Code)
	keys.listErr = nil
	keys.countErr = errors.New("key count failed")
	require.Equal(t, http.StatusInternalServerError, do(http.MethodGet, "/control/api/teams/"+profileID.String()+"/profiles", "").Code)
	keys.countErr = nil

	require.Equal(t, http.StatusUnprocessableEntity, do(http.MethodPost, "/control/api/teams/"+profileID.String()+"/profiles", "{").Code)
	keys.createErr = errors.New("key create failed")
	require.Equal(t, http.StatusInternalServerError, do(http.MethodPost, "/control/api/teams/"+profileID.String()+"/profiles", `{"name":"Key"}`).Code)
	keys.createErr = nil

	require.Equal(t, http.StatusBadRequest, do(http.MethodPatch, "/control/api/teams/"+profileID.String()+"/profiles/not-a-uuid", `{"name":"Key"}`).Code)
	require.Equal(t, http.StatusUnprocessableEntity, do(http.MethodPatch, "/control/api/teams/"+profileID.String()+"/profiles/"+keyID.String(), "{").Code)
	keys.updateErr = errors.New("key update failed")
	require.Equal(t, http.StatusInternalServerError, do(http.MethodPatch, "/control/api/teams/"+profileID.String()+"/profiles/"+keyID.String(), `{"name":"Key"}`).Code)
	keys.updateErr = nil

	require.Equal(t, http.StatusUnprocessableEntity, do(http.MethodPost, "/control/api/teams/"+profileID.String()+"/profiles/"+keyID.String()+"/rotate", `{"expires_at":"bad"}`).Code)
	keys.rotateErr = errors.New("key rotate failed")
	require.Equal(t, http.StatusInternalServerError, do(http.MethodPost, "/control/api/teams/"+profileID.String()+"/profiles/"+keyID.String()+"/rotate", `{"name":"Key"}`).Code)
	keys.rotateErr = nil

	require.Equal(t, http.StatusBadRequest, do(http.MethodDelete, "/control/api/teams/"+profileID.String()+"/profiles/not-a-uuid", "").Code)
	keys.deleteErr = errors.New("key delete failed")
	require.Equal(t, http.StatusInternalServerError, do(http.MethodDelete, "/control/api/teams/"+profileID.String()+"/profiles/"+keyID.String(), "").Code)
	keys.deleteErr = nil

	securitySvc.settingsErr = errors.New("settings failed")
	require.Equal(t, http.StatusInternalServerError, do(http.MethodGet, "/control/api/security/settings", "").Code)
	require.Equal(t, http.StatusInternalServerError, do(http.MethodPatch, "/control/api/security/settings", `{}`).Code)
	securitySvc.settingsErr = nil
	require.Equal(t, http.StatusUnprocessableEntity, do(http.MethodPatch, "/control/api/security/settings", "{").Code)
	securitySvc.updateErr = errors.New("settings update failed")
	require.Equal(t, http.StatusInternalServerError, do(http.MethodPatch, "/control/api/security/settings", `{"enabled":true}`).Code)
	securitySvc.updateErr = nil

	securitySvc.listErr = errors.New("ban list failed")
	require.Equal(t, http.StatusInternalServerError, do(http.MethodGet, "/control/api/security/bans", "").Code)
	securitySvc.listErr = nil
	require.Equal(t, http.StatusUnprocessableEntity, do(http.MethodPost, "/control/api/security/bans", "{").Code)
	securitySvc.createErr = service.ErrInvalidSecurityIP
	require.Equal(t, http.StatusUnprocessableEntity, do(http.MethodPost, "/control/api/security/bans", `{"ip":"bad"}`).Code)
	securitySvc.createErr = errors.New("ban create failed")
	require.Equal(t, http.StatusInternalServerError, do(http.MethodPost, "/control/api/security/bans", `{"ip":"203.0.113.9"}`).Code)
	securitySvc.createErr = nil
	securitySvc.deleteErr = service.ErrInvalidSecurityIP
	require.Equal(t, http.StatusUnprocessableEntity, do(http.MethodDelete, "/control/api/security/bans/bad", "").Code)
	securitySvc.deleteErr = errors.New("ban delete failed")
	require.Equal(t, http.StatusInternalServerError, do(http.MethodDelete, "/control/api/security/bans/203.0.113.9", "").Code)

	require.NoError(t, ShutdownControlPortal(server, nil))
}
