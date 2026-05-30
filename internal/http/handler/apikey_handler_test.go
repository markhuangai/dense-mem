package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

type apiKeyHandlerService struct {
	key          *domain.APIKey
	raw          string
	keys         []*domain.APIKey
	total        int64
	createReq    service.CreateAPIKeyRequest
	rotateReq    service.CreateAPIKeyRequest
	deletedKeyID uuid.UUID
	err          error
	listErr      error
	countErr     error
}

func (s *apiKeyHandlerService) CreateStandardKey(_ context.Context, _ uuid.UUID, req service.CreateAPIKeyRequest, _ *string, actorRole, clientIP, _ string) (*domain.APIKey, string, error) {
	s.createReq = req
	if s.err != nil {
		return nil, "", s.err
	}
	if actorRole == "" || clientIP == "" {
		return nil, "", httperr.New(httperr.INTERNAL_ERROR, "missing actor metadata")
	}
	return s.key, s.raw, nil
}

func (s *apiKeyHandlerService) UpdateNameForProfile(context.Context, uuid.UUID, uuid.UUID, string, *string, string, string, string) (*domain.APIKey, error) {
	return s.key, s.err
}

func (s *apiKeyHandlerService) RotateForProfile(_ context.Context, _ uuid.UUID, _ uuid.UUID, req service.CreateAPIKeyRequest, _ *string, _ string, _ string, _ string) (*domain.APIKey, string, error) {
	s.rotateReq = req
	if s.err != nil {
		return nil, "", s.err
	}
	return s.key, s.raw, nil
}

func (s *apiKeyHandlerService) ListByProfile(context.Context, uuid.UUID, int, int) ([]*domain.APIKey, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.keys, s.err
}

func (s *apiKeyHandlerService) CountByProfile(context.Context, uuid.UUID) (int64, error) {
	if s.countErr != nil {
		return 0, s.countErr
	}
	return s.total, s.err
}

func (s *apiKeyHandlerService) GetByIDForProfile(context.Context, uuid.UUID, uuid.UUID) (*domain.APIKey, error) {
	return s.key, s.err
}

func (s *apiKeyHandlerService) DeleteForProfile(_ context.Context, _ uuid.UUID, id uuid.UUID, _ *string, _ string, _ string, _ string) error {
	s.deletedKeyID = id
	return s.err
}

