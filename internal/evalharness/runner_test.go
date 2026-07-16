package evalharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

func TestRunPreservesDeterministicArtifactsWhenAIJudgeFails(t *testing.T) {
	dir := writeEvalFixture(t)
	tracesPath := filepath.Join(dir, "traces-with-initial-response.jsonl")
	recallResponse := map[string]any{"results": []any{}, "discovery_paths": []any{}, "discovery_guidance": "focus follow-up", "related_hypotheses": []any{}}
	if err := writeJSONL(tracesPath, []RecallTrace{
		{CaseID: "case-1", Query: "What is alpha?", InitialResponse: recallResponse},
		{CaseID: "case-2", Query: "What is beta?", InitialResponse: recallResponse},
	}); err != nil {
		t.Fatalf("write traces: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"judge model unavailable"}}`, http.StatusBadRequest)
	}))
	defer server.Close()
	out := filepath.Join(dir, "judge-failure-run")

	summary, err := Run(context.Background(), RunOptions{
		Mode:             "baseline",
		SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
		SuitePath:        filepath.Join(dir, "suite.jsonl"),
		TracesPath:       tracesPath,
		OutDir:           out,
		RunID:            "judge-failure",
		Judge: JudgeOptions{
			Enabled:     true,
			BaseURL:     server.URL,
			APIKey:      "judge-key",
			Model:       "gpt-5.5",
			Concurrency: 1,
			Timeout:     time.Second,
		},
	})

	if err == nil || !strings.Contains(err.Error(), "judge model unavailable") {
		t.Fatalf("Run error = %v", err)
	}
	if summary.ScoredCaseCount != 2 {
		t.Fatalf("summary = %+v", summary)
	}
	for _, name := range []string{"summary.json", "retrieval_scores.jsonl", "recall_traces.jsonl", "judge_scores.jsonl", "judge_error.json"} {
		if !fileExists(filepath.Join(out, name)) {
			t.Fatalf("missing preserved artifact %s", name)
		}
	}
	if fileExists(filepath.Join(out, "judge_summary.json")) {
		t.Fatal("judge summary should not exist after judge failure")
	}
}

