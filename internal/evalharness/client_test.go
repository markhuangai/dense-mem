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
		case "/api/v1/tools/recall_memory":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode recall body: %v", err)
			}
			_, hasCaseID := input["case_id"]
			_, hasUseCommunities := input["use_communities"]
			if input["query"] != "alpha query" || input["limit"] != float64(3) || input["valid_at"] != "2026-06-25T00:00:00Z" || hasCaseID || hasUseCommunities {
				t.Fatalf("recall input = %#v", input)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"recall_id": "rec-test",
				"results": []map[string]any{{
					"evidence_id": "evidence-alpha",
					"context":     "Alpha content",
				}},
				"discovery_paths": []map[string]any{
					{
						"relationships": []map[string]any{{
							"relationship_id": "relationship-alpha",
							"subject":         map[string]any{"name": "Alpha"},
							"predicate":       "mentions",
							"object":          map[string]any{"value": "Beta"},
						}},
						"evidence_ids": []string{"evidence-alpha"},
					},
				},
				"discovery_guidance": "keep recalling if unsure",
				"related_hypotheses": []map[string]any{{"dream_id": "dream-alpha"}},
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
	if trace.CaseID != "case-alpha" || trace.Query != "alpha query" || trace.LatencyMS < 0 || trace.ContextBlockChars != 0 {
		t.Fatalf("trace = %+v", trace)
	}
	if len(trace.RankedRefs) != 1 || trace.RankedRefs[0].ID != "evidence-alpha" || trace.RankedRefs[0].Type != "evidence" || len(trace.ContextRefs) != 0 || len(trace.ContextEvidenceRefs) != 1 {
		t.Fatalf("trace refs = %+v/%+v/%+v", trace.RankedRefs, trace.ContextRefs, trace.ContextEvidenceRefs)
	}
	if trace.InitialResponse["recall_id"] != "rec-test" {
		t.Fatalf("initial response = %#v", trace.InitialResponse)
	}
	if len(trace.FrontierRefs) != 1 || trace.FrontierRefs[0].ID != "evidence-alpha" || trace.FrontierRefs[0].Rank != 1 {
		t.Fatalf("frontier refs = %+v", trace.FrontierRefs)
	}
	if len(trace.DreamRefs) != 1 || trace.DreamRefs[0].ID != "dream-alpha" {
		t.Fatalf("dream refs = %+v", trace.DreamRefs)
	}
	if !controlPatched {
		t.Fatal("control config was not patched")
	}
}

func TestHTTPClientImportCorpusMapsV2PlacementEvidenceAndRelationship(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/tools/remember":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ingest_id": "ingest-v2",
				"status":    "queued",
			})
		case "/api/v1/tools/get_memory_placement":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"placement": map[string]any{
					"status": "completed",
					"items": []map[string]any{{
						"fragment_id": "evidence-v2",
						"relationship_outcomes": []map[string]any{{
							"relationship_id": "relationship-v2",
						}},
					}},
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	mapping, err := client.ImportCorpus(context.Background(), []CorpusItem{{
		SourceDocID: "doc-v2",
		Content:     "v2 content",
	}})

	if err != nil {
		t.Fatalf("ImportCorpus: %v", err)
	}
	if got := mapping.BySourceDocID["doc-v2"]; got.Type != "evidence" || got.ID != "evidence-v2" {
		t.Fatalf("default source mapping = %+v", got)
	}
	if got := mapping.BySourceDocIDAndType["doc-v2"]["evidence"]; len(got) != 1 || got[0].ID != "evidence-v2" {
		t.Fatalf("evidence mapping = %+v", got)
	}
	if got := mapping.BySourceDocIDAndType["doc-v2"]["relationship"]; len(got) != 1 || got[0].ID != "relationship-v2" {
		t.Fatalf("relationship mapping = %+v", got)
	}
}

