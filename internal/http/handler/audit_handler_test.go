//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

// mockAuditService is a mock implementation of AuditServiceInterface
type mockAuditService struct {
	listFunc func(ctx context.Context, profileID string, limit, offset int) ([]service.AuditLogEntry, int, error)
}

func (m *mockAuditService) List(ctx context.Context, profileID string, limit, offset int) ([]service.AuditLogEntry, int, error) {
	if m.listFunc != nil {
		return m.listFunc(ctx, profileID, limit, offset)
	}
	return []service.AuditLogEntry{}, 0, nil
}

// TestAuditHandler_Get_SameProfile tests that a same-profile principal can access audit log.
func TestAuditHandler_Get_SameProfile(t *testing.T) {
	profileID := uuid.New()
	now := time.Now().UTC()

	tests := []struct {
		name           string
		principal      *middleware.Principal
		expectedStatus int
	}{
		{
			name: "same-profile principal can access their audit log",
			principal: &middleware.Principal{
				KeyID:     uuid.New(),
				ProfileID: &profileID,
				Role:      "standard",
				Scopes:    []string{"read"},
			},
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEcho()
			mockSvc := &mockAuditService{
				listFunc: func(ctx context.Context, pid string, limit, offset int) ([]service.AuditLogEntry, int, error) {
					assert.Equal(t, profileID.String(), pid)
					assert.Equal(t, 20, limit)
					assert.Equal(t, 0, offset)
					return []service.AuditLogEntry{
						{
							ID:            "audit-1",
							ProfileID:     &pid,
							Timestamp:     now,
							Operation:     "CREATE",
							EntityType:    "profile",
							EntityID:      pid,
							ActorRole:     "system",
							ClientIP:      "192.168.1.1",
							CorrelationID: "corr-1",
						},
					}, 1, nil
				},
			}
			h := NewAuditHandler(mockSvc)

			// Set principal in context using the proper context key
			e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					ctx := c.Request().Context()
					ctx = middleware.SetPrincipalForTest(ctx, tt.principal)
					c.SetRequest(c.Request().WithContext(ctx))
					return next(c)
				}
			})

			e.GET("/api/v1/profiles/:profileId/audit-log", h.Get)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/"+profileID.String()+"/audit-log", nil)
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)

			if tt.expectedStatus == http.StatusOK {
				var resp PaginationEnvelope
				err := json.Unmarshal(rec.Body.Bytes(), &resp)
				require.NoError(t, err)
				assert.NotNil(t, resp.Data)
			}
		})
	}
}

