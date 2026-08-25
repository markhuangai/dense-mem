package evalharness

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunBaselineLiveHTTPFlowSeedsExpectedDreams(t *testing.T) {
	dir := t.TempDir()
	manifest := SeedManifest{
		SchemaVersion: SeedSchemaVersionV2,
		SeedID:        "dream-live-fixture",
		CorpusFile:    "corpus.jsonl",
		CasesFile:     "cases.jsonl",
		QrelsFile:     "qrels.jsonl",
		DreamsFile:    "expected_dreams.jsonl",
		Counts: map[string]int{
			"cases":  1,
			"corpus": 2,
			"qrels":  1,
			"dreams": 1,
		},
	}
	if err := writeJSONFile(filepath.Join(dir, "seed_manifest.json"), manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := writeJSONL(filepath.Join(dir, "corpus.jsonl"), []CorpusItem{
		{SourceDocID: "doc-employer", Content: "Employer fact source.", Relationships: []any{evalImportRelationship(t, "Employer fact source.", "relationship-doc-employer")}},
		{SourceDocID: "doc-location", Content: "Location fact source.", Relationships: []any{evalImportRelationship(t, "Location fact source.", "relationship-doc-location")}},
	}); err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	if err := writeJSONL(filepath.Join(dir, "cases.jsonl"), []Case{
		{CaseID: "case-dream", Query: "which hypothesis?", IncludeDreams: true, Limit: 2},
	}); err != nil {
		t.Fatalf("write cases: %v", err)
	}
	if err := writeJSONL(filepath.Join(dir, "qrels.jsonl"), []QRel{{
		CaseID:            "case-dream",
		RequiredRefs:      []Ref{{Type: "fragment", SourceDocID: "doc-employer", Grade: 1}},
		RequiredDreamRefs: []Ref{{Type: "dream", SourceDocID: "expected-dream", Grade: 1}},
	}}); err != nil {
		t.Fatalf("write qrels: %v", err)
	}
	if err := writeJSONL(filepath.Join(dir, "expected_dreams.jsonl"), []ExpectedDream{{
		SourceDocID: "expected-dream",
		CaseID:      "case-dream",
		Hypothesis:  "Employment may explain the location period.",
		SourceRefs: []Ref{
			{Type: "fragment", SourceDocID: "doc-employer"},
			{Type: "fragment", SourceDocID: "doc-location"},
		},
	}}); err != nil {
		t.Fatalf("write expected dreams: %v", err)
	}
	if err := writeJSONL(filepath.Join(dir, "suite.jsonl"), []SuiteCase{{CaseID: "case-dream"}}); err != nil {
		t.Fatalf("write suite: %v", err)
	}

	rememberIDs := map[string]string{
		"eval:doc-employer": "frag-employer",
		"eval:doc-location": "frag-location",
	}
	var dreamCycleCalled bool
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "tool:remember":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode remember body: %v", err)
			}
			idempotencyKey := input["idempotency_key"].(string)
			if _, ok := rememberIDs[idempotencyKey]; !ok {
				t.Fatalf("remember input = %#v", input)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"submission_id":    strings.TrimPrefix(idempotencyKey, "eval:"),
				"processing_state": "completed",
				"search_state":     "current",
				"evidence":         []map[string]any{{"evidence_id": rememberIDs[idempotencyKey], "search_state": "current"}},
			})
		case "tool:eval_run_dream_cycle":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode dream cycle body: %v", err)
			}
			dreamCycleCalled = true
			seeds, ok := input["seed_dreams"].([]any)
			if !ok || len(seeds) != 1 {
				t.Fatalf("seed_dreams = %#v", input["seed_dreams"])
			}
			seed := seeds[0].(map[string]any)
			if seed["hypothesis"] != "Employment may explain the location period." {
				t.Fatalf("seed = %#v", seed)
			}
			refs := seed["source_refs"].([]any)
			firstRef := refs[0].(map[string]any)
			secondRef := refs[1].(map[string]any)
			if firstRef["type"] != "fragment" || firstRef["id"] != "frag-employer" || secondRef["id"] != "frag-location" {
				t.Fatalf("seed refs = %#v", refs)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"run_id": "run-dream"})
		case "tool:eval_list_knowledge_refs":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode list body: %v", err)
			}
			switch input["type"] {
			case "fragment":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"items": []map[string]any{
						{"fragment_id": "frag-employer", "metadata": map[string]any{"source_doc_id": "doc-employer"}},
						{"fragment_id": "frag-location", "metadata": map[string]any{"source_doc_id": "doc-location"}},
					},
					"next_cursor": "",
					"has_more":    false,
				})
			case "dream":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"items": []map[string]any{{
						"dream_id": "dream-expected",
						"source_refs": []map[string]any{
							{"type": "fragment", "id": "frag-employer"},
							{"type": "fragment", "id": "frag-location"},
						},
					}},
					"next_cursor": "",
					"has_more":    false,
				})
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{
					"items":       []map[string]any{},
					"next_cursor": "",
					"has_more":    false,
				})
			}
		case "tool:eval_run_recall_case":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"query": "which hypothesis?",
				"ranked_refs": []map[string]any{{
					"type": "fragment",
					"id":   "frag-employer",
					"rank": 1,
				}},
				"dream_refs": []map[string]any{{
					"type": "dream",
					"id":   "dream-expected",
					"rank": 1,
				}},
				"latency_ms": 7,
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	summary, err := Run(context.Background(), RunOptions{
		Mode:             "baseline",
		SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
		SuitePath:        filepath.Join(dir, "suite.jsonl"),
		BaseURL:          server.URL,
		APIKey:           "api-key",
		ImportSeed:       true,
		MaxPageSize:      50,
		RunID:            "live-dream-baseline-test",
	})
	if err != nil {
		t.Fatalf("Run live dream baseline: %v", err)
	}
	if !dreamCycleCalled {
		t.Fatal("dream cycle was not called")
	}
	if summary.DreamScoredCaseCount != 1 || summary.AverageDreamRecallAtK != 1 || summary.AverageDreamMRR != 1 {
		t.Fatalf("dream summary = %+v", summary)
	}
}
