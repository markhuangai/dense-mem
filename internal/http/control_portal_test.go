package http

import (
	"context"
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
	profiles []*domain.Profile
	deleted  uuid.UUID
}

func (s *controlProfileSvc) Create(_ context.Context, req service.CreateProfileRequest, _ *string, _ string, _ string, _ string) (*domain.Profile, error) {
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
	return s.profiles, nil
}

func (s *controlProfileSvc) Count(context.Context) (int64, error) {
	return int64(len(s.profiles)), nil
}

func (s *controlProfileSvc) Update(_ context.Context, id uuid.UUID, req service.UpdateProfileRequest, _ *string, _ string, _ string, _ string) (*domain.Profile, error) {
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
	s.deleted = id
	return nil
}

type controlKeySvc struct {
	keys       []*domain.APIKey
	rawKey     string
	deletedKey uuid.UUID
}

func (s *controlKeySvc) CreateStandardKey(_ context.Context, profileID uuid.UUID, req service.CreateAPIKeyRequest, _ *string, _ string, _ string, _ string) (*domain.APIKey, string, error) {
	key := &domain.APIKey{
		ID:        uuid.New(),
		ProfileID: profileID,
		TeamID:    profileID,
		Label:     req.Name,
		Name:      req.Name,
		KeyPrefix: "dm_test",
		KeySuffix: "intext",
		Scopes:    service.StandardAPIKeyScopes(),
		RateLimit: req.RateLimit,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: req.ExpiresAt,
	}
	s.rawKey = "dm_test_plaintext"
	s.keys = append(s.keys, key)
	return key, s.rawKey, nil
}

func (s *controlKeySvc) RotateForProfile(_ context.Context, profileID, id uuid.UUID, req service.CreateAPIKeyRequest, _ *string, _ string, _ string, _ string) (*domain.APIKey, string, error) {
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

func (s *controlKeySvc) ListByProfile(_ context.Context, profileID uuid.UUID, _ int, _ int) ([]*domain.APIKey, error) {
	out := make([]*domain.APIKey, 0, len(s.keys))
	for _, key := range s.keys {
		if key.ProfileID == profileID {
			out = append(out, key)
		}
	}
	return out, nil
}

func (s *controlKeySvc) CountByProfile(ctx context.Context, profileID uuid.UUID) (int64, error) {
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
	keyBody := `{"rate_limit":120}`
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
	require.NotContains(t, rec.Body.String(), `"scopes"`)
	require.NotContains(t, rec.Body.String(), `"revoked_at"`)
	require.Empty(t, keys.keys[len(keys.keys)-1].Label)

	keyID := keys.keys[len(keys.keys)-1].ID
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
