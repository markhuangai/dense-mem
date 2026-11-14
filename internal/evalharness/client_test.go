package evalharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
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
			client := &HTTPClient{BaseURL: server.URL, APIKey: "synthetic", Client: server.Client()}
			mapping, err := client.ImportCorpus(context.Background(), []CorpusItem{item})
			if recover {
				require.NoError(t, err)
				require.Len(t, mapping.Partitions["source"], 1)
			} else {
				require.Error(t, err)
				require.Empty(t, mapping.BySourceDocID)
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