func TestAPIKeyHandlerCreateListGetRotateDelete(t *testing.T) {
	profileID := uuid.New()
	keyID := uuid.New()
	createdAt := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	expiresAt := createdAt.Add(time.Hour)
	key := &domain.APIKey{
		ID:        keyID,
		TeamID:    profileID,
		Name:      "ops key",
		KeyHash:   "must-not-leak",
		KeySuffix: "wxyz",
		Scopes:    []string{"read", "write"},
		RateLimit: 42,
		CreatedAt: createdAt,
		ExpiresAt: &expiresAt,
	}
	svc := &apiKeyHandlerService{
		key:   key,
		raw:   "dm_profile_secret",
		keys:  []*domain.APIKey{key},
		total: 9,
	}
	h := NewAPIKeyHandler(svc)

	t.Run("create returns raw key once and maps request", func(t *testing.T) {
		scopes := []string{"read"}
		body := `{"name":"ops key","rate_limit":42,"scopes":["read"],"expires_at":"2026-05-30T13:00:00Z"}`
		rec, err := runAPIKeyHandlerWithBody(t, http.MethodPost, "/api/v1/teams/"+profileID.String()+"/profiles", "/api/v1/teams/:teamId/profiles", []string{"teamId"}, []string{profileID.String()}, body, h.Create)

		if err != nil {
			t.Fatalf("Create error: %v", err)
		}
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d; want 201 body=%s", rec.Code, rec.Body.String())
		}
		if svc.createReq.Name != "ops key" || svc.createReq.RateLimit != 42 {
			t.Fatalf("create request = %+v", svc.createReq)
		}
		if got := svc.createReq.Scopes; len(got) != 1 || got[0] != scopes[0] {
			t.Fatalf("scopes = %v; want %v", got, scopes)
		}
		bodyText := rec.Body.String()
		if !strings.Contains(bodyText, "dm_profile_secret") {
			t.Fatalf("response does not include one-time raw key: %s", bodyText)
		}
		if strings.Contains(bodyText, "must-not-leak") {
			t.Fatalf("response leaked key hash: %s", bodyText)
		}
	})

	t.Run("list returns paginated items", func(t *testing.T) {
		rec, err := runAPIKeyHandler(t, http.MethodGet, "/api/v1/teams/"+profileID.String()+"/profiles?limit=200&offset=3", "/api/v1/teams/:teamId/profiles", []string{"teamId"}, []string{profileID.String()}, h.List)
		if err != nil {
			t.Fatalf("List error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200", rec.Code)
		}
		var out PaginationEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if out.Pagination.Limit != 100 || out.Pagination.Offset != 3 || out.Pagination.Total != 9 {
			t.Fatalf("pagination = %+v", out.Pagination)
		}
	})

	t.Run("get returns scoped key", func(t *testing.T) {
		rec, err := runAPIKeyHandler(t, http.MethodGet, "/api/v1/teams/"+profileID.String()+"/profiles/"+keyID.String(), "/api/v1/teams/:teamId/profiles/:profileId", []string{"teamId", "profileId"}, []string{profileID.String(), keyID.String()}, h.Get)
		if err != nil {
			t.Fatalf("Get error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200", rec.Code)
		}
	})

	t.Run("rotate rejects scope changes and accepts metadata updates", func(t *testing.T) {
		withScopes := `{"name":"rotated","scopes":["read"]}`
		_, err := runAPIKeyHandlerWithBody(t, http.MethodPost, "/api/v1/teams/"+profileID.String()+"/profiles/"+keyID.String()+"/rotate", "/api/v1/teams/:teamId/profiles/:profileId/rotate", []string{"teamId", "profileId"}, []string{profileID.String(), keyID.String()}, withScopes, h.Rotate)
		if err == nil || !strings.Contains(err.Error(), "scopes cannot be changed") {
			t.Fatalf("Rotate error = %v; want scopes rejection", err)
		}

		body := `{"name":"rotated","rate_limit":7,"expires_at":"2026-05-30T13:00:00Z"}`
		rec, err := runAPIKeyHandlerWithBody(t, http.MethodPost, "/api/v1/teams/"+profileID.String()+"/profiles/"+keyID.String()+"/rotate", "/api/v1/teams/:teamId/profiles/:profileId/rotate", []string{"teamId", "profileId"}, []string{profileID.String(), keyID.String()}, body, h.Rotate)
		if err != nil {
			t.Fatalf("Rotate error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200", rec.Code)
		}
		if svc.rotateReq.Name != "rotated" || svc.rotateReq.RateLimit != 7 || svc.rotateReq.ExpiresAt == nil {
			t.Fatalf("rotate request = %+v", svc.rotateReq)
		}
	})

	t.Run("delete delegates scoped delete", func(t *testing.T) {
		rec, err := runAPIKeyHandler(t, http.MethodDelete, "/api/v1/teams/"+profileID.String()+"/profiles/"+keyID.String(), "/api/v1/teams/:teamId/profiles/:profileId", []string{"teamId", "profileId"}, []string{profileID.String(), keyID.String()}, h.Delete)
		if err != nil {
			t.Fatalf("Delete error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200", rec.Code)
		}
		if svc.deletedKeyID != keyID {
			t.Fatalf("deleted key = %s; want %s", svc.deletedKeyID, keyID)
		}
	})
}

