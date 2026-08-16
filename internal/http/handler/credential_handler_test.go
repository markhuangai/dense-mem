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
	key             *domain.Credential
	raw             string
	keys            []*domain.Credential
	total           int64
	createProfileID uuid.UUID
	listProfileID   uuid.UUID
	getProfileID    uuid.UUID
	getKeyID        uuid.UUID
	updateProfileID uuid.UUID
	updateKeyID     uuid.UUID
	rotateProfileID uuid.UUID
	rotateKeyID     uuid.UUID
	deleteProfileID uuid.UUID
	deletedKeyID    uuid.UUID
	createReq       service.CreateCredentialRequest
	rotateReq       service.CreateCredentialRequest
	updatedName     string
	updatedScopes   []string
	err             error
	listErr         error
	countErr        error
}

func (s *apiKeyHandlerService) CreateCredential(_ context.Context, profileID uuid.UUID, req service.CreateCredentialRequest, _ *string, actorRole, clientIP, _ string) (*domain.Credential, string, error) {
	s.createProfileID = profileID
	s.createReq = req
	if s.err != nil {
		return nil, "", s.err
	}
	if actorRole == "" || clientIP == "" {
		return nil, "", httperr.New(httperr.INTERNAL_ERROR, "missing actor metadata")
	}
	return s.key, s.raw, nil
}

func (s *apiKeyHandlerService) UpdateNameForTeam(_ context.Context, profileID, id uuid.UUID, name string, _ *string, _ string, _ string, _ string) (*domain.Credential, error) {
	s.updateProfileID = profileID
	s.updateKeyID = id
	s.updatedName = name
	return s.key, s.err
}

func (s *apiKeyHandlerService) UpdateRoleForTeam(context.Context, uuid.UUID, uuid.UUID, string, *string, string, string, string) (*domain.Credential, error) {
	return s.key, s.err
}

func (s *apiKeyHandlerService) UpdateScopesForTeam(_ context.Context, profileID, id uuid.UUID, scopes []string, _ *string, _ string, _ string, _ string) (*domain.Credential, error) {
	s.updateProfileID = profileID
	s.updateKeyID = id
	s.updatedScopes = append([]string{}, scopes...)
	return s.key, s.err
}

func (s *apiKeyHandlerService) RotateForTeam(_ context.Context, profileID, id uuid.UUID, req service.CreateCredentialRequest, _ *string, _ string, _ string, _ string) (*domain.Credential, string, error) {
	s.rotateProfileID = profileID
	s.rotateKeyID = id
	s.rotateReq = req
	if s.err != nil {
		return nil, "", s.err
	}
	return s.key, s.raw, nil
}

func (s *apiKeyHandlerService) ListByTeam(_ context.Context, profileID uuid.UUID, _ int, _ int) ([]*domain.Credential, error) {
	s.listProfileID = profileID
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.keys, s.err
}

func (s *apiKeyHandlerService) CountByTeam(context.Context, uuid.UUID) (int64, error) {
	if s.countErr != nil {
		return 0, s.countErr
	}
	return s.total, s.err
}

func (s *apiKeyHandlerService) GetByIDForTeam(_ context.Context, profileID, id uuid.UUID) (*domain.Credential, error) {
	s.getProfileID = profileID
	s.getKeyID = id
	return s.key, s.err
}

func (s *apiKeyHandlerService) GetSSOOwnedCredential(context.Context, uuid.UUID, uuid.UUID) (*domain.Credential, error) {
	return nil, nil
}

func (s *apiKeyHandlerService) DeleteForTeam(_ context.Context, profileID uuid.UUID, id uuid.UUID, _ *string, _ string, _ string, _ string) error {
	s.deleteProfileID = profileID
	s.deletedKeyID = id
	return s.err
}

