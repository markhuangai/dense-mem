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
			_ = json.NewEncoder(w).Encode(map[string]any{
				"placement": map[string]any{"status": "completed"},
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
		case "/api/v1/tools/eval_run_recall_case":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode recall body: %v", err)
			}
			recallCalls++
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

func TestResolveRefPrefersTypeAwareSourceMapping(t *testing.T) {
	mapping := newKnowledgeMapping()
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: "fragment-tiered", SourceDocID: "doc-tiered"}, true)
	addSourceMapping(&mapping, Ref{Type: "claim", ID: "claim-tiered", SourceDocID: "doc-tiered"}, false)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-tiered", SourceDocID: "doc-tiered"}, false)

	fragment, ok := resolveRef(Ref{Type: "fragment", SourceDocID: "doc-tiered"}, mapping)
	if !ok || fragment.ID != "fragment-tiered" {
		t.Fatalf("fragment resolve = %+v, %v", fragment, ok)
	}
	claim, ok := resolveRef(Ref{Type: "claim", SourceDocID: "doc-tiered"}, mapping)
	if !ok || claim.ID != "claim-tiered" {
		t.Fatalf("claim resolve = %+v, %v", claim, ok)
	}
	fact, ok := resolveRef(Ref{Type: "fact", SourceDocID: "doc-tiered"}, mapping)
	if !ok || fact.ID != "fact-tiered" {
		t.Fatalf("fact resolve = %+v, %v", fact, ok)
	}
	fallback, ok := resolveRef(Ref{SourceDocID: "doc-tiered"}, mapping)
	if !ok || fallback.ID != "fragment-tiered" {
		t.Fatalf("fallback resolve = %+v, %v", fallback, ok)
	}
	legacySourceDoc, ok := resolveRef(Ref{Type: "source_doc", SourceDocID: "doc-tiered"}, mapping)
	if !ok || legacySourceDoc.ID != "fragment-tiered" || legacySourceDoc.Type != "fragment" {
		t.Fatalf("legacy source_doc resolve = %+v, %v", legacySourceDoc, ok)
	}

	fragmentOnly := newKnowledgeMapping()
	addSourceMapping(&fragmentOnly, Ref{Type: "fragment", ID: "fragment-only", SourceDocID: "doc-fragment-only"}, true)
	if resolved, ok := resolveRef(Ref{Type: "fact", SourceDocID: "doc-fragment-only"}, fragmentOnly); ok {
		t.Fatalf("fact ref resolved to fragment mapping: %+v", resolved)
	}
}

func TestEvaluateGates(t *testing.T) {
	minRecall := 0.8
	minRank1 := 0.5
	maxBad := 0.25
	maxBadRank1 := 0.1
	gates := GateOptions{
		MinRecallAtK:         &minRecall,
		MinRequiredRank1Rate: &minRank1,
		MaxAverageBadAtK:     &maxBad,
		MaxBadRank1Rate:      &maxBadRank1,
	}
	passed := EvaluateGates(Summary{
		AverageRecallAtK:  0.9,
		RequiredRank1Rate: 0.6,
		AverageBadAtK:     0.1,
		BadRank1Rate:      0,
	}, gates)
	if !passed.Passed || len(passed.Failures) != 0 {
		t.Fatalf("passed gate = %+v", passed)
	}
	failed := EvaluateGates(Summary{
		AverageRecallAtK:  0.7,
		RequiredRank1Rate: 0.4,
		AverageBadAtK:     0.3,
		BadRank1Rate:      0.2,
	}, gates)
	if failed.Passed || len(failed.Failures) != 4 {
		t.Fatalf("failed gate = %+v; want four failures", failed)
	}
}

