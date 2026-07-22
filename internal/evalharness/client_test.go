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
	var controlPatched bool
	var rememberCalls int
	var placementPolls int
	var exportCursors []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content-type = %q; want application/json", r.Header.Get("Content-Type"))
		}
		switch r.URL.Path {
		case "/control/api/config/evaluation":
			if r.Method != http.MethodPatch || r.Header.Get("Authorization") != "Bearer control-token" {
				t.Fatalf("control request = %s %s auth %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
			}
			var body map[string][]map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode control body: %v", err)
			}
			if len(body["items"]) != 2 || body["items"][1]["value"] != "100" {
				t.Fatalf("control body = %#v", body)
			}
			controlPatched = true
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "/api/v1/tools/remember":
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
			rememberCalls++
			evidence := input["evidence"].([]any)
			firstEvidence := evidence[0].(map[string]any)
			metadata := firstEvidence["metadata"].(map[string]any)
			if firstEvidence["idempotency_key"] != "eval:doc-alpha" || metadata["source_doc_id"] != "doc-alpha" || metadata["eval_seed"] != true {
				t.Fatalf("remember input = %#v", input)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ingest_id": "ingest-alpha",
				"status":    "queued",
				"evidence":  []map[string]any{{"id": "fragment-alpha"}},
			})
		case "/api/v1/tools/get_memory_placement":
			placementPolls++
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode placement body: %v", err)
			}
			if input["ingest_id"] != "ingest-alpha" {
				t.Fatalf("placement input = %#v", input)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"placement": map[string]any{"status": "completed"},
			})
		case "/api/v1/tools/eval_list_knowledge_refs":
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
		case "/api/v1/tools/eval_run_recall_case":
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
		BaseURL:      server.URL + "/",
		APIKey:       "api-key",
		ControlURL:   server.URL + "/",
		ControlToken: "control-token",
		Client:       server.Client(),
	}

	if err := client.EnableEvaluationMode(context.Background(), 0); err != nil {
		t.Fatalf("EnableEvaluationMode: %v", err)
	}
	mapping, err := client.ImportCorpus(context.Background(), []CorpusItem{{
		SourceDocID:   "doc-alpha",
		Title:         "Alpha",
		Content:       "Alpha content",
		SourceDataset: "fixture",
		Metadata:      map[string]any{"topic": "letters"},
	}})
	if err != nil {
		t.Fatalf("ImportCorpus: %v", err)
	}
	if mapping.BySourceDocID["doc-alpha"].ID != "fragment-alpha" || rememberCalls != 1 || placementPolls != 1 {
		t.Fatalf("import mapping/calls/polls = %+v/%d/%d", mapping, rememberCalls, placementPolls)
	}

	exported, err := client.ExportFragmentMapping(context.Background(), 1)
	if err != nil {
		t.Fatalf("ExportFragmentMapping: %v", err)
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
	if !controlPatched {
		t.Fatal("control config was not patched")
	}
}

func TestHTTPClientV2KnowledgeMapping(t *testing.T) {
	var requestedTypes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tools/eval_list_knowledge_refs" {
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
	mapping, err := client.ExportV2KnowledgeMapping(context.Background(), 50)
	if err != nil {
		t.Fatalf("ExportV2KnowledgeMapping: %v", err)
	}
	if strings.Join(requestedTypes, ",") != "evidence,entity,value,relationship,hypothesis" {
		t.Fatalf("requested types = %v", requestedTypes)
	}
	alphaByType := mapping.BySourceDocIDAndType["doc-alpha"]
	if alphaByType["evidence"][0].ID != "evidence-alpha" ||
		alphaByType["entity"][0].ID != "entity-alpha" ||
		alphaByType["relationship"][0].ID != "relationship-alpha" ||
		alphaByType["hypothesis"][0].ID != "hypothesis-alpha" {
		t.Fatalf("doc-alpha V2 mapping = %+v", alphaByType)
	}
	if mapping.BySourceDocIDAndType["doc-beta"]["value"][0].ID != "value-beta" {
		t.Fatalf("doc-beta V2 mapping = %+v", mapping.BySourceDocIDAndType["doc-beta"])
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusTeapot)
	}))
	defer server.Close()

	client = &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	err := client.CallTool(context.Background(), "remember", map[string]any{}, nil)
	if err == nil || err.Error() != "POST "+server.URL+"/api/v1/tools/remember returned 418: nope" {
		t.Fatalf("status error = %v", err)
	}
	client.ToolTransport = "smtp"
	if err := client.CallTool(context.Background(), "remember", map[string]any{}, nil); err == nil || !strings.Contains(err.Error(), "unsupported tool transport") {
		t.Fatalf("unsupported transport err = %v", err)
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

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", ToolTransport: "mcp", Client: server.Client()}
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
			client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", ToolTransport: "mcp", Client: server.Client()}
			var out map[string]any
			err := client.CallTool(context.Background(), "remember", map[string]any{}, &out)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CallTool err = %v; want %q", err, tc.want)
			}
		})
	}

	client := &HTTPClient{BaseURL: "http://example.test", APIKey: "api-key", ToolTransport: "mcp"}
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
	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", ToolTransport: "mcp", Client: server.Client()}
	if err := client.CallTool(context.Background(), "eval_run_dream_cycle", struct{}{}, nil); err != nil {
		t.Fatalf("CallTool without output: %v", err)
	}
}

