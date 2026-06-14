package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/storage/inmem"
)

type userPortalAuthRepo struct {
	key *domain.APIKey
}

func (r *userPortalAuthRepo) CreateStandardKey(context.Context, *domain.APIKey) error { return nil }
func (r *userPortalAuthRepo) ListByProfile(context.Context, uuid.UUID, int, int) ([]*domain.APIKey, error) {
	return nil, nil
}
func (r *userPortalAuthRepo) CountByProfile(context.Context, uuid.UUID) (int64, error) { return 0, nil }
func (r *userPortalAuthRepo) GetByIDForProfile(context.Context, uuid.UUID, uuid.UUID) (*domain.APIKey, error) {
	return nil, nil
}
func (r *userPortalAuthRepo) GetSSOOwnedKey(context.Context, uuid.UUID, uuid.UUID) (*domain.APIKey, error) {
	return nil, nil
}
func (r *userPortalAuthRepo) GetActiveByPrefix(_ context.Context, prefix string) (*domain.APIKey, error) {
	if r.key != nil && r.key.KeyPrefix == prefix {
		key := *r.key
		return &key, nil
	}
	return nil, nil
}
func (r *userPortalAuthRepo) RevokeForProfile(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return 0, nil
}
func (r *userPortalAuthRepo) DeleteForProfile(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return 0, nil
}
func (r *userPortalAuthRepo) UpdateNameForProfile(context.Context, uuid.UUID, uuid.UUID, string) (int64, error) {
	return 0, nil
}
func (r *userPortalAuthRepo) UpdateRoleForProfile(context.Context, uuid.UUID, uuid.UUID, string) (int64, error) {
	return 0, nil
}
func (r *userPortalAuthRepo) RotateForProfile(context.Context, uuid.UUID, uuid.UUID, string, string, string, *time.Time) (int64, error) {
	return 0, nil
}
func (r *userPortalAuthRepo) TouchLastUsed(context.Context, uuid.UUID) error { return nil }

type userPortalKeySvc struct {
	keys            []*domain.APIKey
	rotateProfileID uuid.UUID
	rotateKeyID     uuid.UUID
	lastRotateReq   service.CreateAPIKeyRequest
	lastCreateReq   service.CreateAPIKeyRequest
	rawRotatedKey   string
	rawCreatedKey   string
}

func (s *userPortalKeySvc) CreateStandardKey(_ context.Context, profileID uuid.UUID, req service.CreateAPIKeyRequest, _ *string, _ string, _ string, _ string) (*domain.APIKey, string, error) {
	scopes, err := service.NormalizeAPIKeyScopes(req.Scopes)
	if err != nil {
		return nil, "", err
	}
	if s.rawCreatedKey == "" {
		s.rawCreatedKey = "dm_created_plaintext"
	}
	s.lastCreateReq = req
	key := &domain.APIKey{
		ID:                 uuid.New(),
		TeamID:             profileID,
		ProfileID:          profileID,
		Name:               req.Name,
		Label:              req.Name,
		KeySuffix:          "own123",
		Scopes:             scopes,
		Role:               service.APIKeyRoleMember,
		RateLimit:          req.RateLimit,
		CreatedAt:          time.Now().UTC().Truncate(time.Second),
		ExpiresAt:          req.ExpiresAt,
		SSOOwnerIdentityID: req.SSOOwnerIdentityID,
	}
	s.keys = append(s.keys, key)
	return key, s.rawCreatedKey, nil
}
func (s *userPortalKeySvc) UpdateNameForProfile(context.Context, uuid.UUID, uuid.UUID, string, *string, string, string, string) (*domain.APIKey, error) {
	return nil, nil
}
func (s *userPortalKeySvc) UpdateRoleForProfile(context.Context, uuid.UUID, uuid.UUID, string, *string, string, string, string) (*domain.APIKey, error) {
	return nil, nil
}
func (s *userPortalKeySvc) RotateForProfile(_ context.Context, profileID, id uuid.UUID, req service.CreateAPIKeyRequest, _ *string, _ string, _ string, _ string) (*domain.APIKey, string, error) {
	key, _ := s.GetByIDForProfile(context.Background(), profileID, id)
	if key == nil {
		return nil, "", httperr.New(httperr.NOT_FOUND, "key not found")
	}
	s.rotateProfileID = profileID
	s.rotateKeyID = id
	s.lastRotateReq = req
	key.KeySuffix = "rot8ed"
	key.LastUsedAt = nil
	if s.rawRotatedKey == "" {
		s.rawRotatedKey = "dm_rotated_plaintext"
	}
	return key, s.rawRotatedKey, nil
}
func (s *userPortalKeySvc) ListByProfile(context.Context, uuid.UUID, int, int) ([]*domain.APIKey, error) {
	return nil, nil
}
func (s *userPortalKeySvc) CountByProfile(context.Context, uuid.UUID) (int64, error) { return 0, nil }
func (s *userPortalKeySvc) GetByIDForProfile(_ context.Context, profileID, id uuid.UUID) (*domain.APIKey, error) {
	for _, key := range s.keys {
		if key.GetTeamID() == profileID && key.ID == id {
			return key, nil
		}
	}
	return nil, httperr.New(httperr.NOT_FOUND, "key not found")
}
func (s *userPortalKeySvc) GetSSOOwnedKey(_ context.Context, profileID, identityID uuid.UUID) (*domain.APIKey, error) {
	for _, key := range s.keys {
		if key.GetTeamID() == profileID && key.SSOOwnerIdentityID != nil && *key.SSOOwnerIdentityID == identityID && key.RevokedAt == nil {
			return key, nil
		}
	}
	return nil, nil
}
func (s *userPortalKeySvc) DeleteForProfile(context.Context, uuid.UUID, uuid.UUID, *string, string, string, string) error {
	return nil
}

