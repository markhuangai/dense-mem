package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/promptcatalog"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

type recallFeedbackConfigStub struct {
	enabled bool
}

func (s recallFeedbackConfigStub) RecallFeedbackRuntimeConfig(context.Context) (domain.RecallFeedbackRuntimeConfig, error) {
	return domain.RecallFeedbackRuntimeConfig{Enabled: s.enabled}, nil
}

func (s recallFeedbackConfigStub) EvaluationRuntimeConfig(context.Context) (domain.EvaluationRuntimeConfig, error) {
	return domain.EvaluationRuntimeConfig{Enabled: s.enabled}, nil
}

// TestServerIgnoresProfileOverride verifies that a caller cannot override the
// server's fixed profile by injecting profile_id into tool arguments (AC-61 /
// R4 — single-profile enforcement).
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

	// Caller attempts to override profile by injecting profile_id into args.
	out := runRPC(t, s, `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"probe","arguments":{"profile_id":"pB","text":"hello"}}}`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v — out=%q", err, out)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	// The server's bound profile must be used — not the attacker-supplied one.
	if gotProfile != "pA" {
		t.Errorf("profileID = %q; want pA (override not stripped)", gotProfile)
	}
	// profile_id must be removed from the args map passed to the tool.
	if _, present := gotArgs["profile_id"]; present {
		t.Errorf("profile_id was not stripped from tool arguments; args = %v", gotArgs)
	}
	// Other args must be passed through.
	if gotArgs["text"] != "hello" {
		t.Errorf("text arg = %v; want hello", gotArgs["text"])
	}
}

