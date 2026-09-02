package evalharness

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRunValidateWritesArtifacts(t *testing.T) {
	dir := writeEvalFixture(t)
	out := filepath.Join(dir, "run")

	summary, err := Run(context.Background(), RunOptions{
		Mode:             "validate",
		SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
		SuitePath:        filepath.Join(dir, "suite.jsonl"),
		OutDir:           out,
		RunID:            "validate-test",
	})
	if err != nil {
		t.Fatalf("Run validate: %v", err)
	}
	if summary.CaseCount != 2 || summary.ScoredCaseCount != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	for _, name := range []string{"run_config.json", "seed_manifest.json", "suite.jsonl", "summary.json"} {
		if !fileExists(filepath.Join(out, name)) {
			t.Fatalf("missing artifact %s", name)
		}
	}
	var config RunConfig
	if err := readJSONFile(filepath.Join(out, "run_config.json"), &config); err != nil {
		t.Fatalf("read run config: %v", err)
	}
	if config.ImportConcurrency != DefaultImportConcurrency {
		t.Fatalf("default import concurrency = %d, want %d", config.ImportConcurrency, DefaultImportConcurrency)
	}
}

func TestRunBaselineWithTraceFileWritesArtifactsAndComparison(t *testing.T) {
	dir := writeEvalFixture(t)
	tracesPath := filepath.Join(dir, "traces.jsonl")
	if err := writeJSONL(tracesPath, []RecallTrace{
		{
			CaseID:     "case-1",
			Query:      "What is alpha?",
			RankedRefs: []Ref{{Type: "fragment", ID: "frag-alpha", Rank: 1}},
			LatencyMS:  12,
		},
		{
			CaseID:     "case-2",
			Query:      "What is beta?",
			RankedRefs: []Ref{{Type: "fragment", ID: "frag-beta", Rank: 1}},
			LatencyMS:  15,
		},
	}); err != nil {
		t.Fatalf("write traces: %v", err)
	}
	out := filepath.Join(dir, "baseline-run")

	summary, err := Run(context.Background(), RunOptions{
		Mode:             "baseline",
		SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
		SuitePath:        filepath.Join(dir, "suite.jsonl"),
		TracesPath:       tracesPath,
		OutDir:           out,
		RunID:            "baseline-test",
	})
	if err != nil {
		t.Fatalf("Run baseline with traces: %v", err)
	}
	if summary.CaseCount != 2 || summary.ScoredCaseCount != 2 || summary.UnmappedSourceRefs != 2 {
		t.Fatalf("summary = %+v", summary)
	}
	for _, name := range []string{"knowledge_mapping.json", "recall_traces.jsonl", "retrieval_scores.jsonl"} {
		if !fileExists(filepath.Join(out, name)) {
			t.Fatalf("missing run artifact %s", name)
		}
	}

	comparison, err := CompareRunDirs(out, out, filepath.Join(dir, "comparison"))
	if err != nil {
		t.Fatalf("CompareRunDirs: %v", err)
	}
	if comparison.RecallDelta != 0 || comparison.MRRDelta != 0 || comparison.NDCGDelta != 0 || comparison.BadAtKDelta != 0 {
		t.Fatalf("comparison = %+v; want zero deltas", comparison)
	}
	if !fileExists(filepath.Join(dir, "comparison", "comparison.json")) {
		t.Fatal("missing comparison artifact")
	}
}

func TestRunBaselineWithTraceFileAndMappingScoresArtifacts(t *testing.T) {
	dir := writeEvalFixture(t)
	tracesPath := filepath.Join(dir, "traces.jsonl")
	if err := writeJSONL(tracesPath, []RecallTrace{
		{CaseID: "case-1", Query: "What is alpha?", RankedRefs: []Ref{{Type: "fragment", ID: "frag-alpha", Rank: 1}}},
		{CaseID: "case-2", Query: "What is beta?", RankedRefs: []Ref{{Type: "fragment", ID: "frag-beta", Rank: 1}}},
	}); err != nil {
		t.Fatalf("write traces: %v", err)
	}
	mapping := newKnowledgeMapping()
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: "frag-alpha", SourceDocID: "doc-alpha"}, true)
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: "frag-beta", SourceDocID: "doc-beta"}, true)
	mappingPath := filepath.Join(dir, "knowledge_mapping.json")
	if err := writeJSONFile(mappingPath, mapping); err != nil {
		t.Fatalf("write mapping: %v", err)
	}
	out := filepath.Join(dir, "mapped-run")

	summary, err := Run(context.Background(), RunOptions{
		Mode:             "baseline",
		SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
		SuitePath:        filepath.Join(dir, "suite.jsonl"),
		TracesPath:       tracesPath,
		MappingPath:      mappingPath,
		OutDir:           out,
		RunID:            "mapped-test",
	})
	if err != nil {
		t.Fatalf("Run baseline with mapped traces: %v", err)
	}
	if summary.AverageRecallAtK != 1 || summary.UnmappedSourceRefs != 0 {
		t.Fatalf("summary = %+v; want mapped trace recall", summary)
	}
	var runConfig RunConfig
	if err := readJSONFile(filepath.Join(out, "run_config.json"), &runConfig); err != nil {
		t.Fatalf("read run config: %v", err)
	}
	if runConfig.MappingPath != mappingPath {
		t.Fatalf("run config mapping path = %q, want %q", runConfig.MappingPath, mappingPath)
	}
	if runConfig.MappingHash == "" {
		t.Fatal("run config mapping hash is empty")
	}
	var persistedMapping KnowledgeMapping
	if err := readJSONFile(filepath.Join(out, "knowledge_mapping.json"), &persistedMapping); err != nil {
		t.Fatalf("read persisted mapping: %v", err)
	}
	persistedHash, err := canonicalJSONHash(persistedMapping)
	if err != nil {
		t.Fatalf("hash persisted mapping: %v", err)
	}
	if runConfig.MappingHash != persistedHash {
		t.Fatalf("run config mapping hash = %q, persisted hash = %q", runConfig.MappingHash, persistedHash)
	}
}

