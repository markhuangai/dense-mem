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
	dir := writeV2EvalImportFixture(t)
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
			relationships, ok := input["relationships"].([]any)
			if !ok || len(relationships) != 1 {
				t.Fatalf("remember relationships = %#v", input["relationships"])
			}
			relationship := relationships[0].(map[string]any)
			evidenceIndices, ok := relationship["evidence_indices"].([]any)
			if !ok || len(evidenceIndices) != 1 || evidenceIndices[0] != float64(0) {
				t.Fatalf("remember relationship evidence_indices = %#v", relationship["evidence_indices"])
			}
			for _, legacyField := range []string{"span", "supports"} {
				if _, exists := relationship[legacyField]; exists {
					t.Fatalf("remember relationship contains legacy field %q: %#v", legacyField, relationship)
				}
			}
			rememberCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"submission_id":    strings.TrimPrefix(idempotencyKey, "eval:"),
				"processing_state": "completed",
				"search_state":     "current",
				"evidence":         []map[string]any{{"evidence_id": rememberIDs[idempotencyKey], "search_state": "current"}},
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
	for _, kind := range []string{"evidence", "entity", "value", "relationship", "hypothesis"} {
		if exportCalls[kind] != 1 {
			t.Fatalf("export calls = %#v; want one post-placement export per canonical type", exportCalls)
		}
	}
	if len(exportCalls) != 5 {
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
				"submission_id": "submission-" + sourceDocID, "processing_state": "completed", "search_state": "current",
				"evidence": []map[string]any{{"evidence_id": "evidence-" + strings.TrimPrefix(sourceDocID, "doc-"), "search_state": "current"}},
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

func TestRunRejectsV1SeedImportBeforeExternalCalls(t *testing.T) {
	dir := writeEvalFixture(t)
	var requests int
	server := newEvalHarnessServer(t, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		requests++
		t.Fatal("V1 import must fail before any external call")
	}))
	defer server.Close()

	_, err := Run(context.Background(), RunOptions{
		Mode:             "import",
		SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
		SuitePath:        filepath.Join(dir, "suite.jsonl"),
		BaseURL:          server.URL,
		ImportSeed:       true,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be imported through the required flat relationship contract") {
		t.Fatalf("Run V1 import error = %v", err)
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
	manifest.RelationshipCount = len(corpus)
	for i := range corpus {
		corpus[i].Relationships = []any{evalImportRelationship(t, corpus[i].Content, "relationship-"+corpus[i].SourceDocID)}
	}
	if err := writeJSONL(filepath.Join(dir, manifest.CorpusFile), corpus); err != nil {
		t.Fatalf("write V2 fixture corpus: %v", err)
	}
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		t.Fatalf("write V2 fixture manifest: %v", err)
	}
	return dir
}

func evalImportRelationship(t *testing.T, content, ref string) map[string]any {
	t.Helper()
	runes := []rune(content)
	firstSpace := -1
	secondSpace := -1
	for index, value := range runes {
		if value != ' ' {
			continue
		}
		if firstSpace < 0 {
			firstSpace = index
			continue
		}
		secondSpace = index
		break
	}
	if firstSpace <= 0 || secondSpace <= firstSpace+1 || secondSpace+1 >= len(runes) {
		t.Fatal("eval import fixture content must contain subject, predicate, and object")
	}
	objectEnd := len(runes)
	for objectEnd > secondSpace+1 && strings.ContainsRune(".!?", runes[objectEnd-1]) {
		objectEnd--
	}
	if objectEnd <= secondSpace+1 {
		t.Fatal("eval import fixture content must contain an object")
	}
	subjectEnd := firstSpace
	predicateStart := firstSpace + 1
	predicateEnd := secondSpace
	objectStart := secondSpace + 1
	return map[string]any{
		"ref": ref,
		"subject": map[string]any{
			"name": string(runes[:subjectEnd]), "entity_kind": "other",
		},
		"predicate": map[string]any{
			"proposed_key": strings.ToLower(string(runes[predicateStart:predicateEnd])),
		},
		"object": map[string]any{"entity": map[string]any{
			"name": string(runes[objectStart:objectEnd]), "entity_kind": "other",
		}},
		"polarity":         "+",
		"evidence_indices": []any{0},
	}
}
