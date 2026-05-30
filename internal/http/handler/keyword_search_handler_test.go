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

	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/tools/keywordsearch"
)

// mockKeywordSearchService implements KeywordSearchServiceInterface for testing.
type mockKeywordSearchService struct {
	searchFunc func(ctx context.Context, profileID string, req *keywordsearch.KeywordSearchRequest) (*keywordsearch.KeywordSearchResult, error)
}

func (m *mockKeywordSearchService) Search(ctx context.Context, profileID string, req *keywordsearch.KeywordSearchRequest) (*keywordsearch.KeywordSearchResult, error) {
	if m.searchFunc != nil {
		return m.searchFunc(ctx, profileID, req)
	}
	return &keywordsearch.KeywordSearchResult{
		Data: []keywordsearch.SearchHit{},
		Meta: keywordsearch.KeywordSearchMeta{LimitApplied: 20},
	}, nil
}

// TestKeywordSearchHandler_Handle_Success tests successful keyword search.
func TestKeywordSearchHandler_Handle_Success(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	mockSvc := &mockKeywordSearchService{
		searchFunc: func(ctx context.Context, pid string, req *keywordsearch.KeywordSearchRequest) (*keywordsearch.KeywordSearchResult, error) {
			assert.Equal(t, profileID.String(), pid)
			assert.Equal(t, "test query", req.Query)
			assert.Equal(t, 20, req.Limit)
			return &keywordsearch.KeywordSearchResult{
				Data: []keywordsearch.SearchHit{
					{ID: "hit-1", Type: "fragment", Content: "test content", Score: 0.9, ProfileID: pid},
				},
				Meta: keywordsearch.KeywordSearchMeta{LimitApplied: 20},
			}, nil
		},
	}
	h := NewKeywordSearchHandler(mockSvc)

	// Set resolved profile ID in context using the proper key
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetResolvedProfileIDForTest(ctx, profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/tools/keyword-search", h.Handle)

	body := `{"keywords": "test query", "limit": 20}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/keyword-search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Profile-ID", profileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp keywordsearch.KeywordSearchResult
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Len(t, resp.Data, 1)
	assert.Equal(t, 20, resp.Meta.LimitApplied)
}

// TestKeywordSearchHandler_Handle_LimitZero_422 tests that limit=0 returns 422.
func TestKeywordSearchHandler_Handle_LimitZero_422(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	mockSvc := &mockKeywordSearchService{
		searchFunc: func(ctx context.Context, pid string, req *keywordsearch.KeywordSearchRequest) (*keywordsearch.KeywordSearchResult, error) {
			return nil, keywordsearch.NewValidationError("limit must be greater than 0")
		},
	}
	h := NewKeywordSearchHandler(mockSvc)

	// Set resolved profile ID in context using the proper key
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetResolvedProfileIDForTest(ctx, profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/tools/keyword-search", h.Handle)

	body := `{"keywords": "test query", "limit": 0}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/keyword-search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Profile-ID", profileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	var resp httperr.APIError
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, httperr.VALIDATION_ERROR, resp.Code)
}