func TestCompareRunDirsIncludesMatchedAIJudge(t *testing.T) {
	dir := t.TempDir()
	baselineDir := filepath.Join(dir, "baseline")
	candidateDir := filepath.Join(dir, "candidate")
	require.NoError(t, writeJSONFile(filepath.Join(baselineDir, "summary.json"), Summary{RunID: "v1", SeedHash: "sha256:same", AverageRecallAtK: 0.8}))
	require.NoError(t, writeJSONFile(filepath.Join(candidateDir, "summary.json"), Summary{RunID: "v2", SeedHash: "sha256:same", AverageRecallAtK: 0.9}))
	require.NoError(t, writeJSONFile(filepath.Join(baselineDir, "judge_summary.json"), JudgeSummary{
		Model: "gpt-5.5", CaseCount: 10, PassRate: 0.6, AverageAnswerabilityScore: 3.5,
		AverageRelevanceScore: 4.0, AverageCompletenessScore: 3.0, AverageFaithfulnessScore: 4.2,
	}))
	require.NoError(t, writeJSONFile(filepath.Join(candidateDir, "judge_summary.json"), JudgeSummary{
		Model: "gpt-5.5", CaseCount: 10, PassRate: 0.8, AverageAnswerabilityScore: 4.0,
		AverageRelevanceScore: 4.3, AverageCompletenessScore: 3.8, AverageFaithfulnessScore: 4.4,
	}))

	comparison, err := CompareRunDirs(baselineDir, candidateDir, filepath.Join(dir, "comparison"))

	require.NoError(t, err)
	require.True(t, comparison.AIJudgeCompared)
	require.Equal(t, "gpt-5.5", comparison.AIJudgeModel)
	require.InDelta(t, 0.2, comparison.JudgePassRateDelta, 1e-9)
	require.InDelta(t, 0.8, comparison.JudgeCompletenessDelta, 1e-9)
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
	dir := writeEvalFixture(t)
	rememberIDs := map[string]string{
		"eval:doc-alpha": "frag-alpha",
		"eval:doc-beta":  "frag-beta",
	}
	var rememberCalls int
	var placementPolls int
	var recallCalls int
	var controlPatched bool
	placementIngests := map[string]bool{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/control/api/config/evaluation":
			if r.Method != http.MethodPatch || r.Header.Get("Authorization") != "Bearer control-token" {
				t.Fatalf("control request = %s auth %q", r.Method, r.Header.Get("Authorization"))
			}
			var body map[string][]map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode control body: %v", err)
			}
			if body["items"][1]["value"] != "50" {
				t.Fatalf("control body = %#v", body)
			}
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
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode placement body: %v", err)
			}
			ingestID, _ := input["ingest_id"].(string)
			if ingestID != "doc-alpha" && ingestID != "doc-beta" {
				t.Fatalf("placement input = %#v", input)
			}
			placementIngests[ingestID] = true
			placementPolls++
			relationshipID := "relationship-alpha"
			evidenceID := "frag-alpha"
			if ingestID == "doc-beta" {
				relationshipID = "relationship-beta"
				evidenceID = "frag-beta"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"placement": map[string]any{
					"status": "completed",
					"items": []map[string]any{{
						"fragment_id":   evidenceID,
						"source_doc_id": ingestID,
						"relationship_outcomes": []map[string]any{{
							"relationship_id": relationshipID,
							"tier":            "fact",
						}},
					}},
				},
			})
		case "/api/v1/tools/eval_list_knowledge_refs":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{"id": "frag-alpha", "metadata": map[string]any{"source_doc_id": "doc-alpha"}},
					{"id": "frag-beta", "metadata": map[string]any{"source_doc_id": "doc-beta"}},
				},
				"next_cursor": "",
				"has_more":    false,
			})
		case "/api/v1/tools/recall_memory":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode recall body: %v", err)
			}
			recallCalls++
			if _, ok := input["case_id"]; ok {
				t.Fatalf("recall_memory input retained case_id: %#v", input)
			}
			refID := "relationship-alpha"
			evidenceID := "frag-alpha"
			if input["query"] == "What is beta?" {
				refID = "relationship-beta"
				evidenceID = "frag-beta"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"recall_id": "rec-" + refID,
				"results": []map[string]any{{
					"evidence_id": evidenceID,
					"context":     input["query"],
				}},
				"discovery_paths": []map[string]any{{
					"relationships": []map[string]any{{
						"relationship_id": refID,
						"subject":         map[string]any{"name": "Greek letter"},
						"predicate":       "definition",
						"object":          map[string]any{"value": input["query"]},
					}},
					"evidence_ids": []string{evidenceID},
				}},
				"discovery_guidance": "focus follow-up",
				"related_hypotheses": []map[string]any{},
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
		ControlURL:       server.URL,
		ControlToken:     "control-token",
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
	if !controlPatched || rememberCalls != 2 || placementPolls != 2 || recallCalls != 2 {
		t.Fatalf("control/remember/placement/recall calls = %v/%d/%d/%d", controlPatched, rememberCalls, placementPolls, recallCalls)
	}
	if !placementIngests["doc-alpha"] || !placementIngests["doc-beta"] {
		t.Fatalf("placement ingests = %#v; want doc-alpha and doc-beta", placementIngests)
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
		{name: "negative placement timeout", opts: RunOptions{
			Mode:             "validate",
			SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
			SuitePath:        filepath.Join(dir, "suite.jsonl"),
			PlacementTimeout: -time.Second,
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

	t.Run("six axis seed missing validation report", func(t *testing.T) {
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
		if err == nil || !strings.Contains(err.Error(), "requires validation_report_file") {
			t.Fatalf("Run err = %v; want missing validation report", err)
		}
	})

	t.Run("validation report hash mismatch", func(t *testing.T) {
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
		if err := writeJSONFile(filepath.Join(dir, "validation_report.json"), seedValidationReport{
			SchemaVersion: "dense-mem.eval.validation.v1",
			SeedID:        "public_6axis_1k_v1",
			Status:        "passed",
			SeedHash:      "sha256:not-the-current-seed",
		}); err != nil {
			t.Fatalf("write validation report: %v", err)
		}
		_, err := Run(context.Background(), RunOptions{
			Mode:             "validate",
			SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
			SuitePath:        filepath.Join(dir, "suite.jsonl"),
		})
		if err == nil || !strings.Contains(err.Error(), "does not match current seed hash") {
			t.Fatalf("Run err = %v; want validation hash mismatch", err)
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