func TestRunWritesGateResultAndFailsThreshold(t *testing.T) {
	dir := writeEvalFixture(t)
	tracesPath := filepath.Join(dir, "traces.jsonl")
	if err := writeJSONL(tracesPath, []RecallTrace{
		{CaseID: "case-1", Query: "What is alpha?", RankedRefs: []Ref{{Type: "fragment", ID: "frag-alpha", Rank: 1}}},
		{CaseID: "case-2", Query: "What is beta?", RankedRefs: []Ref{{Type: "fragment", ID: "frag-beta", Rank: 1}}},
	}); err != nil {
		t.Fatalf("write traces: %v", err)
	}
	out := filepath.Join(dir, "gated-run")
	minRecall := 0.5

	summary, err := Run(context.Background(), RunOptions{
		Mode:             "baseline",
		SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
		SuitePath:        filepath.Join(dir, "suite.jsonl"),
		TracesPath:       tracesPath,
		OutDir:           out,
		RunID:            "gated-test",
		Gates:            GateOptions{MinRecallAtK: &minRecall},
	})
	if err == nil || !strings.Contains(err.Error(), "gate check failed") {
		t.Fatalf("Run gated err = %v; want gate failure", err)
	}
	if summary.AverageRecallAtK != 0 {
		t.Fatalf("summary = %+v; want trace file without mapping to score zero recall", summary)
	}
	for _, name := range []string{"summary.json", "retrieval_scores.jsonl", "gate_result.json"} {
		if !fileExists(filepath.Join(out, name)) {
			t.Fatalf("missing gated artifact %s", name)
		}
	}
}

func TestRunBaselineLiveHTTPFlow(t *testing.T) {
	dir := writeV2EvalImportFixture(t)
	rememberIDs := map[string]string{
		"eval:doc-alpha": "frag-alpha",
		"eval:doc-beta":  "frag-beta",
	}
	var rememberCalls int
	var recallCalls int
	var callsMu sync.Mutex

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
			callsMu.Lock()
			rememberCalls++
			callsMu.Unlock()
			submissionID := strings.TrimPrefix(idempotencyKey, "eval:")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"contract_version":     "dense-mem.v2.6.2",
				"submission_id":        submissionID,
				"submission_kind":      "remember",
				"processing_state":     "completed",
				"search_state":         "current",
				"correlation_id":       "correlation-" + submissionID,
				"evidence":             []map[string]any{{"disposition": "stored", "evidence_id": rememberIDs["eval:"+submissionID], "evidence_index": 0, "superseded_evidence_ids": []string{}, "search_state": "current"}},
				"relationship_results": []map[string]any{}, "errors": []map[string]any{},
			})
		case "tool:eval_list_knowledge_refs":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{"id": "frag-alpha", "metadata": map[string]any{"source_doc_id": "doc-alpha"}},
					{"id": "frag-beta", "metadata": map[string]any{"source_doc_id": "doc-beta"}},
				},
				"next_cursor": "",
				"has_more":    false,
			})
		case "tool:eval_run_recall_case":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode recall body: %v", err)
			}
			callsMu.Lock()
			recallCalls++
			callsMu.Unlock()
			caseID, _ := input["case_id"].(string)
			refID := "frag-alpha"
			if caseID == "case-2" {
				refID = "frag-beta"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"query": input["query"],
				"ranked_refs": []map[string]any{{
					"type": "fragment",
					"id":   refID,
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
		RunID:            "live-baseline-test",
	})
	if err != nil {
		t.Fatalf("Run live baseline: %v", err)
	}
	if summary.ScoredCaseCount != 2 || summary.AverageRecallAtK != 1 || summary.AverageMRR != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	callsMu.Lock()
	gotRememberCalls, gotRecallCalls := rememberCalls, recallCalls
	callsMu.Unlock()
	if gotRememberCalls != 2 || gotRecallCalls != 2 {
		t.Fatalf("remember/recall calls = %d/%d", gotRememberCalls, gotRecallCalls)
	}
}

