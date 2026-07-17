package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestServerRejectsProfileOverrideForV2ContractTools(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	called := false
	tool := registry.V2ContractTools()[0]
	tool.Visibility = "active"
	tool.Invoke = func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
		called = true
		return map[string]any{"ok": true}, nil
	}
	_ = reg.Register(tool)
	s := NewServerWithScopes(reg, "pA", []string{"read", "write"}, logger)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"remember","arguments":{"contract_version":"dense-mem.v2.1","profile_id":"pB","evidence":[{"content":"hello"}]}}}`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v; out=%q", err, out)
	}
	if resp.Error == nil || resp.Error.Code != errCodeInvalidParams {
		t.Fatalf("expected invalid params; got %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "team_id and profile_id are not accepted") {
		t.Fatalf("error message = %q", resp.Error.Message)
	}
	if called {
		t.Fatal("V2 tool must not run when tenant selectors are present")
	}
}

func TestMCP_ToolsListHidesDormantV2ContractTools(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	if err := reg.Register(registry.V2ContractTools()[0]); err != nil {
		t.Fatalf("register V2 contract tool: %v", err)
	}
	s := NewServerWithScopes(reg, "pA", []string{"read", "write"}, logger)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var listResp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &listResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if listResp.Error != nil {
		t.Fatalf("tools/list error = %+v", listResp.Error)
	}
	var listPayload struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(listResp.Result, &listPayload); err != nil {
		t.Fatalf("result unmarshal: %v", err)
	}
	if len(listPayload.Tools) != 0 {
		t.Fatalf("tools/list exposed dormant V2 tools: %+v", listPayload.Tools)
	}

	out = runRPC(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"remember","arguments":{"contract_version":"dense-mem.v2.1","evidence":[{"content":"hello"}]}}}`)
	var callResp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &callResp); err != nil {
		t.Fatalf("unmarshal call: %v", err)
	}
	if callResp.Error == nil || callResp.Error.Code != errCodeMethodNotFound {
		t.Fatalf("tools/call error = %+v; want method not found", callResp.Error)
	}
}
