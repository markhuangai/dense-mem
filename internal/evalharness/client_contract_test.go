package evalharness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestCorpusImportPayloadAndPreflightUseRememberContract(t *testing.T) {
	item := CorpusItem{SourceDocID: "optional-labels", Content: "Project Alpha stores memory in PostgreSQL."}
	tool, ok := contractTool(registry.ToolRemember)
	require.True(t, ok)
	withoutLabels, err := pairRequestHash(item)
	require.NoError(t, err)
	for _, test := range []struct {
		name   string
		labels []string
		valid  bool
	}{
		{"absent", nil, true}, {"empty", []string{}, true}, {"present", []string{"storage"}, true}, {"too long", []string{strings.Repeat("x", 65)}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := item
			row.Labels = test.labels
			encoded, err := json.Marshal(corpusRememberInput(row))
			require.NoError(t, err)
			var wire map[string]any
			require.NoError(t, json.Unmarshal(encoded, &wire))
			wireErr := registry.ValidateContractInput(tool, wire, []string{"read", "write"})
			preflightErr := validateEvidenceCorpus(&SeedManifest{SchemaVersion: SeedSchemaVersionV2}, []CorpusItem{row})
			if test.valid {
				require.NoError(t, wireErr)
				require.NoError(t, preflightErr)
			} else {
				require.Error(t, wireErr)
				require.Error(t, preflightErr)
			}
			if len(test.labels) == 0 {
				hash, err := pairRequestHash(row)
				require.NoError(t, err)
				require.Equal(t, withoutLabels, hash)
			}
		})
	}
}

func TestHTTPClientTargetContractDoesNotPollSubmissionStatus(t *testing.T) {
	var statusCalls int32
	server := newEvalHarnessServerWithContract(t, contractModeV3, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "tool:remember":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"contract_version": "dense-mem.v3.0.0", "submission_id": "submission-target",
				"submission_kind": "remember", "processing_state": "completed", "search_state": "current",
				"correlation_id": "target-correlation",
				"evidence":       []map[string]any{{"disposition": "stored", "evidence_id": "fragment-target", "evidence_index": 0, "superseded_evidence_ids": []string{}, "search_state": "current"}},
				"errors":         []map[string]any{},
			})
		case "tool:get_submission_status":
			atomic.AddInt32(&statusCalls, 1)
			t.Fatalf("target contract polled removed get_submission_status")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	mapping, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-target", Content: "target content"}})
	require.NoError(t, err)
	require.Equal(t, "fragment-target", mapping.BySourceDocID["doc-target"].ID)
	require.Zero(t, atomic.LoadInt32(&statusCalls))
}

