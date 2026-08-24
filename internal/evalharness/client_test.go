package evalharness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPClientEvaluationFlow(t *testing.T) {
	var rememberCalls int
	var statusPolls int
	var exportCursors []string
	flatRelationships := []any{map[string]any{
		"ref": "relationship_1",
		"subject": map[string]any{
			"name": "Alpha", "entity_kind": "other",
		},
		"predicate": map[string]any{
			"proposed_key": "uses",
		},
		"object": map[string]any{"entity": map[string]any{
			"name": "Beta", "entity_kind": "other",
		}},
		"polarity": "+", "evidence_indices": []any{0},
	}}

	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content-type = %q; want application/json", r.Header.Get("Content-Type"))
		}
		switch r.URL.Path {
		case "tool:remember":
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer api-key" {
				t.Fatalf("remember request = %s auth %q", r.Method, r.Header.Get("Authorization"))
			}
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode remember body: %v", err)
			}
			if _, ok := input["claims"]; ok {
				t.Fatalf("remember input contains claims: %#v", input)
			}
			if _, ok := input["auto_promote"]; ok {
				t.Fatalf("remember input contains auto_promote: %#v", input)
			}
			if input["idempotency_key"] != "eval:doc-alpha" {
				t.Fatalf("remember idempotency_key = %#v", input["idempotency_key"])
			}
			relationships, ok := input["relationships"].([]any)
			if !ok || len(relationships) != 1 {
				t.Fatalf("remember relationships = %#v", input["relationships"])
			}
			relationship := relationships[0].(map[string]any)
			predicate := relationship["predicate"].(map[string]any)
			if relationship["ref"] != "relationship_1" || predicate["proposed_key"] != "uses" {
				t.Fatalf("remember relationship = %#v", relationship)
			}
			rememberCalls++
			evidence := input["evidence"].([]any)
			firstEvidence := evidence[0].(map[string]any)
			metadata := firstEvidence["metadata"].(map[string]any)
			if _, exists := firstEvidence["idempotency_key"]; exists ||
				metadata["source_doc_id"] != "doc-alpha" ||
				metadata["eval_seed"] != true {
				t.Fatalf("remember input = %#v", input)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"submission_id":    "submission-alpha",
				"processing_state": "queued",
				"status_tool":      "get_submission_status",
			})
		case "tool:get_submission_status":
			statusPolls++
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode submission status body: %v", err)
			}
			if input["submission_id"] != "submission-alpha" {
				t.Fatalf("status input = %#v", input)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"submission_id":    "submission-alpha",
				"processing_state": "completed",
				"search_state":     "current",
				"evidence":         []map[string]any{{"evidence_id": "fragment-alpha", "search_state": "current"}},
			})
		case "tool:eval_list_knowledge_refs":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode export body: %v", err)
			}
			cursor, _ := input["cursor"].(string)
			exportCursors = append(exportCursors, cursor)
			if cursor == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"items": []map[string]any{{
						"id":       "fragment-alpha",
						"metadata": map[string]any{"source_doc_id": "doc-alpha"},
					}},
					"next_cursor": "next",
					"has_more":    true,
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{
					"id":       "fragment-beta",
					"metadata": map[string]any{"source_doc_id": "doc-beta"},
				}},
				"next_cursor": "",
				"has_more":    false,
			})
		case "tool:eval_run_recall_case":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode recall body: %v", err)
			}
			if input["case_id"] != "case-alpha" || input["limit"] != float64(3) || input["valid_at"] != "2026-06-25T00:00:00Z" || input["use_communities"] != true {
				t.Fatalf("recall input = %#v", input)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"query": "alpha query",
				"ranked_refs": []map[string]any{{
					"type": "fragment",
					"id":   "fragment-alpha",
					"rank": 1,
				}},
				"context_refs": []map[string]any{{
					"type": "fragment",
					"id":   "fragment-alpha",
					"rank": 1,
				}},
				"context_evidence_refs": []map[string]any{{
					"type": "fragment",
					"id":   "fragment-evidence",
					"rank": 1,
				}},
				"latency_ms":          42,
				"context_block_chars": 128,
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{
		BaseURL: server.URL + "/",
		APIKey:  "api-key",
		Client:  server.Client(),
	}

	mapping, err := client.ImportCorpus(context.Background(), []CorpusItem{{
		SourceDocID:   "doc-alpha",
		Title:         "Alpha",
		Content:       "Alpha uses Beta.",
		SourceDataset: "fixture",
		Metadata:      map[string]any{"topic": "letters"},
		Relationships: flatRelationships,
	}})
	if err != nil {
		t.Fatalf("ImportCorpus: %v", err)
	}
	if mapping.BySourceDocID["doc-alpha"].ID != "fragment-alpha" || rememberCalls != 1 || statusPolls != 1 {
		t.Fatalf("import mapping/calls/polls = %+v/%d/%d", mapping, rememberCalls, statusPolls)
	}

	exported, err := client.ExportEvidenceMapping(context.Background(), 1)
	if err != nil {
		t.Fatalf("ExportEvidenceMapping: %v", err)
	}
	if exported.BySourceDocID["doc-beta"].ID != "fragment-beta" || len(exportCursors) != 2 || exportCursors[1] != "next" {
		t.Fatalf("export mapping/cursors = %+v/%v", exported, exportCursors)
	}

	trace, err := client.RunRecallCase(context.Background(), Case{
		CaseID:         "case-alpha",
		Query:          "alpha query",
		Limit:          3,
		ValidAt:        "2026-06-25T00:00:00Z",
		UseCommunities: true,
	})
	if err != nil {
		t.Fatalf("RunRecallCase: %v", err)
	}
	if trace.CaseID != "case-alpha" || trace.Query != "alpha query" || trace.LatencyMS != 42 || trace.ContextBlockChars != 128 {
		t.Fatalf("trace = %+v", trace)
	}
	if len(trace.RankedRefs) != 1 || trace.RankedRefs[0].ID != "fragment-alpha" || len(trace.ContextRefs) != 1 || len(trace.ContextEvidenceRefs) != 1 {
		t.Fatalf("trace refs = %+v/%+v/%+v", trace.RankedRefs, trace.ContextRefs, trace.ContextEvidenceRefs)
	}
}

