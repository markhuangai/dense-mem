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
	reg, err := registry.BuildActive(registry.Dependencies{})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
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
		registry.V2ToolCorrectEntityResolution,
		registry.V2ToolExportMemoryPack,
		registry.V2ToolFindMemoryPackCandidates,
		registry.V2ToolGetDream,
		registry.V2ToolGetMemoryPlacement,
		registry.V2ToolImportMemoryPack,
		registry.V2ToolInspectMemoryPack,
		registry.V2ToolListDreams,
		registry.V2ToolRecallMemory,
		registry.V2ToolRemember,
		registry.V2ToolResolveDreamFeedback,
		registry.V2ToolResolveMemoryPlacement,
		registry.V2ToolRollbackMemoryPackImport,
		registry.V2ToolTraceMemory,
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

func TestToolCatalogHandler_HidesMCPOnlyV2MemoryToolsFromHTTPView(t *testing.T) {
	uat, err := registry.BuildActive(registry.Dependencies{})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	httpReg, err := registry.HTTPRegistryView(uat)
	if err != nil {
		t.Fatalf("HTTPRegistryView: %v", err)
	}
	h := NewToolCatalogHandler(httpReg)

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
	visible := map[string]struct{}{}
	for _, tool := range resp.Tools {
		visible[tool.Name] = struct{}{}
	}
	for _, name := range []string{
		registry.V2ToolRemember,
		registry.V2ToolGetMemoryPlacement,
		registry.V2ToolRecallMemory,
		registry.V2ToolTraceMemory,
	} {
		if _, ok := visible[name]; ok {
			t.Fatalf("HTTP catalog exposed MCP-only V2 memory tool %s", name)
		}
	}
	if _, ok := visible[registry.V2ToolListDreams]; !ok {
		t.Fatal("HTTP catalog hid non-memory V2 tool list_dreams")
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