func TestRunRejectsInvalidOptionsAndSuite(t *testing.T) {
	dir := writeEvalFixture(t)
	for _, tc := range []struct {
		name string
		opts RunOptions
	}{
		{name: "bad mode", opts: RunOptions{Mode: "smoke"}},
		{name: "missing seed", opts: RunOptions{Mode: "validate", SuitePath: filepath.Join(dir, "suite.jsonl")}},
		{name: "missing suite", opts: RunOptions{Mode: "validate", SeedManifestPath: filepath.Join(dir, "seed_manifest.json")}},
		{name: "import concurrency over provider cap", opts: RunOptions{
			Mode:              "validate",
			SeedManifestPath:  filepath.Join(dir, "seed_manifest.json"),
			SuitePath:         filepath.Join(dir, "suite.jsonl"),
			ImportConcurrency: MaxImportConcurrency + 1,
		}},
		{name: "resume outside import mode", opts: RunOptions{
			Mode:                   "validate",
			SeedManifestPath:       filepath.Join(dir, "seed_manifest.json"),
			SuitePath:              filepath.Join(dir, "suite.jsonl"),
			ResumeSourceDocIDsPath: filepath.Join(dir, "resume.txt"),
		}},
	} {
		if _, err := Run(context.Background(), tc.opts); err == nil {
			t.Fatalf("%s: Run error = nil", tc.name)
		}
	}

	missingSuite := filepath.Join(dir, "suite-missing.jsonl")
	if err := writeJSONL(missingSuite, []SuiteCase{{CaseID: "case-missing"}}); err != nil {
		t.Fatalf("write missing suite: %v", err)
	}
	if _, err := Run(context.Background(), RunOptions{
		Mode:             "validate",
		SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
		SuitePath:        missingSuite,
	}); err == nil {
		t.Fatal("Run with missing suite case error = nil")
	}

	summary, err := Run(context.Background(), RunOptions{
		Mode:             "validate",
		SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
		SuitePath:        filepath.Join(dir, "suite.jsonl"),
	})
	if err != nil {
		t.Fatalf("Run validate with generated run id: %v", err)
	}
	if summary.RunID == "" {
		t.Fatal("generated RunID is empty")
	}
}

func TestRunValidationRejectsCrossFileInconsistency(t *testing.T) {
	t.Run("manifest count mismatch", func(t *testing.T) {
		dir := writeEvalFixture(t)
		manifest := SeedManifest{
			SchemaVersion: SeedSchemaVersion,
			SeedID:        "fixture",
			CorpusFile:    "corpus.jsonl",
			CasesFile:     "cases.jsonl",
			QrelsFile:     "qrels.jsonl",
			Counts:        map[string]int{"cases": 3},
		}
		if err := writeJSONFile(filepath.Join(dir, "seed_manifest.json"), manifest); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
		_, err := Run(context.Background(), RunOptions{
			Mode:             "validate",
			SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
			SuitePath:        filepath.Join(dir, "suite.jsonl"),
		})
		if err == nil || !strings.Contains(err.Error(), "manifest count cases=3 mismatch") {
			t.Fatalf("Run err = %v; want count mismatch", err)
		}
	})

	t.Run("qrel source missing from corpus", func(t *testing.T) {
		dir := writeEvalFixture(t)
		if err := writeJSONL(filepath.Join(dir, "qrels.jsonl"), []QRel{
			{CaseID: "case-1", RequiredRefs: []Ref{{Type: "fragment", SourceDocID: "doc-missing", Grade: 1}}},
			{CaseID: "case-2", RequiredRefs: []Ref{{Type: "fragment", SourceDocID: "doc-beta", Grade: 1}}},
		}); err != nil {
			t.Fatalf("write qrels: %v", err)
		}
		_, err := Run(context.Background(), RunOptions{
			Mode:             "validate",
			SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
			SuitePath:        filepath.Join(dir, "suite.jsonl"),
		})
		if err == nil || !strings.Contains(err.Error(), `source_doc_id "doc-missing" missing from corpus`) {
			t.Fatalf("Run err = %v; want missing source_doc_id", err)
		}
	})

	t.Run("adversarial case missing bad refs", func(t *testing.T) {
		dir := writeEvalFixture(t)
		if err := writeJSONL(filepath.Join(dir, "cases.jsonl"), []Case{
			{CaseID: "case-1", Query: "What is alpha?", Slices: []string{"adversarial"}, Limit: 2},
			{CaseID: "case-2", Query: "What is beta?", Slices: []string{"direct"}, Limit: 2},
		}); err != nil {
			t.Fatalf("write cases: %v", err)
		}
		_, err := Run(context.Background(), RunOptions{
			Mode:             "validate",
			SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
			SuitePath:        filepath.Join(dir, "suite.jsonl"),
		})
		if err == nil || !strings.Contains(err.Error(), `adversarial case "case-1" has no bad_refs`) {
			t.Fatalf("Run err = %v; want adversarial bad_refs error", err)
		}
	})

	t.Run("public six axis seed requires validation report", func(t *testing.T) {
		dir := writeEvalFixture(t)
		manifest := SeedManifest{
			SchemaVersion: SeedSchemaVersion,
			SeedID:        "public_6axis_1k_v1",
			CorpusFile:    "corpus.jsonl",
			CasesFile:     "cases.jsonl",
			QrelsFile:     "qrels.jsonl",
		}
		if err := writeJSONFile(filepath.Join(dir, "seed_manifest.json"), manifest); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
		_, err := Run(context.Background(), RunOptions{
			Mode:             "validate",
			SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
			SuitePath:        filepath.Join(dir, "suite.jsonl"),
		})
		if err == nil || !strings.Contains(err.Error(), `requires validation_report_file`) {
			t.Fatalf("Run err = %v; want validation_report_file error", err)
		}
	})

	t.Run("validation report hash must match current seed", func(t *testing.T) {
		dir := writeEvalFixture(t)
		manifest := SeedManifest{
			SchemaVersion:        SeedSchemaVersion,
			SeedID:               "public_6axis_1k_v1",
			CorpusFile:           "corpus.jsonl",
			CasesFile:            "cases.jsonl",
			QrelsFile:            "qrels.jsonl",
			ValidationReportFile: "validation_report.json",
		}
		if err := writeJSONFile(filepath.Join(dir, "seed_manifest.json"), manifest); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
		report := seedValidationReport{
			SchemaVersion: "dense-mem.eval.validation.v1",
			SeedID:        "public_6axis_1k_v1",
			Status:        "passed",
			SeedHash:      "sha256:stale",
		}
		if err := writeJSONFile(filepath.Join(dir, "validation_report.json"), report); err != nil {
			t.Fatalf("write validation report: %v", err)
		}
		_, err := Run(context.Background(), RunOptions{
			Mode:             "validate",
			SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
			SuitePath:        filepath.Join(dir, "suite.jsonl"),
		})
		if err == nil || !strings.Contains(err.Error(), "does not match current seed hash") {
			t.Fatalf("Run err = %v; want validation seed hash mismatch", err)
		}
	})
}