func TestAPIKeyHandlerValidationErrors(t *testing.T) {
	h := NewAPIKeyHandler(&apiKeyHandlerService{})
	profileID := uuid.New().String()

	cases := []struct {
		name       string
		method     string
		target     string
		path       string
		paramNames []string
		paramVals  []string
		body       string
		handler    echo.HandlerFunc
		want       string
	}{
		{
			name:       "create missing team id",
			method:     http.MethodPost,
			target:     "/api/v1/teams//profiles",
			path:       "/api/v1/teams/:teamId/profiles",
			paramNames: []string{"teamId"},
			paramVals:  []string{""},
			body:       `{"name":"ops"}`,
			handler:    h.Create,
			want:       "team ID is required",
		},
		{
			name:       "get invalid key id",
			method:     http.MethodGet,
			target:     "/api/v1/teams/" + profileID + "/profiles/not-a-uuid",
			path:       "/api/v1/teams/:teamId/profiles/:profileId",
			paramNames: []string{"teamId", "profileId"},
			paramVals:  []string{profileID, "not-a-uuid"},
			handler:    h.Get,
			want:       "invalid profile ID format",
		},
		{
			name:       "delete missing key id",
			method:     http.MethodDelete,
			target:     "/api/v1/teams/" + profileID + "/profiles/",
			path:       "/api/v1/teams/:teamId/profiles/:profileId",
			paramNames: []string{"teamId", "profileId"},
			paramVals:  []string{profileID, ""},
			handler:    h.Delete,
			want:       "profile ID is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runAPIKeyHandlerWithOptionalBody(t, tc.method, tc.target, tc.path, tc.paramNames, tc.paramVals, tc.body, tc.handler)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestAPIKeyHandlerAdditionalBranches(t *testing.T) {
	profileID := uuid.New()
	keyID := uuid.New()
	key := &domain.APIKey{
		ID:        keyID,
		TeamID:    profileID,
		Name:      "ops key",
		KeySuffix: "wxyz",
		Scopes:    []string{"read"},
		CreatedAt: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
	}

	t.Run("create missing validated body", func(t *testing.T) {
		h := NewAPIKeyHandler(&apiKeyHandlerService{key: key, raw: "raw"})
		_, err := runAPIKeyHandler(t, http.MethodPost, "/api/v1/teams/"+profileID.String()+"/profiles", "/api/v1/teams/:teamId/profiles", []string{"teamId"}, []string{profileID.String()}, h.Create)
		if err == nil || !strings.Contains(err.Error(), "request body not found") {
			t.Fatalf("Create err = %v; want missing body", err)
		}
	})

	t.Run("create ignores invalid expires_at parse", func(t *testing.T) {
		svc := &apiKeyHandlerService{key: key, raw: "raw"}
		h := NewAPIKeyHandler(svc)
		rec, err := runAPIKeyHandlerWithBody(t, http.MethodPost, "/api/v1/teams/"+profileID.String()+"/profiles", "/api/v1/teams/:teamId/profiles", []string{"teamId"}, []string{profileID.String()}, `{"name":"ops","expires_at":"not-rfc3339"}`, h.Create)
		if err != nil {
			t.Fatalf("Create error: %v", err)
		}
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d; want 201", rec.Code)
		}
		if svc.createReq.ExpiresAt != nil {
			t.Fatalf("ExpiresAt = %v; want nil for invalid timestamp", svc.createReq.ExpiresAt)
		}
	})

	t.Run("list count error propagates", func(t *testing.T) {
		h := NewAPIKeyHandler(&apiKeyHandlerService{keys: []*domain.APIKey{key}, countErr: httperr.New(httperr.INTERNAL_ERROR, "count failed")})
		_, err := runAPIKeyHandler(t, http.MethodGet, "/api/v1/teams/"+profileID.String()+"/profiles", "/api/v1/teams/:teamId/profiles", []string{"teamId"}, []string{profileID.String()}, h.List)
		if err == nil || !strings.Contains(err.Error(), "count failed") {
			t.Fatalf("List err = %v; want count failed", err)
		}
	})

	t.Run("get accepts keyId param name", func(t *testing.T) {
		h := NewAPIKeyHandler(&apiKeyHandlerService{key: key})
		rec, err := runAPIKeyHandler(t, http.MethodGet, "/api/v1/profiles/"+profileID.String()+"/api-keys/"+keyID.String(), "/api/v1/profiles/:profileId/api-keys/:keyId", []string{"profileId", "keyId"}, []string{profileID.String(), keyID.String()}, h.Get)
		if err != nil {
			t.Fatalf("Get error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200", rec.Code)
		}
	})

	t.Run("rotate missing body", func(t *testing.T) {
		h := NewAPIKeyHandler(&apiKeyHandlerService{key: key, raw: "raw"})
		_, err := runAPIKeyHandler(t, http.MethodPost, "/api/v1/teams/"+profileID.String()+"/profiles/"+keyID.String()+"/rotate", "/api/v1/teams/:teamId/profiles/:profileId/rotate", []string{"teamId", "profileId"}, []string{profileID.String(), keyID.String()}, h.Rotate)
		if err == nil || !strings.Contains(err.Error(), "request body not found") {
			t.Fatalf("Rotate err = %v; want missing body", err)
		}
	})

	t.Run("invalid team ids are rejected", func(t *testing.T) {
		h := NewAPIKeyHandler(&apiKeyHandlerService{key: key, raw: "raw"})
		cases := []struct {
			name    string
			method  string
			target  string
			path    string
			handler echo.HandlerFunc
		}{
			{name: "list", method: http.MethodGet, target: "/api/v1/teams/not-a-uuid/profiles", path: "/api/v1/teams/:teamId/profiles", handler: h.List},
			{name: "delete", method: http.MethodDelete, target: "/api/v1/teams/not-a-uuid/profiles/" + keyID.String(), path: "/api/v1/teams/:teamId/profiles/:profileId", handler: h.Delete},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := runAPIKeyHandler(t, tc.method, tc.target, tc.path, []string{"teamId", "profileId"}, []string{"not-a-uuid", keyID.String()}, tc.handler)
				if err == nil || !strings.Contains(err.Error(), "invalid team ID format") {
					t.Fatalf("%s err = %v; want invalid team ID", tc.name, err)
				}
			})
		}
	})
}