func TestServerRejectsProfileOverrideForEvaluationTools(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	called := false
	_ = reg.Register(registry.Tool{
		Name:           "eval_get_manifest",
		Description:    "evaluation probe",
		InputSchema:    map[string]any{"type": "object"},
		RequiredScopes: []string{"read", "write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			called = true
			return map[string]any{"ok": true}, nil
		},
	})
	s := NewServerWithScopesTeamContextAndRuntimeConfig(reg, "pA", []string{"read", "write"}, TeamContext{}, logger, recallFeedbackConfigStub{enabled: true})

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"eval_get_manifest","arguments":{"profile_id":"pB"}}}`)
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

// TestSanitizeToolError verifies that error messages containing secrets (Bearer
// tokens, sk-... keys) are scrubbed before reaching the MCP client (AC-60).
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
	// Bearer token must be redacted.
	if strings.Contains(resp.Error.Message, "sk-abc123secret") {
		t.Errorf("secret leaked in error message: %q", resp.Error.Message)
	}
	// Non-secret context must survive.
	if !strings.Contains(resp.Error.Message, "upstream call failed") {
		t.Errorf("non-secret context stripped from error message: %q", resp.Error.Message)
	}
}

// testLogger returns a LogProvider that writes to a bytes.Buffer.
func testLogger(t *testing.T) (observability.LogProvider, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return observability.NewWithHandler(h), buf
}

// runRPC feeds one request payload through the MCP JSON-RPC dispatcher.
func runRPC(t *testing.T, s *Server, request string) string {
	t.Helper()
	return string(s.HandlePayload(context.Background(), []byte(request)))
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

func TestMCP_Initialize(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	s := NewServer(reg, "pA", logger)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v — out=%q", err, out)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("result unmarshal: %v", err)
	}
	if result["protocolVersion"] != ProtocolVersion {
		t.Errorf("protocolVersion = %v; want %v", result["protocolVersion"], ProtocolVersion)
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok || caps["tools"] == nil {
		t.Errorf("capabilities.tools missing: %v", result["capabilities"])
	}
	if caps["prompts"] == nil {
		t.Errorf("capabilities.prompts missing: %v", result["capabilities"])
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != ServerName {
		t.Errorf("serverInfo.name = %v; want %v", info["name"], ServerName)
	}
	if _, ok := result["instructions"]; ok {
		t.Errorf("legacy initialize unexpectedly included team instructions: %v", result["instructions"])
	}
}

func TestMCP_PromptsListAndGet(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	s := NewServer(reg, "pA", logger)

	listOut := runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"prompts/list","params":{}}`)
	var listResp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(listOut)), &listResp); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if listResp.Error != nil {
		t.Fatalf("prompts/list error = %+v", listResp.Error)
	}
	var listPayload struct {
		Prompts []struct {
			Name      string `json:"name"`
			Arguments []struct {
				Name     string `json:"name"`
				Required bool   `json:"required"`
			} `json:"arguments"`
		} `json:"prompts"`
	}
	if err := json.Unmarshal(listResp.Result, &listPayload); err != nil {
		t.Fatalf("list result unmarshal: %v", err)
	}
	if len(listPayload.Prompts) != 1 || listPayload.Prompts[0].Name != "export_memory_as_agent_skill" {
		t.Fatalf("prompts/list = %+v", listPayload.Prompts)
	}
	foundTopic := false
	for _, arg := range listPayload.Prompts[0].Arguments {
		if arg.Name == "topic" && arg.Required {
			foundTopic = true
		}
	}
	if !foundTopic {
		t.Fatalf("topic argument missing or not required: %+v", listPayload.Prompts[0].Arguments)
	}

	getOut := runRPC(t, s, `{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{"name":"export_memory_as_agent_skill","arguments":{"topic":"review workflows","skill_name":"review-workflows"}}}`)
	var getResp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(getOut)), &getResp); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if getResp.Error != nil {
		t.Fatalf("prompts/get error = %+v", getResp.Error)
	}
	var getPayload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(getResp.Result, &getPayload); err != nil {
		t.Fatalf("get result unmarshal: %v", err)
	}
	if len(getPayload.Messages) != 1 || getPayload.Messages[0].Role != "user" || getPayload.Messages[0].Content.Type != "text" {
		t.Fatalf("messages = %+v", getPayload.Messages)
	}
	text := getPayload.Messages[0].Content.Text
	for _, want := range []string{
		"review workflows",
		"review-workflows",
		"SKILL.md",
		"Exclude secrets",
		"self-contained",
		"recipients who do not have access",
		"Do not tell the generated skill to query Dense-Mem",
		"portability and privacy checks",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered prompt missing %q: %s", want, text)
		}
	}
}

func TestMCP_PromptsGetErrors(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	s := NewServer(reg, "pA", logger)

	cases := []struct {
		name string
		req  string
		code int
	}{
		{"missing params", `{"jsonrpc":"2.0","id":1,"method":"prompts/get"}`, errCodeInvalidParams},
		{"missing required arg", `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"export_memory_as_agent_skill","arguments":{}}}`, errCodeInvalidParams},
		{"non-string optional arg", `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"export_memory_as_agent_skill","arguments":{"topic":"review workflows","skill_name":123}}}`, errCodeInvalidParams},
		{"unknown prompt", `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"missing","arguments":{}}}`, errCodeMethodNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := runRPC(t, s, tc.req)
			var resp rpcResp
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if resp.Error == nil || resp.Error.Code != tc.code {
				t.Fatalf("error = %+v; want code %d", resp.Error, tc.code)
			}
		})
	}
}

func TestDefaultPromptCatalogLogsLoadFailure(t *testing.T) {
	logger, logBuf := testLogger(t)
	catalog := promptCatalogOrEmpty(logger, func() (promptcatalog.Catalog, error) {
		return promptcatalog.Catalog{}, errors.New("manifest missing")
	})
	if prompts := catalog.List(); len(prompts) != 0 {
		t.Fatalf("prompts len = %d, want 0", len(prompts))
	}
	if log := logBuf.String(); !strings.Contains(log, "mcp: prompt catalog unavailable") || !strings.Contains(log, "manifest missing") {
		t.Fatalf("log = %s, want prompt catalog warning", log)
	}
}

