package handler

import (
	"context"
	"encoding/json"
	"errors"
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

// mockProfileService is a mock implementation of ProfileServiceInterface
type mockProfileService struct {
	createFunc  func(ctx context.Context, req service.CreateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error)
	getFunc     func(ctx context.Context, id uuid.UUID) (*domain.Profile, error)
	getByIDFunc func(ctx context.Context, id uuid.UUID) (*domain.Profile, error)
	listFunc    func(ctx context.Context, limit, offset int) ([]*domain.Profile, error)
	countFunc   func(ctx context.Context) (int64, error)
	updateFunc  func(ctx context.Context, id uuid.UUID, req service.UpdateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error)
	deleteFunc  func(ctx context.Context, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error
}

func (m *mockProfileService) Create(ctx context.Context, req service.CreateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error) {
	if m.createFunc != nil {
		return m.createFunc(ctx, req, actorKeyID, actorRole, clientIP, correlationID)
	}
	return &domain.Profile{
		ID:          uuid.New(),
		Name:        req.Name,
		Description: req.Description,
		Metadata:    req.Metadata,
		Config:      req.Config,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}, nil
}

func (m *mockProfileService) Get(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
	if m.getFunc != nil {
		return m.getFunc(ctx, id)
	}
	return nil, httperr.New(httperr.NOT_FOUND, "profile not found")
}

func (m *mockProfileService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
	if m.getByIDFunc != nil {
		return m.getByIDFunc(ctx, id)
	}
	return nil, nil
}

func (m *mockProfileService) List(ctx context.Context, limit, offset int) ([]*domain.Profile, error) {
	if m.listFunc != nil {
		return m.listFunc(ctx, limit, offset)
	}
	return []*domain.Profile{}, nil
}

func (m *mockProfileService) Count(ctx context.Context) (int64, error) {
	if m.countFunc != nil {
		return m.countFunc(ctx)
	}
	return 0, nil
}

func (m *mockProfileService) Update(ctx context.Context, id uuid.UUID, req service.UpdateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error) {
	if m.updateFunc != nil {
		return m.updateFunc(ctx, id, req, actorKeyID, actorRole, clientIP, correlationID)
	}
	name := ""
	if req.Name != nil {
		name = *req.Name
	}
	desc := ""
	if req.Description != nil {
		desc = *req.Description
	}
	return &domain.Profile{
		ID:          id,
		Name:        name,
		Description: desc,
		Metadata:    req.Metadata,
		Config:      req.Config,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}, nil
}

func (m *mockProfileService) Delete(ctx context.Context, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	if m.deleteFunc != nil {
		return m.deleteFunc(ctx, id, actorKeyID, actorRole, clientIP, correlationID)
	}
	return nil
}

// principalContextKey is the context key for principal (copied from middleware)
type principalContextKey struct{}

// newTestEcho creates a new Echo instance with the custom error handler
func newTestEcho() *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	return e
}

// setPrincipal sets a principal in the context for testing
func setPrincipal(ctx context.Context, principal *middleware.Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// TestProfileHandler_Create_RouteAuthorizationElsewhere verifies the handler
// itself does not perform route-level authorization.
func TestProfileHandler_Create_RouteAuthorizationElsewhere(t *testing.T) {
	e := newTestEcho()
	h := NewProfileHandler(&mockProfileService{})

	// Set standard principal
	profileID := uuid.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = setPrincipal(ctx, &middleware.Principal{
				KeyID:     uuid.New(),
				ProfileID: &profileID,
				Role:      "standard",
				Scopes:    []string{"read"},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	// Route-level authorization is enforced by router wiring, not the handler.
	e.POST("/api/v1/profiles", h.Create, middleware.BindAndValidate[dto.CreateProfileRequest](middleware.CreateProfileBodyKey))

	body := `{"name": "Test Profile", "description": "Test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// The handler itself still works when invoked directly.
	assert.Equal(t, http.StatusCreated, rec.Code)
}

// TestProfileHandler_Create_201 tests successful profile creation.
func TestProfileHandler_Create_201(t *testing.T) {
	e := newTestEcho()
	mockSvc := &mockProfileService{
		createFunc: func(ctx context.Context, req service.CreateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error) {
			assert.Equal(t, "Test Profile", req.Name)
			assert.Equal(t, "Test Description", req.Description)
			return &domain.Profile{
				ID:          uuid.New(),
				Name:        req.Name,
				Description: req.Description,
				Metadata:    req.Metadata,
				Config:      req.Config,
				CreatedAt:   time.Now().UTC(),
				UpdatedAt:   time.Now().UTC(),
			}, nil
		},
	}
	h := NewProfileHandler(mockSvc)

	// Set an authenticated principal. Route-level authorization is tested elsewhere.
	profileID := uuid.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = setPrincipal(ctx, &middleware.Principal{
				KeyID:     uuid.New(),
				ProfileID: &profileID,
				Role:      "standard",
				Scopes:    []string{"write"},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/profiles", h.Create, middleware.BindAndValidate[dto.CreateProfileRequest](middleware.CreateProfileBodyKey))

	body := `{"name": "Test Profile", "description": "Test Description"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	var resp response.SuccessEnvelope
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.NotNil(t, resp.Data)
}

// TestProfileHandler_List_RouteAuthorizationElsewhere verifies the handler
// itself does not perform route-level authorization.
func TestProfileHandler_List_RouteAuthorizationElsewhere(t *testing.T) {
	e := newTestEcho()
	h := NewProfileHandler(&mockProfileService{})

	// Set standard principal
	profileID := uuid.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = setPrincipal(ctx, &middleware.Principal{
				KeyID:     uuid.New(),
				ProfileID: &profileID,
				Role:      "standard",
				Scopes:    []string{"read"},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	// Route-level authorization is enforced by router wiring, not the handler.
	e.GET("/api/v1/profiles", h.List)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// The handler itself still works when invoked directly.
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestProfileHandler_List_Pagination tests list with pagination envelope.
func TestProfileHandler_List_Pagination(t *testing.T) {
	e := newTestEcho()

	profile1ID := uuid.New()
	profile2ID := uuid.New()
	now := time.Now().UTC()

	mockSvc := &mockProfileService{
		listFunc: func(ctx context.Context, limit, offset int) ([]*domain.Profile, error) {
			assert.Equal(t, 20, limit)
			assert.Equal(t, 0, offset)
			return []*domain.Profile{
				{ID: profile1ID, Name: "Profile 1", Description: "Desc 1", CreatedAt: now, UpdatedAt: now},
				{ID: profile2ID, Name: "Profile 2", Description: "Desc 2", CreatedAt: now, UpdatedAt: now},
			}, nil
		},
		countFunc: func(ctx context.Context) (int64, error) {
			return 42, nil
		},
	}
	h := NewProfileHandler(mockSvc)

	e.GET("/api/v1/profiles", h.List)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp PaginationEnvelope
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Len(t, resp.Data, 2)
	assert.Equal(t, 20, resp.Pagination.Limit)
	assert.Equal(t, 0, resp.Pagination.Offset)
	assert.Equal(t, int64(42), resp.Pagination.Total)
}

// TestProfileHandler_Get_SameProfile tests get with same-profile access.
func TestProfileHandler_Get_SameProfile(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()
	now := time.Now().UTC()

	mockSvc := &mockProfileService{
		getFunc: func(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
			assert.Equal(t, profileID, id)
			return &domain.Profile{
				ID:          profileID,
				Name:        "Test Profile",
				Description: "Test Description",
				CreatedAt:   now,
				UpdatedAt:   now,
			}, nil
		},
	}
	h := NewProfileHandler(mockSvc)

	e.GET("/api/v1/profiles/:profileId", h.Get)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/"+profileID.String(), nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp response.SuccessEnvelope
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.NotNil(t, resp.Data)
}

// TestProfileHandler_Get_InvalidUUID tests get with invalid UUID returns 400 INVALID_UUID.
func TestProfileHandler_Get_InvalidUUID(t *testing.T) {
	e := newTestEcho()
	h := NewProfileHandler(&mockProfileService{})

	e.GET("/api/v1/profiles/:profileId", h.Get)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/not-a-uuid", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp httperr.APIError
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, httperr.INVALID_UUID, resp.Code)
}

// TestProfileHandler_Get_DeletedProfile_404 tests get on deleted profile returns 404.
func TestProfileHandler_Get_DeletedProfile_404(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	mockSvc := &mockProfileService{
		getFunc: func(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
			return nil, httperr.New(httperr.NOT_FOUND, "profile not found")
		},
	}
	h := NewProfileHandler(mockSvc)

	e.GET("/api/v1/profiles/:profileId", h.Get)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/"+profileID.String(), nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)

	var resp httperr.APIError
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, httperr.NOT_FOUND, resp.Code)
}

// TestProfileHandler_Patch_SameProfile tests patch with same-profile access.
func TestProfileHandler_Patch_SameProfile(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()
	now := time.Now().UTC()

	mockSvc := &mockProfileService{
		updateFunc: func(ctx context.Context, id uuid.UUID, req service.UpdateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error) {
			assert.Equal(t, profileID, id)
			require.NotNil(t, req.Name, "Name pointer should not be nil")
			assert.Equal(t, "Updated Name", *req.Name)
			return &domain.Profile{
				ID:          profileID,
				Name:        "Updated Name",
				Description: "",
				CreatedAt:   now,
				UpdatedAt:   now,
			}, nil
		},
	}
	h := NewProfileHandler(mockSvc)

	e.PATCH("/api/v1/profiles/:profileId", h.Patch, middleware.BindAndValidate[dto.UpdateProfileRequest](middleware.UpdateProfileBodyKey))

	body := `{"name": "Updated Name"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/profiles/"+profileID.String(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp response.SuccessEnvelope
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.NotNil(t, resp.Data)
}

// TestProfileHandler_Delete_RouteAuthorizationElsewhere verifies the handler
// itself does not perform route-level authorization.
func TestProfileHandler_Delete_RouteAuthorizationElsewhere(t *testing.T) {
	e := newTestEcho()
	h := NewProfileHandler(&mockProfileService{})

	// Set standard principal
	profileID := uuid.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = setPrincipal(ctx, &middleware.Principal{
				KeyID:     uuid.New(),
				ProfileID: &profileID,
				Role:      "standard",
				Scopes:    []string{"read"},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	// Route-level authorization is enforced by router wiring.
	e.DELETE("/api/v1/profiles/:profileId", h.Delete)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/profiles/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// The handler itself still works when invoked directly.
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestProfileHandler_Delete_200 tests successful delete returns 200 with status deleted.
func TestProfileHandler_Delete_200(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	mockSvc := &mockProfileService{
		deleteFunc: func(ctx context.Context, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error {
			assert.Equal(t, profileID, id)
			return nil
		},
	}
	h := NewProfileHandler(mockSvc)

	e.DELETE("/api/v1/profiles/:profileId", h.Delete)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/profiles/"+profileID.String(), nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp response.SuccessEnvelope
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)

	data, ok := resp.Data.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "deleted", data["status"])
}

func TestProfileHandler_AdditionalErrorAndActorBranches(t *testing.T) {
	t.Run("create missing validated body", func(t *testing.T) {
		e := newTestEcho()
		h := NewProfileHandler(&mockProfileService{})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.Create(c)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "request body not found")
	})

	t.Run("create forwards actor metadata from principal", func(t *testing.T) {
		e := newTestEcho()
		keyID := uuid.New()
		mockSvc := &mockProfileService{
			createFunc: func(ctx context.Context, req service.CreateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error) {
				require.NotNil(t, actorKeyID)
				assert.Equal(t, keyID.String(), *actorKeyID)
				assert.Equal(t, "admin", actorRole)
				return &domain.Profile{ID: uuid.New(), Name: req.Name, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}, nil
			},
		}
		h := NewProfileHandler(mockSvc)
		e.POST("/api/v1/profiles", h.Create, middleware.BindAndValidate[dto.CreateProfileRequest](middleware.CreateProfileBodyKey))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles", strings.NewReader(`{"name":"Actor Team"}`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(middleware.SetPrincipalForTest(req.Context(), &middleware.Principal{KeyID: keyID, Role: "admin"}))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusCreated, rec.Code)
	})

	t.Run("list propagates list and count errors", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			svc  *mockProfileService
		}{
			{
				name: "list error",
				svc: &mockProfileService{listFunc: func(context.Context, int, int) ([]*domain.Profile, error) {
					return nil, errors.New("list failed")
				}},
			},
			{
				name: "count error",
				svc: &mockProfileService{
					listFunc: func(context.Context, int, int) ([]*domain.Profile, error) { return []*domain.Profile{}, nil },
					countFunc: func(context.Context) (int64, error) {
						return 0, errors.New("count failed")
					},
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				e := newTestEcho()
				h := NewProfileHandler(tc.svc)
				e.GET("/api/v1/profiles", h.List)
				req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles", nil)
				rec := httptest.NewRecorder()

				e.ServeHTTP(rec, req)

				assert.Equal(t, http.StatusInternalServerError, rec.Code)
			})
		}
	})

	t.Run("get nil profile returns not found", func(t *testing.T) {
		e := newTestEcho()
		profileID := uuid.New()
		h := NewProfileHandler(&mockProfileService{getFunc: func(context.Context, uuid.UUID) (*domain.Profile, error) {
			return nil, nil
		}})
		e.GET("/api/v1/profiles/:profileId", h.Get)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/"+profileID.String(), nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("patch missing body and description only", func(t *testing.T) {
		e := newTestEcho()
		profileID := uuid.New()
		h := NewProfileHandler(&mockProfileService{})
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/profiles/"+profileID.String(), nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/api/v1/profiles/:profileId")
		c.SetParamNames("profileId")
		c.SetParamValues(profileID.String())
		err := h.Patch(c)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "request body not found")

		called := false
		h = NewProfileHandler(&mockProfileService{updateFunc: func(ctx context.Context, id uuid.UUID, req service.UpdateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error) {
			called = true
			require.Nil(t, req.Name)
			require.NotNil(t, req.Description)
			return &domain.Profile{ID: id, Description: *req.Description, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}, nil
		}})
		e = newTestEcho()
		e.PATCH("/api/v1/profiles/:profileId", h.Patch, middleware.BindAndValidate[dto.UpdateProfileRequest](middleware.UpdateProfileBodyKey))
		req = httptest.NewRequest(http.MethodPatch, "/api/v1/profiles/"+profileID.String(), strings.NewReader(`{"description":"new desc"}`))
		req.Header.Set("Content-Type", "application/json")
		rec = httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.True(t, called)
	})
}