func TestAPIKeyResponseHelpersPreferCanonicalFields(t *testing.T) {
	teamID := uuid.New()
	legacyID := uuid.New()
	keyID := uuid.New()
	lastUsedAt := time.Date(2026, 5, 30, 12, 1, 0, 0, time.UTC)
	expiresAt := lastUsedAt.Add(time.Hour)
	key := &domain.APIKey{
		ID:         keyID,
		ProfileID:  legacyID,
		TeamID:     teamID,
		Label:      "legacy",
		Name:       "canonical",
		KeySuffix:  "abcd",
		Scopes:     []string{"read"},
		RateLimit:  11,
		LastUsedAt: &lastUsedAt,
		ExpiresAt:  &expiresAt,
		CreatedAt:  lastUsedAt,
	}

	resp := toAPIKeyResponse(key)
	if resp.TeamID != teamID || resp.Name != "canonical" || resp.LastUsedAt == nil || resp.ExpiresAt == nil {
		t.Fatalf("APIKeyResponse = %+v", resp)
	}

	item := toAPIKeyListItem(key)
	if item.TeamID != teamID || item.Name != "canonical" || item.LastUsedAt == nil || item.ExpiresAt == nil {
		t.Fatalf("APIKeyListItem = %+v", item)
	}
}

func runAPIKeyHandlerWithBody(t *testing.T, method, target, path string, paramNames, paramVals []string, body string, handler echo.HandlerFunc) (*httptest.ResponseRecorder, error) {
	return runAPIKeyHandlerWithOptionalBody(t, method, target, path, paramNames, paramVals, body, handler)
}

func runAPIKeyHandler(t *testing.T, method, target, path string, paramNames, paramVals []string, handler echo.HandlerFunc) (*httptest.ResponseRecorder, error) {
	return runAPIKeyHandlerWithOptionalBody(t, method, target, path, paramNames, paramVals, "", handler)
}

func runAPIKeyHandlerWithOptionalBody(t *testing.T, method, target, path string, paramNames, paramVals []string, body string, handler echo.HandlerFunc) (*httptest.ResponseRecorder, error) {
	t.Helper()

	e := echo.New()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderXRealIP, "203.0.113.7")
	req.Header.Set("X-Correlation-ID", "corr-api-key-test")
	principalID := uuid.New()
	req = req.WithContext(middleware.SetPrincipalForTest(req.Context(), &middleware.Principal{
		KeyID: principalID,
		Role:  "admin",
	}))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath(path)
	c.SetParamNames(paramNames...)
	c.SetParamValues(paramVals...)

	if body == "" {
		return rec, handler(c)
	}
	wrapped := middleware.BindAndValidate[dto.CreateAPIKeyRequest](middleware.CreateAPIKeyBodyKey)(handler)
	return rec, wrapped(c)
}