func TestRunValidateLoadsAndValidatesExpectedDreams(t *testing.T) {
	dir := t.TempDir()
	manifest := SeedManifest{
		SchemaVersion: SeedSchemaVersion,
		SeedID:        "dream-fixture",
		CorpusFile:    "corpus.jsonl",
		CasesFile:     "cases.jsonl",
		QrelsFile:     "qrels.jsonl",
		DreamsFile:    "expected_dreams.jsonl",
		Counts: map[string]int{
			"cases":         1,
			"corpus":        2,
			"docs_per_case": 2,
			"qrels":         1,
			"dreams":        1,
		},
	}
	if err := writeJSONFile(filepath.Join(dir, "seed_manifest.json"), manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := writeJSONL(filepath.Join(dir, "corpus.jsonl"), []CorpusItem{
		{SourceDocID: "doc-employer", Content: "Employer fact source."},
		{SourceDocID: "doc-location", Content: "Location fact source."},
	}); err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	if err := writeJSONL(filepath.Join(dir, "cases.jsonl"), []Case{
		{CaseID: "case-dream", Query: "which hypothesis?", IncludeDreams: true},
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

	summary, err := Run(context.Background(), RunOptions{
		Mode:             "validate",
		SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
		SuitePath:        filepath.Join(dir, "suite.jsonl"),
		OutDir:           filepath.Join(dir, "run"),
	})
	if err != nil {
		t.Fatalf("Run validate with expected dreams: %v", err)
	}
	if summary.CaseCount != 1 || summary.SeedHash == "" {
		t.Fatalf("summary = %+v", summary)
	}

	if err := writeJSONL(filepath.Join(dir, "qrels.jsonl"), []QRel{{
		CaseID:            "case-dream",
		RequiredRefs:      []Ref{{Type: "fragment", SourceDocID: "doc-employer", Grade: 1}},
		RequiredDreamRefs: []Ref{{Type: "dream", SourceDocID: "missing-dream", Grade: 1}},
	}}); err != nil {
		t.Fatalf("rewrite qrels: %v", err)
	}
	_, err = Run(context.Background(), RunOptions{
		Mode:             "validate",
		SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
		SuitePath:        filepath.Join(dir, "suite.jsonl"),
		OutDir:           filepath.Join(dir, "run-invalid"),
	})
	if err == nil || !strings.Contains(err.Error(), "missing-dream") {
		t.Fatalf("Run validate missing expected dream err = %v", err)
	}
}

func TestRunLiveSuiteAndCompareErrorBranches(t *testing.T) {
	_, err := runLiveSuite(context.Background(), &HTTPClient{}, []SuiteCase{{CaseID: "case-1"}}, map[string]Case{
		"case-1": {CaseID: "case-1", Query: "query"},
	})
	if err == nil {
		t.Fatal("runLiveSuite error = nil")
	}

	dir := t.TempDir()
	baselineDir := filepath.Join(dir, "baseline")
	candidateDir := filepath.Join(dir, "candidate")
	if err := writeJSONFile(filepath.Join(baselineDir, "summary.json"), Summary{SeedHash: "sha256:a"}); err != nil {
		t.Fatalf("write baseline summary: %v", err)
	}
	if err := writeJSONFile(filepath.Join(candidateDir, "summary.json"), Summary{SeedHash: "sha256:b"}); err != nil {
		t.Fatalf("write candidate summary: %v", err)
	}
	if _, err := CompareRunDirs(baselineDir, candidateDir, ""); err == nil {
		t.Fatal("CompareRunDirs seed mismatch error = nil")
	}
}

func TestScoreTracesUsesMappingAndPenalizesUnmappedRefs(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1", Slices: []string{"direct"}}, {CaseID: "case-2", Slices: []string{"direct"}}}
	cases := map[string]Case{
		"case-1": {CaseID: "case-1", Query: "alpha?", Limit: 2},
		"case-2": {CaseID: "case-2", Query: "missing?", Limit: 2},
	}
	qrels := map[string]QRel{
		"case-1": {
			CaseID:       "case-1",
			RequiredRefs: []Ref{{Type: "fragment", SourceDocID: "doc-alpha", Grade: 2}},
			BadRefs:      []Ref{{Type: "fragment", SourceDocID: "doc-beta"}},
		},
		"case-2": {
			CaseID:       "case-2",
			RequiredRefs: []Ref{{Type: "fragment", SourceDocID: "doc-unmapped", Grade: 1}},
		},
	}
	traces := []RecallTrace{
		{
			CaseID:     "case-1",
			Query:      "alpha?",
			RankedRefs: []Ref{{Type: "fragment", ID: "frag-alpha"}, {Type: "fragment", ID: "frag-beta"}},
			LatencyMS:  10,
		},
		{
			CaseID:     "case-2",
			Query:      "missing?",
			RankedRefs: []Ref{{Type: "fragment", ID: "frag-alpha"}},
			LatencyMS:  11,
		},
	}
	mapping := KnowledgeMapping{BySourceDocID: map[string]Ref{
		"doc-alpha": {Type: "fragment", ID: "frag-alpha", SourceDocID: "doc-alpha"},
		"doc-beta":  {Type: "fragment", ID: "frag-beta", SourceDocID: "doc-beta"},
	}}

	scores, summary, err := ScoreTraces("run-1", "baseline", "seed-1", "sha256:test", "suite.jsonl", suite, cases, qrels, traces, mapping)
	if err != nil {
		t.Fatalf("ScoreTraces: %v", err)
	}
	if len(scores) != 2 {
		t.Fatalf("scores = %d; want 2", len(scores))
	}
	if scores[0].RecallAtK != 1 || scores[0].BadAtK != 1 || len(scores[0].MissingRequired) != 0 {
		t.Fatalf("score[0] = %+v", scores[0])
	}
	if scores[0].FirstRequiredRank != 1 || scores[0].FirstBadRank != 2 {
		t.Fatalf("score[0] ranks = %+v", scores[0])
	}
	if scores[1].RelevantTotal != 1 || scores[1].RecallAtK != 0 || len(scores[1].UnmappedSourceRefs) != 1 {
		t.Fatalf("score[1] = %+v", scores[1])
	}
	if summary.AverageRecallAtK != 0.5 || summary.AverageBadAtK != 0.5 || summary.RequiredRank1Rate != 0.5 || summary.BadRank1Rate != 0 || summary.UnmappedSourceRefs != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.Slices["direct"].CaseCount != 2 {
		t.Fatalf("slice summary = %+v", summary.Slices)
	}
}

func TestScoreTracesScoresContextRefsSeparately(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1", Slices: []string{"context"}}}
	cases := map[string]Case{
		"case-1": {CaseID: "case-1", Query: "alpha?", Limit: 2},
	}
	qrels := map[string]QRel{
		"case-1": {
			CaseID:       "case-1",
			RequiredRefs: []Ref{{Type: "fragment", SourceDocID: "doc-alpha"}},
			BadRefs:      []Ref{{Type: "fragment", SourceDocID: "doc-beta"}},
		},
	}
	traces := []RecallTrace{{
		CaseID:      "case-1",
		Query:       "alpha?",
		RankedRefs:  []Ref{{Type: "fragment", ID: "frag-alpha"}},
		ContextRefs: []Ref{{Type: "fragment", ID: "frag-beta"}},
	}}
	mapping := KnowledgeMapping{BySourceDocID: map[string]Ref{
		"doc-alpha": {Type: "fragment", ID: "frag-alpha", SourceDocID: "doc-alpha"},
		"doc-beta":  {Type: "fragment", ID: "frag-beta", SourceDocID: "doc-beta"},
	}}

	scores, summary, err := ScoreTraces("run-1", "baseline", "seed-1", "sha256:test", "suite.jsonl", suite, cases, qrels, traces, mapping)
	if err != nil {
		t.Fatalf("ScoreTraces: %v", err)
	}
	score := scores[0]
	if score.RecallAtK != 1 || score.BadAtK != 0 || score.FirstRequiredRank != 1 {
		t.Fatalf("ranked score = %+v", score)
	}
	if !score.ContextScored || score.ContextRecallAtK != 0 || score.ContextBadAtK != 1 || score.ContextFirstBadRank != 1 {
		t.Fatalf("context score = %+v", score)
	}
	if len(score.ContextMissingRequired) != 1 || len(score.ContextBadRefsAtK) != 1 {
		t.Fatalf("context miss/bad refs = %+v", score)
	}
	if summary.AverageRecallAtK != 1 || summary.AverageBadAtK != 0 {
		t.Fatalf("ranked summary = %+v", summary)
	}
	if summary.ContextScoredCaseCount != 1 || summary.AverageContextRecallAtK != 0 || summary.AverageContextBadAtK != 1 || summary.ContextBadRank1Rate != 1 {
		t.Fatalf("context summary = %+v", summary)
	}
	if summary.Slices["context"].ContextScoredCaseCount != 1 || summary.Slices["context"].AverageContextBadAtK != 1 {
		t.Fatalf("slice summary = %+v", summary.Slices)
	}
}

func TestScoreTracesScoresFactQRelsThroughSameSourceFragments(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1", Slices: []string{"project_graph"}}}
	cases := map[string]Case{
		"case-1": {CaseID: "case-1", Query: "which ADR applies?", Limit: 2},
	}
	qrels := map[string]QRel{
		"case-1": {
			CaseID:       "case-1",
			RequiredRefs: []Ref{{Type: "fact", SourceDocID: "doc-required:fact:1", Grade: 3}},
			BadRefs:      []Ref{{Type: "fact", SourceDocID: "doc-stale:fact:1", Grade: -1}},
		},
	}
	traces := []RecallTrace{{
		CaseID:      "case-1",
		Query:       "which ADR applies?",
		RankedRefs:  []Ref{{Type: "fragment", ID: "fragment-required"}, {Type: "fragment", ID: "fragment-stale"}},
		ContextRefs: []Ref{{Type: "fragment", ID: "fragment-required"}, {Type: "fragment", ID: "fragment-stale"}},
	}}
	mapping := newKnowledgeMapping()
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: "fragment-required", SourceDocID: "doc-required"}, true)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-required", SourceDocID: "doc-required"}, false)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-required", SourceDocID: "doc-required:fact:1"}, false)
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: "fragment-stale", SourceDocID: "doc-stale"}, true)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-stale", SourceDocID: "doc-stale"}, false)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-stale", SourceDocID: "doc-stale:fact:1"}, false)

	scores, summary, err := ScoreTraces("run-1", "candidate", "seed-1", "sha256:test", "suite.jsonl", suite, cases, qrels, traces, mapping)
	if err != nil {
		t.Fatalf("ScoreTraces: %v", err)
	}
	score := scores[0]
	if score.RecallAtK != 1 || score.BadAtK != 1 || score.FirstRequiredRank != 1 || score.FirstBadRank != 2 {
		t.Fatalf("fragment-backed fact score = %+v", score)
	}
	if len(score.MissingRequired) != 0 || len(score.BadRefsAtK) != 1 {
		t.Fatalf("fragment-backed fact refs = %+v", score)
	}
	if !score.ContextScored || score.ContextRecallAtK != 1 || score.ContextBadAtK != 1 {
		t.Fatalf("fragment-backed context score = %+v", score)
	}
	if summary.AverageRecallAtK != 1 || summary.AverageBadAtK != 1 || summary.Slices["project_graph"].AverageRecallAtK != 1 {
		t.Fatalf("fragment-backed fact summary = %+v", summary)
	}
}

