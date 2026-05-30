package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
)

type mockSemanticSearchHandlerService struct {
	searchFunc func(ctx context.Context, profileID string, req *semanticsearch.SemanticSearchRequest) (*semanticsearch.SemanticSearchResult, error)
}

func (m *mockSemanticSearchHandlerService) Search(ctx context.Context, profileID string, req *semanticsearch.SemanticSearchRequest) (*semanticsearch.SemanticSearchResult, error) {
	if m.searchFunc != nil {
		return m.searchFunc(ctx, profileID, req)
	}
	return &semanticsearch.SemanticSearchResult{
		Data: []semanticsearch.SearchHit{},
		Meta: semanticsearch.SemanticSearchMeta{LimitApplied: 10},
	}, nil
}

func TestSemanticSearchHandler_BindsDTOAndThreshold(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()
	validation.SetEmbeddingDimensions(3)

	mockSvc := &mockSemanticSearchHandlerService{
		searchFunc: func(ctx context.Context, pid string, req *semanticsearch.SemanticSearchRequest) (*semanticsearch.SemanticSearchResult, error) {
			assert.Equal(t, profileID.String(), pid)
			assert.Equal(t, "semantic query", req.Query)
			assert.Equal(t, 5, req.Limit)
			assert.InDelta(t, 0.75, req.Threshold, 0.0001)
			assert.Equal(t, []float32{0.1, 0.2, 0.3}, req.Embedding)
			return &semanticsearch.SemanticSearchResult{
				Data: []semanticsearch.SearchHit{
					{ID: "frag-1", Type: "fragment", Content: "hello", Score: 0.8, ProfileID: pid},
				},
				Meta: semanticsearch.SemanticSearchMeta{LimitApplied: 5},
			}, nil
		},
	}
	h := NewSemanticSearchHandler(mockSvc)

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := middleware.SetResolvedProfileIDForTest(c.Request().Context(), profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/tools/semantic-search", h.Handle)

	body := `{"query":"semantic query","embedding":[0.1,0.2,0.3],"limit":5,"threshold":0.75}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/semantic-search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Profile-ID", profileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp semanticsearch.SemanticSearchResult
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, 5, resp.Meta.LimitApplied)
}

func TestSemanticSearchHandler_RejectsInvalidThreshold(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()
	validation.SetEmbeddingDimensions(3)

	h := NewSemanticSearchHandler(&mockSemanticSearchHandlerService{})

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := middleware.SetResolvedProfileIDForTest(c.Request().Context(), profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/tools/semantic-search", h.Handle)

	body := `{"embedding":[0.1,0.2,0.3],"limit":5,"threshold":1.25}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/semantic-search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Profile-ID", profileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestSemanticSearchHandlerRequiresResolvedProfileAndValidJSON(t *testing.T) {
	t.Run("missing profile id", func(t *testing.T) {
		e := newTestEcho()
		e.HTTPErrorHandler = httperr.ErrorHandler
		h := NewSemanticSearchHandler(&mockSemanticSearchHandlerService{})
		e.POST("/api/v1/tools/semantic-search", h.Handle)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/semantic-search", strings.NewReader(`{"embedding":[0.1,0.2,0.3],"limit":5}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("malformed json", func(t *testing.T) {
		e := newTestEcho()
		e.HTTPErrorHandler = httperr.ErrorHandler
		profileID := uuid.New()
		h := NewSemanticSearchHandler(&mockSemanticSearchHandlerService{})
		e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				ctx := middleware.SetResolvedProfileIDForTest(c.Request().Context(), profileID)
				c.SetRequest(c.Request().WithContext(ctx))
				return next(c)
			}
		})
		e.POST("/api/v1/tools/semantic-search", h.Handle)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/semantic-search", strings.NewReader(`{malformed`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	})
}

func TestHandleSemanticSearchErrorMapsKnownErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code httperr.ErrorCode
	}{
		{"nil", nil, ""},
		{"embedding generation", semanticsearch.NewEmbeddingGenerationNotConfiguredError("missing embedding"), httperr.EMBEDDING_GENERATION_NOT_CONFIGURED},
		{"dimension mismatch", semanticsearch.NewDimensionMismatchError(3, 2), httperr.VALIDATION_ERROR},
		{"validation", semanticsearch.NewValidationError("bad request"), httperr.VALIDATION_ERROR},
		{"default", errors.New("vector index unavailable"), httperr.INTERNAL_ERROR},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := handleSemanticSearchError(tc.err)
			if tc.err == nil {
				require.Nil(t, got)
				return
			}
			require.Equal(t, tc.code, got.Code)
		})
	}
}
