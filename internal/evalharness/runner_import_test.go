package evalharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunImportModeImportsWithoutRecall(t *testing.T) {
	dir := writeEvalFixture(t)
	rememberIDs := map[string]string{
		"eval:doc-alpha": "frag-alpha",
		"eval:doc-beta":  "frag-beta",
	}
	var rememberCalls int
	var placementPolls int
	var recallCalls int
	var controlPatched bool
	exportCalls := map[string]int{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/control/api/config/evaluation":
			controlPatched = true
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "/api/v1/tools/remember":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode remember body: %v", err)
			}
			evidence := input["evidence"].([]any)
			firstEvidence := evidence[0].(map[string]any)
			idempotencyKey := firstEvidence["idempotency_key"].(string)
			id, ok := rememberIDs[idempotencyKey]
			if !ok {
				t.Fatalf("remember input = %#v", input)
			}
			rememberCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ingest_id": strings.TrimPrefix(idempotencyKey, "eval:"),
				"status":    "queued",
				"evidence":  []map[string]any{{"id": id}},
			})
		case "/api/v1/tools/get_memory_placement":
			placementPolls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"placement": map[string]any{"status": "completed"},
			})
		case "/api/v1/tools/eval_list_knowledge_refs":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode export body: %v", err)
			}
			kind := input["type"].(string)
			exportCalls[kind]++
			switch kind {
			case "fragment":
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
					{"id": "frag-alpha", "metadata": map[string]any{"source_doc_id": "doc-alpha"}},
					{"id": "frag-beta", "metadata": map[string]any{"source_doc_id": "doc-beta"}},
				}})
			case "claim":
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
					{"claim_id": "claim-alpha", "supported_by": []string{"frag-alpha"}},
					{"claim_id": "claim-beta", "supported_by": []string{"frag-beta"}},
				}})
			case "fact":
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
					{"fact_id": "fact-alpha", "promoted_from_claim_id": "claim-alpha"},
					{"fact_id": "fact-beta", "promoted_from_claim_id": "claim-beta"},
				}})
			default:
				t.Fatalf("unexpected export type %q", kind)
			}
		case "/api/v1/tools/recall_memory":
			recallCalls++
			t.Fatalf("import mode should not call recall_memory")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	out := filepath.Join(dir, "import-run")
	summary, err := Run(context.Background(), RunOptions{
		Mode:             "import",
		SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
		SuitePath:        filepath.Join(dir, "suite.jsonl"),
		BaseURL:          server.URL,
		APIKey:           "api-key",
		ControlURL:       server.URL,
		ControlToken:     "control-token",
		ImportSeed:       true,
		MaxPageSize:      50,
		OutDir:           out,
		RunID:            "import-test",
	})
	if err != nil {
		t.Fatalf("Run import: %v", err)
	}
	if summary.Mode != "import" || summary.CaseCount != 2 || summary.ScoredCaseCount != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if !controlPatched || rememberCalls != 2 || placementPolls != 2 || recallCalls != 0 {
		t.Fatalf("control/remember/placement/recall calls = %v/%d/%d/%d", controlPatched, rememberCalls, placementPolls, recallCalls)
	}
	if exportCalls["fragment"] != 1 || exportCalls["claim"] != 1 || exportCalls["fact"] != 1 {
		t.Fatalf("export calls = %#v; want one post-placement export per knowledge type", exportCalls)
	}
	for _, name := range []string{"run_config.json", "seed_manifest.json", "suite.jsonl", "summary.json", "knowledge_mapping.json"} {
		if !fileExists(filepath.Join(out, name)) {
			t.Fatalf("missing import artifact %s", name)
		}
	}
	var mapping KnowledgeMapping
	if err := readJSONFile(filepath.Join(out, "knowledge_mapping.json"), &mapping); err != nil {
		t.Fatalf("read knowledge mapping: %v", err)
	}
	if mapping.BySourceDocIDAndType["doc-alpha"]["claim"][0].ID != "claim-alpha" ||
		mapping.BySourceDocIDAndType["doc-alpha"]["fact"][0].ID != "fact-alpha" {
		t.Fatalf("post-placement mapping = %+v", mapping.BySourceDocIDAndType["doc-alpha"])
	}
}

