package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// TestToolCatalogHandler_ReturnsRegisteredTools — backpressure test + AC-32.
func TestToolCatalogHandler_ReturnsRegisteredTools(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:           "save_memory",
		Description:    "store a fragment",
		InputSchema:    map[string]any{"type": "object"},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"write"},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	h := NewToolCatalogHandler(reg)

	e := echo.New()
	e.GET("/api/v1/tools", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tools", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
	var resp dto.ToolCatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Tools) != 1 {
		t.Fatalf("Tools length = %d; want 1", len(resp.Tools))
	}
	if resp.Tools[0].Name != "save_memory" {
		t.Errorf("Tools[0].Name = %q; want save_memory", resp.Tools[0].Name)
	}
	if len(resp.Tools[0].RequiredScopes) == 0 {
		t.Error("Tools[0].RequiredScopes empty")
	}
}

func TestToolCatalogHandler_NoInternalTypeLeaks(t *testing.T) {
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "x", Description: "y"})
	h := NewToolCatalogHandler(reg)

	e := echo.New()
	e.GET("/api/v1/tools", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tools", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	body := rec.Body.String()
	forbidden := []string{
		"github.com/dense-mem",
		"fragmentservice.",
		"registry.Tool",
		"keywordsearch.",
		"semanticsearch.",
		"graphquery.",
		"ToolInvoker",
	}
	for _, s := range forbidden {
		if strings.Contains(body, s) {
			t.Errorf("response body leaked internal reference %q. body=%s", s, body)
		}
	}
}

func TestToolCatalogHandler_ReturnsEmptyListForEmptyRegistry(t *testing.T) {
	h := NewToolCatalogHandler(registry.New())

	e := echo.New()
	e.GET("/api/v1/tools", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tools", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	var resp dto.ToolCatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Tools) != 0 {
		t.Errorf("Tools length = %d; want 0 for empty registry", len(resp.Tools))
	}
}

func TestToolCatalogHandler_FullV1Surface(t *testing.T) {
	reg, err := registry.BuildDefault(registry.Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	h := NewToolCatalogHandler(reg)

	e := echo.New()
	e.GET("/api/v1/tools", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tools", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	var resp dto.ToolCatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	expected := []string{
		"save_memory", "get_memory", "list_recent_memories", "recall_memory",
		"keyword_search", "semantic_search", "graph_query",
	}
	seen := make(map[string]bool, len(resp.Tools))
	for _, te := range resp.Tools {
		seen[te.Name] = true
	}
	for _, name := range expected {
		if !seen[name] {
			t.Errorf("v1 tool %q missing from catalog", name)
		}
	}
	for _, name := range []string{"keyword-search", "semantic-search", "graph-query"} {
		if seen[name] {
			t.Errorf("legacy hyphenated tool %q must not appear in catalog", name)
		}
	}
}

var _ ToolCatalogHandlerInterface = (*ToolCatalogHandler)(nil)