func TestHTTPClientKnowledgeMapping(t *testing.T) {
	var requestedTypes []string
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "tool:eval_list_knowledge_refs" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode export body: %v", err)
		}
		kind := stringValue(input["type"])
		requestedTypes = append(requestedTypes, kind)
		var items []map[string]any
		switch kind {
		case "evidence":
			items = []map[string]any{{
				"id":       "evidence-alpha",
				"metadata": map[string]any{"source_doc_id": "doc-alpha"},
			}}
		case "entity":
			items = []map[string]any{{
				"id":       "entity-alpha",
				"metadata": map[string]any{"source_doc_id": "doc-alpha"},
			}}
		case "value":
			items = []map[string]any{{
				"id":       "value-beta",
				"metadata": map[string]any{"source_doc_id": "doc-beta"},
			}}
		case "relationship":
			items = []map[string]any{{
				"id":       "relationship-alpha",
				"metadata": map[string]any{"source_doc_id": "doc-alpha"},
			}}
		case "hypothesis":
			items = []map[string]any{{
				"id": "hypothesis-alpha",
				"payload": map[string]any{
					"source_refs": []any{map[string]any{"type": "evidence", "id": "evidence-alpha"}},
				},
			}}
		default:
			t.Fatalf("unexpected export type %q", kind)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items":       items,
			"next_cursor": "",
			"has_more":    false,
		})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	mapping, err := client.ExportKnowledgeMapping(context.Background(), 50)
	if err != nil {
		t.Fatalf("ExportKnowledgeMapping: %v", err)
	}
	if strings.Join(requestedTypes, ",") != "evidence,entity,value,relationship,hypothesis" {
		t.Fatalf("requested types = %v", requestedTypes)
	}
	alphaByType := mapping.BySourceDocIDAndType["doc-alpha"]
	if alphaByType["evidence"][0].ID != "evidence-alpha" ||
		alphaByType["entity"][0].ID != "entity-alpha" ||
		alphaByType["relationship"][0].ID != "relationship-alpha" ||
		alphaByType["hypothesis"][0].ID != "hypothesis-alpha" {
		t.Fatalf("doc-alpha mapping = %+v", alphaByType)
	}
	if mapping.BySourceDocIDAndType["doc-beta"]["value"][0].ID != "value-beta" {
		t.Fatalf("doc-beta mapping = %+v", mapping.BySourceDocIDAndType["doc-beta"])
	}
}