func TestHTTPClientTargetStructuredPolicyFailurePreservesPayload(t *testing.T) {
	var calls int32
	server := newRawContractTestServer(t, contractModeV3, func(w http.ResponseWriter, name string, _ map[string]any) {
		if name != "remember" {
			t.Fatalf("unexpected tool %s", name)
		}
		atomic.AddInt32(&calls, 1)
		result := map[string]any{
			"contract_version": "dense-mem.v3.0.0", "submission_id": "submission-policy-failure",
			"submission_kind": "remember", "processing_state": "failed", "search_state": "not_required",
			"correlation_id": "target-correlation",
			"evidence":       []any{map[string]any{"disposition": "not_stored", "evidence_index": 0, "superseded_evidence_ids": []any{}, "search_state": "not_required", "reason": "submission_policy_rejected"}},
			"errors":         []any{map[string]any{"code": "submission_policy_rejected", "message": "submission was rejected by semantic policy", "retryable": false, "next_action": "resubmit_remember", "remediation": "Submit corrected evidence with a new idempotency_key."}},
		}
		payload, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1, "result": map[string]any{
				"content":           []map[string]any{{"type": "text", "text": string(payload)}},
				"structuredContent": result, "isError": true,
			},
		})
	})
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-policy-failure", Content: "policy-failure content"}})
	require.Error(t, err)
	var structured *StructuredToolError
	require.ErrorAs(t, err, &structured)
	require.Equal(t, "submission_policy_rejected", structured.Result["errors"].([]any)[0].(map[string]any)["code"])
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestHTTPClientRetriesOnlyStructuredRetrySameRequest(t *testing.T) {
	var calls int32
	server := newRawContractTestServer(t, contractModeV3, func(w http.ResponseWriter, name string, _ map[string]any) {
		if name != "remember" {
			t.Fatalf("unexpected tool %s", name)
		}
		attempt := atomic.AddInt32(&calls, 1)
		if attempt == 1 {
			result := map[string]any{
				"contract_version": "dense-mem.v3.0.0", "submission_id": "submission-retry",
				"submission_kind": "remember", "processing_state": "failed", "search_state": "not_required",
				"correlation_id": "target-correlation", "evidence": []any{}, "errors": []any{map[string]any{"code": "provider_unavailable", "message": "provider unavailable", "retryable": true, "next_action": "retry_same_request", "remediation": "retry"}},
			}
			payload, _ := json.Marshal(result)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": 1, "result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": string(payload)}}, "structuredContent": result, "isError": true,
				},
			})
			return
		}
		result := map[string]any{"processing_state": "completed"}
		payload, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": string(payload)}}, "structuredContent": result, "isError": false}})
	})
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	var out map[string]any
	err := client.callToolWithRetry(context.Background(), "remember", map[string]any{}, &out)
	require.NoError(t, err)
	require.Equal(t, "completed", out["processing_state"])
	require.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestClassifyContractFailsClosedForRemovedStatusTool(t *testing.T) {
	tools := registry.ContractTools()
	definitions := make([]mcpToolDefinition, 0, len(tools)+1)
	for _, tool := range tools {
		definitions = append(definitions, mcpToolDefinition{Name: tool.Name, OutputSchema: tool.OutputSchema})
	}
	definitions = append(definitions, mcpToolDefinition{Name: "get_submission_status"})
	_, err := classifyContract(definitions)
	require.ErrorContains(t, err, "unsupported or mixed MCP contract")
}

func TestClassifyContractAllowsEvaluationToolsAndRejectsUnknownTools(t *testing.T) {
	definitions := make([]mcpToolDefinition, 0)
	for _, tool := range registry.ContractTools() {
		definitions = append(definitions, mcpToolDefinition{Name: tool.Name, OutputSchema: tool.OutputSchema})
	}
	for _, name := range []string{"eval_list_knowledge_refs", "eval_run_recall_case"} {
		definitions = append(definitions, mcpToolDefinition{Name: name})
	}
	mode, err := classifyContract(definitions)
	require.NoError(t, err)
	require.Equal(t, contractModeV3, mode)

	for _, name := range []string{"eval_run_dream_cycle", "unexpected_tool"} {
		_, err = classifyContract(append(definitions, mcpToolDefinition{Name: name}))
		require.ErrorContains(t, err, "unsupported or mixed MCP contract", name)
	}
}

func TestClassifyContractAllowsOmittedRuntimeOptionalTools(t *testing.T) {
	definitions := make([]mcpToolDefinition, 0, len(registry.ContractTools()))
	for _, tool := range registry.ContractTools() {
		if registry.ContractToolRuntimeOptional(tool.Name) {
			continue
		}
		definitions = append(definitions, mcpToolDefinition{Name: tool.Name, OutputSchema: tool.OutputSchema})
	}
	mode, err := classifyContract(definitions)
	require.NoError(t, err)
	require.Equal(t, contractModeV3, mode)
}

func TestEvidenceOnlyUnitClassifyContractRejectsRetiredV262Schema(t *testing.T) {
	definitions := make([]mcpToolDefinition, 0, len(registry.ContractTools()))
	for _, tool := range registry.ContractTools() {
		schema := tool.OutputSchema
		if tool.Name == registry.ToolRemember {
			schema = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"contract_version": map[string]any{"enum": []any{"dense-mem.v2.6.2"}},
				},
			}
		}
		definitions = append(definitions, mcpToolDefinition{Name: tool.Name, OutputSchema: schema})
	}
	_, err := classifyContract(definitions)
	require.ErrorContains(t, err, "unsupported or mixed MCP contract")
}

