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
	get    func(context.Context, uuid.UUID) (*domain.Team, error)
	update func(context.Context, uuid.UUID, service.UpdateTeamRequest, *string, string, string, string) (*domain.Team, error)
	delete func(context.Context, uuid.UUID, *string, string, string, string) error
}

func (s *profileServiceStub) Create(context.Context, service.CreateTeamRequest, *string, string, string, string) (*domain.Team, error) {
	return nil, nil
}

func (s *profileServiceStub) Get(ctx context.Context, id uuid.UUID) (*domain.Team, error) {
	if s.get != nil {
		return s.get(ctx, id)
	}
	return nil, httperr.New(httperr.NOT_FOUND, "team not found")
}

func (*profileServiceStub) GetByID(context.Context, uuid.UUID) (*domain.Team, error) {
	return nil, nil
}

func (*profileServiceStub) List(context.Context, int, int) ([]*domain.Team, error) {
	return nil, nil
}

func (*profileServiceStub) Count(context.Context) (int64, error) {
	return 0, nil
}

func (s *profileServiceStub) Update(ctx context.Context, id uuid.UUID, req service.UpdateTeamRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Team, error) {
	if s.update != nil {
		return s.update(ctx, id, req, actorKeyID, actorRole, clientIP, correlationID)
	}
	return &domain.Team{ID: id, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}, nil
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
	credentialID := uuid.New()
	return &middleware.Principal{CredentialID: &credentialID, TeamID: teamID, Role: "manager"}
}

func TestTeamHandlerGetUsesAuthenticatedTeam(t *testing.T) {
	teamID := uuid.New()
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	h := NewTeamHandler(&profileServiceStub{
		get: func(_ context.Context, id uuid.UUID) (*domain.Team, error) {
			require.Equal(t, teamID, id)
			return &domain.Team{ID: id, Name: "Team", CreatedAt: now, UpdatedAt: now}, nil
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

func TestTeamHandlerGetRequiresAuthenticatedTeam(t *testing.T) {
	e := newTestEcho()
	e.GET("/ui/api/team", NewTeamHandler(&profileServiceStub{}).Get)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/team", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), string(httperr.PROFILE_ID_REQUIRED))
}

func TestTeamHandlerPatchUsesAuthenticatedTeam(t *testing.T) {
	teamID := uuid.New()
	keyID := uuid.New()
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	h := NewTeamHandler(&profileServiceStub{
		update: func(_ context.Context, id uuid.UUID, req service.UpdateTeamRequest, actorKeyID *string, actorRole, _, _ string) (*domain.Team, error) {
			require.Equal(t, teamID, id)
			require.NotNil(t, req.Description)
			require.Equal(t, "Updated team", *req.Description)
			require.NotNil(t, actorKeyID)
			require.Equal(t, keyID.String(), *actorKeyID)
			require.Equal(t, "manager", actorRole)
			return &domain.Team{ID: id, Description: *req.Description, CreatedAt: now, UpdatedAt: now}, nil
		},
	})
	e := newTestEcho()
	e.PATCH("/ui/api/team", h.Patch, middleware.BindAndValidate[dto.UpdateTeamRequest](middleware.UpdateTeamBodyKey))

	req := httptest.NewRequest(http.MethodPatch, "/ui/api/team", strings.NewReader(`{"description":"Updated team"}`))
	req.Header.Set("Content-Type", "application/json")
	principal := teamPrincipal(teamID)
	principal.CredentialID = &keyID
	req = req.WithContext(middleware.SetPrincipalForTest(req.Context(), principal))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestTeamHandlerPatchRequiresValidatedBody(t *testing.T) {
	teamID := uuid.New()
	e := newTestEcho()
	req := httptest.NewRequest(http.MethodPatch, "/ui/api/team", nil)
	req = req.WithContext(middleware.SetPrincipalForTest(req.Context(), teamPrincipal(teamID)))
	rec := httptest.NewRecorder()

	err := NewTeamHandler(&profileServiceStub{}).Patch(e.NewContext(req, rec))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "request body not found")
}

func TestTeamHandlerDeleteUsesAuthenticatedTeam(t *testing.T) {
	teamID := uuid.New()
	h := NewTeamHandler(&profileServiceStub{
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