func TestCredentialHandlerCreateListGetRotateDelete(t *testing.T) {
	profileID := uuid.New()
	keyID := uuid.New()
	createdAt := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	expiresAt := createdAt.Add(time.Hour)
	key := &domain.Credential{
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
		keys:  []*domain.Credential{key},
		total: 9,
	}
	h := NewCredentialHandler(svc)

	t.Run("create returns raw key once and maps request", func(t *testing.T) {
		scopes := []string{"read"}
		body := `{"name":"ops key","rate_limit":42,"scopes":["read"],"expires_at":"2026-05-30T13:00:00Z"}`
		rec, err := runCredentialHandlerWithBody(t, http.MethodPost, "/ui/api/team/credentials", "/ui/api/team/credentials", []string{"teamId"}, []string{profileID.String()}, body, h.Create)

		if err != nil {
			t.Fatalf("Create error: %v", err)
		}
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d; want 201 body=%s", rec.Code, rec.Body.String())
		}
		if svc.createReq.Name != "ops key" || svc.createReq.RateLimit != 42 {
			t.Fatalf("create request = %+v", svc.createReq)
		}
		if svc.createReq.Role != service.CredentialRoleMember {
			t.Fatalf("role = %q; want %q", svc.createReq.Role, service.CredentialRoleMember)
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
		rec, err := runCredentialHandler(t, http.MethodGet, "/ui/api/team/credentials?limit=200&offset=3", "/ui/api/team/credentials", []string{"teamId"}, []string{profileID.String()}, h.List)
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
		rec, err := runCredentialHandler(t, http.MethodGet, "/ui/api/team/credentials/"+keyID.String(), "/ui/api/team/credentials/:credentialId", []string{"teamId", "credentialId"}, []string{profileID.String(), keyID.String()}, h.Get)
		if err != nil {
			t.Fatalf("Get error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200", rec.Code)
		}
	})

	t.Run("update renames member profile", func(t *testing.T) {
		updateSvc := &apiKeyHandlerService{key: key}
		h := NewCredentialHandler(updateSvc)
		rec, err := runCredentialHandlerWithUpdateBody(t, http.MethodPatch, "/ui/api/team/credentials/"+keyID.String(), "/ui/api/team/credentials/:credentialId", []string{"teamId", "credentialId"}, []string{profileID.String(), keyID.String()}, `{"name":"renamed profile"}`, h.Update)
		if err != nil {
			t.Fatalf("Update error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200", rec.Code)
		}
		if updateSvc.updatedName != "renamed profile" {
			t.Fatalf("updated name = %q; want renamed profile", updateSvc.updatedName)
		}
	})

	t.Run("update changes member profile scopes", func(t *testing.T) {
		updateSvc := &apiKeyHandlerService{key: key}
		h := NewCredentialHandler(updateSvc)
		rec, err := runCredentialHandlerWithUpdateBody(t, http.MethodPatch, "/ui/api/team/credentials/"+keyID.String(), "/ui/api/team/credentials/:credentialId", []string{"teamId", "credentialId"}, []string{profileID.String(), keyID.String()}, `{"scopes":["read","feedback:read"]}`, h.Update)
		if err != nil {
			t.Fatalf("Update error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200", rec.Code)
		}
		if len(updateSvc.updatedScopes) != 2 || updateSvc.updatedScopes[0] != "read" || updateSvc.updatedScopes[1] != "feedback:read" {
			t.Fatalf("updated scopes = %v; want read feedback:read", updateSvc.updatedScopes)
		}
	})

	t.Run("update rejects mixed name and scopes", func(t *testing.T) {
		updateSvc := &apiKeyHandlerService{key: key}
		h := NewCredentialHandler(updateSvc)
		_, err := runCredentialHandlerWithUpdateBody(t, http.MethodPatch, "/ui/api/team/credentials/"+keyID.String(), "/ui/api/team/credentials/:credentialId", []string{"teamId", "credentialId"}, []string{profileID.String(), keyID.String()}, `{"name":"renamed profile","scopes":["read"]}`, h.Update)
		if err == nil || !strings.Contains(err.Error(), "credential name and scopes must be updated separately") {
			t.Fatalf("Update error = %v; want mixed update rejection", err)
		}
		if updateSvc.updatedName != "" || updateSvc.updatedScopes != nil {
			t.Fatalf("delegated update name=%q scopes=%v; want none", updateSvc.updatedName, updateSvc.updatedScopes)
		}
	})

	t.Run("update rejects manager profile targets", func(t *testing.T) {
		managerKey := *key
		managerKey.Role = service.CredentialRoleManager
		updateSvc := &apiKeyHandlerService{key: &managerKey}
		h := NewCredentialHandler(updateSvc)
		_, err := runCredentialHandlerWithUpdateBody(t, http.MethodPatch, "/ui/api/team/credentials/"+keyID.String(), "/ui/api/team/credentials/:credentialId", []string{"teamId", "credentialId"}, []string{profileID.String(), keyID.String()}, `{"name":"renamed profile"}`, h.Update)
		if err == nil || !strings.Contains(err.Error(), "manager credentials can only be managed from the control portal") {
			t.Fatalf("Update error = %v; want manager target rejection", err)
		}
		if updateSvc.updatedName != "" {
			t.Fatalf("updated name = %q; want no delegated update", updateSvc.updatedName)
		}
	})

	t.Run("mutations return not found for missing profile targets", func(t *testing.T) {
		missingSvc := &apiKeyHandlerService{}
		h := NewCredentialHandler(missingSvc)

		_, err := runCredentialHandlerWithUpdateBody(t, http.MethodPatch, "/ui/api/team/credentials/"+keyID.String(), "/ui/api/team/credentials/:credentialId", []string{"teamId", "credentialId"}, []string{profileID.String(), keyID.String()}, `{"name":"renamed profile"}`, h.Update)
		assertCredentialHandlerErrorCode(t, err, httperr.NOT_FOUND)
		if missingSvc.updatedName != "" {
			t.Fatalf("updated name = %q; want no delegated update", missingSvc.updatedName)
		}

		_, err = runCredentialHandlerWithBody(t, http.MethodPost, "/ui/api/team/credentials/"+keyID.String()+"/rotate", "/ui/api/team/credentials/:credentialId/rotate", []string{"teamId", "credentialId"}, []string{profileID.String(), keyID.String()}, `{"name":"rotated"}`, h.Rotate)
		assertCredentialHandlerErrorCode(t, err, httperr.NOT_FOUND)
		if missingSvc.rotateReq.Name != "" {
			t.Fatalf("rotate request name = %q; want no delegated rotate", missingSvc.rotateReq.Name)
		}

		_, err = runCredentialHandler(t, http.MethodDelete, "/ui/api/team/credentials/"+keyID.String(), "/ui/api/team/credentials/:credentialId", []string{"teamId", "credentialId"}, []string{profileID.String(), keyID.String()}, h.Delete)
		assertCredentialHandlerErrorCode(t, err, httperr.NOT_FOUND)
		if missingSvc.deletedKeyID != uuid.Nil {
			t.Fatalf("deleted key = %s; want no delegated delete", missingSvc.deletedKeyID)
		}
	})

	t.Run("rotate rejects scope changes and accepts metadata updates", func(t *testing.T) {
		withScopes := `{"name":"rotated","scopes":["read"]}`
		_, err := runCredentialHandlerWithBody(t, http.MethodPost, "/ui/api/team/credentials/"+keyID.String()+"/rotate", "/ui/api/team/credentials/:credentialId/rotate", []string{"teamId", "credentialId"}, []string{profileID.String(), keyID.String()}, withScopes, h.Rotate)
		if err == nil || !strings.Contains(err.Error(), "scopes cannot be changed") {
			t.Fatalf("Rotate error = %v; want scopes rejection", err)
		}

		body := `{"name":"rotated","rate_limit":7,"expires_at":"2026-05-30T13:00:00Z"}`
		rec, err := runCredentialHandlerWithBody(t, http.MethodPost, "/ui/api/team/credentials/"+keyID.String()+"/rotate", "/ui/api/team/credentials/:credentialId/rotate", []string{"teamId", "credentialId"}, []string{profileID.String(), keyID.String()}, body, h.Rotate)
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
		rec, err := runCredentialHandler(t, http.MethodDelete, "/ui/api/team/credentials/"+keyID.String(), "/ui/api/team/credentials/:credentialId", []string{"teamId", "credentialId"}, []string{profileID.String(), keyID.String()}, h.Delete)
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

	t.Run("canonical member routes scope to principal team", func(t *testing.T) {
		teamID := uuid.New()
		memberID := uuid.New()
		memberKey := &domain.Credential{
			ID:        memberID,
			TeamID:    teamID,
			Name:      "member profile",
			Role:      service.CredentialRoleMember,
			KeySuffix: "lmno",
			Scopes:    []string{"read"},
			CreatedAt: createdAt,
		}

		updateSvc := &apiKeyHandlerService{key: memberKey}
		updateHandler := NewCredentialHandler(updateSvc)
		_, err := runCredentialHandlerWithUpdateBodyAndPrincipalTeam(t, http.MethodPatch, "/ui/api/team/credentials/"+memberID.String(), "/ui/api/team/credentials/:credentialId", []string{"credentialId"}, []string{memberID.String()}, `{"name":"renamed member"}`, teamID, updateHandler.Update)
		if err != nil {
			t.Fatalf("Update error: %v", err)
		}
		if updateSvc.getProfileID != teamID || updateSvc.getKeyID != memberID || updateSvc.updateProfileID != teamID || updateSvc.updateKeyID != memberID {
			t.Fatalf("update scoped get/update = (%s,%s)/(%s,%s); want team %s member %s", updateSvc.getProfileID, updateSvc.getKeyID, updateSvc.updateProfileID, updateSvc.updateKeyID, teamID, memberID)
		}

		rotateSvc := &apiKeyHandlerService{key: memberKey, raw: "rotated-secret"}
		rotateHandler := NewCredentialHandler(rotateSvc)
		_, err = runCredentialHandlerWithBodyAndPrincipalTeam(t, http.MethodPost, "/ui/api/team/credentials/"+memberID.String()+"/rotate", "/ui/api/team/credentials/:credentialId/rotate", []string{"credentialId"}, []string{memberID.String()}, `{"name":"rotated member"}`, teamID, rotateHandler.Rotate)
		if err != nil {
			t.Fatalf("Rotate error: %v", err)
		}
		if rotateSvc.getProfileID != teamID || rotateSvc.getKeyID != memberID || rotateSvc.rotateProfileID != teamID || rotateSvc.rotateKeyID != memberID {
			t.Fatalf("rotate scoped get/rotate = (%s,%s)/(%s,%s); want team %s member %s", rotateSvc.getProfileID, rotateSvc.getKeyID, rotateSvc.rotateProfileID, rotateSvc.rotateKeyID, teamID, memberID)
		}

		deleteSvc := &apiKeyHandlerService{key: memberKey}
		deleteHandler := NewCredentialHandler(deleteSvc)
		_, err = runCredentialHandlerWithPrincipalTeam(t, http.MethodDelete, "/ui/api/team/credentials/"+memberID.String(), "/ui/api/team/credentials/:credentialId", []string{"credentialId"}, []string{memberID.String()}, teamID, deleteHandler.Delete)
		if err != nil {
			t.Fatalf("Delete error: %v", err)
		}
		if deleteSvc.getProfileID != teamID || deleteSvc.getKeyID != memberID || deleteSvc.deleteProfileID != teamID || deleteSvc.deletedKeyID != memberID {
			t.Fatalf("delete scoped get/delete = (%s,%s)/(%s,%s); want team %s member %s", deleteSvc.getProfileID, deleteSvc.getKeyID, deleteSvc.deleteProfileID, deleteSvc.deletedKeyID, teamID, memberID)
		}
	})
}

func TestCredentialHandlerValidationErrors(t *testing.T) {
	h := NewCredentialHandler(&apiKeyHandlerService{})
	profileID := uuid.New().String()
	keyID := uuid.New().String()

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
			target:     "/ui/api/team/credentials",
			path:       "/ui/api/team/credentials",
			paramNames: []string{"teamId"},
			paramVals:  []string{""},
			body:       `{"name":"ops"}`,
			handler:    h.Create,
			want:       "team ID is required",
		},
		{
			name:       "get invalid key id",
			method:     http.MethodGet,
			target:     "/ui/api/team/credentials/not-a-uuid",
			path:       "/ui/api/team/credentials/:credentialId",
			paramNames: []string{"teamId", "credentialId"},
			paramVals:  []string{profileID, "not-a-uuid"},
			handler:    h.Get,
			want:       "invalid credential ID format",
		},
		{
			name:       "update invalid team id",
			method:     http.MethodPatch,
			target:     "/ui/api/team/credentials/" + keyID,
			path:       "/ui/api/team/credentials/:credentialId",
			paramNames: []string{"teamId", "credentialId"},
			paramVals:  []string{"not-a-uuid", keyID},
			body:       `{"name":"ops"}`,
			handler:    h.Update,
			want:       "invalid team ID format",
		},
		{
			name:       "update missing body",
			method:     http.MethodPatch,
			target:     "/ui/api/team/credentials/" + keyID,
			path:       "/ui/api/team/credentials/:credentialId",
			paramNames: []string{"teamId", "credentialId"},
			paramVals:  []string{profileID, keyID},
			handler:    h.Update,
			want:       "request body not found",
		},
		{
			name:       "delete missing key id",
			method:     http.MethodDelete,
			target:     "/ui/api/team/credentials/",
			path:       "/ui/api/team/credentials/:credentialId",
			paramNames: []string{"teamId", "credentialId"},
			paramVals:  []string{profileID, ""},
			handler:    h.Delete,
			want:       "credential ID is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runCredentialHandlerWithOptionalBody(t, tc.method, tc.target, tc.path, tc.paramNames, tc.paramVals, tc.body, tc.handler)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestCredentialHandlerAdditionalBranches(t *testing.T) {
	profileID := uuid.New()
	keyID := uuid.New()
	key := &domain.Credential{
		ID:        keyID,
		TeamID:    profileID,
		Name:      "ops key",
		KeySuffix: "wxyz",
		Scopes:    []string{"read"},
		CreatedAt: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
	}

	t.Run("create missing validated body", func(t *testing.T) {
		h := NewCredentialHandler(&apiKeyHandlerService{key: key, raw: "raw"})
		_, err := runCredentialHandler(t, http.MethodPost, "/ui/api/team/credentials", "/ui/api/team/credentials", []string{"teamId"}, []string{profileID.String()}, h.Create)
		if err == nil || !strings.Contains(err.Error(), "request body not found") {
			t.Fatalf("Create err = %v; want missing body", err)
		}
	})

	t.Run("create rejects invalid expires_at", func(t *testing.T) {
		svc := &apiKeyHandlerService{key: key, raw: "raw"}
		h := NewCredentialHandler(svc)
		_, err := runCredentialHandlerWithBody(t, http.MethodPost, "/ui/api/team/credentials", "/ui/api/team/credentials", []string{"teamId"}, []string{profileID.String()}, `{"name":"ops","expires_at":"not-rfc3339"}`, h.Create)
		assertCredentialHandlerErrorCode(t, err, httperr.VALIDATION_ERROR)
		if !strings.Contains(err.Error(), "expires_at must be in RFC3339 format") {
			t.Fatalf("Create error = %v; want RFC3339 validation", err)
		}
		if svc.createReq.Name != "" {
			t.Fatalf("create request = %+v; want no delegated create", svc.createReq)
		}
	})

	t.Run("rotate rejects invalid expires_at", func(t *testing.T) {
		svc := &apiKeyHandlerService{key: key, raw: "raw"}
		h := NewCredentialHandler(svc)
		_, err := runCredentialHandlerWithBody(t, http.MethodPost, "/ui/api/team/credentials/"+keyID.String()+"/rotate", "/ui/api/team/credentials/:credentialId/rotate", []string{"teamId", "credentialId"}, []string{profileID.String(), keyID.String()}, `{"name":"ops","expires_at":"not-rfc3339"}`, h.Rotate)
		assertCredentialHandlerErrorCode(t, err, httperr.VALIDATION_ERROR)
		if !strings.Contains(err.Error(), "expires_at must be in RFC3339 format") {
			t.Fatalf("Rotate error = %v; want RFC3339 validation", err)
		}
		if svc.rotateReq.Name != "" {
			t.Fatalf("rotate request = %+v; want no delegated rotate", svc.rotateReq)
		}
	})

	t.Run("list count error propagates", func(t *testing.T) {
		h := NewCredentialHandler(&apiKeyHandlerService{keys: []*domain.Credential{key}, countErr: httperr.New(httperr.INTERNAL_ERROR, "count failed")})
		_, err := runCredentialHandler(t, http.MethodGet, "/ui/api/team/credentials", "/ui/api/team/credentials", []string{"teamId"}, []string{profileID.String()}, h.List)
		if err == nil || !strings.Contains(err.Error(), "count failed") {
			t.Fatalf("List err = %v; want count failed", err)
		}
	})

	t.Run("get accepts credentialId param name", func(t *testing.T) {
		h := NewCredentialHandler(&apiKeyHandlerService{key: key})
		rec, err := runCredentialHandlerWithPrincipalTeam(t, http.MethodGet, "/ui/api/team/credentials/"+keyID.String(), "/ui/api/team/credentials/:credentialId", []string{"credentialId"}, []string{keyID.String()}, profileID, h.Get)
		if err != nil {
			t.Fatalf("Get error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200", rec.Code)
		}
	})

	t.Run("rotate missing body", func(t *testing.T) {
		h := NewCredentialHandler(&apiKeyHandlerService{key: key, raw: "raw"})
		_, err := runCredentialHandler(t, http.MethodPost, "/ui/api/team/credentials/"+keyID.String()+"/rotate", "/ui/api/team/credentials/:credentialId/rotate", []string{"teamId", "credentialId"}, []string{profileID.String(), keyID.String()}, h.Rotate)
		if err == nil || !strings.Contains(err.Error(), "request body not found") {
			t.Fatalf("Rotate err = %v; want missing body", err)
		}
	})

	t.Run("invalid team ids are rejected", func(t *testing.T) {
		h := NewCredentialHandler(&apiKeyHandlerService{key: key, raw: "raw"})
		cases := []struct {
			name    string
			method  string
			target  string
			path    string
			handler echo.HandlerFunc
		}{
			{name: "list", method: http.MethodGet, target: "/ui/api/team/credentials", path: "/ui/api/team/credentials", handler: h.List},
			{name: "delete", method: http.MethodDelete, target: "/ui/api/team/credentials/" + keyID.String(), path: "/ui/api/team/credentials/:credentialId", handler: h.Delete},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := runCredentialHandler(t, tc.method, tc.target, tc.path, []string{"teamId", "credentialId"}, []string{"not-a-uuid", keyID.String()}, tc.handler)
				if err == nil || !strings.Contains(err.Error(), "invalid team ID format") {
					t.Fatalf("%s err = %v; want invalid team ID", tc.name, err)
				}
			})
		}
	})
}

