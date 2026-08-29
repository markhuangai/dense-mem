package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestServerIgnoresProfileOverride(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	var gotProfile string
	var gotArgs map[string]any
	_ = reg.Register(registry.Tool{
		Name:        "probe",
		Description: "captures invocation context",
		InputSchema: map[string]any{"type": "object"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			gotProfile = profileID
			gotArgs = input
			return map[string]any{"ok": true}, nil
		},
	})
	s := NewServer(reg, "pA", logger)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"probe","arguments":{"profile_id":"pB","text":"hello"}}}`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v — out=%q", err, out)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if gotProfile != "pA" {
		t.Errorf("profileID = %q; want pA (override not stripped)", gotProfile)
	}
	if _, present := gotArgs["profile_id"]; present {
		t.Errorf("profile_id was not stripped from tool arguments; args = %v", gotArgs)
	}
	if gotArgs["text"] != "hello" {
		t.Errorf("text arg = %v; want hello", gotArgs["text"])
	}
}

func TestServerRejectsProfileOverrideForEvaluationTools(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	called := false
	_ = reg.Register(registry.Tool{
		Name:           "eval_run_recall_case",
		Description:    "evaluation probe",
		InputSchema:    map[string]any{"type": "object"},
		RequiredScopes: []string{"read", "write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			called = true
			return map[string]any{"ok": true}, nil
		},
	})
	s := NewServerWithScopesTeamContextAndRuntimeConfig(
		reg, "pA", []string{"read", "write"}, TeamContext{}, logger,
		recallFeedbackConfigStub{enabled: true},
	)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"eval_run_recall_case","arguments":{"profile_id":"pB"}}}`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v — out=%q", err, out)
	}
	if resp.Error == nil || resp.Error.Code != errCodeInvalidParams {
		t.Fatalf("expected invalid params; got %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "evaluation tools do not accept team_id or profile_id") {
		t.Fatalf("error message = %q", resp.Error.Message)
	}
	if called {
		t.Fatal("evaluation tool must not run when tenant selectors are present")
	}
}

