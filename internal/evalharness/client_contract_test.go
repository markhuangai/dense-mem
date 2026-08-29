package evalharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestHTTPClientTargetContractDoesNotPollSubmissionStatus(t *testing.T) {
	var statusCalls int32
	server := newEvalHarnessServerWithContract(t, contractModeV261, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "tool:remember":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"contract_version": "dense-mem.v2.6.1", "submission_id": "submission-target",
				"submission_kind": "remember", "processing_state": "completed", "search_state": "current",
				"correlation_id":       "target-correlation",
				"evidence":             []map[string]any{{"disposition": "stored", "evidence_id": "fragment-target", "evidence_index": 0, "superseded_evidence_ids": []string{}, "search_state": "current"}},
				"relationship_results": []map[string]any{}, "errors": []map[string]any{},
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

func TestHTTPClientTargetStructuredRejectionPreservesPayload(t *testing.T) {
	var calls int32
	server := newRawContractTestServer(t, contractModeV261, func(w http.ResponseWriter, name string, _ map[string]any) {
		if name != "remember" {
			t.Fatalf("unexpected tool %s", name)
		}
		atomic.AddInt32(&calls, 1)
		result := map[string]any{
			"contract_version": "dense-mem.v2.6.1", "submission_id": "submission-rejected",
			"submission_kind": "remember", "processing_state": "rejected", "search_state": "not_required",
			"correlation_id":       "target-correlation",
			"evidence":             []any{map[string]any{"disposition": "not_stored", "evidence_index": 0, "superseded_evidence_ids": []any{}, "search_state": "not_required", "reason": "not_supported_by_evidence"}},
			"relationship_results": []any{}, "errors": []any{map[string]any{"code": "no_supported_memory", "message": "no supported memory", "retryable": true, "next_action": "resubmit_remember", "remediation": "resubmit"}},
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
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-rejected", Content: "rejected content"}})
	require.Error(t, err)
	var structured *StructuredToolError
	require.ErrorAs(t, err, &structured)
	require.Equal(t, "no_supported_memory", structured.Result["errors"].([]any)[0].(map[string]any)["code"])
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestHTTPClientRetriesOnlyStructuredRetrySameRequest(t *testing.T) {
	var calls int32
	server := newRawContractTestServer(t, contractModeV261, func(w http.ResponseWriter, name string, _ map[string]any) {
		if name != "remember" {
			t.Fatalf("unexpected tool %s", name)
		}
		attempt := atomic.AddInt32(&calls, 1)
		if attempt == 1 {
			result := map[string]any{
				"contract_version": "dense-mem.v2.6.1", "submission_id": "submission-retry",
				"submission_kind": "remember", "processing_state": "failed", "search_state": "not_required",
				"correlation_id": "target-correlation", "evidence": []any{}, "relationship_results": []any{},
				"errors": []any{map[string]any{"code": "provider_unavailable", "message": "provider unavailable", "retryable": true, "next_action": "retry_same_request", "remediation": "retry"}},
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

func TestClassifyContractFailsClosedForMixedVersionAndStatus(t *testing.T) {
	tools := registry.ContractV261Tools()
	definitions := make([]mcpToolDefinition, 0, len(tools)+1)
	for _, tool := range tools {
		definitions = append(definitions, mcpToolDefinition{Name: tool.Name, OutputSchema: tool.OutputSchema})
	}
	definitions = append(definitions, mcpToolDefinition{Name: registry.ToolGetSubmissionStatus})
	_, err := classifyContract(definitions)
	require.ErrorContains(t, err, "unsupported or mixed MCP contract")
}

func TestClassifyContractAllowsGatedOmissionsAndRejectsUnknownTools(t *testing.T) {
	for _, test := range []struct {
		name string
		mode contractMode
		list func() []registry.Tool
	}{
		{name: "legacy", mode: contractModeLegacy, list: registry.ContractTools},
		{name: "terminal", mode: contractModeV261, list: registry.ContractV261Tools},
	} {
		t.Run(test.name, func(t *testing.T) {
			definitions := make([]mcpToolDefinition, 0)
			for _, tool := range test.list() {
				if registry.ContractToolRuntimeOptional(tool.Name) {
					continue
				}
				definitions = append(definitions, mcpToolDefinition{Name: tool.Name, OutputSchema: tool.OutputSchema})
			}
			for _, name := range []string{"eval_list_knowledge_refs", "eval_run_dream_cycle", "eval_run_recall_case"} {
				definitions = append(definitions, mcpToolDefinition{Name: name})
			}
			mode, err := classifyContract(definitions)
			require.NoError(t, err)
			require.Equal(t, test.mode, mode)

			definitions = append(definitions, mcpToolDefinition{Name: "unexpected_tool"})
			_, err = classifyContract(definitions)
			require.ErrorContains(t, err, "unsupported or mixed MCP contract")
		})
	}
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
		tools := registry.ContractV261Tools()
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
	require.Equal(t, contractModeV261, mode)
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
	if mode == contractModeV261 {
		tools = registry.ContractV261Tools()
	}
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
