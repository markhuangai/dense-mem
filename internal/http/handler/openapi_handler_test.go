package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/openapi"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

type countingOpenAPIGenerator struct {
	calls int
	spec  map[string]any
	err   error
}

func (g *countingOpenAPIGenerator) Generate(_ openapi.SpecVariant) (map[string]any, error) {
	g.calls++
	if g.err != nil {
		return nil, g.err
	}
	return g.spec, nil
}

func buildTestGenerator(t *testing.T) openapi.Generator {
	t.Helper()
	reg, err := registry.BuildActive(registry.Dependencies{})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	return openapi.New(reg, openapi.DefaultRoutes())
}

func TestOpenAPIHandler_ServesAISafeVariant(t *testing.T) {
	h := NewOpenAPIHandler(buildTestGenerator(t), openapi.SpecVariantAISafe)

	e := echo.New()
	e.GET("/api/v1/openapi.json", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["openapi"] != "3.0.3" {
		t.Errorf("openapi = %v; want 3.0.3", body["openapi"])
	}
	paths := body["paths"].(map[string]any)
	if _, has := paths["/api/v1/teams/{teamId}/query/stream"]; has {
		t.Errorf("ai-safe response contained runtime-only path")
	}
	if _, has := paths["/mcp"]; has {
		t.Errorf("ai-safe response contained runtime-only MCP path")
	}
}

func TestOpenAPIHandler_ServesFullVariant(t *testing.T) {
	h := NewOpenAPIHandler(buildTestGenerator(t), openapi.SpecVariantFull)

	e := echo.New()
	e.GET("/api/v1/openapi.json", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	paths := body["paths"].(map[string]any)
	if _, has := paths["/mcp"]; !has {
		t.Errorf("full response missing runtime-only path")
	}
	if _, has := paths["/api/v1/teams/{teamId}/query/stream"]; has {
		t.Errorf("full response contained removed query stream path")
	}
}

func TestOpenAPIHandler_CachesGeneratedSpec(t *testing.T) {
	gen := &countingOpenAPIGenerator{
		spec: map[string]any{"openapi": "3.0.3", "paths": map[string]any{}},
	}
	h := NewOpenAPIHandler(gen, openapi.SpecVariantFull)

	e := echo.New()
	e.GET("/api/v1/openapi.json", h.Handle)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d; want 200. body=%s", i+1, rec.Code, rec.Body.String())
		}
	}

	if gen.calls != 1 {
		t.Fatalf("Generate calls = %d; want 1", gen.calls)
	}
}

func TestOpenAPIHandler_CachesGenerationError(t *testing.T) {
	gen := &countingOpenAPIGenerator{err: errors.New("boom")}
	h := NewOpenAPIHandler(gen, openapi.SpecVariantFull)

	e := echo.New()
	e.GET("/api/v1/openapi.json", h.Handle)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("request %d status = %d; want 500. body=%s", i+1, rec.Code, rec.Body.String())
		}
	}

	if gen.calls != 1 {
		t.Fatalf("Generate calls = %d; want 1", gen.calls)
	}
}

var _ OpenAPIHandlerInterface = (*OpenAPIHandler)(nil)
