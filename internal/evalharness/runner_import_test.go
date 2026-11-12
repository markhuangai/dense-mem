package evalharness

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunImportModeImportsWithoutRecall(t *testing.T) {
	for _, schema := range []string{SeedSchemaVersion, SeedSchemaVersionV2} {
		t.Run(schema, func(t *testing.T) {
			dir := writeEvalFixture(t)
			if schema == SeedSchemaVersionV2 {
				dir = writeV2EvalImportFixture(t)
			}
			testRunEvidenceImportWithoutRecall(t, dir)
		})
	}
}

func testRunEvidenceImportWithoutRecall(t *testing.T, dir string) {
	t.Helper()
	rememberIDs := map[string]string{
		"eval:doc-alpha": "evidence-alpha",
		"eval:doc-beta":  "evidence-beta",
	}
	var rememberCalls int
	var recallCalls int
	exportCalls := map[string]int{}

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
			for _, retiredField := range []string{"relationships", "entities", "values"} {
				if _, exists := input[retiredField]; exists {
					t.Fatalf("remember contains retired field %q", retiredField)
				}
			}
			evidence, ok := input["evidence"].([]any)
			if !ok || len(evidence) != 1 {
				t.Fatalf("remember evidence = %#v", input["evidence"])
			}
			item := evidence[0].(map[string]any)
			sourceDocID := strings.TrimPrefix(idempotencyKey, "eval:")
			content := map[string]string{"doc-alpha": "Alpha is the first Greek letter.", "doc-beta": "Beta is the second Greek letter."}
			if item["content"] != content[sourceDocID] || nestedString(item, "metadata", "source_doc_id") != sourceDocID || input["target_space"] != "team_shared" {
				t.Fatalf("remember did not preserve exact Evidence and provenance: %#v", input)
			}
			rememberCalls++
			submissionID := strings.TrimPrefix(idempotencyKey, "eval:")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"contract_version": "dense-mem.v2.8.0",
				"submission_id":    submissionID,
				"submission_kind":  "remember",
				"processing_state": "completed",
				"search_state":     "current",
				"correlation_id":   "correlation-" + submissionID,
				"evidence":         []map[string]any{{"disposition": "stored", "evidence_id": rememberIDs["eval:"+submissionID], "evidence_index": 0, "superseded_evidence_ids": []string{}, "search_state": "current"}},
				"errors":           []map[string]any{},
			})
		case "tool:eval_list_knowledge_refs":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode export body: %v", err)
			}
			kind := input["type"].(string)
			exportCalls[kind]++
			switch kind {
			case "evidence":
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
					{"id": "evidence-alpha", "metadata": map[string]any{"source_doc_id": "doc-alpha"}},
					{"id": "evidence-beta", "metadata": map[string]any{"source_doc_id": "doc-beta"}},
				}})
			default:
				t.Fatalf("unexpected export type %q", kind)
			}
		case "tool:eval_run_recall_case":
			recallCalls++
			t.Fatalf("import mode should not call recall")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	out := filepath.Join(dir, "import-run")
	summary, err := Run(context.Background(), RunOptions{
		Mode:              "import",
		SeedManifestPath:  filepath.Join(dir, "seed_manifest.json"),
		SuitePath:         filepath.Join(dir, "suite.jsonl"),
		BaseURL:           server.URL,
		APIKey:            "api-key",
		ImportSeed:        true,
		ImportConcurrency: 1,
		MaxPageSize:       50,
		OutDir:            out,
		RunID:             "import-test",
	})
	if err != nil {
		t.Fatalf("Run import: %v", err)
	}
	if summary.Mode != "import" || summary.CaseCount != 2 || summary.ScoredCaseCount != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if rememberCalls != 2 || recallCalls != 0 {
		t.Fatalf("remember/recall calls = %d/%d", rememberCalls, recallCalls)
	}
	if exportCalls["evidence"] != 1 || len(exportCalls) != 1 {
		t.Fatalf("export calls = %#v; want one post-placement Evidence export", exportCalls)
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
	if mapping.BySourceDocID["doc-alpha"].ID != "evidence-alpha" ||
		mapping.BySourceDocIDAndType["doc-alpha"]["evidence"][0].ID != "evidence-alpha" {
		t.Fatalf("post-placement mapping = %+v", mapping.BySourceDocIDAndType["doc-alpha"])
	}
}