func TestEvaluateGatesChecksContextMetrics(t *testing.T) {
	minRecall := 0.8
	minRank1 := 0.5
	maxBad := 0.25
	maxBadRank1 := 0.1
	gates := GateOptions{
		MinContextRecallAtK:         &minRecall,
		MinContextRequiredRank1Rate: &minRank1,
		MaxAverageContextBadAtK:     &maxBad,
		MaxContextBadRank1Rate:      &maxBadRank1,
	}
	passed := EvaluateGates(Summary{
		ContextScoredCaseCount:   2,
		AverageContextRecallAtK:  0.9,
		ContextRequiredRank1Rate: 0.6,
		AverageContextBadAtK:     0.1,
		ContextBadRank1Rate:      0,
	}, gates)
	if !passed.Passed || len(passed.Failures) != 0 {
		t.Fatalf("passed context gate = %+v", passed)
	}
	failed := EvaluateGates(Summary{
		ContextScoredCaseCount:   2,
		AverageContextRecallAtK:  0.7,
		ContextRequiredRank1Rate: 0.4,
		AverageContextBadAtK:     0.3,
		ContextBadRank1Rate:      0.2,
	}, gates)
	if failed.Passed || len(failed.Failures) != 4 {
		t.Fatalf("failed context gate = %+v; want four failures", failed)
	}
	unavailable := EvaluateGates(Summary{}, gates)
	if unavailable.Passed || len(unavailable.Failures) != 1 || !strings.Contains(unavailable.Failures[0], "context metrics unavailable") {
		t.Fatalf("unavailable context gate = %+v", unavailable)
	}
}

func TestValidateRequiredQRelMappingsRejectsUnmappedRequiredRefs(t *testing.T) {
	qrels := map[string]QRel{
		"case-1": {
			CaseID:       "case-1",
			RequiredRefs: []Ref{{Type: "fact", SourceDocID: "doc-required"}},
			BadRefs:      []Ref{{Type: "fact", SourceDocID: "doc-bad"}},
		},
	}
	suite := []SuiteCase{{CaseID: "case-1"}}
	mapping := KnowledgeMapping{
		BySourceDocID: map[string]Ref{
			"doc-required": {Type: "fragment", ID: "fragment-required", SourceDocID: "doc-required"},
			"doc-bad":      {Type: "fragment", ID: "fragment-bad", SourceDocID: "doc-bad"},
		},
		BySourceDocIDAndType: map[string]map[string]Ref{
			"doc-bad": {
				"fragment": {Type: "fragment", ID: "fragment-bad", SourceDocID: "doc-bad"},
			},
		},
	}

	err := validateRequiredQRelMappings(qrels, suite, mapping)

	if err == nil || !strings.Contains(err.Error(), "required qrel ref for case \"case-1\" is unmapped") {
		t.Fatalf("validateRequiredQRelMappings err = %v", err)
	}
	mapping.BySourceDocIDAndType["doc-required"] = map[string]Ref{
		"fact": {Type: "fact", ID: "fact-required", SourceDocID: "doc-required"},
	}
	if err := validateRequiredQRelMappings(qrels, suite, mapping); err != nil {
		t.Fatalf("validateRequiredQRelMappings mapped required ref: %v", err)
	}
}

func TestScoreTracesErrorsAndRefEdges(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1"}}
	cases := map[string]Case{"case-1": {CaseID: "case-1", Query: "query"}}
	_, _, err := ScoreTraces("run", "baseline", "seed", "sha256:test", "suite.jsonl", suite, cases, map[string]QRel{}, nil, KnowledgeMapping{})
	if err == nil {
		t.Fatal("ScoreTraces missing qrels error = nil")
	}
	qrels := map[string]QRel{"case-1": {CaseID: "case-1", RequiredRefs: []Ref{{Type: "fragment", ID: "frag-1"}}}}
	_, _, err = ScoreTraces("run", "baseline", "seed", "sha256:test", "suite.jsonl", suite, cases, qrels, nil, KnowledgeMapping{})
	if err == nil {
		t.Fatal("ScoreTraces missing trace error = nil")
	}
	if got := topKSet([]Ref{{Type: "fragment", ID: "frag-1"}}, -1); len(got) != 0 {
		t.Fatalf("topKSet negative k = %v; want empty", got)
	}
	if _, ok := resolveRef(Ref{Type: "fragment"}, KnowledgeMapping{}); ok {
		t.Fatal("resolveRef without id or source_doc_id ok = true")
	}
}

func TestCompareSummariesRejectsSeedMismatch(t *testing.T) {
	_, err := CompareSummaries(Summary{SeedHash: "sha256:a"}, Summary{SeedHash: "sha256:b"})
	if err == nil {
		t.Fatal("CompareSummaries expected seed mismatch error")
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
		{CaseID: "case-1", RequiredRefs: []Ref{{Type: "fragment", SourceDocID: "doc-alpha", Grade: 1}}},
		{CaseID: "case-2", RequiredRefs: []Ref{{Type: "fragment", SourceDocID: "doc-beta", Grade: 1}}},
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
