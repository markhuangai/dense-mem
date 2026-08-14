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
	"github.com/markhuangai/dense-mem/internal/sse"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestMCPHandlerPostInitializeJSON(t *testing.T) {
	h := NewMCPHandler(registry.New(), testMCPLogger())
	e := echo.New()
	profileID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`))
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
	require.Equal(t, "2025-11-25", result["protocolVersion"])
}

func TestMCPHandlerPostInitializeUsesResolvedTeamContext(t *testing.T) {
	h := NewMCPHandler(registry.New(), testMCPLogger())
	e := echo.New()
	profileID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	ctx := middleware.SetResolvedTeamContextForTest(req.Context(), middleware.ResolvedTeamContext{
		ID:          profileID,
		Name:        "Dense-Mem Project",
		Description: "Project memory only",
	})
	ctx = middleware.SetPrincipalForTest(ctx, &middleware.Principal{TeamID: profileID, Scopes: []string{"read"}})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.HandlePost(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	result := resp["result"].(map[string]any)
	serverInfo := result["serverInfo"].(map[string]any)
	require.Equal(t, "dense-mem-dense-mem-project", serverInfo["name"])
	require.Contains(t, serverInfo["description"], "Project memory only")
	require.Contains(t, result["instructions"], "Dense-Mem Project")
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
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`))
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

func TestMCPHandlerPostNotificationAcceptedWithSSEAccept(t *testing.T) {
	h := NewMCPHandler(registry.New(), testMCPLogger())
	e := echo.New()
	profileID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	req.Header.Set(echo.HeaderAccept, "text/event-stream, application/json")
	req = req.WithContext(mcpTestContext(req.Context(), profileID, []string{"read"}))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.HandlePost(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Empty(t, rec.Body.String())
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

func TestMCPHandlerPostValidationBranches(t *testing.T) {
	h := NewMCPHandler(registry.New(), testMCPLogger())
	e := echo.New()

	t.Run("missing profile", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
		req = req.WithContext(middleware.SetPrincipalForTest(req.Context(), &middleware.Principal{Scopes: []string{"read"}}))
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.HandlePost(c)

		require.Error(t, err)
		require.Contains(t, err.Error(), "PROFILE_ID_REQUIRED")
	})

	t.Run("missing principal", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
		req = req.WithContext(middleware.SetResolvedProfileIDForTest(req.Context(), uuid.New()))
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.HandlePost(c)

		require.Error(t, err)
		require.Contains(t, err.Error(), "AUTH_MISSING")
	})

	t.Run("empty body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`   `))
		req = req.WithContext(mcpTestContext(req.Context(), uuid.New(), []string{"read"}))
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.HandlePost(c)

		require.Error(t, err)
		require.Contains(t, err.Error(), "request body is required")
	})
}