func TestRunImportResumeSkipsOnlyCompletedDocumentsWithLiveEvidence(t *testing.T) {
	dir := writeV2EvalImportFixture(t)
	resumePath := filepath.Join(dir, "completed_source_doc_ids.txt")
	if err := os.WriteFile(resumePath, []byte("doc-alpha\ndoc-beta\n"), 0o644); err != nil {
		t.Fatalf("write resume checkpoint: %v", err)
	}

	var remembered []string
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "tool:eval_list_knowledge_refs":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{
					"id":       "evidence-alpha",
					"metadata": map[string]any{"source_doc_id": "doc-alpha"},
				}},
			})
		case "tool:remember":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode remember body: %v", err)
			}
			sourceDocID := strings.TrimPrefix(input["idempotency_key"].(string), "eval:")
			remembered = append(remembered, sourceDocID)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"contract_version": "dense-mem.v2.8.0",
				"submission_id":    "submission-" + sourceDocID,
				"submission_kind":  "remember",
				"processing_state": "completed",
				"search_state":     "current",
				"correlation_id":   "correlation-" + sourceDocID,
				"evidence":         []map[string]any{{"disposition": "stored", "evidence_id": "evidence-" + sourceDocID, "evidence_index": 0, "superseded_evidence_ids": []string{}, "search_state": "current"}},
				"errors":           []map[string]any{},
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
		ImportSeed:             true,
		ImportConcurrency:      1,
		ResumeSourceDocIDsPath: resumePath,
		OutDir:                 out,
	})
	if err != nil {
		t.Fatalf("Run resumed import: %v", err)
	}
	if len(remembered) != 1 || remembered[0] != "doc-beta" {
		t.Fatalf("remembered source documents = %v; want only stale checkpoint doc-beta", remembered)
	}

	var mapping KnowledgeMapping
	if err := readJSONFile(filepath.Join(out, "knowledge_mapping.json"), &mapping); err != nil {
		t.Fatalf("read knowledge mapping: %v", err)
	}
	if mapping.BySourceDocID["doc-alpha"].ID != "evidence-alpha" ||
		mapping.BySourceDocID["doc-beta"].Type != "fragment" ||
		mapping.BySourceDocID["doc-beta"].ID != "evidence-doc-beta" {
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

func TestRunRejectsInvalidEvidenceBeforeExternalCalls(t *testing.T) {
	dir := writeEvalFixture(t)
	if err := writeJSONL(filepath.Join(dir, "corpus.jsonl"), []CorpusItem{
		{SourceDocID: "doc-alpha", Content: ""},
		{SourceDocID: "doc-beta", Content: "Beta is the second Greek letter."},
	}); err != nil {
		t.Fatal(err)
	}
	var requests int
	server := newEvalHarnessServer(t, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		requests++
		t.Fatal("invalid Evidence must fail before any external call")
	}))
	defer server.Close()

	_, err := Run(context.Background(), RunOptions{
		Mode:             "import",
		SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
		SuitePath:        filepath.Join(dir, "suite.jsonl"),
		BaseURL:          server.URL,
		APIKey:           "api-key",
		ImportSeed:       true,
	})
	if err == nil || !strings.Contains(err.Error(), "content") {
		t.Fatalf("Run invalid Evidence import error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("external requests = %d, want 0", requests)
	}
}

func writeV2EvalImportFixture(t *testing.T) string {
	t.Helper()
	dir := writeEvalFixture(t)
	manifestPath := filepath.Join(dir, "seed_manifest.json")
	manifest, err := LoadSeedManifest(manifestPath)
	if err != nil {
		t.Fatalf("load fixture manifest: %v", err)
	}
	corpus, err := LoadCorpus(manifestPath, manifest)
	if err != nil {
		t.Fatalf("load fixture corpus: %v", err)
	}
	manifest.SchemaVersion = SeedSchemaVersionV2
	if err := writeJSONL(filepath.Join(dir, manifest.CorpusFile), corpus); err != nil {
		t.Fatalf("write V2 fixture corpus: %v", err)
	}
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		t.Fatalf("write V2 fixture manifest: %v", err)
	}
	return dir
}