// TestAuditHandler_Get_TeamRouteUsesTeamID verifies the canonical
// /api/v1/teams/:teamId/audit-log route reads teamId and authorizes using
// Principal.TeamID, not the team-profile key ID.
func TestAuditHandler_Get_TeamRouteUsesTeamID(t *testing.T) {
	e := newTestEcho()
	teamID := uuid.New()
	teamProfileKeyID := uuid.New()

	mockSvc := &mockAuditService{
		listFunc: func(ctx context.Context, pid string, limit, offset int) ([]service.AuditLogEntry, int, error) {
			assert.Equal(t, teamID.String(), pid)
			return []service.AuditLogEntry{}, 0, nil
		},
	}
	h := NewAuditHandler(mockSvc)

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetPrincipalForTest(ctx, &middleware.Principal{
				KeyID:     teamProfileKeyID,
				TeamID:    teamID,
				ProfileID: &teamProfileKeyID,
				Role:      "standard",
				Scopes:    []string{"read"},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.GET("/api/v1/teams/:teamId/audit-log", h.Get)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/teams/"+teamID.String()+"/audit-log", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestAuditHandler_Get_DefaultPagination tests that default pagination is limit=20, offset=0.
func TestAuditHandler_Get_DefaultPagination(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	mockSvc := &mockAuditService{
		listFunc: func(ctx context.Context, pid string, limit, offset int) ([]service.AuditLogEntry, int, error) {
			assert.Equal(t, 20, limit, "default limit should be 20")
			assert.Equal(t, 0, offset, "default offset should be 0")
			return []service.AuditLogEntry{}, 0, nil
		},
	}
	h := NewAuditHandler(mockSvc)

	// Set same-profile principal using proper context key
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetPrincipalForTest(ctx, &middleware.Principal{
				KeyID:     uuid.New(),
				ProfileID: &profileID,
				Role:      "standard",
				Scopes:    []string{"read"},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.GET("/api/v1/profiles/:profileId/audit-log", h.Get)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/"+profileID.String()+"/audit-log", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp PaginationEnvelope
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, 20, resp.Pagination.Limit)
	assert.Equal(t, 0, resp.Pagination.Offset)
}

// TestAuditHandler_Get_MaxLimit tests that max limit is clamped to 100.
func TestAuditHandler_Get_MaxLimit(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	mockSvc := &mockAuditService{
		listFunc: func(ctx context.Context, pid string, limit, offset int) ([]service.AuditLogEntry, int, error) {
			assert.LessOrEqual(t, limit, 100, "limit should be clamped to max 100")
			return []service.AuditLogEntry{}, 0, nil
		},
	}
	h := NewAuditHandler(mockSvc)

	// Set same-profile principal using proper context key
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetPrincipalForTest(ctx, &middleware.Principal{
				KeyID:     uuid.New(),
				ProfileID: &profileID,
				Role:      "standard",
				Scopes:    []string{"read"},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.GET("/api/v1/profiles/:profileId/audit-log", h.Get)

	// Request limit=500, should be clamped to 100
	req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/"+profileID.String()+"/audit-log?limit=500", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp PaginationEnvelope
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, 100, resp.Pagination.Limit, "limit should be clamped to 100")
}

// TestAuditHandler_Get_DifferentProfile_Forbidden tests cross-profile access returns 403.
func TestAuditHandler_Get_DifferentProfile_Forbidden(t *testing.T) {
	e := newTestEcho()
	targetProfileID := uuid.New()
	actorProfileID := uuid.New() // Different profile

	mockSvc := &mockAuditService{
		listFunc: func(ctx context.Context, pid string, limit, offset int) ([]service.AuditLogEntry, int, error) {
			// This should never be called for forbidden requests
			t.Error("listFunc should not be called for forbidden requests")
			return nil, 0, nil
		},
	}
	h := NewAuditHandler(mockSvc)

	// Set standard principal with different profile ID using proper context key
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetPrincipalForTest(ctx, &middleware.Principal{
				KeyID:     uuid.New(),
				ProfileID: &actorProfileID, // Different from target
				Role:      "standard",
				Scopes:    []string{"read"},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.GET("/api/v1/profiles/:profileId/audit-log", h.Get)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/"+targetProfileID.String()+"/audit-log", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)

	var resp httperr.APIError
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, httperr.FORBIDDEN, resp.Code)
}

// TestAuditHandler_NoUpdateDelete verifies no update/delete routes exist for audit log.
// Echo returns 405 (Method Not Allowed) when a route path exists but the method isn't registered,
// which proves no update/delete routes exist for the audit log endpoint.
func TestAuditHandler_NoUpdateDelete(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	mockSvc := &mockAuditService{}
	h := NewAuditHandler(mockSvc)

	// Set same-profile principal using proper context key
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetPrincipalForTest(ctx, &middleware.Principal{
				KeyID:     uuid.New(),
				ProfileID: &profileID,
				Role:      "standard",
				Scopes:    []string{"read"},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	// Only register the GET endpoint - audit log is append-only
	e.GET("/api/v1/profiles/:profileId/audit-log", h.Get)

	// Verify PUT returns 405 (method not allowed - no update route)
	reqPUT := httptest.NewRequest(http.MethodPut, "/api/v1/profiles/"+profileID.String()+"/audit-log", nil)
	recPUT := httptest.NewRecorder()
	e.ServeHTTP(recPUT, reqPUT)
	assert.Equal(t, http.StatusMethodNotAllowed, recPUT.Code, "PUT should return 405 - no update route")

	// Verify POST returns 405 (method not allowed - no create route)
	reqPOST := httptest.NewRequest(http.MethodPost, "/api/v1/profiles/"+profileID.String()+"/audit-log", nil)
	recPOST := httptest.NewRecorder()
	e.ServeHTTP(recPOST, reqPOST)
	assert.Equal(t, http.StatusMethodNotAllowed, recPOST.Code, "POST should return 405 - no create route")

	// Verify DELETE returns 405 (method not allowed - no delete route)
	reqDELETE := httptest.NewRequest(http.MethodDelete, "/api/v1/profiles/"+profileID.String()+"/audit-log", nil)
	recDELETE := httptest.NewRecorder()
	e.ServeHTTP(recDELETE, reqDELETE)
	assert.Equal(t, http.StatusMethodNotAllowed, recDELETE.Code, "DELETE should return 405 - no delete route")

	// Verify PATCH returns 405 (method not allowed - no update route)
	reqPATCH := httptest.NewRequest(http.MethodPatch, "/api/v1/profiles/"+profileID.String()+"/audit-log", nil)
	recPATCH := httptest.NewRecorder()
	e.ServeHTTP(recPATCH, reqPATCH)
	assert.Equal(t, http.StatusMethodNotAllowed, recPATCH.Code, "PATCH should return 405 - no update route")
}

// TestAuditHandler_Get_InvalidUUID tests that invalid UUID returns 400 INVALID_UUID.
func TestAuditHandler_Get_InvalidUUID(t *testing.T) {
	e := newTestEcho()
	h := NewAuditHandler(&mockAuditService{})

	e.GET("/api/v1/profiles/:profileId/audit-log", h.Get)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/not-a-uuid/audit-log", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp httperr.APIError
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, httperr.INVALID_UUID, resp.Code)
}
