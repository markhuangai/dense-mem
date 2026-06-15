package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

// mockProfileResolutionService implements ProfileResolutionServiceInterface for testing.
type mockProfileResolutionService struct {
	getFunc func(ctx context.Context, id uuid.UUID) (*domain.Profile, error)
}

func (m *mockProfileResolutionService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
	if m.getFunc != nil {
		return m.getFunc(ctx, id)
	}
	return nil, nil
}

// TestProfileResolution_PathParam_Valid tests that a valid profile ID in the path param
// is correctly resolved and stored in context.
func TestProfileResolution_PathParam_Valid(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	profileID := uuid.New()
	profile := &domain.Profile{
		ID:        profileID,
		Name:      "test-profile",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	mockSvc := &mockProfileResolutionService{
		getFunc: func(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
			assert.Equal(t, profileID, id)
			return profile, nil
		},
	}

	var capturedProfileID uuid.UUID
	e.GET("/api/v1/profiles/:profileId/test", func(c echo.Context) error {
		id, ok := GetResolvedProfileID(c.Request().Context())
		require.True(t, ok, "profile ID should be in context")
		capturedProfileID = id
		return c.String(http.StatusOK, "ok")
	}, ProfileResolutionMiddleware(mockSvc))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/"+profileID.String()+"/test", nil)
	req.Header.Set("Authorization", "Bearer testprefix12345678901234567890")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, profileID, capturedProfileID)
}

// TestProfileResolution_PathParam_InvalidUUID tests that an invalid UUID in the path param
// returns a 400 INVALID_UUID error.
func TestProfileResolution_PathParam_InvalidUUID(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	mockSvc := &mockProfileResolutionService{}

	handlerCalled := false
	e.GET("/api/v1/profiles/:profileId/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	}, ProfileResolutionMiddleware(mockSvc))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/not-a-uuid/test", nil)
	req.Header.Set("Authorization", "Bearer testprefix12345678901234567890")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.False(t, handlerCalled, "handler should not be called")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "INVALID_UUID")
}

// TestProfileResolution_Header_Valid tests that a valid profile ID in the X-Profile-ID header
// is correctly resolved and stored in context for tool routes.
func TestProfileResolution_Header_Valid(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	profileID := uuid.New()
	profile := &domain.Profile{
		ID:        profileID,
		Name:      "test-profile",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	mockSvc := &mockProfileResolutionService{
		getFunc: func(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
			assert.Equal(t, profileID, id)
			return profile, nil
		},
	}

	var capturedProfileID uuid.UUID
	e.POST("/api/v1/tools/some-tool", func(c echo.Context) error {
		id, ok := GetResolvedProfileID(c.Request().Context())
		require.True(t, ok, "profile ID should be in context")
		capturedProfileID = id
		return c.String(http.StatusOK, "ok")
	}, ProfileResolutionMiddleware(mockSvc))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/some-tool", nil)
	req.Header.Set(ProfileIDHeader, profileID.String())
	req.Header.Set("Authorization", "Bearer testprefix12345678901234567890")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, profileID, capturedProfileID)
}

func TestProfileResolution_HeaderScopedCanonicalRoutes(t *testing.T) {
	routes := []string{
		"/api/v1/fragments",
		"/api/v1/claims",
		"/api/v1/facts",
		"/api/v1/communities",
		"/api/v1/recall",
	}

	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			e := echo.New()
			e.HTTPErrorHandler = httperr.ErrorHandler

			profileID := uuid.New()
			profile := &domain.Profile{
				ID:        profileID,
				Name:      "test-profile",
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}

			mockSvc := &mockProfileResolutionService{
				getFunc: func(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
					assert.Equal(t, profileID, id)
					return profile, nil
				},
			}

			var capturedProfileID uuid.UUID
			e.POST(route, func(c echo.Context) error {
				id, ok := GetResolvedProfileID(c.Request().Context())
				require.True(t, ok, "profile ID should be in context")
				capturedProfileID = id
				return c.String(http.StatusOK, "ok")
			}, ProfileResolutionMiddleware(mockSvc))

			req := httptest.NewRequest(http.MethodPost, route, nil)
			req.Header.Set(ProfileIDHeader, profileID.String())
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, profileID, capturedProfileID)
		})
	}
}

func TestHeaderScopedProfileRouteRequiresPathBoundary(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/api/v1/dreaming", want: true},
		{path: "/api/v1/dreaming/status", want: true},
		{path: "/api/v1/dreaming-extra", want: false},
		{path: "/api/v1/dreams", want: true},
		{path: "/api/v1/dreams/dream-1", want: true},
		{path: "/api/v1/dreams-extra", want: false},
		{path: "/api/v1/recallXYZ", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.want, isHeaderScopedProfileRoute(tt.path))
		})
	}
}