func TestMCP_InitializeIncludesTeamContext(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	s := NewServerWithScopesAndTeamContext(reg, "pA", nil, TeamContext{
		Name:        "Dense-Mem Project",
		Description: "Project memory only",
	}, logger)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v — out=%q", err, out)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("result unmarshal: %v", err)
	}

	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != "dense-mem-dense-mem-project" {
		t.Errorf("serverInfo.name = %v; want dense-mem-dense-mem-project", info["name"])
	}
	if info["title"] != "Dense-Mem: Dense-Mem Project" {
		t.Errorf("serverInfo.title = %v", info["title"])
	}
	description, _ := info["description"].(string)
	if !strings.Contains(description, "Project memory only") {
		t.Errorf("serverInfo.description missing team description: %q", description)
	}
	instructions, _ := result["instructions"].(string)
	for _, want := range []string{"Dense-Mem Project", "personal memory MCP", "ask before writing"} {
		if !strings.Contains(instructions, want) {
			t.Errorf("instructions missing %q: %q", want, instructions)
		}
	}
}

func TestMCP_ToolsListMirrorsRegistry(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	_ = reg.Register(registry.Tool{
		Name:        "remember",
		Description: "store",
		InputSchema: map[string]any{"type": "object"},
	})
	_ = reg.Register(registry.Tool{
		Name:        "recall_memory",
		Description: "recall",
		InputSchema: map[string]any{"type": "object"},
	})
	s := NewServer(reg, "pA", logger)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if !strings.Contains(out, `"remember"`) {
		t.Errorf("tools/list missing remember; got %s", out)
	}
	if !strings.Contains(out, `"recall_memory"`) {
		t.Errorf("tools/list missing recall_memory; got %s", out)
	}

	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var payload struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		t.Fatalf("result unmarshal: %v", err)
	}
	if len(payload.Tools) != 2 {
		t.Fatalf("tools count = %d; want 2", len(payload.Tools))
	}
	// Registry.List is sorted, so remember comes after recall_memory.
	if payload.Tools[0]["name"] != "recall_memory" || payload.Tools[1]["name"] != "remember" {
		t.Errorf("unsorted: %v", payload.Tools)
	}
	if _, ok := payload.Tools[0]["inputSchema"]; !ok {
		t.Errorf("missing inputSchema")
	}
}

func TestMCP_ToolsListHidesRecallFeedbackWhenRuntimeDisabled(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	_ = reg.Register(registry.Tool{
		Name:        registry.SubmitRecallSessionFeedbackToolName,
		Description: "feedback",
		InputSchema: map[string]any{"type": "object"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			return nil, registry.ErrToolDisabled
		},
	})
	s := NewServerWithScopesTeamContextAndRuntimeConfig(reg, "pA", []string{"read"}, TeamContext{}, logger, recallFeedbackConfigStub{enabled: false})

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
	found := false
	for _, tool := range listPayload.Tools {
		if tool.Name == registry.SubmitRecallSessionFeedbackToolName {
			found = true
			break
		}
	}
	if found {
		t.Fatalf("tools/list exposed disabled recall feedback tool: %s", out)
	}

	out = runRPC(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"submit_recall_session_feedback","arguments":{}}}`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != errCodeMethodNotFound {
		t.Fatalf("tools/call error = %+v; want method not found", resp.Error)
	}
}

func TestMCP_ToolsCallMapsDisabledInvokeToNotFound(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	_ = reg.Register(registry.Tool{
		Name:        registry.SubmitRecallSessionFeedbackToolName,
		Description: "feedback",
		InputSchema: map[string]any{"type": "object"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			return nil, registry.ErrToolDisabled
		},
	})
	s := NewServerWithScopesTeamContextAndRuntimeConfig(reg, "pA", []string{"read"}, TeamContext{}, logger, recallFeedbackConfigStub{enabled: true})

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"submit_recall_session_feedback","arguments":{}}}`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != errCodeMethodNotFound {
		t.Fatalf("tools/call error = %+v; want method not found", resp.Error)
	}
}

