package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestMCPHandlerPostInitializeJSON(t *testing.T) {
	h := NewMCPHandler(registry.New(), testMCPLogger())
	e := echo.New()
	profileID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req = req.WithContext(mcpTestContext(req.Context(), profileID, []string{"read"}))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.HandlePost(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get(echo.HeaderContentType))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "2.0", resp["jsonrpc"])
	result := resp["result"].(map[string]any)
	require.Equal(t, "2024-11-05", result["protocolVersion"])
}

func TestMCPHandlerPostToolCallUsesAuthenticatedProfile(t *testing.T) {
	reg := registry.New()
	profileID := uuid.New()
	var gotProfile string
	require.NoError(t, reg.Register(registry.Tool{
		Name:           "probe",
		Description:    "captures profile",
		InputSchema:    map[string]any{"type": "object"},
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, pid string, input map[string]any) (map[string]any, error) {
			gotProfile = pid
			_, hasOverride := input["team_id"]
			return map[string]any{"has_override": hasOverride}, nil
		},
	}))

	h := NewMCPHandler(reg, testMCPLogger())
	e := echo.New()
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"probe","arguments":{"team_id":"attacker"}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req = req.WithContext(mcpTestContext(req.Context(), profileID, []string{"read"}))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.HandlePost(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, profileID.String(), gotProfile)
	require.Contains(t, rec.Body.String(), `\"has_override\":false`)
}

func TestMCPHandlerPostSSE(t *testing.T) {
	h := NewMCPHandler(registry.New(), testMCPLogger())
	e := echo.New()
	profileID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set(echo.HeaderAccept, "text/event-stream")
	req = req.WithContext(mcpTestContext(req.Context(), profileID, []string{"read"}))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.HandlePost(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream", rec.Header().Get(echo.HeaderContentType))
	require.Contains(t, rec.Body.String(), "event: message\n")
	require.Contains(t, rec.Body.String(), "data: {")
}

func TestMCPHandlerToolsListFiltersByScope(t *testing.T) {
	reg := registry.New()
	require.NoError(t, reg.Register(registry.Tool{Name: "read_tool", Description: "read", RequiredScopes: []string{"read"}}))
	require.NoError(t, reg.Register(registry.Tool{Name: "write_tool", Description: "write", RequiredScopes: []string{"write"}}))

	h := NewMCPHandler(reg, testMCPLogger())
	e := echo.New()
	profileID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req = req.WithContext(mcpTestContext(req.Context(), profileID, []string{"read"}))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.HandlePost(c)
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), "read_tool")
	require.NotContains(t, rec.Body.String(), "write_tool")
}

func mcpTestContext(ctx context.Context, profileID uuid.UUID, scopes []string) context.Context {
	ctx = middleware.SetResolvedProfileIDForTest(ctx, profileID)
	return middleware.SetPrincipalForTest(ctx, &middleware.Principal{ProfileID: &profileID, Scopes: scopes})
}

func testMCPLogger() observability.LogProvider {
	return observability.NewWithHandler(slog.NewJSONHandler(io.Discard, nil))
}
