package evalharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCandidateWithV2KnowledgeMappingScoresEvidenceRefs(t *testing.T) {
	dir := writeEvalFixture(t)
	if err := writeJSONL(filepath.Join(dir, "qrels.jsonl"), []QRel{
		{CaseID: "case-1", RequiredRefs: []Ref{{Type: "source_doc", SourceDocID: "doc-alpha", Grade: 1}}},
		{CaseID: "case-2", RequiredRefs: []Ref{{Type: "source_doc", SourceDocID: "doc-beta", Grade: 1}}},
	}); err != nil {
		t.Fatalf("write source-doc qrels: %v", err)
	}

	var controlPatched bool
	var requestedTypes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/control/api/config/evaluation":
			controlPatched = true
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "/api/v1/tools/eval_list_knowledge_refs":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode export body: %v", err)
			}
			kind := stringValue(input["type"])
			requestedTypes = append(requestedTypes, kind)
			items := []map[string]any{}
			switch kind {
			case "evidence":
				items = []map[string]any{
					{"id": "evidence-alpha", "metadata": map[string]any{"legacy_metadata": map[string]any{"source_doc_id": "doc-alpha"}}},
					{"id": "evidence-beta", "metadata": map[string]any{"legacy_metadata": map[string]any{"source_doc_id": "doc-beta"}}},
				}
			case "entity", "value", "relationship", "hypothesis":
			default:
				t.Fatalf("unexpected export type %q", kind)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items":       items,
				"next_cursor": "",
				"has_more":    false,
			})
		case "/api/v1/tools/eval_run_recall_case":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode recall body: %v", err)
			}
			id := "evidence-alpha"
			if stringValue(input["case_id"]) == "case-2" {
				id = "evidence-beta"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"query":       input["query"],
				"ranked_refs": []map[string]any{{"type": "evidence", "id": id, "rank": 1}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	out := filepath.Join(dir, "v2-candidate-run")
	summary, err := Run(context.Background(), RunOptions{
		Mode:                 "candidate",
		SeedManifestPath:     filepath.Join(dir, "seed_manifest.json"),
		SuitePath:            filepath.Join(dir, "suite.jsonl"),
		BaseURL:              server.URL,
		APIKey:               "api-key",
		ControlURL:           server.URL,
		ControlToken:         "control-token",
		KnowledgeMappingMode: "v2",
		OutDir:               out,
		RunID:                "v2-candidate-test",
	})
	if err != nil {
		t.Fatalf("Run candidate with V2 mapping: %v", err)
	}
	if !controlPatched {
		t.Fatal("control config was not patched")
	}
	if strings.Join(requestedTypes, ",") != "evidence,entity,value,relationship,hypothesis" {
		t.Fatalf("requested types = %v", requestedTypes)
	}
	if summary.AverageRecallAtK != 1 || summary.UnmappedSourceRefs != 0 {
		t.Fatalf("summary = %+v; want evidence refs to satisfy source-doc qrels", summary)
	}
	var runConfig RunConfig
	if err := readJSONFile(filepath.Join(out, "run_config.json"), &runConfig); err != nil {
		t.Fatalf("read run config: %v", err)
	}
	if runConfig.KnowledgeMappingMode != "v2" {
		t.Fatalf("run config mapping mode = %q, want v2", runConfig.KnowledgeMappingMode)
	}
	var mapping KnowledgeMapping
	if err := readJSONFile(filepath.Join(out, "knowledge_mapping.json"), &mapping); err != nil {
		t.Fatalf("read mapping: %v", err)
	}
	if mapping.BySourceDocID["doc-alpha"].Type != "evidence" || mapping.BySourceDocID["doc-alpha"].ID != "evidence-alpha" {
		t.Fatalf("doc-alpha mapping = %+v", mapping.BySourceDocID["doc-alpha"])
	}
}