func TestHTTPClientKnowledgeMappingPreservesLegacyMetadataSourceDocID(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "tool:eval_list_knowledge_refs" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode export body: %v", err)
		}
		items := []map[string]any{}
		if stringValue(input["type"]) == "evidence" {
			items = []map[string]any{{
				"id": "evidence-legacy",
				"metadata": map[string]any{
					"legacy_metadata": map[string]any{"source_doc_id": "doc-legacy"},
				},
			}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items":       items,
			"next_cursor": "",
			"has_more":    false,
		})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	mapping, err := client.ExportKnowledgeMapping(context.Background(), 50)
	if err != nil {
		t.Fatalf("ExportKnowledgeMapping: %v", err)
	}
	if mapping.BySourceDocID["doc-legacy"].ID != "evidence-legacy" {
		t.Fatalf("legacy metadata mapping = %+v", mapping.BySourceDocID["doc-legacy"])
	}
}

func TestHTTPClientErrors(t *testing.T) {
	client := &HTTPClient{}
	if err := client.CallTool(context.Background(), "remember", map[string]any{}, nil); err == nil || err.Error() != "base URL is required" {
		t.Fatalf("missing base URL err = %v", err)
	}
	client.BaseURL = "http://example.test"
	if err := client.CallTool(context.Background(), "remember", map[string]any{}, nil); err == nil || err.Error() != "API key is required" {
		t.Fatalf("missing API key err = %v", err)
	}

	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusTeapot)
	}))
	defer server.Close()

	client = &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	err := client.CallTool(context.Background(), "remember", map[string]any{}, nil)
	if err == nil || err.Error() != "POST "+server.URL+"/mcp returned 418: nope" {
		t.Fatalf("status error = %v", err)
	}
}

func TestHTTPClientCallsMCPToolsCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			t.Fatalf("path = %q, want /mcp", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer api-key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		var request struct {
			JSONRPC string `json:"jsonrpc"`
			Method  string `json:"method"`
			Params  struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		if request.JSONRPC != "2.0" || request.Method != "tools/call" || request.Params.Name != "eval_run_recall_case" {
			t.Fatalf("MCP request = %+v", request)
		}
		if request.Params.Arguments["case_id"] != "case-1" {
			t.Fatalf("MCP arguments = %#v", request.Params.Arguments)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"content": []map[string]any{{
					"type": "text",
					"text": `{"query":"question","ranked_refs":[{"type":"fragment","id":"fragment-1","rank":1}]}`,
				}},
			},
		})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	trace, err := client.RunRecallCase(context.Background(), Case{CaseID: "case-1", Query: "question"})
	if err != nil {
		t.Fatalf("RunRecallCase through MCP: %v", err)
	}
	if len(trace.RankedRefs) != 1 || trace.RankedRefs[0].ID != "fragment-1" {
		t.Fatalf("trace = %+v", trace)
	}
}

