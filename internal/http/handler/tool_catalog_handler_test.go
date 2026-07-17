package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

type catalogRecallFeedbackConfigStub struct {
	enabled bool
}

func (s catalogRecallFeedbackConfigStub) RecallFeedbackRuntimeConfig(context.Context) (domain.RecallFeedbackRuntimeConfig, error) {
	return domain.RecallFeedbackRuntimeConfig{Enabled: s.enabled}, nil
}

func (s catalogRecallFeedbackConfigStub) EvaluationRuntimeConfig(context.Context) (domain.EvaluationRuntimeConfig, error) {
	return domain.EvaluationRuntimeConfig{Enabled: s.enabled}, nil
}

// TestToolCatalogHandler_ReturnsRegisteredTools — backpressure test + AC-32.
func TestToolCatalogHandler_ReturnsRegisteredTools(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:           "remember",
		Description:    "store memory evidence and typed claims",
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
	if resp.Tools[0].Name != "remember" {
		t.Errorf("Tools[0].Name = %q; want remember", resp.Tools[0].Name)
	}
	if len(resp.Tools[0].RequiredScopes) == 0 {
		t.Error("Tools[0].RequiredScopes empty")
	}
}

func TestToolCatalogHandler_ReturnsContractMetadata(t *testing.T) {
	reg := registry.New()
	tool := registry.V2ContractTools()[0]
	tool.Visibility = "active"
	if err := reg.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
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
	if len(resp.Tools) != 1 {
		t.Fatalf("Tools length = %d; want 1", len(resp.Tools))
	}
	got := resp.Tools[0]
	if got.ContractVersion != domain.V2ContractVersion {
		t.Fatalf("contract_version = %q", got.ContractVersion)
	}
	if got.FeatureGate != domain.V2FeatureGate || got.Visibility != "active" {
		t.Fatalf("gate metadata = %q/%q", got.FeatureGate, got.Visibility)
	}
}

func TestToolCatalogHandler_HidesDormantV2ContractTools(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.V2ContractTools()[0]); err != nil {
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
	if len(resp.Tools) != 0 {
		t.Fatalf("dormant V2 tools visible in catalog: %+v", resp.Tools)
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

func TestToolCatalogHandler_FullV2Surface(t *testing.T) {
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
		"assemble_context",
		"confirm_memory",
		"dispute_memory_placement",
		"export_memory_pack",
		"find_memory_pack_candidates",
		"get_dream",
		"get_memory_placement",
		"import_memories",
		"import_memory_pack",
		"inspect_memory_pack",
		"list_dreams",
		"recall_memory",
		"reflect_memories",
		"remember",
		"resolve_dream_feedback",
		"rollback_memory_pack_import",
		"trace_memory",
	}
	got := make([]string, 0, len(resp.Tools))
	for _, te := range resp.Tools {
		got = append(got, te.Name)
	}
	slices.Sort(got)
	slices.Sort(expected)
	if !slices.Equal(got, expected) {
		t.Fatalf("visible default catalog tools = %#v; want %#v", got, expected)
	}
}

func TestToolCatalogHandler_HidesRecallFeedbackToolWithRuntimeDisabled(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:           registry.SubmitRecallSessionFeedbackToolName,
		Description:    "feedback",
		InputSchema:    map[string]any{"type": "object"},
		RequiredScopes: []string{"read"},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	h := NewToolCatalogHandlerWithRuntimeConfig(reg, catalogRecallFeedbackConfigStub{enabled: false})

	e := echo.New()
	e.GET("/api/v1/tools", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tools", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), registry.SubmitRecallSessionFeedbackToolName) {
		t.Fatalf("disabled recall feedback tool leaked into catalog: %s", rec.Body.String())
	}
}

func TestToolCatalogHandler_HidesRecallFeedbackEventToolsWithoutScope(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:           "eval_list_recall_feedback_events",
		Description:    "feedback events",
		InputSchema:    map[string]any{"type": "object"},
		RequiredScopes: []string{"read", "feedback:read"},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	h := NewToolCatalogHandlerWithRuntimeConfig(reg, catalogRecallFeedbackConfigStub{enabled: true})

	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := middleware.SetPrincipalForTest(c.Request().Context(), &middleware.Principal{Scopes: []string{"read", "write"}})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	e.GET("/api/v1/tools", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tools", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "eval_list_recall_feedback_events") {
		t.Fatalf("feedback event tool leaked without feedback scope: %s", rec.Body.String())
	}

	e = echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := middleware.SetPrincipalForTest(c.Request().Context(), &middleware.Principal{Scopes: []string{"read", "feedback:read"}})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	e.GET("/api/v1/tools", h.Handle)
	req = httptest.NewRequest(http.MethodGet, "/api/v1/tools", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "eval_list_recall_feedback_events") {
		t.Fatalf("feedback event tool hidden despite feedback scope: %s", rec.Body.String())
	}
}

var _ ToolCatalogHandlerInterface = (*ToolCatalogHandler)(nil)