type userPortalNilKeySvc struct {
	userPortalKeySvc
	err error
}

func (s *userPortalNilKeySvc) GetByIDForProfile(context.Context, uuid.UUID, uuid.UUID) (*domain.APIKey, error) {
	return nil, s.err
}

type userPortalRotateErrorKeySvc struct {
	userPortalKeySvc
	err error
}

func (s *userPortalRotateErrorKeySvc) RotateForProfile(context.Context, uuid.UUID, uuid.UUID, service.CreateAPIKeyRequest, *string, string, string, string) (*domain.APIKey, string, error) {
	return nil, "", s.err
}

func TestUserPortalSessionShowsOnlyAuthenticatedKey(t *testing.T) {
	teamID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, uuid.New(), "Mine", []string{"read"})
	otherKey := &domain.APIKey{
		ID:        uuid.New(),
		TeamID:    teamID,
		ProfileID: teamID,
		Name:      "Other",
		Label:     "Other",
		KeySuffix: "other1",
		Scopes:    []string{"read", "write"},
		RateLimit: 120,
		CreatedAt: time.Now().UTC(),
	}
	server := userPortalTestServer(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey, otherKey}}, "")

	req := httptest.NewRequest(http.MethodGet, "/ui/api/session", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), authKey.ID.String())
	require.Contains(t, rec.Body.String(), `"config":{"dreaming":{"enabled":true},"retention":"standard"}`)
	require.Contains(t, rec.Body.String(), `"can_rotate":false`)
	require.NotContains(t, rec.Body.String(), otherKey.ID.String())
	require.NotContains(t, rec.Body.String(), "Other")
}

func TestUserPortalTelemetry(t *testing.T) {
	teamID := uuid.New()
	keyID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, keyID, "Mine", []string{"read"})
	telemetry := &controlTelemetrySvc{snapshot: &service.TelemetrySnapshot{
		Available: true,
		Window:    service.TelemetryWindow{Key: "15m"},
		Cards:     []service.TelemetryCard{{ID: "http_requests", Label: "HTTP requests", Unit: "requests", Value: 4}},
	}}
	server := userPortalTestServerWithTelemetry(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey}}, "", telemetry)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/telemetry?window=15m", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"available":true`)
	require.Equal(t, "15m", telemetry.filter.Window)
	require.Equal(t, "self", telemetry.filter.Scope)
	require.Equal(t, teamID, *telemetry.filter.TeamID)
	require.Equal(t, keyID, *telemetry.filter.ProfileID)

	req = httptest.NewRequest(http.MethodGet, "/ui/api/telemetry?window=30m&scope=team", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "15m", telemetry.filter.Window)
	require.Equal(t, "self", telemetry.filter.Scope)
	require.Equal(t, keyID, *telemetry.filter.ProfileID)

	req = httptest.NewRequest(http.MethodGet, "/ui/api/telemetry?scope=profile", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)

	noTelemetry := userPortalTestServer(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey}}, "")
	req = httptest.NewRequest(http.MethodGet, "/ui/api/telemetry", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec = httptest.NewRecorder()
	noTelemetry.ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestUserPortalRotateRequiresWriteScope(t *testing.T) {
	teamID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, uuid.New(), "Read only", []string{"read"})
	server := userPortalTestServer(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey}}, "")

	req := httptest.NewRequest(http.MethodPost, "/ui/api/key/rotate", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "insufficient permissions")
}