func TestCredentialResponseHelpersPreferCanonicalFields(t *testing.T) {
	teamID := uuid.New()
	keyID := uuid.New()
	lastUsedAt := time.Date(2026, 5, 30, 12, 1, 0, 0, time.UTC)
	expiresAt := lastUsedAt.Add(time.Hour)
	key := &domain.Credential{
		ID:         keyID,
		TeamID:     teamID,
		Name:       "canonical",
		KeySuffix:  "abcd",
		Scopes:     []string{"read"},
		RateLimit:  11,
		LastUsedAt: &lastUsedAt,
		ExpiresAt:  &expiresAt,
		CreatedAt:  lastUsedAt,
	}

	resp := toCredentialResponse(key)
	if resp.TeamID != teamID || resp.Name != "canonical" || resp.LastUsedAt == nil || resp.ExpiresAt == nil {
		t.Fatalf("CredentialResponse = %+v", resp)
	}

	item := toCredentialListItem(key)
	if item.TeamID != teamID || item.Name != "canonical" || item.LastUsedAt == nil || item.ExpiresAt == nil {
		t.Fatalf("CredentialListItem = %+v", item)
	}
}

func runCredentialHandlerWithBody(t *testing.T, method, target, path string, paramNames, paramVals []string, body string, handler echo.HandlerFunc) (*httptest.ResponseRecorder, error) {
	return runCredentialHandlerWithOptionalBody(t, method, target, path, paramNames, paramVals, body, handler)
}

