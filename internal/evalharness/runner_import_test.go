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
	dir := writeEvalFixture(t)
	rememberIDs := map[string]string{
		"eval:doc-alpha": "evidence-alpha",
		"eval:doc-beta":  "evidence-beta",
	}
	var rememberCalls int
	var submissionPolls int
	var recallCalls int
	var controlPatched bool
	exportCalls := map[string]int{}

	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/control/api/config/evaluation":
			controlPatched = true
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "tool:remember":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode remember body: %v", err)
			}
			evidence := input["evidence"].([]any)
			firstEvidence := evidence[0].(map[string]any)
			idempotencyKey := firstEvidence["idempotency_key"].(string)
			_, ok := rememberIDs[idempotencyKey]
			if !ok {
				t.Fatalf("remember input = %#v", input)
			}
			rememberCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"submission_id": "submission-" + strings.TrimPrefix(idempotencyKey, "eval:"), "processing_state": "queued"})
		case "tool:get_submission_status":
			submissionPolls++
			_ = json.NewEncoder(w).Encode(map[string]any{"processing_state": "completed", "search_state": "current"})
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
			case "entity", "value", "relationship", "hypothesis":
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{}})
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
	if !controlPatched || rememberCalls != 2 || submissionPolls != 2 || recallCalls != 0 {
		t.Fatalf("control/remember/submission/recall calls = %v/%d/%d/%d", controlPatched, rememberCalls, submissionPolls, recallCalls)
	}
	for _, kind := range []string{"evidence", "entity", "value", "relationship", "hypothesis"} {
		if exportCalls[kind] != 1 {
			t.Fatalf("export calls = %#v; want one post-submission export per canonical type", exportCalls)
		}
	}
	if len(exportCalls) != 5 {
		t.Fatalf("export calls = %#v; want one post-submission export per knowledge type", exportCalls)
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
		t.Fatalf("post-submission mapping = %+v", mapping.BySourceDocIDAndType["doc-alpha"])
	}
}

func TestRunImportResumeSkipsOnlyCompletedDocumentsWithLiveEvidence(t *testing.T) {
	dir := writeEvalFixture(t)
	resumePath := filepath.Join(dir, "completed_source_doc_ids.txt")
	if err := os.WriteFile(resumePath, []byte("doc-alpha\ndoc-beta\n"), 0o644); err != nil {
		t.Fatalf("write resume checkpoint: %v", err)
	}

	var remembered []string
	var evidenceExports int
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/control/api/config/evaluation":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "tool:eval_list_knowledge_refs":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode export body: %v", err)
			}
			items := []map[string]any{}
			if input["type"] == "evidence" {
				evidenceExports++
				items = append(items, map[string]any{"id": "evidence-alpha", "metadata": map[string]any{"source_doc_id": "doc-alpha"}})
				if evidenceExports > 1 {
					items = append(items, map[string]any{"id": "evidence-beta", "metadata": map[string]any{"source_doc_id": "doc-beta"}})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "has_more": false})
		case "tool:remember":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode remember body: %v", err)
			}
			evidence := input["evidence"].([]any)[0].(map[string]any)
			sourceDocID := strings.TrimPrefix(evidence["idempotency_key"].(string), "eval:")
			remembered = append(remembered, sourceDocID)
			_ = json.NewEncoder(w).Encode(map[string]any{"submission_id": "submission-" + sourceDocID, "processing_state": "queued"})
		case "tool:get_submission_status":
			_ = json.NewEncoder(w).Encode(map[string]any{"processing_state": "completed", "search_state": "current"})
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
		t.Fatalf("remembered source documents = %v; want only stale checkpoint doc-beta", remembered)
	}

	var mapping KnowledgeMapping
	if err := readJSONFile(filepath.Join(out, "knowledge_mapping.json"), &mapping); err != nil {
		t.Fatalf("read knowledge mapping: %v", err)
	}
	if mapping.BySourceDocID["doc-alpha"].ID != "evidence-alpha" || mapping.BySourceDocID["doc-beta"].ID != "evidence-beta" {
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
