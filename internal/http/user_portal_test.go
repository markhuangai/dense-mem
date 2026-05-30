package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
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
func (r *userPortalAuthRepo) RotateForProfile(context.Context, uuid.UUID, uuid.UUID, string, string, string, *time.Time) (int64, error) {
	return 0, nil
}
func (r *userPortalAuthRepo) TouchLastUsed(context.Context, uuid.UUID) error { return nil }

type userPortalKeySvc struct {
	keys            []*domain.APIKey
	rotateProfileID uuid.UUID
	rotateKeyID     uuid.UUID
	lastRotateReq   service.CreateAPIKeyRequest
	rawRotatedKey   string
}

func (s *userPortalKeySvc) CreateStandardKey(context.Context, uuid.UUID, service.CreateAPIKeyRequest, *string, string, string, string) (*domain.APIKey, string, error) {
	return nil, "", nil
}
func (s *userPortalKeySvc) UpdateNameForProfile(context.Context, uuid.UUID, uuid.UUID, string, *string, string, string, string) (*domain.APIKey, error) {
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
func (s *userPortalKeySvc) DeleteForProfile(context.Context, uuid.UUID, uuid.UUID, *string, string, string, string) error {
	return nil
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
	require.Contains(t, rec.Body.String(), `"can_rotate":false`)
	require.NotContains(t, rec.Body.String(), otherKey.ID.String())
	require.NotContains(t, rec.Body.String(), "Other")
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
	t.Helper()
	profiles := &controlProfileSvc{profiles: []*domain.Profile{{
		ID:        teamID,
		Name:      "Team",
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
		Config:        &config.Config{RateLimitPerMinute: 100, FragmentCreateRateLimit: 60, FragmentReadRateLimit: 300, ClaimWriteRateLimit: 60, ClaimReadRateLimit: 300},
		UserStaticDir: staticDir,
	})
	return e
}