func TestScoreTracesDoesNotMapAmbiguousFragmentsToFactQRels(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1"}}
	cases := map[string]Case{"case-1": {CaseID: "case-1", Query: "which fact?", Limit: 1}}
	qrels := map[string]QRel{"case-1": {
		CaseID:       "case-1",
		RequiredRefs: []Ref{{Type: "fact", SourceDocID: "doc-multi:fact:2"}},
	}}
	traces := []RecallTrace{{
		CaseID:     "case-1",
		Query:      "which fact?",
		RankedRefs: []Ref{{Type: "fragment", ID: "fragment-multi"}},
	}}
	mapping := newKnowledgeMapping()
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: "fragment-multi", SourceDocID: "doc-multi"}, true)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-1", SourceDocID: "doc-multi"}, false)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-2", SourceDocID: "doc-multi"}, false)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-2", SourceDocID: "doc-multi:fact:2"}, false)

	scores, _, err := ScoreTraces("run-1", "candidate", "seed-1", "sha256:test", "suite.jsonl", suite, cases, qrels, traces, mapping)
	if err != nil {
		t.Fatalf("ScoreTraces: %v", err)
	}
	if scores[0].RecallAtK != 0 || scores[0].FirstRequiredRank != 0 || len(scores[0].MissingRequired) != 1 {
		t.Fatalf("ambiguous fragment fact score = %+v", scores[0])
	}
}

