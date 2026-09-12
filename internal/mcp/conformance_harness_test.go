package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// TestConformanceHarness is the production-entry proof used by the MCP SDK
// parity scenario. It keeps registry metadata and SDK execution on one path.
func TestConformanceHarness(t *testing.T) {
	active, err := registry.BuildActive(registry.Dependencies{})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	if got := len(active.List()); got != conformanceCatalogSize() {
		t.Fatalf("active catalog size = %d, want %d", got, conformanceCatalogSize())
	}

	probeCalls := 0
	probe := registry.Tool{
		Name: "probe",
		InputSchema: map[string]any{
			"type":                 "object",
			"required":             []string{"value"},
			"additionalProperties": false,
			"properties": map[string]any{
				"value": map[string]any{"type": "string", "minLength": 1},
			},
		},
		OutputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{"value": map[string]any{"type": "string"}},
		},
		RequiredScopes: []string{"read"},
		Invoke: func(_ context.Context, _ string, input map[string]any) (map[string]any, error) {
			probeCalls++
			return map[string]any{"value": input["value"]}, nil
		},
	}
	reg := registry.New()
	if err := reg.Register(probe); err != nil {
		t.Fatalf("register probe: %v", err)
	}
	server := NewServerWithScopes(reg, "team-a", []string{"read"}, nil)

	list := conformanceRPC(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if list.Error != nil {
		t.Fatalf("tools/list error = %+v", list.Error)
	}
	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(list.Result, &listed); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	if len(listed.Tools) != 1 || listed.Tools[0].Name != "probe" {
		t.Fatalf("tools/list = %+v, want probe", listed.Tools)
	}

	call := conformanceRPC(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"probe","arguments":{"value":"ok"}}}`)
	if call.Error != nil {
		t.Fatalf("tools/call error = %+v", call.Error)
	}
	var result struct {
		StructuredContent map[string]any `json:"structuredContent"`
		IsError           bool           `json:"isError"`
	}
	if err := json.Unmarshal(call.Result, &result); err != nil {
		t.Fatalf("decode tools/call: %v", err)
	}
	if result.IsError || result.StructuredContent["value"] != "ok" || probeCalls != 1 {
		t.Fatalf("tools/call result = %+v, calls = %d", result, probeCalls)
	}

	invalid := conformanceRPC(t, server, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"probe","arguments":{"value":"ok","unexpected":true}}}`)
	if invalid.Error == nil || invalid.Error.Code != errCodeInvalidParams {
		t.Fatalf("invalid tools/call = %+v, want invalid params", invalid.Error)
	}
	if probeCalls != 1 {
		t.Fatalf("invalid call invoked probe %d times", probeCalls)
	}

	unknown := conformanceRPC(t, server, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"missing","arguments":{}}}`)
	if unknown.Error == nil || unknown.Error.Code != errCodeMethodNotFound {
		t.Fatalf("unknown tools/call = %+v, want method not found", unknown.Error)
	}

	unauthorized := NewServerWithScopes(reg, "team-a", []string{"write"}, nil)
	unauthorizedList := conformanceRPC(t, unauthorized, `{"jsonrpc":"2.0","id":5,"method":"tools/list","params":{}}`)
	var unauthorizedPayload struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(unauthorizedList.Result, &unauthorizedPayload); err != nil {
		t.Fatalf("decode unauthorized tools/list: %v", err)
	}
	if len(unauthorizedPayload.Tools) != 0 {
		t.Fatalf("unauthorized tools/list = %+v, want empty", unauthorizedPayload.Tools)
	}
	unauthorizedCall := conformanceRPC(t, unauthorized, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"probe","arguments":{"value":"ok"}}}`)
	if unauthorizedCall.Error == nil || unauthorizedCall.Error.Code != errCodeToolFailure {
		t.Fatalf("unauthorized tools/call = %+v, want bounded authorization failure", unauthorizedCall.Error)
	}
	if unauthorizedCall.Error.Data == nil {
		t.Fatal("unauthorized tools/call omitted actionable authorization data")
	}

	testConformanceCancellation(t)
}

func conformanceRPC(t *testing.T, server *Server, payload string) rpcResp {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	response := httptest.NewRecorder()
	server.NewSDKHTTPHandler(true).ServeHTTP(response, req)
	var decoded rpcResp
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode MCP response: %v; body=%q", err, response.Body.String())
	}
	return decoded
}

func testConformanceCancellation(t *testing.T) {
	t.Helper()
	started := make(chan struct{})
	finished := make(chan struct{})
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:           "wait_for_cancel",
		InputSchema:    map[string]any{"type": "object", "additionalProperties": false},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
			close(started)
			<-ctx.Done()
			close(finished)
			return nil, ctx.Err()
		},
	}); err != nil {
		t.Fatalf("register cancellation tool: %v", err)
	}
	server := NewServerWithScopes(reg, "team-a", []string{"read"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"wait_for_cancel","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	// Request cancellation propagation is defined for the current stateless
	// protocol, whose POST owns the complete request lifecycle.
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "wait_for_cancel")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.NewSDKHTTPHandler(true).ServeHTTP(response, req)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cancellation tool did not start")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cancellation was not propagated to the registry invoker")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SDK handler did not return after cancellation")
	}
}