func TestMCP_ToolsListIncludesTeamContextWithoutMutatingRegistry(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	_ = reg.Register(registry.Tool{
		Name:        "remember",
		Description: "Store durable memory.",
		InputSchema: map[string]any{"type": "object"},
	})
	s := NewServerWithScopesAndTeamContext(reg, "pA", nil, TeamContext{
		Name:        "Dense-Mem Project",
		Description: "Project memory only",
	}, logger)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var payload struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		t.Fatalf("result unmarshal: %v", err)
	}
	if len(payload.Tools) != 1 {
		t.Fatalf("tools count = %d; want 1", len(payload.Tools))
	}

	description, _ := payload.Tools[0]["description"].(string)
	for _, want := range []string{"Dense-Mem team scope: Dense-Mem Project", "Project memory only", "Store durable memory."} {
		if !strings.Contains(description, want) {
			t.Errorf("tool description missing %q: %q", want, description)
		}
	}

	tool, ok := reg.Get("remember")
	if !ok {
		t.Fatal("remember not registered")
	}
	if tool.Description != "Store durable memory." {
		t.Fatalf("registry description was mutated: %q", tool.Description)
	}
}

func TestMCP_ToolsCallInvokesRegistry(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	var gotProfile string
	var gotArgs map[string]any
	_ = reg.Register(registry.Tool{
		Name:        "remember",
		Description: "store",
		InputSchema: map[string]any{"type": "object"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			gotProfile = profileID
			gotArgs = input
			return map[string]any{"id": "abc", "status": "created"}, nil
		},
	})
	s := NewServer(reg, "pA", logger)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"remember","arguments":{"text":"hello"}}}`)
	if gotProfile != "pA" {
		t.Errorf("profileID = %q; want pA", gotProfile)
	}
	if gotArgs["text"] != "hello" {
		t.Errorf("arguments.text = %v; want hello", gotArgs["text"])
	}

	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	var result struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("result unmarshal: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0]["type"] != "text" {
		t.Fatalf("content: %+v", result.Content)
	}
	text, _ := result.Content[0]["text"].(string)
	if !strings.Contains(text, `"id":"abc"`) || !strings.Contains(text, `"status":"created"`) {
		t.Errorf("text payload missing fields: %s", text)
	}
}

func TestMCP_MemoryToolsScopeProfileAndClarifications(t *testing.T) {
	logger, _ := testLogger(t)
	mem := &mcpMemoryStub{}
	reg, err := registry.BuildDefault(registry.Dependencies{Memory: mem})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}

	readOnly := NewServerWithScopes(reg, "profileA", []string{"read"}, logger)
	listOut := runRPC(t, readOnly, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if strings.Contains(listOut, `"remember"`) || strings.Contains(listOut, `"reflect_memories"`) || strings.Contains(listOut, `"confirm_memory"`) {
		t.Fatalf("read-scoped list exposed write tools: %s", listOut)
	}

	readWrite := NewServerWithScopes(reg, "profileA", []string{"read", "write"}, logger)
	callOut := runRPC(t, readWrite, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"remember","arguments":{"profile_id":"profileB","evidence":[{"content":"remember this"}]}}}`)
	if mem.lastProfile != "profileA" {
		t.Fatalf("memory tool profile = %q; want profileA", mem.lastProfile)
	}
	if strings.Contains(callOut, "profileB") {
		t.Fatalf("caller-supplied profile_id leaked into MCP result: %s", callOut)
	}
	if !strings.Contains(callOut, `\"ingest_id\"`) || !strings.Contains(callOut, `\"queued\"`) {
		t.Fatalf("placement payload missing from MCP result: %s", callOut)
	}
}