func TestClassifyContractRejectsMixedUnknownContractVersions(t *testing.T) {
	definitions := make([]mcpToolDefinition, 0, len(registry.ContractTools()))
	for _, tool := range registry.ContractTools() {
		schema := tool.OutputSchema
		if tool.Name == registry.ToolRemember {
			schema = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"contract_version": map[string]any{"enum": []any{"dense-mem.v2.6.2", "dense-mem.v9.0.0"}},
				},
			}
		}
		definitions = append(definitions, mcpToolDefinition{Name: tool.Name, OutputSchema: schema})
	}
	_, err := classifyContract(definitions)
	require.ErrorContains(t, err, "unsupported or mixed MCP contract")
}

func TestHTTPClientRetriesContractDiscoveryAfterFailure(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if atomic.AddInt32(&calls, 1) == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		tools := registry.ContractTools()
		listed := make([]map[string]any, 0, len(tools))
		for _, tool := range tools {
			listed = append(listed, map[string]any{"name": tool.Name, "outputSchema": tool.OutputSchema})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": listed}})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ensureContract(context.Background())
	require.Error(t, err)
	mode, err := client.ensureContract(context.Background())
	require.NoError(t, err)
	require.Equal(t, contractModeV3, mode)
	require.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestDiscoverContractDoesNotExposeServerErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"error": map[string]any{"code": -32000, "message": "provider secret details"},
		})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.discoverContract(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "error code -32000")
	require.NotContains(t, err.Error(), "provider secret details")
}

func newRawContractTestServer(t *testing.T, mode contractMode, call func(http.ResponseWriter, string, map[string]any)) *httptest.Server {
	t.Helper()
	tools := registry.ContractTools()
	listed := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		listed = append(listed, map[string]any{"name": tool.Name, "inputSchema": tool.InputSchema, "outputSchema": tool.OutputSchema})
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		w.Header().Set("Content-Type", "application/json")
		if request.Method == "tools/list" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": listed}})
			return
		}
		if request.Method != "tools/call" {
			t.Fatalf("unexpected method %s", request.Method)
		}
		call(w, request.Params.Name, request.Params.Arguments)
	}))
}

func TestEvidenceOnlyUnitClientRetainsOneIdentityPreservingRecovery(t *testing.T) {
	for _, retryable := range []bool{true, false} {
		for _, failAgain := range []bool{true, false} {
			t.Run(fmt.Sprintf("retryable=%t/fail_again=%t", retryable, failAgain), func(t *testing.T) {
				originalID := uuid.NewString()
				inputs := []map[string]any{}
				server := newRawContractTestServer(t, contractModeV3, func(w http.ResponseWriter, name string, input map[string]any) {
					require.Equal(t, "remember", name)
					inputs = append(inputs, input)
					var result map[string]any
					failed := len(inputs) == 1 || failAgain
					if failed {
						result = map[string]any{"contract_version": "dense-mem.v3.0.0", "submission_id": originalID, "processing_state": "failed", "errors": []any{map[string]any{"code": "provider_unavailable", "retryable": retryable, "next_action": map[bool]string{true: "retry_same_request", false: "contact_operator"}[retryable]}}}
					} else {
						result = map[string]any{"contract_version": "dense-mem.v3.0.0", "submission_id": uuid.NewString(), "processing_state": "completed", "evidence": []any{map[string]any{"evidence_id": "stored", "disposition": "stored", "evidence_index": 0}}}
					}
					payload, err := json.Marshal(result)
					require.NoError(t, err)
					require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"structuredContent": result, "content": []any{map[string]any{"type": "text", "text": string(payload)}}, "isError": failed}}))
				})
				defer server.Close()
				client := &HTTPClient{BaseURL: server.URL, APIKey: "synthetic", Client: server.Client()}
				mapping, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "source", Content: "Exact original café Evidence"}})
				if failAgain {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
					require.Equal(t, "stored", mapping.BySourceDocID["source"].ID)
				}
				require.Len(t, inputs, 2)
				require.Equal(t, inputs[0]["evidence"], inputs[1]["evidence"])
				require.Equal(t, "team_shared", inputs[1]["target_space"])
				key := "eval:source"
				if !retryable {
					key += ":resubmit1"
				}
				require.Equal(t, "eval:source", inputs[0]["idempotency_key"])
				require.Equal(t, key, inputs[1]["idempotency_key"])
			})
		}
	}
}
