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
			rememberCalls++
			evidence := input["evidence"].([]any)
			firstEvidence := evidence[0].(map[string]any)
			metadata := firstEvidence["metadata"].(map[string]any)
			if firstEvidence["idempotency_key"] != "eval:doc-alpha" || metadata["source_doc_id"] != "doc-alpha" || metadata["eval_seed"] != true {
				t.Fatalf("remember input = %#v", input)
			}
			proposal := input["proposal"].(map[string]any)
			if len(proposal["entities"].([]any)) == 0 || len(proposal["relationships"].([]any)) == 0 {
				t.Fatalf("remember proposal = %#v", proposal)
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
	if err == nil || !strings.Contains(err.Error(), "remember response missing fragment id") {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}

func TestHTTPClientImportCorpusUsesV2RememberForTypedClaims(t *testing.T) {
	validFrom := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	validTo := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/tools/remember":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode remember body: %v", err)
			}
			if _, exists := input["auto_promote"]; exists {
				t.Fatalf("V2 remember must not carry client promotion hints: %#v", input)
			}
			proposal := input["proposal"].(map[string]any)
			relationship := proposal["relationships"].([]any)[0].(map[string]any)
			if relationship["proposal_id"] != "typed-claim-1" || relationship["predicate"] != "uses" {
				t.Fatalf("typed relationship = %#v", relationship)
			}
			if relationship["valid_from"] != validFrom.Format(time.RFC3339Nano) || relationship["valid_to"] != validTo.Format(time.RFC3339Nano) {
				t.Fatalf("typed relationship validity = %#v", relationship)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ingest_id": "ingest-tiered",
				"evidence":  []map[string]any{{"id": "fragment-tiered"}},
			})
		case "/api/v1/tools/get_memory_placement":
			_ = json.NewEncoder(w).Encode(map[string]any{"placement": map[string]any{
				"status": "completed",
				"items": []map[string]any{{
					"assertion_id": "assertion-tiered", "tier": "fact",
					"reviewed_relationship": map[string]any{"proposal_id": "typed-claim-1"},
				}},
			}})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	mapping, err := client.ImportCorpus(context.Background(), []CorpusItem{{
		SourceDocID: "doc-tiered",
		Content:     "Service OBS-001 current owner is bob.",
		AutoPromote: true,
		Claims: []TypedClaim{{
			Subject:        "service OBS-001",
			Predicate:      "uses",
			Object:         "owner bob",
			ExtractConf:    0.95,
			ResolutionConf: 0.95,
			ValidFrom:      &validFrom,
			ValidTo:        &validTo,
		}},
	}})

	if err != nil {
		t.Fatalf("ImportCorpus typed claims: %v", err)
	}
	if mapping.BySourceDocID["doc-tiered"].ID != "fragment-tiered" {
		t.Fatalf("default mapping = %+v", mapping.BySourceDocID["doc-tiered"])
	}
	if mapping.BySourceDocIDAndType["doc-tiered"]["assertion"][0].ID != "assertion-tiered" {
		t.Fatalf("assertion mapping = %+v", mapping.BySourceDocIDAndType)
	}
	if mapping.BySourceDocIDAndType["doc-tiered:claim:1"]["assertion"][0].ID != "assertion-tiered" {
		t.Fatalf("typed claim alias mapping = %+v", mapping.BySourceDocIDAndType)
	}
	if mapping.BySourceDocIDAndType["doc-tiered:fact:1"]["assertion"][0].ID != "assertion-tiered" {
		t.Fatalf("typed fact alias mapping = %+v", mapping.BySourceDocIDAndType)
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

func TestHTTPClientImportCorpusRejectsFailedV2Placement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/tools/remember" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ingest_id": "ingest-tiered", "evidence": []map[string]any{{"id": "fragment-tiered"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"placement": map[string]any{"status": "failed"}})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{
		SourceDocID: "doc-tiered",
		Content:     "Service OBS-001 current owner is bob.",
		Claims: []TypedClaim{{
			Subject:        "service OBS-001",
			Predicate:      "uses",
			Object:         "owner bob",
			ExtractConf:    0.95,
			ResolutionConf: 0.95,
		}},
	}})

	if err == nil || !strings.Contains(err.Error(), "memory placement ingest-tiered failed") {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}

func TestHTTPClientImportCorpusDoesNotForceAutoPromotion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/tools/remember" {
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode remember: %v", err)
			}
			if _, exists := input["auto_promote"]; exists {
				t.Fatalf("remember input contains auto_promote: %#v", input)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ingest_id": "ingest-tiered", "evidence": []map[string]any{{"id": "fragment-tiered"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"placement": map[string]any{
			"status": "completed",
			"items": []map[string]any{{
				"assertion_id": "assertion-tiered", "tier": "validated_claim",
				"reviewed_relationship": map[string]any{"proposal_id": "typed-claim-1"},
			}},
		}})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	mapping, err := client.ImportCorpus(context.Background(), []CorpusItem{{
		SourceDocID: "doc-tiered",
		Content:     "Service OBS-001 current owner is bob.",
		AutoPromote: true,
		Claims: []TypedClaim{{
			Subject:        "service OBS-001",
			Predicate:      "uses",
			Object:         "owner bob",
			ExtractConf:    0.95,
			ResolutionConf: 0.95,
		}},
	}})

	if err != nil {
		t.Fatalf("ImportCorpus err = %v", err)
	}
	if mapping.BySourceDocIDAndType["doc-tiered:claim:1"]["assertion"][0].ID != "assertion-tiered" {
		t.Fatalf("claim assertion mapping = %+v", mapping.BySourceDocIDAndType)
	}
	if _, ok := mapping.BySourceDocIDAndType["doc-tiered:fact:1"]; ok {
		t.Fatalf("fact alias mapping = %+v; want none for unpromoted assertion", mapping.BySourceDocIDAndType)
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
		case "assertion":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
				"assertion_id":  "assertion-tiered",
				"evidence_json": `[{"fragment_id":"fragment-tiered","start":0,"end":10}]`,
			}}})
		case "claim":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
				"claim_id":       "claim-tiered",
				"classification": map[string]any{"source_doc_id": "doc-tiered"},
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
	if mapping.BySourceDocIDAndType["doc-tiered"]["assertion"][0].ID != "assertion-tiered" {
		t.Fatalf("assertion mapping = %+v", mapping.BySourceDocIDAndType)
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

func TestSeedMemoryProposalUsesUnicodeCodePointOffsets(t *testing.T) {
	item := CorpusItem{SourceDocID: "doc-unicode", Content: "Mark 🚀 Dense-Mem"}
	proposal := seedMemoryProposal(item)
	relationship := proposal["relationships"].([]map[string]any)[0]
	evidence := relationship["evidence"].([]map[string]any)[0]
	if evidence["end"] != 16 {
		t.Fatalf("evidence end = %v; want 16 Unicode code points", evidence["end"])
	}
	if seedSourceType("web") != "document" || seedAuthority("public_qrels") != "secondary" {
		t.Fatalf("seed normalization failed")
	}
}

func TestAssertionSourceDocIDsRejectsMalformedEvidence(t *testing.T) {
	_, err := assertionSourceDocIDs(map[string]any{
		"assertion_id": "assertion-broken", "evidence_json": "{",
	}, map[string]string{"fragment-1": "doc-1"})
	if err == nil || !strings.Contains(err.Error(), "invalid evidence_json") {
		t.Fatalf("assertionSourceDocIDs error = %v", err)
	}
}