func TestScoreTracesScoresContextEvidenceRefsSeparately(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1", Slices: []string{"evidence"}}}
	cases := map[string]Case{
		"case-1": {CaseID: "case-1", Query: "who owns alpha?", Limit: 2},
	}
	qrels := map[string]QRel{
		"case-1": {
			CaseID:               "case-1",
			RequiredRefs:         []Ref{{Type: "fact", SourceDocID: "doc-fact"}},
			RequiredEvidenceRefs: []Ref{{Type: "fragment", SourceDocID: "doc-source-good"}},
			BadEvidenceRefs:      []Ref{{Type: "fragment", SourceDocID: "doc-source-bad"}},
		},
	}
	traces := []RecallTrace{{
		CaseID:              "case-1",
		Query:               "who owns alpha?",
		RankedRefs:          []Ref{{Type: "fact", ID: "fact-alpha"}},
		ContextRefs:         []Ref{{Type: "fact", ID: "fact-alpha"}},
		ContextEvidenceRefs: []Ref{{Type: "fragment", ID: "fragment-good"}, {Type: "fragment", ID: "fragment-bad"}},
	}}
	mapping := KnowledgeMapping{BySourceDocID: map[string]Ref{
		"doc-fact":        {Type: "fact", ID: "fact-alpha", SourceDocID: "doc-fact"},
		"doc-source-good": {Type: "fragment", ID: "fragment-good", SourceDocID: "doc-source-good"},
		"doc-source-bad":  {Type: "fragment", ID: "fragment-bad", SourceDocID: "doc-source-bad"},
	}}

	scores, summary, err := ScoreTraces("run-1", "baseline", "seed-1", "sha256:test", "suite.jsonl", suite, cases, qrels, traces, mapping)
	if err != nil {
		t.Fatalf("ScoreTraces: %v", err)
	}
	score := scores[0]
	if score.RecallAtK != 1 || score.ContextRecallAtK != 1 || score.ContextBadAtK != 0 {
		t.Fatalf("top-level score = %+v", score)
	}
	if !score.EvidenceScored || score.EvidenceRecallAtK != 1 || score.EvidenceBadAtK != 1 || score.EvidenceFirstRequiredRank != 1 || score.EvidenceFirstBadRank != 2 {
		t.Fatalf("evidence score = %+v", score)
	}
	if len(score.EvidenceBadRefsAtK) != 1 || len(score.EvidenceMissingRequired) != 0 {
		t.Fatalf("evidence refs = %+v", score)
	}
	if summary.EvidenceScoredCaseCount != 1 || summary.AverageEvidenceRecallAtK != 1 || summary.AverageEvidenceBadAtK != 1 || summary.EvidenceRequiredRank1Rate != 1 {
		t.Fatalf("evidence summary = %+v", summary)
	}
	if summary.Slices["evidence"].EvidenceScoredCaseCount != 1 || summary.Slices["evidence"].AverageEvidenceBadAtK != 1 {
		t.Fatalf("slice summary = %+v", summary.Slices)
	}
}

