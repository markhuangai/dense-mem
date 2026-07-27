package mcp

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestServerRejectsProfileOverrideForContractTools(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	called := false
	tool := registry.ContractTools()[0]
	tool.Visibility = "active"
	tool.Invoke = func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
		called = true
		return map[string]any{"ok": true}, nil
	}
	_ = reg.Register(tool)
	s := NewServerWithScopes(reg, "pA", []string{"read", "write"}, logger)

	out := runRPC(t, s, `{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"remember","arguments":{"profile_id":"pB","evidence":[{"content":"hello"}]}}}`)
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
		t.Fatal("contract tool must not run when tenant selectors are present")
	}
}

func TestMCP_ToolsListHidesDormantContractTools(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	if err := reg.Register(registry.ContractTools()[0]); err != nil {
		t.Fatalf("register contract tool: %v", err)
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
		t.Fatalf("tools/list exposed dormant contract tools: %+v", listPayload.Tools)
	}

	out = runRPC(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"remember","arguments":{"evidence":[{"content":"hello"}]}}}`)
	var callResp rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &callResp); err != nil {
		t.Fatalf("unmarshal call: %v", err)
	}
	if callResp.Error == nil || callResp.Error.Code != errCodeMethodNotFound {
		t.Fatalf("tools/call error = %+v; want method not found", callResp.Error)
	}
}

func TestMCP_RoundTripsContractFixtures(t *testing.T) {
	fixtures := readMCPContractFixtures(t)
	tools := map[string]registry.Tool{}
	for _, tool := range registry.ContractTools() {
		tools[tool.Name] = tool
	}

	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			tool, ok := tools[fixture.Tool]
			if !ok {
				t.Fatalf("fixture tool %s does not exist", fixture.Tool)
			}
			if fixture.ContractVersion != "" {
				tool.ContractVersion = fixture.ContractVersion
			}
			tool.Visibility = "active"
			called := false
			var invoked map[string]any
			tool.Invoke = func(_ context.Context, _ string, input map[string]any) (map[string]any, error) {
				called = true
				invoked = input
				return fixture.Output, nil
			}
			reg := registry.New()
			if err := reg.Register(tool); err != nil {
				t.Fatal(err)
			}
			logger, _ := testLogger(t)
			server := NewServerWithScopesTeamContextAndRuntimeConfig(
				reg,
				"profile-a",
				fixture.Scopes,
				TeamContext{},
				logger,
				recallFeedbackConfigStub{enabled: true},
			)

			params, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  "tools/call",
				"params":  map[string]any{"name": fixture.Tool, "arguments": fixture.Input},
			})
			if err != nil {
				t.Fatal(err)
			}
			out := runRPC(t, server, string(params))
			var response rpcResp
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &response); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if !fixture.Valid {
				if response.Error == nil {
					t.Fatal("invalid fixture returned success")
				}
				if called {
					t.Fatal("invalid fixture invoked tool")
				}
				return
			}
			if response.Error != nil {
				t.Fatalf("tools/call error = %+v", response.Error)
			}
			if !called {
				t.Fatal("valid fixture did not invoke tool")
			}
			if !reflect.DeepEqual(invoked, fixture.Input) {
				t.Fatalf("invoked input = %#v, want %#v", invoked, fixture.Input)
			}
			var result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			if err := json.Unmarshal(response.Result, &result); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}
			if len(result.Content) != 1 {
				t.Fatalf("content length = %d", len(result.Content))
			}
			var output map[string]any
			if err := json.Unmarshal([]byte(result.Content[0].Text), &output); err != nil {
				t.Fatalf("unmarshal tool output: %v", err)
			}
			if !reflect.DeepEqual(output, fixture.Output) {
				t.Fatalf("output = %#v, want %#v", output, fixture.Output)
			}
		})
	}
}

func TestMCP_ToolListCarriesContractVersionMetadata(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	tool := registry.ContractTools()[0]
	tool.Visibility = "active"
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	server := NewServerWithScopes(reg, "profile-a", []string{"write"}, logger)
	out := runRPC(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var response rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &response); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Tools []struct {
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 1 || result.Tools[0].InputSchema["x-contract-version"] != domain.ContractVersion {
		t.Fatalf("tools/list contract metadata = %#v", result.Tools)
	}
}

func TestMCP_ToolListRequiredFieldsAreArraysOrOmitted(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	for _, tool := range registry.ContractTools() {
		tool.Visibility = "active"
		if err := reg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServerWithScopes(reg, "profile-a", []string{"read", "write"}, logger)
	out := runRPC(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var response rpcResp
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatalf("tools/list error = %+v", response.Error)
	}
	var result struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	listDreamsFound := false
	for _, tool := range result.Tools {
		required, exists := tool.InputSchema["required"]
		if exists {
			if _, ok := required.([]any); !ok {
				t.Errorf("%s inputSchema.required = %#v; want array or omitted", tool.Name, required)
			}
		}
		if tool.Name == registry.ToolListDreams {
			listDreamsFound = true
			if exists {
				t.Errorf("%s inputSchema.required = %#v; want omitted", tool.Name, required)
			}
		}
	}
	if !listDreamsFound {
		t.Fatal("tools/list did not expose list_dreams")
	}
}

type mcpContractFixture struct {
	Name            string         `json:"name"`
	Tool            string         `json:"tool"`
	ContractVersion string         `json:"contract_version"`
	Scopes          []string       `json:"scopes"`
	Valid           bool           `json:"valid"`
	Input           map[string]any `json:"input"`
	Output          map[string]any `json:"output"`
}

func readMCPContractFixtures(t *testing.T) []mcpContractFixture {
	t.Helper()
	data, err := os.ReadFile("../tools/registry/testdata/contract_fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []mcpContractFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}