func TestProfileResolution_HeaderScoped_UsesPrincipalProfile(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	profileID := uuid.New()
	profile := &domain.Profile{
		ID:        profileID,
		Name:      "test-profile",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	mockSvc := &mockProfileResolutionService{
		getFunc: func(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
			assert.Equal(t, profileID, id)
			return profile, nil
		},
	}

	var capturedProfileID uuid.UUID
	e.POST("/api/v1/tools/some-tool", func(c echo.Context) error {
		id, ok := GetResolvedProfileID(c.Request().Context())
		require.True(t, ok, "profile ID should be in context")
		capturedProfileID = id
		return c.String(http.StatusOK, "ok")
	}, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			principal := &Principal{ProfileID: &profileID}
			ctx := context.WithValue(c.Request().Context(), principalContextKey{}, principal)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}, ProfileResolutionMiddleware(mockSvc))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/some-tool", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, profileID, capturedProfileID)
}

func TestProfileResolution_HeaderScoped_PrincipalOverridesLegacyHeader(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	principalProfileID := uuid.New()
	legacyHeaderProfileID := uuid.New()
	profile := &domain.Profile{
		ID:        principalProfileID,
		Name:      "test-profile",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	mockSvc := &mockProfileResolutionService{
		getFunc: func(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
			assert.Equal(t, principalProfileID, id)
			return profile, nil
		},
	}

	e.POST("/api/v1/tools/some-tool", func(c echo.Context) error {
		id, ok := GetResolvedProfileID(c.Request().Context())
		require.True(t, ok, "profile ID should be in context")
		assert.Equal(t, principalProfileID, id)
		return c.String(http.StatusOK, "ok")
	}, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			principal := &Principal{ProfileID: &principalProfileID}
			ctx := context.WithValue(c.Request().Context(), principalContextKey{}, principal)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}, ProfileResolutionMiddleware(mockSvc))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/some-tool", nil)
	req.Header.Set(ProfileIDHeader, legacyHeaderProfileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestProfileResolution_Header_Missing tests that a missing X-Profile-ID header
// on tool routes returns a 400 PROFILE_ID_REQUIRED error.
func TestProfileResolution_Header_Missing(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	mockSvc := &mockProfileResolutionService{}

	handlerCalled := false
	e.POST("/api/v1/tools/some-tool", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	}, ProfileResolutionMiddleware(mockSvc))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/some-tool", nil)
	req.Header.Set("Authorization", "Bearer testprefix12345678901234567890")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.False(t, handlerCalled, "handler should not be called")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "PROFILE_ID_REQUIRED")
}

// TestProfileResolution_DeletedProfile_Returns404 tests that a soft-deleted profile
// returns a 404 NOT_FOUND error.
func TestProfileResolution_DeletedProfile_Returns404(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	profileID := uuid.New()

	// Service returns NOT_FOUND for deleted profiles
	mockSvc := &mockProfileResolutionService{
		getFunc: func(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
			return nil, httperr.New(httperr.NOT_FOUND, "profile not found")
		},
	}

	handlerCalled := false
	e.GET("/api/v1/profiles/:profileId/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	}, ProfileResolutionMiddleware(mockSvc))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/"+profileID.String()+"/test", nil)
	req.Header.Set("Authorization", "Bearer testprefix12345678901234567890")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.False(t, handlerCalled, "handler should not be called")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "NOT_FOUND")
}

// TestProfileResolution_StoresInContext tests that the resolved profile ID is
// correctly stored in context and retrievable via GetResolvedProfileID.
func TestProfileResolution_StoresInContext(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	profileID := uuid.New()
	profile := &domain.Profile{
		ID:          profileID,
		Name:        "test-profile",
		Description: "Profile resolution test scope",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}

	mockSvc := &mockProfileResolutionService{
		getFunc: func(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
			return profile, nil
		},
	}

	var capturedID uuid.UUID
	var capturedTeam ResolvedTeamContext
	var found bool
	var teamFound bool

	e.GET("/api/v1/profiles/:profileId/test", func(c echo.Context) error {
		ctx := c.Request().Context()
		capturedID, found = GetResolvedProfileID(ctx)
		capturedTeam, teamFound = GetResolvedTeamContext(ctx)
		return c.String(http.StatusOK, "ok")
	}, ProfileResolutionMiddleware(mockSvc))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/"+profileID.String()+"/test", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, found, "profile ID should be found in context")
	assert.Equal(t, profileID, capturedID)
	assert.True(t, teamFound, "team context should be found in context")
	assert.Equal(t, ResolvedTeamContext{
		ID:          profileID,
		Name:        "test-profile",
		Description: "Profile resolution test scope",
	}, capturedTeam)
}