func TestScoreTracesScoresFragmentContextRefsAsEvidenceFallback(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1", Slices: []string{"evidence"}}}
	cases := map[string]Case{
		"case-1": {CaseID: "case-1", Query: "which source backs alpha?", Limit: 2},
	}
	qrels := map[string]QRel{
		"case-1": {
			CaseID:               "case-1",
			RequiredRefs:         []Ref{{Type: "fragment", SourceDocID: "doc-source-good"}},
			RequiredEvidenceRefs: []Ref{{Type: "fragment", SourceDocID: "doc-source-good"}},
			BadEvidenceRefs:      []Ref{{Type: "fragment", SourceDocID: "doc-source-bad"}},
		},
	}
	traces := []RecallTrace{{
		CaseID:      "case-1",
		Query:       "which source backs alpha?",
		RankedRefs:  []Ref{{Type: "fragment", ID: "fragment-good"}},
		ContextRefs: []Ref{{Type: "fragment", ID: "fragment-good"}, {Type: "fragment", ID: "fragment-bad"}},
	}}
	mapping := KnowledgeMapping{BySourceDocID: map[string]Ref{
		"doc-source-good": {Type: "fragment", ID: "fragment-good", SourceDocID: "doc-source-good"},
		"doc-source-bad":  {Type: "fragment", ID: "fragment-bad", SourceDocID: "doc-source-bad"},
	}}

	scores, summary, err := ScoreTraces("run-1", "baseline", "seed-1", "sha256:test", "suite.jsonl", suite, cases, qrels, traces, mapping)
	if err != nil {
		t.Fatalf("ScoreTraces: %v", err)
	}
	score := scores[0]
	if !score.EvidenceScored || score.EvidenceRecallAtK != 1 || score.EvidenceBadAtK != 1 || score.EvidenceFirstRequiredRank != 1 || score.EvidenceFirstBadRank != 2 {
		t.Fatalf("fallback evidence score = %+v", score)
	}
	if len(score.EvidenceMissingRequired) != 0 || len(score.EvidenceBadRefsAtK) != 1 {
		t.Fatalf("fallback evidence refs = %+v", score)
	}
	if summary.AverageEvidenceRecallAtK != 1 || summary.AverageEvidenceBadAtK != 1 || summary.Slices["evidence"].AverageEvidenceRecallAtK != 1 {
		t.Fatalf("fallback evidence summary = %+v", summary)
	}
}

func TestScoreTracesKeepsExplicitEmptyContextEvidenceRefs(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1", Slices: []string{"evidence"}}}
	cases := map[string]Case{
		"case-1": {CaseID: "case-1", Query: "which source backs alpha?", Limit: 2},
	}
	qrels := map[string]QRel{
		"case-1": {
			CaseID:               "case-1",
			RequiredEvidenceRefs: []Ref{{Type: "fragment", SourceDocID: "doc-source-good"}},
		},
	}
	traces := []RecallTrace{{
		CaseID:              "case-1",
		Query:               "which source backs alpha?",
		ContextRefs:         []Ref{{Type: "fragment", ID: "fragment-good"}},
		ContextEvidenceRefs: []Ref{},
	}}
	mapping := KnowledgeMapping{BySourceDocID: map[string]Ref{
		"doc-source-good": {Type: "fragment", ID: "fragment-good", SourceDocID: "doc-source-good"},
	}}

	scores, summary, err := ScoreTraces("run-1", "baseline", "seed-1", "sha256:test", "suite.jsonl", suite, cases, qrels, traces, mapping)
	if err != nil {
		t.Fatalf("ScoreTraces: %v", err)
	}
	score := scores[0]
	if !score.EvidenceScored || score.EvidenceRecallAtK != 0 || len(score.EvidenceMissingRequired) != 1 {
		t.Fatalf("explicit empty evidence score = %+v; want scored miss without context fallback", score)
	}
	if summary.AverageEvidenceRecallAtK != 0 {
		t.Fatalf("explicit empty evidence summary = %+v; want no evidence recall", summary)
	}
}