func runCredentialHandlerWithBodyAndPrincipalTeam(t *testing.T, method, target, path string, paramNames, paramVals []string, body string, principalTeamID uuid.UUID, handler echo.HandlerFunc) (*httptest.ResponseRecorder, error) {
	return runCredentialHandlerWithOptionalBodyAndPrincipalTeam(t, method, target, path, paramNames, paramVals, body, principalTeamID, handler)
}

func assertCredentialHandlerErrorCode(t *testing.T, err error, want httperr.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil; want %s", want)
	}
	apiErr, ok := err.(httperr.APIErrorProvider)
	if !ok {
		t.Fatalf("error = %T %v; want httperr.APIErrorProvider", err, err)
	}
	if got := apiErr.GetCode(); got != want {
		t.Fatalf("error code = %s; want %s", got, want)
	}
}

func runCredentialHandlerWithUpdateBody(t *testing.T, method, target, path string, paramNames, paramVals []string, body string, handler echo.HandlerFunc) (*httptest.ResponseRecorder, error) {
	t.Helper()

	rec, c := newCredentialHandlerContext(t, method, target, path, paramNames, paramVals, body)
	wrapped := middleware.BindAndValidate[dto.UpdateCredentialRequest](middleware.UpdateCredentialBodyKey)(handler)
	return rec, wrapped(c)
}