func TestHTTPClientRejectsInvalidMCPResponses(t *testing.T) {
	tests := []struct {
		name     string
		response map[string]any
		want     string
	}{
		{
			name: "RPC error",
			response: map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"error":   map[string]any{"code": -32602, "message": "invalid params"},
			},
			want: "returned -32602: invalid params",
		},
		{
			name: "missing text",
			response: map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result":  map[string]any{"content": []map[string]any{{"type": "image"}}},
			},
			want: "returned no JSON text content",
		},
		{
			name: "malformed text",
			response: map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": "{"}}},
			},
			want: "decode mcp tools/call",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(tc.response)
			}))
			defer server.Close()
			client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
			var out map[string]any
			err := client.CallTool(context.Background(), "remember", map[string]any{}, &out)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CallTool err = %v; want %q", err, tc.want)
			}
		})
	}

	client := &HTTPClient{BaseURL: "http://example.test", APIKey: "api-key"}
	if err := client.CallTool(context.Background(), "remember", "not-an-object", nil); err == nil || !strings.Contains(err.Error(), "arguments must be an object") {
		t.Fatalf("non-object MCP arguments err = %v", err)
	}
}

func TestHTTPClientMCPCallWithoutOutputAcceptsEmptyResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]any{},
		})
	}))
	defer server.Close()
	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	if err := client.CallTool(context.Background(), "eval_run_dream_cycle", struct{}{}, nil); err != nil {
		t.Fatalf("CallTool without output: %v", err)
	}
}