func TestScoreTracesScoresDreamRefsSeparately(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1", Slices: []string{"dream_neighbor_hypothesis"}}}
	cases := map[string]Case{
		"case-1": {CaseID: "case-1", Query: "which hypothesis?", Limit: 2, IncludeDreams: true},
	}
	qrels := map[string]QRel{
		"case-1": {
			CaseID:            "case-1",
			RequiredRefs:      []Ref{{Type: "fact", SourceDocID: "doc-fact"}},
			RequiredDreamRefs: []Ref{{Type: "dream", SourceDocID: "dream-good"}},
			BadDreamRefs:      []Ref{{Type: "dream", SourceDocID: "dream-bad"}},
		},
	}
	traces := []RecallTrace{{
		CaseID:     "case-1",
		Query:      "which hypothesis?",
		RankedRefs: []Ref{{Type: "fact", ID: "fact-1"}},
		DreamRefs:  []Ref{{Type: "dream", ID: "dream-1"}, {Type: "dream", ID: "dream-bad-1"}},
	}}
	mapping := KnowledgeMapping{BySourceDocID: map[string]Ref{
		"doc-fact":   {Type: "fact", ID: "fact-1", SourceDocID: "doc-fact"},
		"dream-good": {Type: "dream", ID: "dream-1", SourceDocID: "dream-good"},
		"dream-bad":  {Type: "dream", ID: "dream-bad-1", SourceDocID: "dream-bad"},
	}}

	scores, summary, err := ScoreTraces("run-1", "baseline", "seed-1", "sha256:test", "suite.jsonl", suite, cases, qrels, traces, mapping)
	if err != nil {
		t.Fatalf("ScoreTraces: %v", err)
	}
	score := scores[0]
	if score.RecallAtK != 1 || score.BadAtK != 0 {
		t.Fatalf("memory score = %+v", score)
	}
	if !score.DreamScored || score.DreamRecallAtK != 1 || score.DreamBadAtK != 1 || score.DreamFirstRequiredRank != 1 || score.DreamFirstBadRank != 2 {
		t.Fatalf("dream score = %+v", score)
	}
	if summary.DreamScoredCaseCount != 1 || summary.AverageDreamRecallAtK != 1 || summary.AverageDreamBadAtK != 1 || summary.DreamRequiredRank1Rate != 1 {
		t.Fatalf("dream summary = %+v", summary)
	}
	if summary.Slices["dream_neighbor_hypothesis"].DreamScoredCaseCount != 1 || summary.Slices["dream_neighbor_hypothesis"].AverageDreamBadAtK != 1 {
		t.Fatalf("slice summary = %+v", summary.Slices)
	}
}

func writeEvalFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	manifest := SeedManifest{
		SchemaVersion: SeedSchemaVersion,
		SeedID:        "fixture",
		CorpusFile:    "corpus.jsonl",
		CasesFile:     "cases.jsonl",
		QrelsFile:     "qrels.jsonl",
	}
	if err := writeJSONFile(filepath.Join(dir, "seed_manifest.json"), manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := writeJSONL(filepath.Join(dir, "corpus.jsonl"), []CorpusItem{
		{SourceDocID: "doc-alpha", Content: "Alpha is the first Greek letter."},
		{SourceDocID: "doc-beta", Content: "Beta is the second Greek letter."},
	}); err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	if err := writeJSONL(filepath.Join(dir, "cases.jsonl"), []Case{
		{CaseID: "case-1", Query: "What is alpha?", Slices: []string{"direct"}, Limit: 2},
		{CaseID: "case-2", Query: "What is beta?", Slices: []string{"direct"}, Limit: 2},
	}); err != nil {
		t.Fatalf("write cases: %v", err)
	}
	if err := writeJSONL(filepath.Join(dir, "qrels.jsonl"), []QRel{
		{CaseID: "case-1", RequiredRefs: []Ref{{Type: "source_doc", SourceDocID: "doc-alpha", Grade: 1}}},
		{CaseID: "case-2", RequiredRefs: []Ref{{Type: "source_doc", SourceDocID: "doc-beta", Grade: 1}}},
	}); err != nil {
		t.Fatalf("write qrels: %v", err)
	}
	if err := writeJSONL(filepath.Join(dir, "suite.jsonl"), []SuiteCase{
		{CaseID: "case-1"},
		{CaseID: "case-2"},
	}); err != nil {
		t.Fatalf("write suite: %v", err)
	}
	return dir
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
