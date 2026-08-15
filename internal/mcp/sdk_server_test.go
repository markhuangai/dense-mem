package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	sdkjsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestSDKHTTPHandlerUsesTheSharedRegistryAndSupportsLegacyAndCurrentFlows(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	require.NoError(t, reg.Register(registry.Tool{
		Name:           "read_tool",
		Description:    "read",
		InputSchema:    map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}},
		RequiredScopes: []string{"read"},
		Invoke: func(_ context.Context, _ string, input map[string]any) (map[string]any, error) {
			return map[string]any{"value": input["value"]}, nil
		},
	}))
	server := NewServerWithScopes(reg, "profile-a", []string{"read"}, logger)
	handler := server.NewSDKHTTPHandler(true)

	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			method := "initialize"
			params := `{"protocolVersion":"` + version + `"}`
			if version == "2026-07-28" {
				method = "server/discover"
				params = `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}`
			}
			body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
			request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			request.Header.Set("MCP-Protocol-Version", version)
			if version == "2026-07-28" {
				request.Header.Set("Mcp-Method", "server/discover")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code)
			var envelope struct {
				Result struct {
					ProtocolVersion   string   `json:"protocolVersion"`
					SupportedVersions []string `json:"supportedVersions"`
				} `json:"result"`
				Error any `json:"error"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
			require.Nil(t, envelope.Error)
			if version == "2026-07-28" {
				require.Contains(t, envelope.Result.SupportedVersions, version)
			} else {
				require.Equal(t, version, envelope.Result.ProtocolVersion)
			}
		})
	}

	discoverRequest := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":11,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`))
	discoverRequest.Header.Set("Content-Type", "application/json")
	discoverRequest.Header.Set("Accept", "application/json, text/event-stream")
	discoverRequest.Header.Set("MCP-Protocol-Version", "2026-07-28")
	discoverRequest.Header.Set("Mcp-Method", "server/discover")
	discoverResponse := httptest.NewRecorder()
	handler.ServeHTTP(discoverResponse, discoverRequest)
	require.Equal(t, http.StatusOK, discoverResponse.Code)
	require.Contains(t, discoverResponse.Body.String(), `"supportedVersions"`)
	require.Contains(t, discoverResponse.Body.String(), `2026-07-28`)

	listRequest := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
	listRequest.Header.Set("Content-Type", "application/json")
	listRequest.Header.Set("Accept", "application/json, text/event-stream")
	listRequest.Header.Set("MCP-Protocol-Version", "2025-11-25")
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	require.Equal(t, http.StatusOK, listResponse.Code)
	require.Contains(t, listResponse.Body.String(), `"name":"read_tool"`)

	callRequest := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_tool","arguments":{"value":"ok"}}}`))
	callRequest.Header.Set("Content-Type", "application/json")
	callRequest.Header.Set("Accept", "application/json, text/event-stream")
	callRequest.Header.Set("MCP-Protocol-Version", "2025-11-25")
	callResponse := httptest.NewRecorder()
	handler.ServeHTTP(callResponse, callRequest)
	require.Equal(t, http.StatusOK, callResponse.Code)
	require.Contains(t, callResponse.Body.String(), `\"value\":\"ok\"`)
}

func TestSDKHTTPHandlerRejectsUnknownProtocolHeader(t *testing.T) {
	logger, _ := testLogger(t)
	server := NewServer(registry.New(), "profile-a", logger)
	handler := server.NewSDKHTTPHandler(true)
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2099-01-01"}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2099-01-01")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
}

func TestSDKErrorAdaptersPreserveJSONRPCDetails(t *testing.T) {
	require.Nil(t, sdkRPCError(nil))

	err := sdkRPCError(&rpcError{Code: errCodeToolFailure, Message: "tool failed", Data: map[string]any{"retryable": true}})
	var sdkErr *sdkjsonrpc.Error
	require.ErrorAs(t, err, &sdkErr)
	require.Equal(t, int64(errCodeToolFailure), sdkErr.Code)
	require.Equal(t, "tool failed", sdkErr.Message)
	require.JSONEq(t, `{"retryable":true}`, string(sdkErr.Data))

	err = sdkRPCError(&rpcError{Code: errCodeInvalidParams, Message: "invalid params"})
	require.ErrorAs(t, err, &sdkErr)
	require.Nil(t, sdkErr.Data)

	err = sdkProtocolError("  malformed request ")
	require.ErrorAs(t, err, &sdkErr)
	require.Equal(t, int64(errCodeInvalidRequest), sdkErr.Code)
	require.Equal(t, "malformed request", sdkErr.Message)
}

