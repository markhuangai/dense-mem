package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestMCP_ProtectsRecallFeedbackEventToolsWithFeedbackScope(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	called := false
	_ = reg.Register(registry.Tool{
		Name:           "eval_list_recall_feedback_events",
		Description:    "feedback events",
		InputSchema:    map[string]any{"type": "object"},
		RequiredScopes: []string{"read", "feedback:read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			called = true
			return map[string]any{"events": []any{}}, nil
		},
	})

	readWrite := NewServerWithScopesTeamContextAndRuntimeConfig(reg, "pA", []string{"read", "write"}, TeamContext{}, logger, recallFeedbackConfigStub{enabled: true})
	out := runRPC(t, readWrite, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if strings.Contains(out, "eval_list_recall_feedback_events") {
		t.Fatalf("tools/list exposed feedback event tool without feedback scope: %s", out)
	}
	out = runRPC(t, readWrite, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"eval_list_recall_feedback_events","arguments":{}}}`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != errCodeToolFailure {
		t.Fatalf("tools/call error = %+v; want insufficient scope", resp.Error)
	}
	if called {
		t.Fatal("feedback event tool must not run without feedback scope")
	}

	withFeedback := NewServerWithScopesTeamContextAndRuntimeConfig(reg, "pA", []string{"read", "feedback:read"}, TeamContext{}, logger, recallFeedbackConfigStub{enabled: true})
	out = runRPC(t, withFeedback, `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
	if !strings.Contains(out, "eval_list_recall_feedback_events") {
		t.Fatalf("tools/list hid feedback event tool despite feedback scope: %s", out)
	}
	out = runRPC(t, withFeedback, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"eval_list_recall_feedback_events","arguments":{}}}`)
	resp = rpcResp{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("tools/call error = %+v", resp.Error)
	}
	if !called {
		t.Fatal("feedback event tool did not run with feedback scope")
	}
}