func TestHTTPClientRunRecallCaseRetriesTransientStatus(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tools/eval_run_recall_case" {
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

func TestHTTPClientImportRejectsMissingFragmentID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"fragment": map[string]any{}})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-1", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), "remember response missing fragment or evidence id") {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}

func TestHTTPClientImportCorpusMapsV2PlacementEvidenceID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/tools/remember":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ingest_id":        "ingest-v2",
				"processing_state": "queued",
			})
		case "/api/v1/tools/get_memory_placement":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ingest_id":        "ingest-v2",
				"processing_state": "completed",
				"items": []map[string]any{{
					"evidence_id": "evidence-v2",
				}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	mapping, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-v2", Content: "content"}})
	if err != nil {
		t.Fatalf("ImportCorpus: %v", err)
	}
	ref, ok := mapping.BySourceDocID["doc-v2"]
	if !ok || ref.Type != "evidence" || ref.ID != "evidence-v2" {
		t.Fatalf("default source mapping = %+v, %v", ref, ok)
	}
	if len(mapping.BySourceDocIDAndType["doc-v2"]["fragment"]) != 0 {
		t.Fatalf("fragment mappings = %+v", mapping.BySourceDocIDAndType["doc-v2"]["fragment"])
	}
	if refs := mapping.BySourceDocIDAndType["doc-v2"]["evidence"]; len(refs) != 1 || refs[0].ID != "evidence-v2" {
		t.Fatalf("evidence mappings = %+v", refs)
	}
}

func TestHTTPClientImportCorpusReportsPlacementFailureCause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/tools/remember":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ingest_id": "ingest-failed",
				"evidence":  []map[string]any{{"id": "fragment-failed"}},
			})
		case "/api/v1/tools/get_memory_placement":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"placement": map[string]any{
					"status": "failed",
					"error":  "verifier unavailable",
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-failed", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), "memory placement ingest-failed failed: verifier unavailable") {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}

func TestHTTPClientImportCorpusReportsV2PlacementFailureCause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/tools/remember":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ingest_id":        "ingest-v2-failed",
				"processing_state": "queued",
			})
		case "/api/v1/tools/get_memory_placement":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ingest_id":        "ingest-v2-failed",
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
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-v2-failed", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), "memory placement ingest-v2-failed failed: verifier unavailable") {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}

func TestHTTPClientImportCorpusHonorsPlacementTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/tools/remember":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ingest_id": "ingest-slow",
				"evidence":  []map[string]any{{"id": "fragment-slow"}},
			})
		case "/api/v1/tools/get_memory_placement":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"placement": map[string]any{"status": "processing"},
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
	if err == nil || !strings.Contains(err.Error(), "memory placement ingest-slow did not complete within "+timeout.String()) {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}

func TestHTTPClientImportCorpusWithConcurrency(t *testing.T) {
	var active int32
	var maxActive int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		evidence := input["evidence"].([]any)[0].(map[string]any)
		sourceDocID := strings.TrimPrefix(evidence["idempotency_key"].(string), "eval:")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"fragment": map[string]any{"id": "fragment-" + sourceDocID},
		})
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		evidence := input["evidence"].([]any)[0].(map[string]any)
		sourceDocID := strings.TrimPrefix(evidence["idempotency_key"].(string), "eval:")
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
		_ = json.NewEncoder(w).Encode(map[string]any{
			"fragment": map[string]any{"id": "fragment-doc-2"},
		})
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

func TestHTTPClientExportKnowledgeMappingIncludesClaimsAndFacts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tools/eval_list_knowledge_refs" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode eval_list_knowledge_refs body: %v", err)
		}
		switch input["type"] {
		case "fragment":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
				"fragment_id": "fragment-tiered",
				"metadata":    map[string]any{"source_doc_id": "doc-tiered"},
			}}})
		case "claim":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
				"claim_id":     "claim-tiered",
				"supported_by": []string{"fragment-tiered"},
			}}})
		case "fact":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
				"fact_id":                "fact-tiered",
				"promoted_from_claim_id": "claim-tiered",
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
	if mapping.BySourceDocID["doc-tiered"].ID != "fragment-tiered" {
		t.Fatalf("default mapping = %+v", mapping.BySourceDocID["doc-tiered"])
	}
	if mapping.BySourceDocIDAndType["doc-tiered"]["claim"][0].ID != "claim-tiered" {
		t.Fatalf("claim mapping = %+v", mapping.BySourceDocIDAndType)
	}
	if mapping.BySourceDocIDAndType["doc-tiered"]["fact"][0].ID != "fact-tiered" {
		t.Fatalf("fact mapping = %+v", mapping.BySourceDocIDAndType)
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