func TestSDKHTTPHandlerMapsUnknownAndUnauthorizedTools(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	require.NoError(t, reg.Register(registry.Tool{Name: "write_tool", RequiredScopes: []string{"write"}}))
	server := NewServerWithScopesAndTeamContext(reg, "profile-a", []string{"read"}, TeamContext{Name: "Project"}, logger)

	for _, tc := range []struct {
		name        string
		accept      string
		tool        string
		wantCode    int
		wantMessage string
	}{
		{name: "unknown JSON", accept: "application/json", tool: "missing_tool", wantCode: errCodeMethodNotFound, wantMessage: "tool not found"},
		{name: "scope JSON", accept: "application/json", tool: "write_tool", wantCode: errCodeToolFailure, wantMessage: "insufficient scope"},
		{name: "scope SSE", accept: "text/event-stream", tool: "write_tool", wantCode: errCodeToolFailure, wantMessage: "insufficient scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"`+tc.tool+`","arguments":{}}}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			request.Header.Set("MCP-Protocol-Version", "2025-11-25")
			response := httptest.NewRecorder()
			server.NewSDKHTTPHandler(tc.accept != "text/event-stream").ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code)
			require.Contains(t, response.Body.String(), `"code":`+strconv.Itoa(tc.wantCode))
			require.Contains(t, response.Body.String(), tc.wantMessage)
			if tc.accept == "text/event-stream" {
				require.Equal(t, "text/event-stream", response.Header().Get("Content-Type"))
			}
		})
	}

	require.False(t, server.writeSDKToolLookupError(httptest.NewRecorder(), &http.Request{Method: http.MethodGet}, true))
}

func TestSDKHTTPHandlerPreservesOversizedBodyForSDKValidation(t *testing.T) {
	logger, _ := testLogger(t)
	server := NewServer(registry.New(), "profile-a", logger)
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("x", 4<<20+1)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	response := httptest.NewRecorder()
	server.NewSDKHTTPHandler(true).ServeHTTP(response, request)
	require.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
}

func TestSDKHTTPHandlerValidatesBeforeToolLookup(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	require.NoError(t, reg.Register(registry.Tool{Name: "write_tool", RequiredScopes: []string{"write"}}))
	server := NewServerWithScopes(reg, "profile-a", []string{"read"}, logger)
	handler := server.NewSDKHTTPHandler(true)

	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"missing_tool","arguments":{}}}`))
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnsupportedMediaType, response.Code)
	require.NotContains(t, response.Body.String(), "tool not found")

	request = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"missing_tool","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), `"code":-32020`)

	request = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":{},"method":"tools/call","params":{"name":"missing_tool","arguments":{}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.NotContains(t, response.Body.String(), "tool not found")

	request = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"missing_tool","arguments":"invalid"}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.NotContains(t, response.Body.String(), "tool not found")
}

func TestSDKServerResolvesRuntimeToolPolicyOnce(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	for _, name := range []string{
		registry.ToolSubmitRecallSessionFeedback,
		registry.ToolRecallMemory,
		registry.ToolListDreams,
	} {
		require.NoError(t, reg.Register(registry.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}))
	}
	var recallCalls, dreamCalls int
	server := NewServerWithScopesTeamContextAndRuntimeConfig(
		reg,
		"profile-a",
		[]string{"read"},
		TeamContext{},
		logger,
		recallFeedbackConfigStub{enabled: true, calls: &recallCalls},
		dreamingConfigStub{enabled: true, calls: &dreamCalls},
	)

	server.newSDKServer(context.Background())
	require.Equal(t, 1, recallCalls)
	require.Equal(t, 1, dreamCalls)
}

func TestSDKHTTPHandlerDoesNotAnswerInitializedNotification(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	server := NewServerWithScopes(reg, "profile-a", []string{"read"}, logger)
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	response := httptest.NewRecorder()
	server.NewSDKHTTPHandler(true).ServeHTTP(response, request)
	require.Equal(t, http.StatusAccepted, response.Code)
	require.Empty(t, response.Body.String())
}

func TestSDKToolHandlerRejectsInvalidAndUnserializableResults(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	register := func(name string, invoke registry.ToolInvoker) {
		require.NoError(t, reg.Register(registry.Tool{Name: name, Invoke: invoke}))
	}
	register("good", func(context.Context, string, map[string]any) (map[string]any, error) {
		return map[string]any{"content": []map[string]any{{"text": "ok"}}}, nil
	})
	register("failed", func(context.Context, string, map[string]any) (map[string]any, error) {
		return nil, errors.New("provider unavailable")
	})
	server := NewServer(reg, "profile-a", logger)

	good, err := server.sdkToolHandler("good")(context.Background(), &sdkmcp.CallToolRequest{Params: &sdkmcp.CallToolParamsRaw{Arguments: json.RawMessage(`{"value":"ok"}`)}})
	require.NoError(t, err)
	require.Len(t, good.Content, 1)

	_, err = server.sdkToolHandler("good")(context.Background(), &sdkmcp.CallToolRequest{Params: &sdkmcp.CallToolParamsRaw{Arguments: json.RawMessage("{")}})
	requireSDKError(t, err, errCodeInvalidParams, "invalid params")

	_, err = server.sdkToolHandler("failed")(context.Background(), nil)
	requireSDKError(t, err, errCodeToolFailure, "provider unavailable")
}

func requireSDKError(t *testing.T, err error, code int, message string) {
	t.Helper()
	var sdkErr *sdkjsonrpc.Error
	require.ErrorAs(t, err, &sdkErr)
	require.Equal(t, int64(code), sdkErr.Code)
	require.Contains(t, sdkErr.Message, message)
}