// TestKeywordSearchHandler_Handle_EmptyResult_200 tests that empty result returns 200 with {"data":[]}.
func TestKeywordSearchHandler_Handle_EmptyResult_200(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	mockSvc := &mockKeywordSearchService{
		searchFunc: func(ctx context.Context, pid string, req *keywordsearch.KeywordSearchRequest) (*keywordsearch.KeywordSearchResult, error) {
			return &keywordsearch.KeywordSearchResult{
				Data: []keywordsearch.SearchHit{},
				Meta: keywordsearch.KeywordSearchMeta{LimitApplied: 20},
			}, nil
		},
	}
	h := NewKeywordSearchHandler(mockSvc)

	// Set resolved profile ID in context using the proper key
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetResolvedProfileIDForTest(ctx, profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/tools/keyword-search", h.Handle)

	body := `{"keywords": "nonexistent", "limit": 20}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/keyword-search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Profile-ID", profileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp keywordsearch.KeywordSearchResult
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Empty(t, resp.Data)
	assert.Equal(t, []keywordsearch.SearchHit{}, resp.Data)
}

// TestKeywordSearchHandler_Handle_MissingProfileID tests that missing profile ID returns error.
func TestKeywordSearchHandler_Handle_MissingProfileID(t *testing.T) {
	e := newTestEcho()
	h := NewKeywordSearchHandler(&mockKeywordSearchService{})

	e.POST("/api/v1/tools/keyword-search", h.Handle)

	body := `{"keywords": "test", "limit": 20}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/keyword-search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No X-Profile-ID header
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// The profile resolution middleware would catch this before the handler
	// But the handler also checks for it
	assert.NotEqual(t, http.StatusOK, rec.Code)
}

// TestKeywordSearchHandler_Handle_LimitCapped tests that limit over 100 is rejected by DTO validation.
func TestKeywordSearchHandler_Handle_LimitCapped(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	mockSvc := &mockKeywordSearchService{}
	h := NewKeywordSearchHandler(mockSvc)

	// Set resolved profile ID in context using the proper key
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetResolvedProfileIDForTest(ctx, profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/tools/keyword-search", h.Handle)

	// DTO validation enforces max=100, so limit=150 should be rejected with 422
	body := `{"keywords": "test query", "limit": 150}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/keyword-search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Profile-ID", profileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// Should return 422 because limit exceeds max=100
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	var resp httperr.APIError
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, httperr.VALIDATION_ERROR, resp.Code)
}

// TestKeywordSearchHandler_BindsDTO_Keywords tests that handler binds dto.KeywordSearchRequest with "keywords" field.
func TestKeywordSearchHandler_BindsDTO_Keywords(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	mockSvc := &mockKeywordSearchService{
		searchFunc: func(ctx context.Context, pid string, req *keywordsearch.KeywordSearchRequest) (*keywordsearch.KeywordSearchResult, error) {
			assert.Equal(t, profileID.String(), pid)
			assert.Equal(t, "alpha", req.Query)
			assert.Equal(t, 5, req.Limit)
			return &keywordsearch.KeywordSearchResult{
				Data: []keywordsearch.SearchHit{},
				Meta: keywordsearch.KeywordSearchMeta{LimitApplied: 5},
			}, nil
		},
	}
	h := NewKeywordSearchHandler(mockSvc)

	// Set resolved profile ID in context using the proper key
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetResolvedProfileIDForTest(ctx, profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/tools/keyword-search", h.Handle)

	body := `{"keywords": "alpha", "limit": 5}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/keyword-search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Profile-ID", profileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestKeywordSearchHandler_PassesLabelsAndTemporalFilters(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()
	validAt := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	knownAt := time.Date(2024, 3, 2, 12, 0, 0, 0, time.UTC)

	mockSvc := &mockKeywordSearchService{
		searchFunc: func(ctx context.Context, pid string, req *keywordsearch.KeywordSearchRequest) (*keywordsearch.KeywordSearchResult, error) {
			assert.Equal(t, profileID.String(), pid)
			assert.Equal(t, "alpha", req.Query)
			assert.Equal(t, []string{"science", "history"}, req.Labels)
			require.NotNil(t, req.ValidAt)
			require.NotNil(t, req.KnownAt)
			assert.True(t, req.ValidAt.Equal(validAt))
			assert.True(t, req.KnownAt.Equal(knownAt))
			return &keywordsearch.KeywordSearchResult{
				Data: []keywordsearch.SearchHit{},
				Meta: keywordsearch.KeywordSearchMeta{LimitApplied: 5},
			}, nil
		},
	}
	h := NewKeywordSearchHandler(mockSvc)

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := middleware.SetResolvedProfileIDForTest(c.Request().Context(), profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/tools/keyword-search", h.Handle)

	body := `{"keywords":"alpha","limit":5,"labels":["science","history"],"valid_at":"2024-03-01T12:00:00Z","known_at":"2024-03-02T12:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/keyword-search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Profile-ID", profileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

// TestKeywordSearchHandler_RejectsLegacyQueryField tests that handler rejects "query" field (should use "keywords").
func TestKeywordSearchHandler_RejectsLegacyQueryField(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	mockSvc := &mockKeywordSearchService{}
	h := NewKeywordSearchHandler(mockSvc)

	// Set resolved profile ID in context using the proper key
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetResolvedProfileIDForTest(ctx, profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/tools/keyword-search", h.Handle)

	body := `{"query": "alpha"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/keyword-search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Profile-ID", profileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// Should reject because "keywords" is required, not "query"
	assert.Contains(t, []int{http.StatusBadRequest, http.StatusUnprocessableEntity}, rec.Code)
}

func TestKeywordSearchHandler_MalformedJSONAndErrorMapping(t *testing.T) {
	e := newTestEcho()
	e.HTTPErrorHandler = httperr.ErrorHandler
	profileID := uuid.New()
	h := NewKeywordSearchHandler(&mockKeywordSearchService{})
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := middleware.SetResolvedProfileIDForTest(c.Request().Context(), profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	e.POST("/api/v1/tools/keyword-search", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/keyword-search", strings.NewReader(`{malformed`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	require.Nil(t, handleKeywordSearchError(nil))
	require.Equal(t, httperr.VALIDATION_ERROR, handleKeywordSearchError(keywordsearch.NewValidationError("bad request")).Code)
	require.Equal(t, httperr.INTERNAL_ERROR, handleKeywordSearchError(errors.New("index down")).Code)
}