func TestSanitizeToolError(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	_ = reg.Register(registry.Tool{
		Name:        "leaky",
		Description: "returns error with secrets",
		InputSchema: map[string]any{"type": "object"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			return nil, errors.New("upstream call failed: Bearer sk-abc123secret — retry later")
		},
	})
	s := NewServer(reg, "pA", logger)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"leaky","arguments":{}}}`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v — out=%q", err, out)
	}
	if resp.Error == nil || resp.Error.Code != errCodeToolFailure {
		t.Fatalf("expected tool failure; got %+v", resp.Error)
	}
	if strings.Contains(resp.Error.Message, "sk-abc123secret") {
		t.Errorf("secret leaked in error message: %q", resp.Error.Message)
	}
	if !strings.Contains(resp.Error.Message, "upstream call failed") {
		t.Errorf("non-secret context stripped from error message: %q", resp.Error.Message)
	}
}

func TestServerLogsContractInputRejectionWithoutArgumentsOrValidationText(t *testing.T) {
	logger, logs := testLogger(t)
	reg := registry.New()
	tool := registry.ContractTools()[0]
	tool.Visibility = "active"
	tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) {
		t.Fatal("invalid contract input invoked remember")
		return nil, nil
	}
	require.NoError(t, reg.Register(tool))
	teamID := uuid.New()
	ownerID := uuid.New()
	ctx := correlation.WithID(context.Background(), "corr-input-rejected")
	ctx = requestctx.WithActor(ctx, requestctx.Actor{TeamID: teamID, OwnerID: ownerID})
	server := NewServer(reg, teamID.String(), logger)

	_, rpcErr := server.invokeTool(ctx, registry.ToolRemember, map[string]any{
		"evidence": []any{map[string]any{"content": "must-never-appear-in-logs"}},
	})
	require.NotNil(t, rpcErr)
	require.Equal(t, errCodeInvalidParams, rpcErr.Code)

	logged := logs.String()
	require.Contains(t, logged, `"msg":"mcp_tool_input_rejected"`)
	require.Contains(t, logged, `"reason_code":"contract_validation_failed"`)
	require.Contains(t, logged, `"reference_id":"remember"`)
	require.Contains(t, logged, `"correlation_id":"corr-input-rejected"`)
	require.NotContains(t, logged, "must-never-appear-in-logs")
	require.NotContains(t, logged, "relationships is required")
}

func TestServerProjectsDynamicRememberPreflightAsInvalidParams(t *testing.T) {
	logger, logs := testLogger(t)
	reg := registry.New()
	tool := registry.ContractTools()[0]
	tool.Visibility = "active"
	tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) {
		return nil, &registry.ContractValidationFailure{Result: registry.ContractValidationResult{
			Issues: []registry.ContractValidationIssue{{
				Path: "/relationships/0/subject/known_entity_id", Code: "unavailable", Message: "known_entity_id is unavailable",
			}},
		}}
	}
	require.NoError(t, reg.Register(tool))
	teamID := uuid.New()
	ownerID := uuid.New()
	ctx := correlation.WithID(context.Background(), "corr-preflight-rejected")
	ctx = requestctx.WithActor(ctx, requestctx.Actor{TeamID: teamID, OwnerID: ownerID})
	server := NewServer(reg, teamID.String(), logger)

	_, rpcErr := server.invokeTool(ctx, registry.ToolRemember, map[string]any{
		"idempotency_key": "dynamic-preflight",
		"evidence":        []any{map[string]any{"content": "Dense-Mem uses PostgreSQL."}},
		"relationships": []any{map[string]any{
			"ref": "uses-postgresql", "subject": map[string]any{"name": "Dense-Mem"},
			"predicate": map[string]any{"proposed_key": "uses"},
			"object":    map[string]any{"entity": map[string]any{"name": "PostgreSQL"}},
			"polarity":  "+", "evidence_indices": []any{0},
		}},
	})
	require.NotNil(t, rpcErr)
	require.Equal(t, errCodeInvalidParams, rpcErr.Code)
	require.Equal(t, "validation_failed", rpcErr.Data.(map[string]any)["reason"])
	require.Contains(t, logs.String(), `"reason_code":"contract_preflight_failed"`)
}

func TestServerRejectsInvalidSupersessionUUIDAsInvalidParams(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	tool := registry.ContractTools()[0]
	tool.Visibility = "active"
	tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) {
		t.Fatal("invalid supersession UUID invoked remember")
		return nil, nil
	}
	require.NoError(t, reg.Register(tool))
	teamID := uuid.New()
	ownerID := uuid.New()
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: teamID, OwnerID: ownerID, Grants: []string{"write"}})
	server := NewServer(reg, teamID.String(), logger)

	_, rpcErr := server.invokeTool(ctx, registry.ToolRemember, map[string]any{
		"idempotency_key": "invalid-supersession-uuid",
		"evidence": []any{map[string]any{
			"content":                 "A supersession target must be a UUID.",
			"supersedes_evidence_ids": []any{"not-a-uuid"},
		}},
		"relationships": []any{map[string]any{
			"ref": "uses-postgresql", "subject": map[string]any{"name": "Dense-Mem"},
			"predicate": map[string]any{"proposed_key": "uses"},
			"object":    map[string]any{"value": map[string]any{"type": "string", "value": "PostgreSQL"}},
			"polarity":  "+", "evidence_indices": []any{0},
		}},
	})

	require.NotNil(t, rpcErr)
	require.Equal(t, errCodeInvalidParams, rpcErr.Code)
	require.Equal(t, "validation_failed", rpcErr.Data.(map[string]any)["reason"])
	require.Contains(t, rpcErr.Data.(map[string]any)["issues"], map[string]any{
		"path": "/evidence/0/supersedes_evidence_ids/0", "code": "format", "message": "evidence[0].supersedes_evidence_ids[0]: target must be a UUID",
	})
}
