package evalharness

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/stretchr/testify/require"
)

func TestImporterRetriesOnlyOneVerifiedTerminalFailureAndPreservesInput(t *testing.T) {
	for _, recover := range []bool{true, false} {
		t.Run(map[bool]string{true: "recovery", false: "exhausted"}[recover], func(t *testing.T) {
			inputs := []map[string]any{}
			attempt := uuid.NewString()
			item := CorpusItem{SourceDocID: "source", Content: "First.\n第二。"}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var rpc struct {
					Method string `json:"method"`
					Params struct {
						Arguments map[string]any `json:"arguments"`
					} `json:"params"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&rpc))
				if rpc.Method == "tools/list" {
					tools := []mcpToolDefinition{}
					for _, tool := range registry.ContractTools() {
						tools = append(tools, mcpToolDefinition{Name: tool.Name, OutputSchema: tool.OutputSchema})
					}
					require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"tools": tools}}))
					return
				}
				inputs = append(inputs, rpc.Params.Arguments)
				out := map[string]any{"processing_state": "failed", "submission_id": attempt, "errors": []any{map[string]any{"code": "provider_unavailable", "message": "provider failed", "retryable": false, "next_action": "resubmit_remember"}}}
				failed := true
				if recover && len(inputs) == 2 {
					out = map[string]any{"processing_state": "completed", "submission_id": uuid.NewString(), "evidence": []any{map[string]any{"evidence_index": 0, "partition_index": 0, "source_item_id": uuid.NewString(), "occurrence_id": uuid.NewString(), "evidence_id": uuid.NewString(), "span_start": 0, "span_end": len([]rune(item.Content)), "content_hash": knowledge.EvidenceContentHash(item.Content), "disposition": "stored"}}}
					failed = false
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"isError": failed, "structuredContent": out}}))
			}))
			defer server.Close()
			var logs bytes.Buffer
			client := &HTTPClient{BaseURL: server.URL, APIKey: "synthetic", Client: server.Client(), Capture: captureFixture(&logs)}
			mapping, err := client.ImportCorpus(context.Background(), []CorpusItem{item})
			progress := capturedProgress(t, logs.Bytes())
			require.Len(t, progress, 2, "retry attempts count one terminal document")
			require.Equal(t, captureProgress{Stage: "import", Unit: "documents", Total: 1, Remaining: 1}, progress[0])
			if recover {
				require.NoError(t, err)
				require.Len(t, mapping.Partitions["source"], 1)
				require.Equal(t, captureProgress{Stage: "import", Unit: "documents", Total: 1, Completed: 1}, progress[1])
			} else {
				require.Error(t, err)
				require.Empty(t, mapping.BySourceDocID)
				require.Equal(t, captureProgress{Stage: "import", Unit: "documents", Total: 1, Failed: 1}, progress[1])
			}
			require.Len(t, inputs, 2)
			require.Equal(t, "eval:source", inputs[0]["idempotency_key"])
			require.Equal(t, "eval:source:resubmit1", inputs[1]["idempotency_key"])
			evidence := inputs[0]["evidence"].([]any)[0].(map[string]any)
			require.NotContains(t, evidence, "source_key", "corpus intake must remain eligible for semantic reuse")
			require.NotContains(t, evidence, "source_revision")
			require.Equal(t, "source", evidence["metadata"].(map[string]any)["source_doc_id"])
			delete(inputs[0], "idempotency_key")
			delete(inputs[1], "idempotency_key")
			require.Equal(t, inputs[0], inputs[1])
		})
	}
}

func TestConcurrentImporterReportsMonotonicDocumentProgress(t *testing.T) {
	items := []CorpusItem{{SourceDocID: "first", Content: "First."}, {SourceDocID: "second", Content: "Second."}, {SourceDocID: "third", Content: "Third."}, {SourceDocID: "last", Content: "Last."}}
	lastStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			Method string
			Params struct{ Arguments map[string]any }
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&rpc))
		if rpc.Method == "tools/list" {
			tools := []mcpToolDefinition{}
			for _, tool := range registry.ContractTools() {
				tools = append(tools, mcpToolDefinition{Name: tool.Name, OutputSchema: tool.OutputSchema})
			}
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"tools": tools}}))
			return
		}
		evidence := rpc.Params.Arguments["evidence"].([]any)[0].(map[string]any)
		source := evidence["metadata"].(map[string]any)["source_doc_id"]
		if source == "first" {
			select {
			case <-lastStarted:
			case <-r.Context().Done():
				return
			}
		} else if source == "last" {
			close(lastStarted)
		}
		content := evidence["content"].(string)
		out := map[string]any{"processing_state": "completed", "submission_id": uuid.NewString(), "evidence": []any{map[string]any{
			"evidence_index": 0, "partition_index": 0, "source_item_id": uuid.NewString(), "occurrence_id": uuid.NewString(), "evidence_id": uuid.NewString(),
			"span_start": 0, "span_end": len([]rune(content)), "content_hash": knowledge.EvidenceContentHash(content), "disposition": "stored",
		}}}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"structuredContent": out}}))
	}))
	defer server.Close()
	var logs bytes.Buffer
	client := &HTTPClient{BaseURL: server.URL, APIKey: "synthetic", Client: server.Client(), Capture: captureFixture(&logs)}
	mapping, err := client.ImportCorpusWithConcurrency(context.Background(), items, 4)
	require.NoError(t, err)
	require.Len(t, mapping.BySourceDocID, len(items))
	progress := capturedProgress(t, logs.Bytes())
	require.Len(t, progress, len(items)+1)
	for i, update := range progress {
		require.Equal(t, captureProgress{Stage: "import", Unit: "documents", Total: len(items), Completed: i, Remaining: len(items) - i}, update)
	}
}

func TestPartitionReceiptValidationIsAtomicAndExact(t *testing.T) {
	item := CorpusItem{SourceDocID: "source", Content: "First.\n第二。"}
	boundary := len([]rune("First.\n"))
	entries := []any{map[string]any{"evidence_id": "first", "disposition": "stored", "evidence_index": 0, "partition_index": 0, "source_item_id": "original", "occurrence_id": "one", "span_start": 0, "span_end": boundary, "content_hash": knowledge.EvidenceContentHash("First.\n")}, map[string]any{"evidence_id": "second", "disposition": "stored", "evidence_index": 0, "partition_index": 1, "source_item_id": "original", "occurrence_id": "two", "span_start": boundary, "span_end": len([]rune(item.Content)), "content_hash": knowledge.EvidenceContentHash("第二。")}}
	sourceItem := uuid.NewString()
	for _, raw := range entries {
		entry := raw.(map[string]any)
		entry["evidence_id"] = uuid.NewString()
		entry["occurrence_id"] = uuid.NewString()
		entry["source_item_id"] = sourceItem
	}
	output := map[string]any{"processing_state": "completed", "evidence": entries}
	mapping, err := (&HTTPClient{}).importTerminalRememberResult(item, output, newKnowledgeMapping())
	require.NoError(t, err)
	require.Len(t, mapping.Partitions["source"], 2)
	for _, field := range []string{"span_start", "span_end", "content_hash", "source_item_id", "occurrence_id", "partition_index"} {
		t.Run(field, func(t *testing.T) {
			second := entries[1].(map[string]any)
			saved := second[field]
			second[field] = "invalid"
			original := newKnowledgeMapping()
			_, err := (&HTTPClient{}).importTerminalRememberResult(item, output, original)
			require.Error(t, err)
			require.Empty(t, original.BySourceDocID)
			second[field] = saved
		})
	}
}

func TestProductionCatalogIsUnchangedAndEvaluationCallsAreRetired(t *testing.T) {
	reg, err := registry.BuildActive(registry.Dependencies{})
	require.NoError(t, err)
	wanted := []string{"remember", "ingest_session", "retract_evidence", "recall_memory", "trace_memory", "submit_recall_session_feedback", "export_memory_pack"}
	require.ElementsMatch(t, wanted, registry.ContractToolNames())
	require.Len(t, reg.List(), len(wanted))
	for _, name := range []string{"eval_list_knowledge_refs", "eval_run_recall_case"} {
		_, found := reg.Get(name)
		require.False(t, found)
	}
}

func TestMCPResponseDecodingAndWireFailures(t *testing.T) {
	const key = "mcp-key-canary-9483"
	for _, item := range []struct {
		name, body, failure string
		status              int
		interrupted         bool
	}{
		{"structured", "{\"result\":{\"structuredContent\":{\"number\":9007199254740993}}}", "", 200, false},
		{"text", "{\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"{\\\"number\\\":9007199254740993}\"}]}}", "", 200, false},
		{"protocol", "{\"error\":{\"code\":-32603,\"message\":\"mcp-key-canary-9483\"}}", "protocol error -32603", 200, false},
		{"missing result", "{\"result\":{}}", "no structured result", 200, false},
		{"invalid text", "{\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"invalid\"}]}}", "invalid MCP content", 200, false},
		{"malformed JSON", "{", "invalid HTTP JSON", 200, false},
		{"trailing JSON", "{}{}", "invalid HTTP JSON", 200, false},
		{"unavailable", "{\"private\":\"mcp-key-canary-9483\"}", "status 503", 503, false},
		{"interrupted", "{\"result\":", "body interrupted", 200, true},
		{"structured rejection", "{\"result\":{\"isError\":true,\"structuredContent\":{\"errors\":[{\"message\":\"intake declined\"}]}}}", "intake declined", 200, false},
		{"bounded rejection", "{\"result\":{\"isError\":true,\"structuredContent\":{\"errors\":[{\"code\":\"permission_denied\"}]}}}", "permission_denied", 200, false},
		{"partition rejection", "{\"result\":{\"isError\":true,\"structuredContent\":{\"evidence\":[{\"errors\":[{\"message\":\"partition declined\"}]}]}}}", "partition declined", 200, false},
		{"partition code", "{\"result\":{\"isError\":true,\"structuredContent\":{\"evidence\":[{\"errors\":[{\"code\":\"partition_invalid\"}]}]}}}", "partition_invalid", 200, false},
		{"empty rejection", "{\"result\":{\"isError\":true,\"structuredContent\":{}}}", "MCP recall_memory:", 200, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer "+key, r.Header.Get("Authorization"))
				require.Equal(t, "tools/call", r.Header.Get("Mcp-Method"))
				require.Equal(t, "recall_memory", r.Header.Get("Mcp-Name"))
				if item.interrupted {
					w.Header().Set("Content-Length", "100")
				}
				w.WriteHeader(item.status)
				_, err := w.Write([]byte(item.body))
				require.NoError(t, err)
			}))
			defer server.Close()
			var logs bytes.Buffer
			capture := captureFixture(&logs)
			capture.Protector = NewRunnerCredentialProtector(key)
			require.NoError(t, capture.Emit("started", map[string]string{"model": "synthetic"}))
			client := &HTTPClient{BaseURL: server.URL, APIKey: key, Client: server.Client(), ExchangeCapture: capture}
			out := map[string]any{"stale": "prior question"}
			err := client.CallTool(context.Background(), "recall_memory", map[string]any{"query": "public question"}, &out)
			if item.failure != "" {
				require.ErrorContains(t, err, item.failure)
				require.NotContains(t, err.Error(), key)
			} else {
				require.NoError(t, err)
				require.Equal(t, json.Number("9007199254740993"), out["number"])
				require.NotContains(t, out, "stale")
			}
			require.NoError(t, capture.Emit("finished", map[string]bool{"complete": err == nil}))
			var protected strings.Builder
			for _, event := range capturedEvents(t, logs.Bytes()) {
				body, err := json.Marshal(event)
				require.NoError(t, err)
				protected.Write(body)
			}
			require.NotContains(t, protected.String(), key)
			require.Contains(t, protected.String(), "answer_wire_response")
		})
	}
	t.Run("oversized response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, err := w.Write(bytes.Repeat([]byte("x"), (16<<20)+1))
			require.NoError(t, err)
		}))
		defer server.Close()
		require.ErrorContains(t, (&HTTPClient{Client: server.Client()}).doJSON(context.Background(), http.MethodGet, server.URL, "", nil, nil), "exceeds capture bound")
	})
	t.Run("transport and empty control response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		client := &HTTPClient{Client: server.Client()}
		require.NoError(t, client.doJSON(context.Background(), http.MethodPost, server.URL, "", nil, nil))
		server.Close()
		var logs bytes.Buffer
		client.ExchangeCapture = captureFixture(&logs)
		require.ErrorContains(t, client.doJSON(context.Background(), http.MethodPost, server.URL, "", nil, nil), "dependency unavailable")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.ErrorContains(t, client.doJSON(ctx, http.MethodPost, server.URL, "", nil, nil), "context canceled")
		require.ErrorContains(t, client.doJSON(ctx, http.MethodPost, "://invalid", "", nil, nil), "invalid request configuration")
	})
}

func TestImporterReconcilesTransientFailureWithTheSameOperationKey(t *testing.T) {
	item := CorpusItem{SourceDocID: "source", SourceDataset: "synthetic", Content: "Exact source.", Labels: []string{"public"}}
	requests := make(chan map[string]any, 2)
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			Method string
			Params struct{ Arguments map[string]any }
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&rpc))
		if rpc.Method == "tools/list" {
			tools := []mcpToolDefinition{}
			for _, tool := range registry.ContractTools() {
				tools = append(tools, mcpToolDefinition{Name: tool.Name, OutputSchema: tool.OutputSchema})
			}
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"tools": tools}}))
			return
		}
		requests <- rpc.Params.Arguments
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		result := map[string]any{"processing_state": "completed", "submission_id": uuid.NewString(), "evidence": []any{map[string]any{
			"evidence_index": 0, "partition_index": 0, "source_item_id": uuid.NewString(), "occurrence_id": uuid.NewString(), "evidence_id": uuid.NewString(),
			"span_start": 0, "span_end": len([]rune(item.Content)), "content_hash": knowledge.EvidenceContentHash(item.Content), "disposition": "stored",
		}}}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"structuredContent": result}}))
	}))
	defer server.Close()
	var logs bytes.Buffer
	capture := captureFixture(&logs)
	client := &HTTPClient{BaseURL: server.URL, APIKey: "synthetic", Client: server.Client(), Capture: capture}
	mapping, err := client.ImportCorpusWithConcurrency(context.Background(), []CorpusItem{item}, 4)
	require.NoError(t, err)
	require.Len(t, mapping.Partitions["source"], 1)
	require.Equal(t, <-requests, <-requests, "transient recovery preserves exact Evidence, target and operation key")
	require.EqualValues(t, 2, attempts.Load())
}

func TestMCPContractDiscoveryRejectsIncompleteAndMixedCatalogs(t *testing.T) {
	schema := func(version string) map[string]any {
		return map[string]any{"properties": map[string]any{"contract_version": map[string]any{"enum": []any{version}}}}
	}
	for _, item := range []struct {
		name    string
		change  func([]mcpToolDefinition, int) []mcpToolDefinition
		allowed bool
	}{
		{"production", nil, true},
		{"duplicate", func(tools []mcpToolDefinition, index int) []mcpToolDefinition { return append(tools, tools[index]) }, false},
		{"missing name", func(tools []mcpToolDefinition, index int) []mcpToolDefinition { tools[index].Name = ""; return tools }, false},
		{"missing remember", func(tools []mcpToolDefinition, index int) []mcpToolDefinition {
			return append(tools[:index], tools[index+1:]...)
		}, false},
		{"missing Recall", func(tools []mcpToolDefinition, _ int) []mcpToolDefinition {
			for index, tool := range tools {
				if tool.Name == registry.ToolRecallMemory {
					return append(tools[:index], tools[index+1:]...)
				}
			}
			return tools
		}, false},
		{"missing schema", func(tools []mcpToolDefinition, index int) []mcpToolDefinition {
			tools[index].OutputSchema = nil
			return tools
		}, false},
		{"retired tool", func(tools []mcpToolDefinition, _ int) []mcpToolDefinition {
			return append(tools, mcpToolDefinition{Name: "eval_run_recall_case"})
		}, false},
		{"unsupported version", func(tools []mcpToolDefinition, index int) []mcpToolDefinition {
			tools[index].OutputSchema = schema("dense-mem.v0")
			return tools
		}, false},
		{"compatible variants", func(tools []mcpToolDefinition, index int) []mcpToolDefinition {
			tools[index].OutputSchema = map[string]any{"oneOf": []any{schema(domain.ContractVersion), map[string]any{"type": "null"}}}
			return tools
		}, true},
		{"mixed variants", func(tools []mcpToolDefinition, index int) []mcpToolDefinition {
			tools[index].OutputSchema = map[string]any{"oneOf": []any{schema(domain.ContractVersion), schema("dense-mem.v0")}}
			return tools
		}, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			tools := []mcpToolDefinition{}
			for _, tool := range registry.ContractTools() {
				tools = append(tools, mcpToolDefinition{Name: tool.Name, OutputSchema: tool.OutputSchema})
			}
			body, err := json.Marshal(tools)
			require.NoError(t, err)
			tools = nil
			require.NoError(t, json.Unmarshal(body, &tools))
			for index, tool := range tools {
				if tool.Name == registry.ToolRemember && item.change != nil {
					tools = item.change(tools, index)
					break
				}
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				require.Equal(t, "tools/list", r.Header.Get("Mcp-Method"))
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"tools": tools}}))
			}))
			defer server.Close()
			client := &HTTPClient{BaseURL: server.URL, APIKey: "synthetic", Client: server.Client()}
			_, err = client.ensureContract(context.Background())
			if item.allowed {
				require.NoError(t, err)
				_, err = client.ensureContract(context.Background())
				require.NoError(t, err)
				require.EqualValues(t, 1, calls.Load(), "validated discovery is reused")
			} else {
				require.Error(t, err)
			}
		})
	}
	_, err := (&HTTPClient{}).ensureContract(context.Background())
	require.ErrorContains(t, err, "base URL")
	_, err = (&HTTPClient{BaseURL: "http://unused.invalid"}).ensureContract(context.Background())
	require.ErrorContains(t, err, "API key")
}