// TestProfileResolution_ProfileNotFound tests that a non-existent profile
// returns a 404 NOT_FOUND error.
func TestProfileResolution_ProfileNotFound(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	profileID := uuid.New()

	// Service returns nil profile (not found)
	mockSvc := &mockProfileResolutionService{
		getFunc: func(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
			return nil, nil
		},
	}

	handlerCalled := false
	e.GET("/api/v1/profiles/:profileId/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	}, ProfileResolutionMiddleware(mockSvc))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/"+profileID.String()+"/test", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.False(t, handlerCalled, "handler should not be called")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "NOT_FOUND")
}

// TestProfileResolution_NonProfileRoute_PassesThrough tests that routes outside
// /api/v1/profiles/ and /api/v1/tools pass through without modification.
func TestProfileResolution_NonProfileRoute_PassesThrough(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	mockSvc := &mockProfileResolutionService{}

	handlerCalled := false
	e.GET("/api/v1/health", func(c echo.Context) error {
		handlerCalled = true
		// Verify no profile ID in context
		_, found := GetResolvedProfileID(c.Request().Context())
		assert.False(t, found, "profile ID should not be in context for non-profile route")
		return c.String(http.StatusOK, "ok")
	}, ProfileResolutionMiddleware(mockSvc))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.True(t, handlerCalled, "handler should be called")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestProfileResolution_ServiceError tests that service errors return 500 INTERNAL_ERROR.
func TestProfileResolution_ServiceError(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	profileID := uuid.New()

	mockSvc := &mockProfileResolutionService{
		getFunc: func(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
			return nil, errors.New("database error")
		},
	}

	handlerCalled := false
	e.GET("/api/v1/profiles/:profileId/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	}, ProfileResolutionMiddleware(mockSvc))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profiles/"+profileID.String()+"/test", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.False(t, handlerCalled, "handler should not be called")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "INTERNAL_ERROR")
}

// TestProfileResolution_MustGetResolvedProfileID_Panics tests that MustGetResolvedProfileID
// panics when no profile ID is in context.
func TestProfileResolution_MustGetResolvedProfileID_Panics(t *testing.T) {
	ctx := context.Background()

	assert.Panics(t, func() {
		MustGetResolvedProfileID(ctx)
	}, "MustGetResolvedProfileID should panic when no profile ID is in context")
}

// TestProfileResolution_MustGetResolvedProfileID_ReturnsID tests that MustGetResolvedProfileID
// returns the profile ID when it is in context.
func TestProfileResolution_MustGetResolvedProfileID_ReturnsID(t *testing.T) {
	profileID := uuid.New()
	ctx := context.WithValue(context.Background(), ResolvedProfileKey{}, profileID)

	result := MustGetResolvedProfileID(ctx)
	assert.Equal(t, profileID, result)
}

func TestProfileResolution_TeamAliasHelpers(t *testing.T) {
	teamID := uuid.New()

	ctx := SetResolvedTeamIDForTest(context.Background(), teamID)

	gotProfileID, ok := GetResolvedProfileID(ctx)
	require.True(t, ok)
	assert.Equal(t, teamID, gotProfileID)

	gotTeamID, ok := GetResolvedTeamID(ctx)
	require.True(t, ok)
	assert.Equal(t, teamID, gotTeamID)
	assert.Equal(t, teamID, MustGetResolvedTeamID(ctx))
}

func TestProfileResolution_SetResolvedProfileIDForTest(t *testing.T) {
	profileID := uuid.New()

	ctx := SetResolvedProfileIDForTest(context.Background(), profileID)

	got, ok := GetResolvedProfileID(ctx)
	require.True(t, ok)
	assert.Equal(t, profileID, got)
}

func TestProfileResolution_SetResolvedTeamContextForTest(t *testing.T) {
	teamID := uuid.New()
	team := ResolvedTeamContext{
		ID:          teamID,
		Name:        "Dense-Mem Project",
		Description: "Project memory only",
	}

	ctx := SetResolvedTeamContextForTest(context.Background(), team)

	gotID, ok := GetResolvedProfileID(ctx)
	require.True(t, ok)
	assert.Equal(t, teamID, gotID)

	gotTeam, ok := GetResolvedTeamContext(ctx)
	require.True(t, ok)
	assert.Equal(t, team, gotTeam)
}