func TestHTTPClientImportCorpusUsesOneProductionRememberPerEvidence(t *testing.T) {
	var rememberCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/tools/remember":
			rememberCalls++
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode remember body: %v", err)
			}
			evidence := input["evidence"].([]any)
			if len(evidence) != 1 {
				t.Fatalf("evidence count = %d; want one production call per evidence item", len(evidence))
			}
			first := evidence[0].(map[string]any)
			sourceDocID := strings.TrimPrefix(first["idempotency_key"].(string), "eval:")
			if sourceDocID != "case-1:doc-1" && sourceDocID != "case-1:doc-2" {
				t.Fatalf("remember evidence = %#v", evidence)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ingest_id": "ingest-" + sourceDocID,
				"status":    "queued",
			})
		case "/api/v1/tools/get_memory_placement":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode placement body: %v", err)
			}
			sourceDocID := strings.TrimPrefix(input["ingest_id"].(string), "ingest-")
			suffix := strings.TrimPrefix(sourceDocID, "case-1:doc-")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"placement": map[string]any{
					"status": "completed",
					"items": []map[string]any{{
						"evidence_index": 0,
						"fragment_id":    "evidence-" + suffix,
						"relationship_outcomes": []map[string]any{{
							"relationship_id": "relationship-" + suffix,
						}},
					}},
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	mapping, err := client.ImportCorpus(context.Background(), []CorpusItem{
		{SourceDocID: "case-1:doc-1", Content: "first", Metadata: map[string]any{"case_id": "case-1"}},
		{SourceDocID: "case-1:doc-2", Content: "second", Metadata: map[string]any{"case_id": "case-1"}},
	})

	if err != nil {
		t.Fatalf("ImportCorpus: %v", err)
	}
	if rememberCalls != 2 {
		t.Fatalf("remember calls = %d; want 2", rememberCalls)
	}
	if got := mapping.BySourceDocID["case-1:doc-1"]; got.Type != "evidence" || got.ID != "evidence-1" {
		t.Fatalf("doc-1 default mapping = %+v", got)
	}
	if got := mapping.BySourceDocID["case-1:doc-2"]; got.Type != "evidence" || got.ID != "evidence-2" {
		t.Fatalf("doc-2 default mapping = %+v", got)
	}
	if got := mapping.BySourceDocIDAndType["case-1:doc-1"]["relationship"]; len(got) != 1 || got[0].ID != "relationship-1" {
		t.Fatalf("doc-1 relationship mapping = %+v", got)
	}
	if got := mapping.BySourceDocIDAndType["case-1:doc-2"]["relationship"]; len(got) != 1 || got[0].ID != "relationship-2" {
		t.Fatalf("doc-2 relationship mapping = %+v", got)
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
}

func TestHTTPClientRunRecallCaseRetriesTransientStatus(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tools/recall_memory" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		calls++
		if calls == 1 {
			http.Error(w, `{"code":"SERVICE_UNAVAILABLE","message":"embedding provider unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{
				"relationship_id": "relationship-retry",
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
	if len(trace.RankedRefs) != 1 || trace.RankedRefs[0].ID != "relationship-retry" {
		t.Fatalf("trace = %+v", trace)
	}
}

func TestRecallResultRefsParseLegacyV1FragmentSourceDoc(t *testing.T) {
	response := map[string]any{
		"results": []any{
			map[string]any{
				"id": "fragment-legacy",
				"metadata": map[string]any{
					"source_doc_id": "doc-legacy",
				},
				"fragment": map[string]any{
					"id": "fragment-nested",
				},
			},
		},
	}

	ranked := recallResultRefs(response)
	if len(ranked) != 1 || ranked[0].Type != "fragment" || ranked[0].ID != "fragment-legacy" || ranked[0].SourceDocID != "doc-legacy" || ranked[0].Rank != 1 {
		t.Fatalf("ranked refs = %+v", ranked)
	}

	evidence := evidenceRefsFromInitialResponse(response)
	if len(evidence) != 1 || evidence[0].Type != "fragment" || evidence[0].ID != "fragment-legacy" || evidence[0].SourceDocID != "doc-legacy" || evidence[0].Rank != 1 {
		t.Fatalf("evidence refs = %+v", evidence)
	}
}

func TestHTTPClientImportRejectsMissingPlacementRef(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"fragment": map[string]any{}})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-1", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), "remember placement produced no relationship or evidence id") {
		t.Fatalf("ImportCorpus err = %v", err)
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

func TestHTTPClientExportKnowledgeMappingSkipsUnavailableLegacyKinds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tools/eval_list_knowledge_refs" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		http.Error(w, "tool not available (dependency missing or not yet implemented)", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	mapping, err := client.ExportKnowledgeMapping(context.Background(), 100)

	if err != nil {
		t.Fatalf("ExportKnowledgeMapping: %v", err)
	}
	if len(mapping.BySourceDocID) != 0 {
		t.Fatalf("mapping = %+v; want empty", mapping.BySourceDocID)
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