func TestMCPHandlerSDKPostInitialize(t *testing.T) {
	h := NewMCPHandlerWithLifecycleAndRuntimeConfigAndTransport(registry.New(), testMCPLogger(), nil, nil, "sdk")
	e := echo.New()
	profileID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAccept, echo.MIMEApplicationJSON)
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	req = req.WithContext(mcpTestContext(req.Context(), profileID, []string{"read"}))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.HandlePost(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"protocolVersion":"2025-11-25"`)
}

func TestMCPHandlerSDKPostValidationBranches(t *testing.T) {
	h := NewMCPHandlerWithLifecycleAndRuntimeConfigAndTransport(registry.New(), testMCPLogger(), nil, nil, "sdk")
	e := echo.New()
	profileID := uuid.New()
	newContext := func(body string) (echo.Context, *httptest.ResponseRecorder) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req = req.WithContext(mcpTestContext(req.Context(), profileID, []string{"read"}))
		rec := httptest.NewRecorder()
		return e.NewContext(req, rec), rec
	}

	t.Run("unsupported header", func(t *testing.T) {
		c, rec := newContext(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
		c.Request().Header.Set(echo.HeaderAccept, echo.MIMEApplicationJSON)
		c.Request().Header.Set("MCP-Protocol-Version", "2099-01-01")
		require.NoError(t, h.HandlePost(c))
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "unsupported MCP protocol version")
	})

	t.Run("unsupported content type precedes initialize validation", func(t *testing.T) {
		c, rec := newContext(`{"jsonrpc":"2.0","id":"init-1","method":"initialize","params":{"protocolVersion":"2099-01-01"}}`)
		c.Request().Header.Set(echo.HeaderContentType, echo.MIMETextPlain)
		c.Request().Header.Set(echo.HeaderAccept, echo.MIMEApplicationJSON)
		c.Request().Header.Set("MCP-Protocol-Version", "2025-11-25")
		require.NoError(t, h.HandlePost(c))
		require.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
		require.Contains(t, rec.Body.String(), "unsupported Content-Type")
	})

	t.Run("unsupported accept", func(t *testing.T) {
		c, rec := newContext(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
		c.Request().Header.Set(echo.HeaderAccept, "text/plain")
		c.Request().Header.Set("MCP-Protocol-Version", "2025-11-25")
		require.NoError(t, h.HandlePost(c))
		require.Equal(t, http.StatusNotAcceptable, rec.Code)
		require.Contains(t, rec.Body.String(), "unsupported Accept header")
	})

	t.Run("unsupported initialize payload preserves id", func(t *testing.T) {
		c, rec := newContext(`{"jsonrpc":"2.0","id":"init-1","method":"initialize","params":{"protocolVersion":"2099-01-01"}}`)
		c.Request().Header.Set(echo.HeaderAccept, echo.MIMEApplicationJSON)
		c.Request().Header.Set("MCP-Protocol-Version", "2025-11-25")
		require.NoError(t, h.HandlePost(c))
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), `"id":"init-1"`)
	})

	t.Run("request body limit", func(t *testing.T) {
		c, rec := newContext(strings.Repeat("x", 4<<20+1))
		c.Request().Header.Set(echo.HeaderAccept, echo.MIMEApplicationJSON)
		c.Request().Header.Set("MCP-Protocol-Version", "2025-11-25")
		require.NoError(t, h.HandlePost(c))
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	})
}

func TestMCPHandlerSDKWildcardDefaultsToJSON(t *testing.T) {
	h := NewMCPHandlerWithLifecycleAndRuntimeConfigAndTransport(registry.New(), testMCPLogger(), nil, nil, "sdk")
	e := echo.New()
	profileID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAccept, "*/*")
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	req = req.WithContext(mcpTestContext(req.Context(), profileID, []string{"read"}))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.HandlePost(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, echo.MIMEApplicationJSON, rec.Header().Get(echo.HeaderContentType))
	var response map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.NotNil(t, response["result"])
}

func TestMCPHandlerSDKHeaderHelpers(t *testing.T) {
	require.True(t, sdkSupportedProtocolVersion(" 2025-11-25 "))
	require.False(t, sdkSupportedProtocolVersion("2099-01-01"))
	require.NoError(t, validateSDKProtocolHeader(""))
	require.Error(t, validateSDKProtocolHeader("2099-01-01"))
	require.Equal(t, "2026-07-28", sdkHeaderProtocolVersion(" 2026-07-28 "))

	for _, value := range []string{"", "*/*", "application/*", "text/*", "application/json", "text/event-stream", "application/json; charset=utf-8, text/plain"} {
		require.True(t, acceptsSDKResponse(value), "Accept %q", value)
	}
	require.True(t, acceptsSDKResponse("application/json, text/event-stream;q=0"))
	require.False(t, acceptsSDKResponse("application/*;q=0, text/*;q=0"))
	require.False(t, acceptsSDKResponse("application/json;q=0, text/event-stream;q=0"))
	require.False(t, acceptsEventStream("text/event-stream;q=0"))
	require.False(t, acceptsEventStream("*/*"))
	require.True(t, acceptsEventStream("text/*"))
	require.True(t, acceptsEventStream("application/json, text/event-stream;q=0.5"))
	require.False(t, acceptsSDKResponse("text/plain"))

	tests := []struct {
		name       string
		payload    string
		method     string
		nameHeader string
		wantErr    string
		wantMethod string
		wantName   string
	}{
		{name: "malformed payload", payload: "{", wantMethod: ""},
		{name: "invalid envelope", payload: `{"jsonrpc":"1.0","method":"tools/list"}`, wantMethod: ""},
		{name: "method only", payload: `{"jsonrpc":"2.0","method":"tools/list","params":{}}`, wantMethod: "tools/list"},
		{name: "tool call", payload: `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"remember"}}`, wantMethod: "tools/call", wantName: "remember"},
		{name: "resource read", payload: `{"jsonrpc":"2.0","method":"resources/read","params":{"uri":"memory://one"}}`, wantMethod: "resources/read", wantName: "memory://one"},
		{name: "bad params", payload: `{"jsonrpc":"2.0","method":"tools/call","params":42}`, wantErr: "invalid MCP request params"},
		{name: "missing name", payload: `{"jsonrpc":"2.0","method":"tools/call","params":{}}`, wantErr: "Mcp-Name is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tt.method != "" {
				req.Header.Set("Mcp-Method", tt.method)
			}
			req.Header.Set("Mcp-Name", "")
			err := populateSDKStandardHeaders(req, []byte(tt.payload))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantMethod, req.Header.Get("Mcp-Method"))
			require.Equal(t, tt.wantName, req.Header.Get("Mcp-Name"))
		})
	}

	t.Run("method mismatch", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Mcp-Method", "tools/list")
		err := populateSDKStandardHeaders(req, []byte(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"remember"}}`))
		require.EqualError(t, err, "Mcp-Method header does not match request method")
	})
	t.Run("name mismatch", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Mcp-Name", "other")
		err := populateSDKStandardHeaders(req, []byte(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"remember"}}`))
		require.EqualError(t, err, "Mcp-Name header does not match request name")
	})
}

func TestMCPHandlerSDKInitializePayloadAndProtocolError(t *testing.T) {
	id, version, ok := sdkInitializePayload([]byte(`{"jsonrpc":"2.0","id":7,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`))
	require.True(t, ok)
	require.Equal(t, "7", string(id))
	require.Equal(t, "2025-11-25", version)
	for _, payload := range []string{
		"{",
		`{"jsonrpc":"2.0","method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":""}}`,
	} {
		_, _, ok = sdkInitializePayload([]byte(payload))
		require.False(t, ok)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, writeSDKProtocolError(c, http.StatusBadRequest, json.RawMessage(`"request-1"`), " bad request "))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `"id":"request-1"`)
	require.Contains(t, rec.Body.String(), `"message":"bad request"`)
}

func TestMCPHandlerTransportConstructors(t *testing.T) {
	legacy := NewMCPHandlerWithLifecycleAndRuntimeConfig(registry.New(), testMCPLogger(), nil, nil)
	require.Equal(t, "legacy", legacy.transport)
	sdk := NewMCPHandlerWithLifecycleAndRuntimeConfigAndTransport(registry.New(), testMCPLogger(), nil, nil, "sdk")
	require.Equal(t, "sdk", sdk.transport)
	fallback := NewMCPHandlerWithLifecycleAndRuntimeConfigAndTransport(registry.New(), testMCPLogger(), nil, nil, "unknown")
	require.Equal(t, "legacy", fallback.transport)
}

func TestMCPHandlerGetBranches(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()

	t.Run("non SSE accept is method not allowed", func(t *testing.T) {
		h := NewMCPHandlerWithLifecycle(registry.New(), testMCPLogger(), &mockStreamLifecycle{})
		req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
		req.Header.Set(echo.HeaderAccept, "application/json")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		require.NoError(t, h.HandleGet(c))
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("missing lifecycle", func(t *testing.T) {
		h := NewMCPHandler(registry.New(), testMCPLogger())
		req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
		req.Header.Set(echo.HeaderAccept, "text/event-stream")
		req = req.WithContext(middleware.SetResolvedProfileIDForTest(req.Context(), profileID))
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.HandleGet(c)

		require.Error(t, err)
		require.Contains(t, err.Error(), "SERVICE_UNAVAILABLE")
	})

	t.Run("missing profile", func(t *testing.T) {
		h := NewMCPHandlerWithLifecycle(registry.New(), testMCPLogger(), &mockStreamLifecycle{})
		req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
		req.Header.Set(echo.HeaderAccept, "text/event-stream")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.HandleGet(c)

		require.Error(t, err)
		require.Contains(t, err.Error(), "PROFILE_ID_REQUIRED")
	})

	t.Run("too many streams", func(t *testing.T) {
		h := NewMCPHandlerWithLifecycle(registry.New(), testMCPLogger(), &mockStreamLifecycle{
			startFunc: func(ctx context.Context, profileID string, writer sse.SSEWriter, work func(context.Context) error) error {
				return sse.ErrTooManyStreams
			},
		})
		req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
		req.Header.Set(echo.HeaderAccept, "text/event-stream")
		req = req.WithContext(middleware.SetResolvedProfileIDForTest(req.Context(), profileID))
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.HandleGet(c)

		require.Error(t, err)
		require.Contains(t, err.Error(), "RATE_LIMITED")
	})

	t.Run("stream terminated is clean", func(t *testing.T) {
		h := NewMCPHandlerWithLifecycle(registry.New(), testMCPLogger(), &mockStreamLifecycle{
			startFunc: func(ctx context.Context, profileID string, writer sse.SSEWriter, work func(context.Context) error) error {
				return sse.ErrStreamTerminated
			},
		})
		req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
		req.Header.Set(echo.HeaderAccept, "text/event-stream")
		req = req.WithContext(middleware.SetResolvedProfileIDForTest(req.Context(), profileID))
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		require.NoError(t, h.HandleGet(c))
	})

	t.Run("writes ready comment", func(t *testing.T) {
		h := NewMCPHandlerWithLifecycle(registry.New(), testMCPLogger(), &mockStreamLifecycle{
			startFunc: func(ctx context.Context, profileID string, writer sse.SSEWriter, work func(context.Context) error) error {
				workCtx, cancel := context.WithCancel(ctx)
				cancel()
				return work(workCtx)
			},
		})
		req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
		req.Header.Set(echo.HeaderAccept, "text/event-stream")
		req = req.WithContext(middleware.SetResolvedProfileIDForTest(req.Context(), profileID))
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		require.NoError(t, h.HandleGet(c))
		require.Contains(t, rec.Body.String(), ": dense-mem MCP stream ready")
	})
}

func TestWriteSSEComment(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, writeSSEComment(c, "hello"))
	require.Equal(t, ": hello\n\n", rec.Body.String())
}

func mcpTestContext(ctx context.Context, profileID uuid.UUID, scopes []string) context.Context {
	ctx = middleware.SetResolvedProfileIDForTest(ctx, profileID)
	return middleware.SetPrincipalForTest(ctx, &middleware.Principal{ProfileID: &profileID, Scopes: scopes})
}

func testMCPLogger() observability.LogProvider {
	return observability.NewWithHandler(slog.NewJSONHandler(io.Discard, nil))
}