func runCredentialHandlerWithUpdateBodyAndPrincipalTeam(t *testing.T, method, target, path string, paramNames, paramVals []string, body string, principalTeamID uuid.UUID, handler echo.HandlerFunc) (*httptest.ResponseRecorder, error) {
	t.Helper()

	rec, c := newCredentialHandlerContextWithPrincipalTeam(t, method, target, path, paramNames, paramVals, body, principalTeamID)
	wrapped := middleware.BindAndValidate[dto.UpdateCredentialRequest](middleware.UpdateCredentialBodyKey)(handler)
	return rec, wrapped(c)
}

func runCredentialHandler(t *testing.T, method, target, path string, paramNames, paramVals []string, handler echo.HandlerFunc) (*httptest.ResponseRecorder, error) {
	return runCredentialHandlerWithOptionalBody(t, method, target, path, paramNames, paramVals, "", handler)
}

func runCredentialHandlerWithPrincipalTeam(t *testing.T, method, target, path string, paramNames, paramVals []string, principalTeamID uuid.UUID, handler echo.HandlerFunc) (*httptest.ResponseRecorder, error) {
	return runCredentialHandlerWithOptionalBodyAndPrincipalTeam(t, method, target, path, paramNames, paramVals, "", principalTeamID, handler)
}

func runCredentialHandlerWithOptionalBody(t *testing.T, method, target, path string, paramNames, paramVals []string, body string, handler echo.HandlerFunc) (*httptest.ResponseRecorder, error) {
	return runCredentialHandlerWithOptionalBodyAndPrincipalTeam(t, method, target, path, paramNames, paramVals, body, uuid.Nil, handler)
}