func TestMCP_RememberAcceptsLegacyContentCallAsEvidence(t *testing.T) {
	logger, _ := testLogger(t)
	mem := &mcpMemoryStub{}
	reg, err := registry.BuildDefault(registry.Dependencies{Memory: mem})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	server := NewServerWithScopes(reg, "profileA", []string{"read", "write"}, logger)

	out := runRPC(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"remember","arguments":{"content":"legacy client content","labels":["decision","security"],"source":"chat: compatibility","idempotency_key":"legacy-content-call","auto_promote":true,"claims":[{"subject":"client","predicate":"uses","object":"old remember shape","extract_conf":0.99,"resolution_conf":0.99}]}}}`)
	if strings.Contains(out, `"error"`) {
		t.Fatalf("legacy remember call failed: %s", out)
	}
	if len(mem.lastRemember.Evidence) != 1 {
		t.Fatalf("remember evidence count = %d; want 1", len(mem.lastRemember.Evidence))
	}
	evidence := mem.lastRemember.Evidence[0]
	if evidence.Content != "legacy client content" {
		t.Fatalf("remember evidence content = %q", evidence.Content)
	}
	if evidence.Source != "chat: compatibility" || evidence.IdempotencyKey != "legacy-content-call" {
		t.Fatalf("remember evidence provenance = %#v", evidence)
	}
	if len(evidence.Labels) != 2 || evidence.Labels[0] != "decision" || evidence.Labels[1] != "security" {
		t.Fatalf("remember evidence labels = %#v", evidence.Labels)
	}
}

type mcpMemoryStub struct {
	lastProfile  string
	lastRemember memoryservice.RememberRequest
}

func (s *mcpMemoryStub) Remember(ctx context.Context, profileID string, req memoryservice.RememberRequest) (*memoryservice.RememberResult, error) {
	s.lastProfile = profileID
	s.lastRemember = req
	return &memoryservice.RememberResult{
		IngestID: "ingest-1",
		Status:   "queued",
	}, nil
}

func (s *mcpMemoryStub) GetMemoryPlacement(ctx context.Context, profileID string, req memoryservice.PlacementStatusRequest) (*memoryservice.PlacementStatusResult, error) {
	s.lastProfile = profileID
	return &memoryservice.PlacementStatusResult{
		Run: domain.MemoryPlacementRun{IngestID: req.IngestID, Status: domain.MemoryPlacementCompleted},
	}, nil
}

func (s *mcpMemoryStub) DisputeMemoryPlacement(ctx context.Context, profileID string, req memoryservice.DisputeRequest) (*memoryservice.DisputeResult, error) {
	s.lastProfile = profileID
	return &memoryservice.DisputeResult{
		Session: domain.MemoryDisputeSession{DisputeID: "dispute-1", Status: domain.MemoryDisputeOpen},
	}, nil
}

func (s *mcpMemoryStub) ImportMemories(ctx context.Context, profileID string, req memoryservice.ImportRequest) (*memoryservice.RememberResult, error) {
	s.lastProfile = profileID
	return &memoryservice.RememberResult{Fragment: memoryservice.FragmentOutcome{ID: "fragment-import", Status: "created"}}, nil
}

func (s *mcpMemoryStub) Reflect(ctx context.Context, profileID string, req memoryservice.ReflectRequest) (*memoryservice.ReflectResult, error) {
	s.lastProfile = profileID
	return &memoryservice.ReflectResult{}, nil
}

func (s *mcpMemoryStub) ConfirmMemory(ctx context.Context, profileID string, req memoryservice.ConfirmRequest) (*memoryservice.ConfirmResult, error) {
	s.lastProfile = profileID
	return &memoryservice.ConfirmResult{ClaimID: req.ClaimID, Decision: req.Decision, Status: "accepted"}, nil
}

func TestMCP_ToolsCallUnknownToolReturnsError(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	s := NewServer(reg, "pA", logger)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ghost","arguments":{}}}`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected error, got result: %s", resp.Result)
	}
	if resp.Error.Code != errCodeMethodNotFound {
		t.Errorf("code = %d; want %d", resp.Error.Code, errCodeMethodNotFound)
	}
}

