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

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type handlerRecallFeedbackConfigStub struct {
	enabled bool
}

func (s handlerRecallFeedbackConfigStub) RecallFeedbackRuntimeConfig(context.Context) (domain.RecallFeedbackRuntimeConfig, error) {
	return domain.RecallFeedbackRuntimeConfig{Enabled: s.enabled}, nil
}

func (s handlerRecallFeedbackConfigStub) EvaluationRuntimeConfig(context.Context) (domain.EvaluationRuntimeConfig, error) {
	return domain.EvaluationRuntimeConfig{Enabled: s.enabled}, nil
}

func TestToolExecuteHandler_RejectsMissingRequiredField(t *testing.T) {
	reg := registry.New()
	err := reg.Register(registry.Tool{
		Name:           "remember",
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

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/remember", strings.NewReader(`{}`))
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

func TestToolExecuteHandler_RejectsUnknownNonTenantFieldWhenAdditionalPropertiesFalse(t *testing.T) {
	reg := registry.New()
	called := false
	err := reg.Register(registry.Tool{
		Name:           "example_tool",
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

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/example_tool", strings.NewReader(`{"id":"frag-1","unexpected":"forged"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Profile-ID", profileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.False(t, called, "tool invoker must not run when the input schema rejects the request")

	var apiErr httperr.APIError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.Contains(t, apiErr.Message, "unknown field: unexpected")
}

func TestToolExecuteHandler_StripsTenantFieldsBeforeStrictValidation(t *testing.T) {
	reg := registry.New()
	var gotInput map[string]any
	err := reg.Register(registry.Tool{
		Name:           "example_tool",
		InputSchema:    map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "additionalProperties": false},
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			gotInput = input
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

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/example_tool", strings.NewReader(`{"id":"frag-1","team_id":"forged","profile_id":"forged-profile"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Profile-ID", profileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, map[string]any{"id": "frag-1"}, gotInput)
}

func TestToolExecuteHandler_NormalizesInputBeforeStrictValidation(t *testing.T) {
	reg := registry.New()
	var gotInput map[string]any
	err := reg.Register(registry.Tool{
		Name: "remember",
		InputSchema: map[string]any{
			"type":                 "object",
			"required":             []string{"evidence"},
			"properties":           map[string]any{"evidence": map[string]any{"type": "array"}},
			"additionalProperties": false,
		},
		RequiredScopes: []string{"write"},
		NormalizeInput: func(input map[string]any) map[string]any {
			return map[string]any{
				"evidence": []any{map[string]any{"content": input["content"]}},
			}
		},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			gotInput = input
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

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/remember", strings.NewReader(`{"content":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Profile-ID", profileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []any{map[string]any{"content": "hello"}}, gotInput["evidence"])
}

func TestToolExecuteHandler_RejectsTenantFieldsForEvaluationTools(t *testing.T) {
	reg := registry.New()
	called := false
	err := reg.Register(registry.Tool{
		Name:           "eval_get_manifest",
		InputSchema:    map[string]any{"type": "object", "additionalProperties": true},
		RequiredScopes: []string{"read", "write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			called = true
			return map[string]any{"ok": true}, nil
		},
	})
	require.NoError(t, err)

	h := NewToolExecuteHandlerWithRuntimeConfig(reg, handlerRecallFeedbackConfigStub{enabled: true})
	e := newTestEcho()
	profileID := uuid.New()

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := middleware.SetResolvedProfileIDForTest(c.Request().Context(), profileID)
			ctx = middleware.SetPrincipalForTest(ctx, &middleware.Principal{
				KeyID:  uuid.New(),
				Role:   "user",
				Scopes: []string{"read", "write"},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/tools/:name", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/eval_get_manifest", strings.NewReader(`{"team_id":"forged","x":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Profile-ID", profileID.String())
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.False(t, called, "evaluation tool must not run when tenant selectors are present")

	var apiErr httperr.APIError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.Equal(t, httperr.VALIDATION_ERROR, apiErr.Code)
	assert.Contains(t, apiErr.Message, "evaluation tools do not accept team_id or profile_id")
}

func TestToolExecuteHandler_ProtectsRecallFeedbackEventToolsWithFeedbackScope(t *testing.T) {
	reg := registry.New()
	called := false
	err := reg.Register(registry.Tool{
		Name:           "eval_list_recall_feedback_events",
		InputSchema:    map[string]any{"type": "object", "additionalProperties": false},
		RequiredScopes: []string{"read", "feedback:read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			called = true
			return map[string]any{"events": []any{}}, nil
		},
	})
	require.NoError(t, err)

	h := NewToolExecuteHandlerWithRuntimeConfig(reg, handlerRecallFeedbackConfigStub{enabled: true})
	profileID := uuid.New()

	for _, tc := range []struct {
		name       string
		scopes     []string
		wantStatus int
		wantCalled bool
	}{
		{name: "read write without feedback scope", scopes: []string{"read", "write"}, wantStatus: http.StatusForbidden},
		{name: "read with feedback scope", scopes: []string{"read", "feedback:read"}, wantStatus: http.StatusOK, wantCalled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			e := newTestEcho()
			e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					ctx := middleware.SetResolvedProfileIDForTest(c.Request().Context(), profileID)
					ctx = middleware.SetPrincipalForTest(ctx, &middleware.Principal{
						KeyID:  uuid.New(),
						Role:   "user",
						Scopes: tc.scopes,
					})
					c.SetRequest(c.Request().WithContext(ctx))
					return next(c)
				}
			})
			e.POST("/api/v1/tools/:name", h.Handle)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/eval_list_recall_feedback_events", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Profile-ID", profileID.String())
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			require.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, tc.wantCalled, called)
		})
	}
}

func TestToolExecuteHandler_MapsEmbeddingFailureToServiceUnavailable(t *testing.T) {
	reg := registry.New()
	err := reg.Register(registry.Tool{
		Name:           "remember",
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

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/remember", strings.NewReader(`{"content":"hello"}`))
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

func TestToolExecuteHandler_AdditionalBranches(t *testing.T) {
	t.Run("missing profile", func(t *testing.T) {
		reg := registry.New()
		h := NewToolExecuteHandler(reg)
		e := newTestEcho()
		e.POST("/api/v1/tools/:name", h.Handle)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/probe", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("missing principal", func(t *testing.T) {
		reg := registry.New()
		h := NewToolExecuteHandler(reg)
		e := newTestEcho()
		profileID := uuid.New()
		e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				ctx := middleware.SetResolvedProfileIDForTest(c.Request().Context(), profileID)
				c.SetRequest(c.Request().WithContext(ctx))
				return next(c)
			}
		})
		e.POST("/api/v1/tools/:name", h.Handle)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/probe", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("missing name", func(t *testing.T) {
		h := NewToolExecuteHandler(registry.New())
		e := newTestEcho()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/", strings.NewReader(`{}`))
		req = req.WithContext(middleware.SetResolvedProfileIDForTest(req.Context(), uuid.New()))
		req = req.WithContext(middleware.SetPrincipalForTest(req.Context(), &middleware.Principal{Scopes: []string{"read"}}))
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("name")
		c.SetParamValues("")

		err := h.Handle(c)

		require.Error(t, err)
		apiErr, ok := err.(*httperr.APIError)
		require.True(t, ok)
		require.Equal(t, httperr.VALIDATION_ERROR, apiErr.Code)
	})

	t.Run("not found and not executable", func(t *testing.T) {
		reg := registry.New()
		require.NoError(t, reg.Register(registry.Tool{
			Name:           "descriptor_only",
			InputSchema:    map[string]any{"type": "object"},
			RequiredScopes: []string{"read"},
		}))
		h := NewToolExecuteHandler(reg)
		e := newTestEcho()
		profileID := uuid.New()
		e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				ctx := middleware.SetResolvedProfileIDForTest(c.Request().Context(), profileID)
				ctx = middleware.SetPrincipalForTest(ctx, &middleware.Principal{Scopes: []string{"read"}})
				c.SetRequest(c.Request().WithContext(ctx))
				return next(c)
			}
		})
		e.POST("/api/v1/tools/:name", h.Handle)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/missing", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNotFound, rec.Code)

		req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/descriptor_only", strings.NewReader(`{}`))
		rec = httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("runtime disabled tool is not found", func(t *testing.T) {
		reg := registry.New()
		require.NoError(t, reg.Register(registry.Tool{
			Name:           registry.SubmitRecallSessionFeedbackToolName,
			InputSchema:    map[string]any{"type": "object"},
			RequiredScopes: []string{"read"},
			Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
				return nil, registry.ErrToolDisabled
			},
		}))
		h := NewToolExecuteHandlerWithRuntimeConfig(reg, handlerRecallFeedbackConfigStub{enabled: false})
		e := newTestEcho()
		profileID := uuid.New()
		e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				ctx := middleware.SetResolvedProfileIDForTest(c.Request().Context(), profileID)
				ctx = middleware.SetPrincipalForTest(ctx, &middleware.Principal{Scopes: []string{"read"}})
				c.SetRequest(c.Request().WithContext(ctx))
				return next(c)
			}
		})
		e.POST("/api/v1/tools/:name", h.Handle)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/submit_recall_session_feedback", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("success strips forged tenant fields", func(t *testing.T) {
		reg := registry.New()
		var gotInput map[string]any
		require.NoError(t, reg.Register(registry.Tool{
			Name:           "probe",
			InputSchema:    map[string]any{"type": "object", "additionalProperties": true},
			RequiredScopes: []string{"write"},
			Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
				gotInput = input
				return map[string]any{"profile": profileID}, nil
			},
		}))
		h := NewToolExecuteHandler(reg)
		e := newTestEcho()
		profileID := uuid.New()
		e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				ctx := middleware.SetResolvedProfileIDForTest(c.Request().Context(), profileID)
				ctx = middleware.SetPrincipalForTest(ctx, &middleware.Principal{Scopes: []string{"write"}})
				c.SetRequest(c.Request().WithContext(ctx))
				return next(c)
			}
		})
		e.POST("/api/v1/tools/:name", h.Handle)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/probe", strings.NewReader(`{"team_id":"attacker","profile_id":"attacker-profile","x":1}`))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.NotContains(t, gotInput, "team_id")
		require.NotContains(t, gotInput, "profile_id")
	})
}

func TestToolReadHandler_Handle(t *testing.T) {
	reg := registry.New()
	require.NoError(t, reg.Register(registry.Tool{
		Name:           "readable",
		Description:    "visible with read scope",
		InputSchema:    map[string]any{"type": "object"},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read"},
	}))
	h := NewToolReadHandler(reg)

	t.Run("missing id", func(t *testing.T) {
		e := newTestEcho()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tools/", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues("")

		err := h.Handle(c)

		require.Error(t, err)
		apiErr, ok := err.(*httperr.APIError)
		require.True(t, ok)
		require.Equal(t, httperr.VALIDATION_ERROR, apiErr.Code)
	})

	t.Run("no principal can read descriptor", func(t *testing.T) {
		e := newTestEcho()
		e.GET("/api/v1/tools/:id", h.Handle)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tools/readable", nil)
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), `"name":"readable"`)
	})

	t.Run("principal without scope gets not found", func(t *testing.T) {
		e := newTestEcho()
		e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				ctx := middleware.SetPrincipalForTest(c.Request().Context(), &middleware.Principal{Scopes: []string{"write"}})
				c.SetRequest(c.Request().WithContext(ctx))
				return next(c)
			}
		})
		e.GET("/api/v1/tools/:id", h.Handle)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tools/readable", nil)
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("unknown id", func(t *testing.T) {
		e := newTestEcho()
		e.GET("/api/v1/tools/:id", h.Handle)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tools/missing", nil)
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("runtime disabled tool descriptor is not found", func(t *testing.T) {
		runtimeReg := registry.New()
		require.NoError(t, runtimeReg.Register(registry.Tool{
			Name:           registry.SubmitRecallSessionFeedbackToolName,
			Description:    "feedback",
			InputSchema:    map[string]any{"type": "object"},
			RequiredScopes: []string{"read"},
		}))
		runtimeHandler := NewToolReadHandlerWithRuntimeConfig(runtimeReg, handlerRecallFeedbackConfigStub{enabled: false})
		e := newTestEcho()
		e.GET("/api/v1/tools/:id", runtimeHandler.Handle)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tools/submit_recall_session_feedback", nil)
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestMapToolExecuteError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code httperr.ErrorCode
	}{
		{"tool unavailable", registry.ErrToolUnavailable, httperr.SERVICE_UNAVAILABLE},
		{"tool disabled", registry.ErrToolDisabled, httperr.NOT_FOUND},
		{"supporting fragment missing", claimservice.ErrSupportingFragmentMissing, httperr.ErrSupportingFragmentMissing},
		{"claim not found", claimservice.ErrClaimNotFound, httperr.ErrClaimNotFound},
		{"fact not found", factservice.ErrFactNotFound, httperr.ErrFactNotFound},
		{"fragment not found", fragmentservice.ErrFragmentNotFound, httperr.NOT_FOUND},
		{"embedding timeout", embedding.ErrEmbeddingTimeout, httperr.SERVICE_UNAVAILABLE},
		{"embedding provider", embedding.ErrEmbeddingProvider, httperr.SERVICE_UNAVAILABLE},
		{"embedding rate limit", embedding.ErrEmbeddingRateLimit, httperr.SERVICE_UNAVAILABLE},
		{"fragment embedding failed", fragmentservice.ErrEmbeddingFailed, httperr.SERVICE_UNAVAILABLE},
		{"community unavailable", communityservice.ErrCommunityUnavailable, httperr.SERVICE_UNAVAILABLE},
		{"community graph too large", communityservice.ErrCommunityGraphTooLarge, httperr.ErrCommunityGraphTooLarge},
		{"community not found", communityservice.ErrCommunityNotFound, httperr.NOT_FOUND},
		{"predicate not policed", factservice.ErrPredicateNotPoliced, httperr.ErrPredicateNotPoliced},
		{"unsupported policy", factservice.ErrUnsupportedPolicy, httperr.ErrUnsupportedPolicy},
		{"claim not validated", factservice.ErrClaimNotValidated, httperr.ErrNeedsClaimValidated},
		{"gate rejected", factservice.ErrGateRejected, httperr.ErrGateRejected},
		{"promotion deferred disputed", factservice.ErrPromotionDeferredDisputed, httperr.ErrComparableDisputed},
		{"promotion rejected", factservice.ErrPromotionRejected, httperr.ErrRejectedWeaker},
		{"verifier rate limit", verifier.ErrVerifierRateLimit, httperr.ErrVerifierRateLimit},
		{"verifier timeout", verifier.ErrVerifierTimeout, httperr.ErrVerifierTimeout},
		{"verifier provider", verifier.ErrVerifierProvider, httperr.ErrVerifierProvider},
		{"verifier malformed", verifier.ErrVerifierMalformedResponse, httperr.ErrVerifierMalformedResponse},
		{"recall embedding", recallservice.ErrEmbeddingUnavailable, httperr.SERVICE_UNAVAILABLE},
		{"recall keyword", recallservice.ErrKeywordUnavailable, httperr.SERVICE_UNAVAILABLE},
		{"validation string", errors.New("field is required"), httperr.VALIDATION_ERROR},
		{"default", errors.New("database exploded"), httperr.INTERNAL_ERROR},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apiErr := mapToolExecuteError(tc.err)
			require.Equal(t, tc.code, apiErr.Code)
		})
	}
}
