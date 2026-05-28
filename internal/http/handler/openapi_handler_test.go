package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/openapi"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func buildTestGenerator(t *testing.T) openapi.Generator {
	t.Helper()
	reg, err := registry.BuildDefault(registry.Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
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
	if _, has := paths["/api/v1/teams/{teamId}/query/stream"]; !has {
		t.Errorf("full response missing runtime-only path")
	}
}

var _ OpenAPIHandlerInterface = (*OpenAPIHandler)(nil)
