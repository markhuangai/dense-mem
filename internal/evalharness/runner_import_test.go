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
	var placementPolls int
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
			id, ok := rememberIDs[idempotencyKey]
			if !ok {
				t.Fatalf("remember input = %#v", input)
			}
			relationships, ok := input["relationships"].([]any)
			if !ok || len(relationships) != 1 {
				t.Fatalf("remember relationships = %#v", input["relationships"])
			}
			rememberCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ingest_id": strings.TrimPrefix(idempotencyKey, "eval:"),
				"status":    "queued",
				"evidence":  []map[string]any{{"id": id}},
			})
		case "tool:get_memory_placement":
			placementPolls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"placement": map[string]any{"status": "completed"},
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
		ControlURL:        server.URL,
		ControlToken:      "control-token",
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
	if !controlPatched || rememberCalls != 2 || placementPolls != 2 || recallCalls != 0 {
		t.Fatalf("control/remember/placement/recall calls = %v/%d/%d/%d", controlPatched, rememberCalls, placementPolls, recallCalls)
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
		case "/control/api/config/evaluation":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
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
			evidence := input["evidence"].([]any)[0].(map[string]any)
			sourceDocID := strings.TrimPrefix(evidence["idempotency_key"].(string), "eval:")
			remembered = append(remembered, sourceDocID)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ingest_id": "ingest-" + sourceDocID,
				"evidence":  []map[string]any{{"id": "evidence-" + strings.TrimPrefix(sourceDocID, "doc-")}},
			})
		case "tool:get_memory_placement":
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
		ControlURL:       server.URL,
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
			"span": map[string]any{"evidence_index": 0, "start": 0, "end": subjectEnd},
		},
		"predicate": map[string]any{
			"proposed_key": strings.ToLower(string(runes[predicateStart:predicateEnd])),
			"surface":      string(runes[predicateStart:predicateEnd]),
			"span":         map[string]any{"evidence_index": 0, "start": predicateStart, "end": predicateEnd},
		},
		"object": map[string]any{"entity": map[string]any{
			"name": string(runes[objectStart:objectEnd]), "entity_kind": "other",
			"span": map[string]any{"evidence_index": 0, "start": objectStart, "end": objectEnd},
		}},
		"polarity": "+",
		"modality": "statement",
		"supports": []any{map[string]any{"evidence_index": 0, "start": 0, "end": len(runes)}},
	}
}
