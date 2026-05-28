package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestToolExecuteHandler_RejectsMissingRequiredField(t *testing.T) {
	reg := registry.New()
	err := reg.Register(registry.Tool{
		Name:           "save_memory",
		InputSchema:    map[string]any{"type": "object", "required": []string{"content"}, "properties": map[string]any{"content": map[string]any{"type": "string"}}, "additionalProperties": false},
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	})
	require.NoError(t, err)

	h := NewToolExecuteHandler(reg)
	e := newTestEcho()
	profileID := uuid.New()

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := middleware.SetResolvedProfileIDForTest(c.Request().Context(), profileID)
			ctx = middleware.SetPrincipalForTest(ctx, &middleware.Principal{
				KeyID:  uuid.New(),
				Role:   "user",
				Scopes: []string{"write"},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/tools/:name", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/save_memory", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Profile-ID", profileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	var apiErr httperr.APIError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.Equal(t, httperr.VALIDATION_ERROR, apiErr.Code)
	assert.Contains(t, apiErr.Message, "content is required")
}

func TestToolExecuteHandler_RejectsUnknownFieldWhenAdditionalPropertiesFalse(t *testing.T) {
	reg := registry.New()
	called := false
	err := reg.Register(registry.Tool{
		Name:           "get_memory",
		InputSchema:    map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "additionalProperties": false},
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			called = true
			return map[string]any{"ok": true}, nil
		},
	})
	require.NoError(t, err)

	h := NewToolExecuteHandler(reg)
	e := newTestEcho()
	profileID := uuid.New()

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := middleware.SetResolvedProfileIDForTest(c.Request().Context(), profileID)
			ctx = middleware.SetPrincipalForTest(ctx, &middleware.Principal{
				KeyID:  uuid.New(),
				Role:   "user",
				Scopes: []string{"read"},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/tools/:name", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/get_memory", strings.NewReader(`{"id":"frag-1","team_id":"forged"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Profile-ID", profileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.False(t, called, "tool invoker must not run when the input schema rejects the request")

	var apiErr httperr.APIError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.Contains(t, apiErr.Message, "unknown field: team_id")
}

func TestToolExecuteHandler_MapsEmbeddingFailureToServiceUnavailable(t *testing.T) {
	reg := registry.New()
	err := reg.Register(registry.Tool{
		Name:           "save_memory",
		InputSchema:    map[string]any{"type": "object", "required": []string{"content"}, "properties": map[string]any{"content": map[string]any{"type": "string"}}, "additionalProperties": false},
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			return nil, fragmentservice.ErrEmbeddingFailed
		},
	})
	require.NoError(t, err)

	h := NewToolExecuteHandler(reg)
	e := newTestEcho()
	profileID := uuid.New()

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := middleware.SetResolvedProfileIDForTest(c.Request().Context(), profileID)
			ctx = middleware.SetPrincipalForTest(ctx, &middleware.Principal{
				KeyID:  uuid.New(),
				Role:   "user",
				Scopes: []string{"write"},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/tools/:name", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/save_memory", strings.NewReader(`{"content":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Profile-ID", profileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var apiErr httperr.APIError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.Equal(t, httperr.SERVICE_UNAVAILABLE, apiErr.Code)
	assert.Contains(t, apiErr.Message, "embedding service unavailable")
}
