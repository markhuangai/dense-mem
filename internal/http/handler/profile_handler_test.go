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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

type profileServiceStub struct {
	get    func(context.Context, uuid.UUID) (*domain.Profile, error)
	update func(context.Context, uuid.UUID, service.UpdateProfileRequest, *string, string, string, string) (*domain.Profile, error)
	delete func(context.Context, uuid.UUID, *string, string, string, string) error
}

func (s *profileServiceStub) Create(context.Context, service.CreateProfileRequest, *string, string, string, string) (*domain.Profile, error) {
	return nil, nil
}

func (s *profileServiceStub) Get(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
	if s.get != nil {
		return s.get(ctx, id)
	}
	return nil, httperr.New(httperr.NOT_FOUND, "team not found")
}

func (*profileServiceStub) GetByID(context.Context, uuid.UUID) (*domain.Profile, error) {
	return nil, nil
}

func (*profileServiceStub) List(context.Context, int, int) ([]*domain.Profile, error) {
	return nil, nil
}

func (*profileServiceStub) Count(context.Context) (int64, error) {
	return 0, nil
}

func (s *profileServiceStub) Update(ctx context.Context, id uuid.UUID, req service.UpdateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error) {
	if s.update != nil {
		return s.update(ctx, id, req, actorKeyID, actorRole, clientIP, correlationID)
	}
	return &domain.Profile{ID: id, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}, nil
}

func (s *profileServiceStub) Delete(ctx context.Context, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	if s.delete != nil {
		return s.delete(ctx, id, actorKeyID, actorRole, clientIP, correlationID)
	}
	return nil
}

func newTestEcho() *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	return e
}

func teamPrincipal(teamID uuid.UUID) *middleware.Principal {
	return &middleware.Principal{KeyID: uuid.New(), TeamID: teamID, Role: "manager"}
}

func TestProfileHandlerGetUsesAuthenticatedTeam(t *testing.T) {
	teamID := uuid.New()
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	h := NewProfileHandler(&profileServiceStub{
		get: func(_ context.Context, id uuid.UUID) (*domain.Profile, error) {
			require.Equal(t, teamID, id)
			return &domain.Profile{ID: id, Name: "Team", CreatedAt: now, UpdatedAt: now}, nil
		},
	})
	e := newTestEcho()
	e.GET("/ui/api/team", h.Get)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/team", nil)
	req = req.WithContext(middleware.SetPrincipalForTest(req.Context(), teamPrincipal(teamID)))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body response.SuccessEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.Data)
}

func TestProfileHandlerGetRequiresAuthenticatedTeam(t *testing.T) {
	e := newTestEcho()
	e.GET("/ui/api/team", NewProfileHandler(&profileServiceStub{}).Get)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/team", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), string(httperr.PROFILE_ID_REQUIRED))
}

func TestProfileHandlerPatchUsesAuthenticatedTeam(t *testing.T) {
	teamID := uuid.New()
	keyID := uuid.New()
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	h := NewProfileHandler(&profileServiceStub{
		update: func(_ context.Context, id uuid.UUID, req service.UpdateProfileRequest, actorKeyID *string, actorRole, _, _ string) (*domain.Profile, error) {
			require.Equal(t, teamID, id)
			require.NotNil(t, req.Description)
			require.Equal(t, "Updated team", *req.Description)
			require.NotNil(t, actorKeyID)
			require.Equal(t, keyID.String(), *actorKeyID)
			require.Equal(t, "manager", actorRole)
			return &domain.Profile{ID: id, Description: *req.Description, CreatedAt: now, UpdatedAt: now}, nil
		},
	})
	e := newTestEcho()
	e.PATCH("/ui/api/team", h.Patch, middleware.BindAndValidate[dto.UpdateProfileRequest](middleware.UpdateProfileBodyKey))

	req := httptest.NewRequest(http.MethodPatch, "/ui/api/team", strings.NewReader(`{"description":"Updated team"}`))
	req.Header.Set("Content-Type", "application/json")
	principal := teamPrincipal(teamID)
	principal.KeyID = keyID
	req = req.WithContext(middleware.SetPrincipalForTest(req.Context(), principal))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestProfileHandlerPatchRequiresValidatedBody(t *testing.T) {
	teamID := uuid.New()
	e := newTestEcho()
	req := httptest.NewRequest(http.MethodPatch, "/ui/api/team", nil)
	req = req.WithContext(middleware.SetPrincipalForTest(req.Context(), teamPrincipal(teamID)))
	rec := httptest.NewRecorder()

	err := NewProfileHandler(&profileServiceStub{}).Patch(e.NewContext(req, rec))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "request body not found")
}

func TestProfileHandlerDeleteUsesAuthenticatedTeam(t *testing.T) {
	teamID := uuid.New()
	h := NewProfileHandler(&profileServiceStub{
		delete: func(_ context.Context, id uuid.UUID, _ *string, actorRole, _, _ string) error {
			require.Equal(t, teamID, id)
			require.Equal(t, "manager", actorRole)
			return nil
		},
	})
	e := newTestEcho()
	e.DELETE("/ui/api/team", h.Delete)

	req := httptest.NewRequest(http.MethodDelete, "/ui/api/team", nil)
	req = req.WithContext(middleware.SetPrincipalForTest(req.Context(), teamPrincipal(teamID)))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"status":"deleted"`)
}