func runCredentialHandlerWithOptionalBodyAndPrincipalTeam(t *testing.T, method, target, path string, paramNames, paramVals []string, body string, principalTeamID uuid.UUID, handler echo.HandlerFunc) (*httptest.ResponseRecorder, error) {
	t.Helper()

	rec, c := newCredentialHandlerContextWithPrincipalTeam(t, method, target, path, paramNames, paramVals, body, principalTeamID)
	if body == "" {
		return rec, handler(c)
	}
	wrapped := middleware.BindAndValidate[dto.CreateCredentialRequest](middleware.CreateCredentialBodyKey)(handler)
	return rec, wrapped(c)
}

func newCredentialHandlerContext(t *testing.T, method, target, path string, paramNames, paramVals []string, body string) (*httptest.ResponseRecorder, echo.Context) {
	return newCredentialHandlerContextWithPrincipalTeam(t, method, target, path, paramNames, paramVals, body, uuid.Nil)
}

func newCredentialHandlerContextWithPrincipalTeam(t *testing.T, method, target, path string, paramNames, paramVals []string, body string, principalTeamID uuid.UUID) (*httptest.ResponseRecorder, echo.Context) {
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
		CredentialID: &principalID,
		TeamID:       principalTeamID,
		Role:         "admin",
	}))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath(path)
	c.SetParamNames(paramNames...)
	c.SetParamValues(paramVals...)

	return rec, c
}