func TestHTTPClientRunRecallCaseRetriesTransientStatus(t *testing.T) {
	var calls int
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "tool:eval_run_recall_case" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		calls++
		if calls == 1 {
			http.Error(w, `{"code":"SERVICE_UNAVAILABLE","message":"embedding provider unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"query": "retry query",
			"ranked_refs": []map[string]any{{
				"type": "fragment",
				"id":   "fragment-retry",
				"rank": 1,
			}},
		})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	trace, err := client.RunRecallCase(context.Background(), Case{CaseID: "case-retry", Query: "retry query"})
	if err != nil {
		t.Fatalf("RunRecallCase retry: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d; want 2", calls)
	}
	if len(trace.RankedRefs) != 1 || trace.RankedRefs[0].ID != "fragment-retry" {
		t.Fatalf("trace = %+v", trace)
	}
}

func TestHTTPClientImportRejectsMissingSubmissionID(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-1", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), "remember response missing submission_id") {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}

func TestHTTPClientImportCorpusMapsSubmissionEvidenceID(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "tool:remember":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"submission_id":    "submission-canonical",
				"processing_state": "queued",
			})
		case "tool:get_submission_status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"submission_id":    "submission-canonical",
				"processing_state": "completed",
				"evidence": []map[string]any{{
					"evidence_id": "evidence-canonical",
				}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	mapping, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-canonical", Content: "content"}})
	if err != nil {
		t.Fatalf("ImportCorpus: %v", err)
	}
	ref, ok := mapping.BySourceDocID["doc-canonical"]
	if !ok || ref.Type != "fragment" || ref.ID != "evidence-canonical" {
		t.Fatalf("default source mapping = %+v, %v", ref, ok)
	}
	if refs := mapping.BySourceDocIDAndType["doc-canonical"]["fragment"]; len(refs) != 1 || refs[0].ID != "evidence-canonical" {
		t.Fatalf("fragment mappings = %+v", refs)
	}
}

func TestHTTPClientImportCorpusReportsSubmissionErrorCause(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "tool:remember":
			_ = json.NewEncoder(w).Encode(map[string]any{"submission_id": "submission-failed"})
		case "tool:get_submission_status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"submission_id":    "submission-failed",
				"processing_state": "failed",
				"errors":           []map[string]any{{"message": "verifier unavailable"}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-failed", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), "submission submission-failed failed: verifier unavailable") {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}

func TestHTTPClientImportCorpusAllowsCompletedSubmission(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "tool:remember":
			_ = json.NewEncoder(w).Encode(map[string]any{"submission_id": "submission-review"})
		case "tool:get_submission_status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"submission_id":    "submission-review",
				"processing_state": "completed",
				"evidence": []map[string]any{{
					"evidence_id":  "evidence-review",
					"search_state": "current",
				}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	mapping, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-review", Content: "content"}})
	if err != nil {
		t.Fatalf("ImportCorpus: %v", err)
	}
	ref, ok := mapping.BySourceDocID["doc-review"]
	if !ok || ref.Type != "fragment" || ref.ID != "evidence-review" {
		t.Fatalf("source mapping = %+v, %v", ref, ok)
	}
	if refs := mapping.BySourceDocIDAndType["doc-review"]["fragment"]; len(refs) != 1 || refs[0].ID != "evidence-review" {
		t.Fatalf("fragment mappings = %+v", refs)
	}
}

func TestHTTPClientImportCorpusReportsSubmissionFailureCause(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "tool:remember":
			_ = json.NewEncoder(w).Encode(map[string]any{"submission_id": "submission-canonical-failed", "processing_state": "queued"})
		case "tool:get_submission_status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"submission_id":    "submission-canonical-failed",
				"processing_state": "failed",
				"errors": []map[string]any{{
					"message": "verifier unavailable",
				}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-canonical-failed", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), "submission submission-canonical-failed failed: verifier unavailable") {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}

func TestHTTPClientWaitForSubmissionStatusResultWaitsForSearchProjection(t *testing.T) {
	var calls int32
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "tool:get_submission_status" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		call := atomic.AddInt32(&calls, 1)
		searchState := "pending"
		if call > 1 {
			searchState = "current"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"submission_id":    "submission-search",
			"processing_state": "completed",
			"search_state":     searchState,
		})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	out, err := client.WaitForSubmissionStatusResult(context.Background(), "submission-search", 2*time.Second)
	if err != nil {
		t.Fatalf("WaitForSubmissionStatusResult: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 || out["search_state"] != "current" {
		t.Fatalf("calls/out = %d/%#v", calls, out)
	}
}

func TestHTTPClientWaitForSubmissionStatusResultReportsSearchFailure(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "tool:get_submission_status" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"submission_id":    "submission-search-failed",
			"processing_state": "completed",
			"evidence": []map[string]any{{
				"evidence_id":  "evidence-canonical",
				"search_state": "failed",
				"errors":       []map[string]any{{"message": "embedding failed"}},
			}},
		})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.WaitForSubmissionStatusResult(context.Background(), "submission-search-failed", time.Second)
	if err == nil || !strings.Contains(err.Error(), "submission submission-search-failed search_state failed: embedding failed") {
		t.Fatalf("WaitForSubmissionStatusResult err = %v", err)
	}
}

func TestHTTPClientWaitForSubmissionStatusResultRejectsUnknownProcessingState(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "tool:get_submission_status" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"submission_id":    "submission-unknown-state",
			"processing_state": "mystery",
		})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.WaitForSubmissionStatusResult(context.Background(), "submission-unknown-state", time.Second)
	if err == nil || !strings.Contains(err.Error(), `unknown processing_state "mystery"`) {
		t.Fatalf("WaitForSubmissionStatusResult err = %v", err)
	}
}

func TestHTTPClientImportCorpusHonorsSubmissionStatusTimeout(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "tool:remember":
			_ = json.NewEncoder(w).Encode(map[string]any{"submission_id": "submission-slow"})
		case "tool:get_submission_status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"submission_id":    "submission-slow",
				"processing_state": "processing",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	timeout := 10 * time.Millisecond
	client := &HTTPClient{
		BaseURL:          server.URL,
		APIKey:           "api-key",
		PlacementTimeout: timeout,
		Client:           server.Client(),
	}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-slow", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), "submission submission-slow did not complete within "+timeout.String()) {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}

func TestHTTPClientImportCorpusWithConcurrency(t *testing.T) {
	var active int32
	var maxActive int32
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "tool:get_submission_status" {
			var statusInput map[string]any
			if err := json.NewDecoder(r.Body).Decode(&statusInput); err != nil {
				t.Fatalf("decode status body: %v", err)
			}
			submissionID := statusInput["submission_id"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"submission_id":    submissionID,
				"processing_state": "completed",
				"search_state":     "current",
				"evidence":         []map[string]any{{"evidence_id": "fragment-" + strings.TrimPrefix(submissionID, "submission-")}},
			})
			return
		}
		if r.URL.Path != "tool:remember" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		current := atomic.AddInt32(&active, 1)
		for {
			seen := atomic.LoadInt32(&maxActive)
			if current <= seen || atomic.CompareAndSwapInt32(&maxActive, seen, current) {
				break
			}
		}
		defer atomic.AddInt32(&active, -1)
		time.Sleep(25 * time.Millisecond)

		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		sourceDocID := strings.TrimPrefix(input["idempotency_key"].(string), "eval:")
		_ = json.NewEncoder(w).Encode(map[string]any{"submission_id": "submission-" + sourceDocID})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	mapping, err := client.ImportCorpusWithConcurrency(context.Background(), []CorpusItem{
		{SourceDocID: "doc-1", Content: "content 1"},
		{SourceDocID: "doc-2", Content: "content 2"},
		{SourceDocID: "doc-3", Content: "content 3"},
		{SourceDocID: "doc-4", Content: "content 4"},
	}, 2)

	if err != nil {
		t.Fatalf("ImportCorpusWithConcurrency: %v", err)
	}
	if atomic.LoadInt32(&maxActive) < 2 {
		t.Fatalf("max concurrent imports = %d, want at least 2", maxActive)
	}
	for i := 1; i <= 4; i++ {
		sourceDocID := fmt.Sprintf("doc-%d", i)
		if mapping.BySourceDocID[sourceDocID].ID != "fragment-"+sourceDocID {
			t.Fatalf("mapping[%s] = %+v", sourceDocID, mapping.BySourceDocID[sourceDocID])
		}
	}
}

func TestHTTPClientConcurrentFileImportDrainsActiveRequestsAfterError(t *testing.T) {
	startedTwo := make(chan struct{})
	docTwoCompleted := make(chan struct{})
	var started int32
	var docTwoCanceled int32
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "tool:get_submission_status" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"processing_state": "completed",
				"search_state":     "current",
				"evidence":         []map[string]any{{"evidence_id": "fragment-doc-2"}},
			})
			return
		}
		if r.URL.Path != "tool:remember" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		sourceDocID := strings.TrimPrefix(input["idempotency_key"].(string), "eval:")
		if atomic.AddInt32(&started, 1) == 2 {
			close(startedTwo)
		}
		select {
		case <-startedTwo:
		case <-time.After(time.Second):
			t.Fatal("both import requests did not start")
		}

		if sourceDocID == "doc-1" {
			http.Error(w, "invalid evidence", http.StatusBadRequest)
			return
		}
		select {
		case <-r.Context().Done():
			atomic.StoreInt32(&docTwoCanceled, 1)
			return
		case <-time.After(40 * time.Millisecond):
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"submission_id": "submission-doc-2"})
		close(docTwoCompleted)
	}))
	defer server.Close()

	corpusPath := filepath.Join(t.TempDir(), "corpus.jsonl")
	if err := writeJSONL(corpusPath, []CorpusItem{
		{SourceDocID: "doc-1", Content: "bad content"},
		{SourceDocID: "doc-2", Content: "valid content"},
	}); err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	mapping, _, err := client.ImportCorpusFileWithConcurrency(context.Background(), corpusPath, 2, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid evidence") {
		t.Fatalf("ImportCorpusFileWithConcurrency err = %v", err)
	}
	select {
	case <-docTwoCompleted:
	default:
		t.Fatal("active doc-2 request did not finish")
	}
	if atomic.LoadInt32(&docTwoCanceled) != 0 {
		t.Fatal("active doc-2 request was canceled after doc-1 failed")
	}
	if mapping.BySourceDocID["doc-2"].ID != "fragment-doc-2" {
		t.Fatalf("doc-2 mapping = %+v", mapping.BySourceDocID["doc-2"])
	}
}

func TestHTTPClientExportKnowledgeMappingIncludesCanonicalRefs(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "tool:eval_list_knowledge_refs" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode eval_list_knowledge_refs body: %v", err)
		}
		switch input["type"] {
		case "evidence":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
				"id":       "evidence-tiered",
				"metadata": map[string]any{"source_doc_id": "doc-tiered"},
			}}})
		case "entity":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
				"id":       "entity-tiered",
				"metadata": map[string]any{"source_doc_id": "doc-tiered"},
			}}})
		case "value":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
				"id":       "value-other",
				"metadata": map[string]any{"source_doc_id": "doc-other"},
			}}})
		case "relationship":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
				"id":       "relationship-tiered",
				"metadata": map[string]any{"source_doc_id": "doc-tiered"},
			}}})
		case "hypothesis":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
				"id":          "hypothesis-tiered",
				"source_refs": []map[string]any{{"type": "evidence", "id": "evidence-tiered"}},
			}}})
		default:
			t.Fatalf("unexpected export type %v", input["type"])
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	mapping, err := client.ExportKnowledgeMapping(context.Background(), 100)

	if err != nil {
		t.Fatalf("ExportKnowledgeMapping: %v", err)
	}
	if mapping.BySourceDocID["doc-tiered"].ID != "evidence-tiered" {
		t.Fatalf("default mapping = %+v", mapping.BySourceDocID["doc-tiered"])
	}
	if mapping.BySourceDocIDAndType["doc-tiered"]["entity"][0].ID != "entity-tiered" {
		t.Fatalf("entity mapping = %+v", mapping.BySourceDocIDAndType)
	}
	if mapping.BySourceDocIDAndType["doc-tiered"]["relationship"][0].ID != "relationship-tiered" {
		t.Fatalf("relationship mapping = %+v", mapping.BySourceDocIDAndType)
	}
	if mapping.BySourceDocIDAndType["doc-tiered"]["hypothesis"][0].ID != "hypothesis-tiered" {
		t.Fatalf("hypothesis mapping = %+v", mapping.BySourceDocIDAndType)
	}
	if mapping.BySourceDocIDAndType["doc-other"]["value"][0].ID != "value-other" {
		t.Fatalf("value mapping = %+v", mapping.BySourceDocIDAndType)
	}
}

func TestHTTPClientDecodeHelpers(t *testing.T) {
	if got := refsFromAny("not-array"); got != nil {
		t.Fatalf("refsFromAny non-array = %#v; want nil", got)
	}
	refs := refsFromAny([]any{"skip", map[string]any{
		"type": " fragment ",
		"id":   " fragment-1 ",
		"rank": json.Number("4"),
	}})
	if len(refs) != 1 || refs[0].Type != "fragment" || refs[0].ID != "fragment-1" || refs[0].Rank != 4 {
		t.Fatalf("refsFromAny = %+v", refs)
	}
	if stringValue(json.Number("9")) != "9" || stringValue(9) != "" {
		t.Fatalf("stringValue json/default branches failed")
	}
	for _, tc := range []struct {
		value any
		want  int
	}{
		{value: int(1), want: 1},
		{value: int64(2), want: 2},
		{value: float64(3), want: 3},
		{value: json.Number("4"), want: 4},
		{value: "x", want: 0},
	} {
		if got := intValue(tc.value); got != tc.want {
			t.Fatalf("intValue(%#v) = %d; want %d", tc.value, got, tc.want)
		}
	}
	for _, tc := range []struct {
		value any
		want  int64
	}{
		{value: int64(5), want: 5},
		{value: int(6), want: 6},
		{value: float64(7), want: 7},
		{value: json.Number("8"), want: 8},
		{value: "x", want: 0},
	} {
		if got := int64Value(tc.value); got != tc.want {
			t.Fatalf("int64Value(%#v) = %d; want %d", tc.value, got, tc.want)
		}
	}
	if firstNonEmpty(" ", "\t", "value") != "value" {
		t.Fatalf("firstNonEmpty did not skip blank values")
	}
}