func TestMCP_ToolsCallProviderErrorReturnsError(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	_ = reg.Register(registry.Tool{
		Name:        "recall_memory",
		Description: "recall",
		InputSchema: map[string]any{"type": "object"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			return nil, errors.New("embedding provider unavailable")
		},
	})
	s := NewServer(reg, "pA", logger)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"recall_memory","arguments":{}}}`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != errCodeToolFailure {
		t.Errorf("expected tool failure code; got %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "embedding provider unavailable") {
		t.Errorf("error message should surface sanitized reason; got %q", resp.Error.Message)
	}
}

func TestMCP_ToolErrorSurfacesWithoutLeak(t *testing.T) {
	logger, logBuf := testLogger(t)
	reg := registry.New()
	_ = reg.Register(registry.Tool{
		Name:        "boom",
		Description: "broken",
		InputSchema: map[string]any{"type": "object"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			return nil, errors.New("recall: embedding provider unavailable")
		},
	})
	s := NewServer(reg, "pA", logger)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"boom","arguments":{}}}`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != errCodeToolFailure {
		t.Fatalf("expected tool failure; got %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "embedding provider unavailable") {
		t.Errorf("error message should surface sanitized reason; got %q", resp.Error.Message)
	}
	if logBuf.Len() == 0 {
		t.Errorf("expected tool failure to be logged to the provided logger")
	}
}

func TestMCP_ParseErrorReturnsJSONRPCError(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	s := NewServer(reg, "pA", logger)

	out := runRPC(t, s, `this is not json`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v — out=%q", err, out)
	}
	if resp.Error == nil || resp.Error.Code != errCodeParseError {
		t.Errorf("expected parse error; got %+v", resp.Error)
	}
}

func TestMCP_UnknownMethodReturnsError(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	s := NewServer(reg, "pA", logger)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":7,"method":"does/not/exist"}`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != errCodeMethodNotFound {
		t.Errorf("expected method not found; got %+v", resp.Error)
	}
}

func TestMCP_HandlePayloadResultNotifications(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	s := NewServer(reg, "pA", logger)

	notification := s.HandlePayloadResult(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if notification.Respond {
		t.Fatalf("notification Respond = true, payload = %q", string(notification.Payload))
	}
	if len(notification.Payload) != 0 {
		t.Fatalf("notification payload = %q; want empty", string(notification.Payload))
	}

	invalid := s.HandlePayloadResult(context.Background(), []byte(`{"jsonrpc":"2.0"}`))
	if !invalid.Respond {
		t.Fatal("invalid no-id payload Respond = false; want error response")
	}
	var resp rpcResp
	if err := json.Unmarshal(invalid.Payload, &resp); err != nil {
		t.Fatalf("unmarshal invalid response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != errCodeMethodNotFound {
		t.Fatalf("invalid no-id error = %+v; want method not found", resp.Error)
	}
}

func TestMCP_HandlePayloadProducesOnlyJSONRPC(t *testing.T) {
	logger, logBuf := testLogger(t)
	reg := registry.New()
	_ = reg.Register(registry.Tool{
		Name:        "noop",
		Description: "noop",
		InputSchema: map[string]any{"type": "object"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	})
	s := NewServer(reg, "pA", logger)

	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"noop","arguments":{}}}`,
	}
	for _, request := range requests {
		out := runRPC(t, s, request)
		var probe rpcResp
		if err := json.Unmarshal([]byte(out), &probe); err != nil {
			t.Fatalf("response is not valid JSON-RPC: %q — %v", out, err)
		}
		if probe.JSONRPC != "2.0" {
			t.Errorf("jsonrpc = %q; want 2.0", probe.JSONRPC)
		}
	}

	_ = logBuf
}

func TestMCP_ToolsListDefaultsNilSchemaAndFiltersScopes(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "public", Description: "public"})
	_ = reg.Register(registry.Tool{Name: "write_only", Description: "write", RequiredScopes: []string{"write"}})
	s := NewServerWithScopes(reg, "pA", []string{"read"}, logger)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":8,"method":"tools/list"}`)

	if !strings.Contains(out, `"public"`) {
		t.Fatalf("tools/list missing public tool: %s", out)
	}
	if strings.Contains(out, `"write_only"`) {
		t.Fatalf("tools/list exposed insufficient-scope tool: %s", out)
	}
	if !strings.Contains(out, `"inputSchema":{"type":"object"}`) {
		t.Fatalf("nil schema was not defaulted: %s", out)
	}
}