func TestUserPortalRotateSelfOnlyPreservesMetadata(t *testing.T) {
	teamID := uuid.New()
	keyID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, keyID, "Current profile", []string{"read", "write"})
	expiresAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	authKey.ExpiresAt = &expiresAt
	authKey.RateLimit = 77
	keySvc := &userPortalKeySvc{keys: []*domain.APIKey{authKey}}
	server := userPortalTestServer(t, teamID, authKey, keySvc, "")

	req := httptest.NewRequest(http.MethodPost, "/ui/api/key/rotate", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"api_key":"dm_rotated_plaintext"`)
	require.Equal(t, teamID, keySvc.rotateProfileID)
	require.Equal(t, keyID, keySvc.rotateKeyID)
	require.Equal(t, "Current profile", keySvc.lastRotateReq.Name)
	require.Equal(t, 77, keySvc.lastRotateReq.RateLimit)
	require.Equal(t, expiresAt, *keySvc.lastRotateReq.ExpiresAt)
}

func TestUserPortalRotateRejectsEditableFields(t *testing.T) {
	teamID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, uuid.New(), "Current profile", []string{"read", "write"})
	server := userPortalTestServer(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey}}, "")

	req := httptest.NewRequest(http.MethodPost, "/ui/api/key/rotate", strings.NewReader(`{"name":"changed"}`))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), "does not accept editable fields")
}

func TestUserPortalCurrentSessionErrors(t *testing.T) {
	teamID := uuid.New()
	keyID := uuid.New()
	authKey, _ := userPortalTestKey(t, teamID, keyID, "Current profile", []string{"read", "write"})

	h := &userPortalHandler{
		profiles: &controlProfileSvc{},
		keys:     &userPortalKeySvc{keys: []*domain.APIKey{authKey}},
	}
	err := h.session(userPortalEchoContext(t, http.MethodGet, "/ui/api/session", "", nil))
	require.ErrorContains(t, err, "authentication required")

	_, err = h.currentSession(userPortalEchoContext(t, http.MethodGet, "/ui/api/session", "", &httpmw.Principal{
		KeyID:  keyID,
		TeamID: teamID,
		Role:   service.APIKeyRoleMember,
		Scopes: []string{"read"},
	}))
	require.ErrorContains(t, err, "team not found")

	h.profiles = &controlProfileSvc{profiles: []*domain.Profile{{
		ID:        teamID,
		Name:      "Team",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}}
	h.keys = &userPortalNilKeySvc{err: errors.New("lookup failed")}
	_, err = h.currentSession(userPortalEchoContext(t, http.MethodGet, "/ui/api/session", "", &httpmw.Principal{
		KeyID:  keyID,
		TeamID: teamID,
		Role:   service.APIKeyRoleMember,
		Scopes: []string{"read"},
	}))
	require.ErrorContains(t, err, "lookup failed")

	h.keys = &userPortalNilKeySvc{}
	_, err = h.currentSession(userPortalEchoContext(t, http.MethodGet, "/ui/api/session", "", &httpmw.Principal{
		KeyID:  keyID,
		TeamID: teamID,
		Role:   service.APIKeyRoleMember,
		Scopes: []string{"read"},
	}))
	require.ErrorContains(t, err, "key not found")
}

func TestUserPortalRotateCurrentKeyErrors(t *testing.T) {
	teamID := uuid.New()
	keyID := uuid.New()
	authKey, _ := userPortalTestKey(t, teamID, keyID, "Current profile", []string{"read", "write"})
	h := &userPortalHandler{
		profiles: &controlProfileSvc{},
		keys:     &userPortalKeySvc{keys: []*domain.APIKey{authKey}},
	}

	err := h.rotateCurrentKey(userPortalEchoContext(t, http.MethodPost, "/ui/api/key/rotate", "{}", nil))
	require.ErrorContains(t, err, "authentication required")

	err = h.rotateCurrentKey(userPortalEchoContext(t, http.MethodPost, "/ui/api/key/rotate", "{}", &httpmw.Principal{
		KeyID:      keyID,
		TeamID:     teamID,
		Role:       service.APIKeyRoleMember,
		Scopes:     []string{"read", "write"},
		AuthMethod: "sso_session",
	}))
	require.ErrorContains(t, err, "sso sessions cannot rotate api keys")

	principal := &httpmw.Principal{
		KeyID:  keyID,
		TeamID: teamID,
		Role:   service.APIKeyRoleMember,
		Scopes: []string{"read", "write"},
	}
	h.keys = &userPortalNilKeySvc{err: errors.New("lookup failed")}
	err = h.rotateCurrentKey(userPortalEchoContext(t, http.MethodPost, "/ui/api/key/rotate", "{}", principal))
	require.ErrorContains(t, err, "lookup failed")

	h.keys = &userPortalNilKeySvc{}
	err = h.rotateCurrentKey(userPortalEchoContext(t, http.MethodPost, "/ui/api/key/rotate", "{}", principal))
	require.ErrorContains(t, err, "key not found")

	h.keys = &userPortalRotateErrorKeySvc{
		userPortalKeySvc: userPortalKeySvc{keys: []*domain.APIKey{authKey}},
		err:              errors.New("rotate failed"),
	}
	err = h.rotateCurrentKey(userPortalEchoContext(t, http.MethodPost, "/ui/api/key/rotate", "{}", principal))
	require.ErrorContains(t, err, "rotate failed")
}

func TestRejectEditableRotateBodyBranches(t *testing.T) {
	c := userPortalEchoContext(t, http.MethodPost, "/ui/api/key/rotate", "", nil)
	c.Request().Body = nil
	require.NoError(t, rejectEditableRotateBody(c))

	c = userPortalEchoContext(t, http.MethodPost, "/ui/api/key/rotate", "   ", nil)
	require.NoError(t, rejectEditableRotateBody(c))

	c = userPortalEchoContext(t, http.MethodPost, "/ui/api/key/rotate", `{"name"`, nil)
	require.ErrorContains(t, rejectEditableRotateBody(c), "malformed JSON body")
}

func TestUserPortalStaticDoesNotShadowAPI(t *testing.T) {
	teamID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, uuid.New(), "Mine", []string{"read", "write"})
	staticDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("<!doctype html><title>ui</title>"), 0o644))
	server := userPortalTestServer(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey}}, staticDir)

	uiReq := httptest.NewRequest(http.MethodGet, "/ui", nil)
	uiRec := httptest.NewRecorder()
	server.ServeHTTP(uiRec, uiReq)
	require.Equal(t, http.StatusOK, uiRec.Code)
	require.Contains(t, uiRec.Body.String(), "<title>ui</title>")

	apiReq := httptest.NewRequest(http.MethodGet, "/ui/api/session", nil)
	apiReq.Header.Set("Authorization", "Bearer "+rawKey)
	apiRec := httptest.NewRecorder()
	server.ServeHTTP(apiRec, apiReq)
	require.Equal(t, http.StatusOK, apiRec.Code)
	require.Contains(t, apiRec.Body.String(), `"can_rotate":true`)
	require.NotContains(t, apiRec.Body.String(), "<title>ui</title>")
}

func userPortalTestKey(t *testing.T, teamID, keyID uuid.UUID, name string, scopes []string) (*domain.APIKey, string) {
	t.Helper()
	rawKey, err := crypto.GenerateRawKeyForProfile(name)
	require.NoError(t, err)
	keyHash, err := crypto.HashKey(rawKey)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	return &domain.APIKey{
		ID:        keyID,
		TeamID:    teamID,
		ProfileID: teamID,
		Name:      name,
		Label:     name,
		KeyHash:   keyHash,
		KeyPrefix: crypto.GetKeyPrefix(rawKey),
		KeySuffix: crypto.GetKeySuffix(rawKey),
		Scopes:    scopes,
		RateLimit: 120,
		CreatedAt: now,
	}, rawKey
}

func userPortalTestServer(t *testing.T, teamID uuid.UUID, authKey *domain.APIKey, keySvc *userPortalKeySvc, staticDir string) http.Handler {
	return userPortalTestServerWithTelemetry(t, teamID, authKey, keySvc, staticDir, nil)
}

func userPortalTestServerWithTelemetry(t *testing.T, teamID uuid.UUID, authKey *domain.APIKey, keySvc *userPortalKeySvc, staticDir string, telemetry service.TelemetryReader) http.Handler {
	t.Helper()
	profiles := &controlProfileSvc{profiles: []*domain.Profile{{
		ID:        teamID,
		Name:      "Team",
		Config:    map[string]any{"dreaming": map[string]any{"enabled": true}, "retention": "standard"},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}}
	e := NewServer(config.Config{
		HTTPMaxBodyBytes:        1048576,
		RateLimitPerMinute:      100,
		FragmentCreateRateLimit: 60,
		FragmentReadRateLimit:   300,
		ClaimWriteRateLimit:     60,
		ClaimReadRateLimit:      300,
	}, nil, HealthConfig{})
	RegisterUserPortal(e, UserPortalDeps{
		APIKeyRepo:    &userPortalAuthRepo{key: authKey},
		ProfileSvc:    profiles,
		APIKeySvc:     keySvc,
		RateLimitSvc:  service.NewRateLimitService(inmem.NewInMemoryRateLimitStore()),
		Telemetry:     telemetry,
		Config:        &config.Config{RateLimitPerMinute: 100, FragmentCreateRateLimit: 60, FragmentReadRateLimit: 300, ClaimWriteRateLimit: 60, ClaimReadRateLimit: 300},
		UserStaticDir: staticDir,
	})
	return e
}

func userPortalEchoContext(t *testing.T, method, target, body string, principal *httpmw.Principal) echo.Context {
	t.Helper()

	e := echo.New()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if principal != nil {
		req = req.WithContext(httpmw.SetPrincipalForTest(req.Context(), principal))
	}
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec)
}