func TestRunImportResumeTrustsCompletedCheckpoint(t *testing.T) {
	dir := writeEvalFixture(t)
	resumePath := filepath.Join(dir, "completed_source_doc_ids.txt")
	if err := os.WriteFile(resumePath, []byte("doc-alpha\n"), 0o644); err != nil {
		t.Fatalf("write resume checkpoint: %v", err)
	}

	var remembered []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/control/api/config/evaluation":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "/api/v1/tools/eval_list_knowledge_refs":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{
					"id":       "frag-alpha",
					"metadata": map[string]any{"source_doc_id": "doc-alpha"},
				}},
			})
		case "/api/v1/tools/remember":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode remember body: %v", err)
			}
			evidence := input["evidence"].([]any)[0].(map[string]any)
			sourceDocID := strings.TrimPrefix(evidence["idempotency_key"].(string), "eval:")
			remembered = append(remembered, sourceDocID)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ingest_id": "ingest-" + sourceDocID,
				"evidence":  []map[string]any{{"id": "frag-" + strings.TrimPrefix(sourceDocID, "doc-")}},
			})
		case "/api/v1/tools/get_memory_placement":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"placement": map[string]any{"status": "completed"},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	out := filepath.Join(dir, "resume-import-run")
	_, err := Run(context.Background(), RunOptions{
		Mode:                   "import",
		SeedManifestPath:       filepath.Join(dir, "seed_manifest.json"),
		SuitePath:              filepath.Join(dir, "suite.jsonl"),
		BaseURL:                server.URL,
		APIKey:                 "api-key",
		ControlURL:             server.URL,
		ControlToken:           "control-token",
		ImportSeed:             true,
		ResumeSourceDocIDsPath: resumePath,
		OutDir:                 out,
	})
	if err != nil {
		t.Fatalf("Run resumed import: %v", err)
	}
	if len(remembered) != 1 || remembered[0] != "doc-beta" {
		t.Fatalf("remembered source documents = %v; want only non-checkpoint doc-beta", remembered)
	}

	var mapping KnowledgeMapping
	if err := readJSONFile(filepath.Join(out, "knowledge_mapping.json"), &mapping); err != nil {
		t.Fatalf("read knowledge mapping: %v", err)
	}
	if mapping.BySourceDocID["doc-alpha"].ID != "frag-alpha" || mapping.BySourceDocID["doc-beta"].ID != "frag-beta" {
		t.Fatalf("knowledge mapping = %+v", mapping.BySourceDocID)
	}
	var config RunConfig
	if err := readJSONFile(filepath.Join(out, "run_config.json"), &config); err != nil {
		t.Fatalf("read run config: %v", err)
	}
	if config.ImportRoute != "remember" || config.ResumeSourceDocIDsPath != resumePath {
		t.Fatalf("run config = %+v", config)
	}
}

func TestRunImportResumeUsesSeedMappingForCompletedDocuments(t *testing.T) {
	dir := writeEvalFixture(t)
	resumePath := filepath.Join(dir, "completed_source_doc_ids.txt")
	if err := os.WriteFile(resumePath, []byte("doc-alpha\ndoc-beta\n"), 0o644); err != nil {
		t.Fatalf("write resume checkpoint: %v", err)
	}
	if err := writeJSONL(filepath.Join(dir, "qrels.jsonl"), []QRel{
		{CaseID: "case-1", RequiredRefs: []Ref{{Type: "source_doc", SourceDocID: "doc-alpha", Grade: 1}}},
		{CaseID: "case-2", RequiredRefs: []Ref{{Type: "source_doc", SourceDocID: "doc-beta", Grade: 1}}},
	}); err != nil {
		t.Fatalf("write v2 qrels: %v", err)
	}
	mappingPath := filepath.Join(dir, "completed_knowledge_mapping.json")
	mapping := newKnowledgeMapping()
	for _, sourceDocID := range []string{"doc-alpha", "doc-beta"} {
		addSourceMapping(&mapping, Ref{Type: "evidence", ID: "evidence-" + sourceDocID, SourceDocID: sourceDocID}, true)
		addSourceMapping(&mapping, Ref{Type: "relationship", ID: "relationship-" + sourceDocID, SourceDocID: sourceDocID}, false)
	}
	addSourceMapping(&mapping, Ref{Type: "relationship", ID: "relationship-doc-alpha-2", SourceDocID: "doc-alpha"}, false)
	if err := writeJSONFile(mappingPath, mapping); err != nil {
		t.Fatalf("write recovered mapping: %v", err)
	}

	var rememberCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/control/api/config/evaluation":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "/api/v1/tools/eval_list_knowledge_refs":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		case "/api/v1/tools/remember":
			rememberCalls++
			t.Fatalf("completed documents must not be remembered again")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	out := filepath.Join(dir, "recovered-import-run")
	_, err := Run(context.Background(), RunOptions{
		Mode:                   "import",
		SeedManifestPath:       filepath.Join(dir, "seed_manifest.json"),
		SuitePath:              filepath.Join(dir, "suite.jsonl"),
		BaseURL:                server.URL,
		APIKey:                 "api-key",
		ControlURL:             server.URL,
		ControlToken:           "control-token",
		ImportSeed:             true,
		ResumeSourceDocIDsPath: resumePath,
		MappingPath:            mappingPath,
		OutDir:                 out,
	})
	if err != nil {
		t.Fatalf("Run resumed import with recovered mapping: %v", err)
	}
	if rememberCalls != 0 {
		t.Fatalf("remember calls = %d; want 0", rememberCalls)
	}

	var recovered KnowledgeMapping
	if err := readJSONFile(filepath.Join(out, "knowledge_mapping.json"), &recovered); err != nil {
		t.Fatalf("read recovered import mapping: %v", err)
	}
	for _, sourceDocID := range []string{"doc-alpha", "doc-beta"} {
		if recovered.BySourceDocID[sourceDocID].Type != "evidence" {
			t.Fatalf("default mapping for %s = %+v", sourceDocID, recovered.BySourceDocID[sourceDocID])
		}
		wantRelationships := 1
		if sourceDocID == "doc-alpha" {
			wantRelationships = 2
		}
		if len(recovered.BySourceDocIDAndType[sourceDocID]["relationship"]) != wantRelationships {
			t.Fatalf("relationship mapping for %s = %+v", sourceDocID, recovered.BySourceDocIDAndType[sourceDocID])
		}
	}
}