func TestMCP_ToolsCallRejectsInvalidParamsAndScope(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	_ = reg.Register(registry.Tool{
		Name:           "write_tool",
		Description:    "needs write",
		RequiredScopes: []string{"write"},
		Invoke: func(context.Context, string, map[string]any) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	})
	_ = reg.Register(registry.Tool{
		Name:        "needs_text",
		Description: "validates input",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"text"},
			"properties": map[string]any{
				"text": map[string]any{"type": "string"},
			},
		},
		Invoke: func(context.Context, string, map[string]any) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	})
	_ = reg.Register(registry.Tool{Name: "metadata_only", Description: "no invoker"})
	s := NewServerWithScopes(reg, "pA", []string{"read"}, logger)

	cases := []struct {
		name string
		req  string
		code int
	}{
		{"missing params", `{"jsonrpc":"2.0","id":1,"method":"tools/call"}`, errCodeInvalidParams},
		{"invalid params", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":42}`, errCodeInvalidParams},
		{"missing tool name", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"arguments":{}}}`, errCodeInvalidParams},
		{"insufficient scope", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write_tool","arguments":{}}}`, errCodeToolFailure},
		{"not executable", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"metadata_only","arguments":{}}}`, errCodeToolFailure},
		{"validation error", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"needs_text","arguments":{}}}`, errCodeInvalidParams},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := runRPC(t, s, tc.req)
			var resp rpcResp
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if resp.Error == nil || resp.Error.Code != tc.code {
				t.Fatalf("error = %+v; want code %d", resp.Error, tc.code)
			}
		})
	}
}

func TestMCP_ToolsCallHandlesNilArgumentsAndMarshalFailure(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	var gotArgs map[string]any
	_ = reg.Register(registry.Tool{
		Name:        "nil_args",
		Description: "captures nil args",
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			gotArgs = input
			return map[string]any{"ok": true}, nil
		},
	})
	_ = reg.Register(registry.Tool{
		Name:        "bad_result",
		Description: "returns unmarshalable value",
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			return map[string]any{"bad": make(chan int)}, nil
		},
	})
	s := NewServer(reg, "pA", logger)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nil_args"}}`)
	var resp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if gotArgs == nil || len(gotArgs) != 0 {
		t.Fatalf("nil arguments were not normalized to empty map: %v", gotArgs)
	}

	out = runRPC(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"bad_result","arguments":{}}}`)
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != errCodeToolFailure {
		t.Fatalf("expected marshal failure, got %+v", resp.Error)
	}
}

func TestMCPMustMarshalResponseFallsBackOnBadResult(t *testing.T) {
	out := mustMarshalResponse(okResponse(json.RawMessage(`1`), map[string]any{"bad": make(chan int)}))
	var resp rpcResp
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("fallback response was not valid JSON: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != errCodeToolFailure {
		t.Fatalf("fallback error = %+v; want tool failure", resp.Error)
	}
}
