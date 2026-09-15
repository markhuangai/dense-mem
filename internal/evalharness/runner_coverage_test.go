package evalharness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvaluationRunnerRunRejectsInvalidExecutionContracts(t *testing.T) {
	root := writeEvalFixture(t)
	manifestPath := filepath.Join(root, "seed_manifest.json")
	suitePath := filepath.Join(root, "suite.jsonl")
	tests := []struct {
		name string
		opts RunOptions
		want string
	}{
		{name: "unsupported mode", opts: RunOptions{Mode: "unknown", SeedManifestPath: manifestPath, SuitePath: suitePath}, want: "unsupported mode"},
		{name: "release policy import", opts: RunOptions{Mode: "import", SeedManifestPath: manifestPath, SuitePath: suitePath, ReleaseGatePolicyPath: "policy.json"}, want: "release gate policy requires"},
		{name: "missing manifest", opts: RunOptions{SuitePath: suitePath}, want: "seed manifest path is required"},
		{name: "missing suite", opts: RunOptions{SeedManifestPath: manifestPath}, want: "suite path is required"},
		{name: "resume without import", opts: RunOptions{Mode: "validate", SeedManifestPath: manifestPath, SuitePath: suitePath, ResumeSourceDocIDsPath: "resume.txt"}, want: "resume source document IDs"},
		{name: "resume without import flag", opts: RunOptions{Mode: "baseline", SeedManifestPath: manifestPath, SuitePath: suitePath, ResumeSourceDocIDsPath: "resume.txt"}, want: "resume source document IDs"},
		{name: "concurrency too high", opts: RunOptions{Mode: "validate", SeedManifestPath: manifestPath, SuitePath: suitePath, ImportConcurrency: MaxImportConcurrency + 1}, want: "import concurrency"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Run(context.Background(), tt.opts); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() error = %v, want substring %q", err, tt.want)
			}
		})
	}

	if summary, err := Run(context.Background(), RunOptions{Mode: "validate", SeedManifestPath: manifestPath, SuitePath: suitePath, OutDir: filepath.Join(root, "validation")}); err != nil {
		t.Fatalf("validate run failed: %v", err)
	} else if summary.Mode != "validate" || !fileExists(filepath.Join(root, "validation", "summary.json")) {
		t.Fatalf("validate summary/artifacts = %+v", summary)
	}

	if _, err := Run(context.Background(), RunOptions{Mode: "baseline", SeedManifestPath: manifestPath, SuitePath: suitePath, TracesPath: filepath.Join(root, "missing-traces.jsonl")}); err == nil {
		t.Fatal("missing traces file was accepted")
	}
	if _, err := Run(context.Background(), RunOptions{Mode: "import", SeedManifestPath: manifestPath, SuitePath: suitePath}); err == nil || !strings.Contains(err.Error(), "requires --import-seed") {
		t.Fatalf("import without seed error = %v", err)
	}
}

func TestEvaluationRunnerRunRejectsV1ImportAndWritesGateInput(t *testing.T) {
	root := writeEvalFixture(t)
	manifestPath := filepath.Join(root, "seed_manifest.json")
	suitePath := filepath.Join(root, "suite.jsonl")
	if _, err := Run(context.Background(), RunOptions{Mode: "import", ImportSeed: true, SeedManifestPath: manifestPath, SuitePath: suitePath}); err == nil || !strings.Contains(err.Error(), "cannot be imported") {
		t.Fatalf("V1 import error = %v", err)
	}

	policyPath := filepath.Join(root, "policy.json")
	if err := writeJSONFile(policyPath, ReleaseGatePolicy{
		SchemaVersion:           ReleaseGatePolicySchemaVersion,
		GateID:                  "fixture-gate",
		Release:                 "fixture-release",
		SeedID:                  "different-seed",
		SeedHash:                "sha256:different",
		RequiredCaseCount:       2,
		RequiredScoredCaseCount: 2,
		BaselineSummary:         ReleaseGateBaseline{CaseCount: 2, ScoredCaseCount: 2},
		Maximums:                ReleaseGateMetricMaximums{UnmappedSourceRefs: 0},
	}); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(root, "gate-input")
	_, err := Run(context.Background(), RunOptions{
		Mode:                  "validate",
		SeedManifestPath:      manifestPath,
		SuitePath:             suitePath,
		OutDir:                outDir,
		ReleaseGatePolicyPath: policyPath,
	})
	if err == nil || !strings.Contains(err.Error(), "release gate input check failed") {
		t.Fatalf("failed release gate input = %v", err)
	}
	if !fileExists(filepath.Join(outDir, "release_gate_input_result.json")) {
		t.Fatal("release gate input artifact was not written")
	}
}

func TestEvaluationRunnerValidationRejectsCrossFileReferences(t *testing.T) {
	root := writeEvalFixture(t)
	manifestPath := filepath.Join(root, "seed_manifest.json")
	manifest, corpus, cases, qrels, dreams, suite, seedHash, err := loadRunInputs(manifestPath, filepath.Join(root, "suite.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	cases[0].CaseID = "missing"
	if err := validateRunInputs(manifestPath, manifest, corpus, cases, qrels, dreams, suite, seedHash); err == nil || !strings.Contains(err.Error(), "missing from seed cases") {
		t.Fatalf("missing suite case was accepted: %v", err)
	}
	cases[0].CaseID = "case-1"
	qrels[0].RequiredDreamRefs = []Ref{{Type: "dream", SourceDocID: "missing-dream"}}
	if err := validateRunInputs(manifestPath, manifest, corpus, cases, qrels, dreams, suite, seedHash); err == nil || !strings.Contains(err.Error(), "missing-dream") {
		t.Fatalf("missing dream reference was accepted: %v", err)
	}
}

func TestEvaluationRunnerValidationRejectsEveryReferenceFamily(t *testing.T) {
	root := writeEvalFixture(t)
	manifestPath := filepath.Join(root, "seed_manifest.json")
	manifest, corpus, cases, qrels, dreams, suite, seedHash, err := loadRunInputs(manifestPath, filepath.Join(root, "suite.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	missing := Ref{Type: "source_doc", SourceDocID: "missing"}
	for name, mutate := range map[string]func(*QRel){
		"acceptable":        func(q *QRel) { q.AcceptableRefs = []Ref{missing} },
		"bad":               func(q *QRel) { q.BadRefs = []Ref{missing} },
		"required evidence": func(q *QRel) { q.RequiredEvidenceRefs = []Ref{missing} },
		"bad evidence":      func(q *QRel) { q.BadEvidenceRefs = []Ref{missing} },
		"acceptable dream":  func(q *QRel) { q.AcceptableDreamRefs = []Ref{missing} },
		"bad dream":         func(q *QRel) { q.BadDreamRefs = []Ref{missing} },
	} {
		t.Run(name, func(t *testing.T) {
			copyQrels := append([]QRel(nil), qrels...)
			copyQrels[0].AcceptableRefs = nil
			copyQrels[0].BadRefs = nil
			copyQrels[0].RequiredEvidenceRefs = nil
			copyQrels[0].BadEvidenceRefs = nil
			copyQrels[0].AcceptableDreamRefs = nil
			copyQrels[0].BadDreamRefs = nil
			mutate(&copyQrels[0])
			if err := validateRunInputs(manifestPath, manifest, corpus, cases, copyQrels, dreams, suite, seedHash); err == nil || !strings.Contains(err.Error(), "missing") {
				t.Fatalf("reference family was accepted: %v", err)
			}
		})
	}
	adversarial := append([]Case(nil), cases...)
	adversarial[0].Slices = []string{"adversarial"}
	noBad := append([]QRel(nil), qrels...)
	noBad[0].BadRefs = nil
	if err := validateRunInputs(manifestPath, manifest, corpus, adversarial, noBad, dreams, suite, seedHash); err == nil || !strings.Contains(err.Error(), "no bad_refs") {
		t.Fatalf("adversarial qrel without bad refs was accepted: %v", err)
	}
	duplicate := append([]QRel(nil), qrels[0], qrels[0])
	if err := validateRunInputs(manifestPath, manifest, corpus, cases, duplicate, dreams, suite, seedHash); err == nil || !strings.Contains(err.Error(), "duplicate qrels") {
		t.Fatalf("duplicate qrels were accepted: %v", err)
	}
}

func TestEvaluationRunnerValidationReportRejectsInvalidReports(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "seed_manifest.json")
	manifest := &SeedManifest{SeedID: "fixture", ValidationReportFile: "validation.json"}
	for name, report := range map[string]seedValidationReport{
		"schema": {SchemaVersion: "wrong", SeedID: "fixture", Status: "passed", SeedHash: "hash"},
		"seed":   {SchemaVersion: "dense-mem.eval.validation.v1", SeedID: "other", Status: "passed", SeedHash: "hash"},
		"status": {SchemaVersion: "dense-mem.eval.validation.v1", SeedID: "fixture", Status: "failed", SeedHash: "hash"},
		"hash":   {SchemaVersion: "dense-mem.eval.validation.v1", SeedID: "fixture", Status: "passed", SeedHash: "other"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := writeJSONFile(filepath.Join(root, "validation.json"), report); err != nil {
				t.Fatal(err)
			}
			if err := validateSeedValidationReport(manifestPath, manifest, "hash"); err == nil {
				t.Fatal("invalid validation report was accepted")
			}
		})
	}
	if err := validateSeedValidationReport(manifestPath, &SeedManifest{SeedID: "fixture", ValidationReportFile: "validation.json"}, "hash"); err == nil {
		t.Fatal("missing validation report was accepted")
	}
	if err := validateSeedValidationReport(manifestPath, &SeedManifest{SeedID: "fixture"}, "hash"); err != nil {
		t.Fatalf("optional validation report rejected: %v", err)
	}
}

func TestEvaluationRunnerLoaderAndComparisonFailures(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	for name, call := range map[string]func() error{
		"manifest": func() error { _, _, _, _, _, _, _, err := loadRunInputs(missing, missing); return err },
		"suite": func() error {
			fixture := writeEvalFixture(t)
			_, _, _, _, _, _, _, err := loadRunInputs(filepath.Join(fixture, "seed_manifest.json"), missing)
			return err
		},
		"source ids": func() error { _, err := loadSourceDocIDs(missing); return err },
		"jsonl rows": func() error { _, err := countSeedJSONLRows(missing, "answers.jsonl"); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("missing input was accepted")
			}
		})
	}
	if _, err := CompareRunDirs(filepath.Join(root, "baseline"), filepath.Join(root, "candidate"), ""); err == nil {
		t.Fatal("missing comparison summaries were accepted")
	}
}

func TestEvaluationRunnerAdditionalErrorBranches(t *testing.T) {
	root := writeEvalFixture(t)
	manifestPath := filepath.Join(root, "seed_manifest.json")
	suitePath := filepath.Join(root, "suite.jsonl")
	if _, err := Run(context.Background(), RunOptions{Mode: "validate", SeedManifestPath: manifestPath, SuitePath: suitePath, ReleaseGatePolicyPath: filepath.Join(root, "missing-policy.json")}); err == nil || !strings.Contains(err.Error(), "release gate policy") {
		t.Fatalf("missing release policy error = %v", err)
	}
	outFile := filepath.Join(root, "out-file")
	if err := os.WriteFile(outFile, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), RunOptions{Mode: "validate", SeedManifestPath: manifestPath, SuitePath: suitePath, OutDir: outFile}); err == nil {
		t.Fatal("file output directory was accepted")
	}
	if _, err := Run(context.Background(), RunOptions{Mode: "baseline", SeedManifestPath: manifestPath, SuitePath: suitePath, ImportSeed: true, ResumeSourceDocIDsPath: filepath.Join(root, "missing-checkpoint")}); err == nil || !strings.Contains(err.Error(), "resume source document IDs") {
		t.Fatalf("missing resume checkpoint error = %v", err)
	}
	if err := validateSeedValidationReport(filepath.Join(root, "manifest.json"), nil, "hash"); err != nil {
		t.Fatalf("nil validation report manifest rejected: %v", err)
	}
	if err := validateSeedValidationReport(filepath.Join(root, "manifest.json"), &SeedManifest{SeedID: "seed", ValidationReportFile: "missing.json"}, "hash"); err == nil {
		t.Fatal("missing validation report was accepted")
	}
	if err := validateRequiredQRelMappings(map[string]QRel{"case": {RequiredRefs: []Ref{{}}}}, []SuiteCase{{CaseID: "case"}}, KnowledgeMapping{}); err != nil {
		t.Fatalf("empty source ref should be ignored: %v", err)
	}
	if err := validateQRelRefs("case", "field", []Ref{{}}, map[string]struct{}{}); err != nil {
		t.Fatalf("empty qrel source ref should be ignored: %v", err)
	}
	if err := validateManifestCounts(filepath.Join(root, "manifest.json"), &SeedManifest{Counts: map[string]int{"docs_per_case": 1}}, nil, nil, nil); err == nil {
		t.Fatal("docs_per_case without cases was accepted")
	}
	if err := validateManifestCounts(filepath.Join(root, "manifest.json"), &SeedManifest{Counts: map[string]int{"docs_per_case": 1}}, []CorpusItem{{SourceDocID: "a"}, {SourceDocID: "b"}}, []Case{{CaseID: "case"}}, nil); err == nil {
		t.Fatal("non-divisible docs_per_case was accepted")
	}
	baseline := filepath.Join(root, "baseline")
	candidate := filepath.Join(root, "candidate")
	if err := os.MkdirAll(baseline, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(baseline, "summary.json"), Summary{RunID: "base"}); err != nil {
		t.Fatal(err)
	}
	if _, err := CompareRunDirs(baseline, candidate, ""); err == nil {
		t.Fatal("missing candidate summary was accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "comparison-file"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(candidate, "summary.json"), Summary{RunID: "candidate"}); err != nil {
		t.Fatal(err)
	}
	if _, err := CompareRunDirs(baseline, candidate, filepath.Join(root, "comparison-file")); err == nil {
		t.Fatal("file comparison output was accepted")
	}
}

func TestEvaluationRunnerMappingAndReferenceValidation(t *testing.T) {
	mapping := KnowledgeMapping{BySourceDocIDAndType: map[string]map[string][]Ref{
		"doc-1": {"evidence": {{Type: "evidence", ID: "e-1", SourceDocID: "doc-1"}}},
	}}
	qrels := map[string]QRel{"case-1": {
		RequiredRefs:         []Ref{{Type: "evidence", SourceDocID: "doc-1"}},
		RequiredEvidenceRefs: []Ref{{Type: "evidence", SourceDocID: "doc-1"}},
		BadRefs:              []Ref{{Type: "evidence", SourceDocID: "doc-1"}},
		BadEvidenceRefs:      []Ref{{Type: "evidence", SourceDocID: "doc-1"}},
		RequiredDreamRefs:    []Ref{{Type: "evidence", SourceDocID: "doc-1"}},
		BadDreamRefs:         []Ref{{Type: "evidence", SourceDocID: "doc-1"}},
	}}
	if err := validateRequiredQRelMappings(qrels, []SuiteCase{{CaseID: "case-1"}}, mapping); err != nil {
		t.Fatalf("mapped qrels rejected: %v", err)
	}
	if err := validateRequiredQRelMappings(map[string]QRel{}, []SuiteCase{{CaseID: "missing"}}, mapping); err == nil {
		t.Fatal("missing qrel was accepted")
	}
	if err := validateRequiredQRelMappings(map[string]QRel{"case-1": {RequiredRefs: []Ref{{Type: "evidence", SourceDocID: "missing"}}}}, []SuiteCase{{CaseID: "case-1"}}, mapping); err == nil || !strings.Contains(err.Error(), "unmapped") {
		t.Fatalf("unmapped qrel error = %v", err)
	}
	if got := sourceDocIDsForQRelMappings([]QRel{{RequiredRefs: []Ref{{SourceDocID: "doc-1"}}, BadRefs: []Ref{{SourceDocID: "doc-2"}}}}, []ExpectedDream{{SourceRefs: []Ref{{SourceDocID: "doc-3"}}}}); len(got) != 3 {
		t.Fatalf("source doc mapping index = %#v", got)
	}
	if got := sourceDocIDIndexForCorpus([]CorpusItem{{SourceDocID: "doc-1"}, {SourceDocID: " "}}); len(got) != 1 {
		t.Fatalf("corpus index = %#v", got)
	}
	if err := validateQRelRefs("case", "required", []Ref{{SourceDocID: "missing"}}, map[string]struct{}{}); err == nil {
		t.Fatal("missing qrel source doc was accepted")
	}
	if !hasSlice([]string{" Other ", "Adversarial"}, "adversarial") || hasSlice(nil, "adversarial") {
		t.Fatal("hasSlice result was incorrect")
	}
}

func TestEvaluationRunnerManifestCountsAndCheckpoints(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "seed_manifest.json")
	manifest := &SeedManifest{Counts: map[string]int{"corpus": 2, "cases": 1, "qrels": 1, "docs_per_case": 2, "answers": 1}, AnswersFile: "answers.jsonl"}
	if err := os.WriteFile(filepath.Join(root, manifest.AnswersFile), []byte("{}\n# comment\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateManifestCounts(manifestPath, manifest, []CorpusItem{{SourceDocID: "a"}, {SourceDocID: "b"}}, []Case{{CaseID: "case"}}, []QRel{{CaseID: "case"}}); err != nil {
		t.Fatalf("valid manifest counts rejected: %v", err)
	}
	for name, mutate := range []func(*SeedManifest, *[]CorpusItem, *[]Case, *[]QRel){
		func(m *SeedManifest, corpus *[]CorpusItem, _ *[]Case, _ *[]QRel) { m.Counts["corpus"] = 3 },
		func(m *SeedManifest, corpus *[]CorpusItem, _ *[]Case, _ *[]QRel) { m.Counts["docs_per_case"] = 3 },
		func(m *SeedManifest, _ *[]CorpusItem, cases *[]Case, _ *[]QRel) { *cases = nil },
		func(m *SeedManifest, _ *[]CorpusItem, _ *[]Case, _ *[]QRel) { m.Counts["answers"] = 2 },
	} {
		t.Run(string(rune('a'+name)), func(t *testing.T) {
			copyManifest := &SeedManifest{Counts: map[string]int{"corpus": 2, "cases": 1, "qrels": 1, "docs_per_case": 2, "answers": 1}, AnswersFile: "answers.jsonl"}
			corpus := []CorpusItem{{SourceDocID: "a"}, {SourceDocID: "b"}}
			cases := []Case{{CaseID: "case"}}
			qrels := []QRel{{CaseID: "case"}}
			mutate(copyManifest, &corpus, &cases, &qrels)
			if err := validateManifestCounts(manifestPath, copyManifest, corpus, cases, qrels); err == nil {
				t.Fatal("invalid manifest counts were accepted")
			}
		})
	}
	if err := validateManifestCounts(manifestPath, &SeedManifest{Counts: map[string]int{"answers": 1}}, nil, nil, nil); err == nil {
		t.Fatal("unset optional file count was accepted")
	}
	checkpoint := filepath.Join(root, "checkpoint.txt")
	if err := os.WriteFile(checkpoint, []byte("doc-1\n\n doc-2 \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ids, err := loadSourceDocIDs(checkpoint)
	if err != nil || len(ids) != 2 {
		t.Fatalf("loadSourceDocIDs = %#v, %v", ids, err)
	}
	completed := completedMappedSourceDocIDs(ids, KnowledgeMapping{BySourceDocIDAndType: map[string]map[string][]Ref{"doc-1": {"evidence": {{ID: "e"}}}}})
	if len(completed) != 1 {
		t.Fatalf("completed source docs = %#v", completed)
	}
}

func TestEvaluationRunnerComparisonAndValidationReport(t *testing.T) {
	root := t.TempDir()
	baselineDir := filepath.Join(root, "baseline")
	candidateDir := filepath.Join(root, "candidate")
	if err := os.MkdirAll(baselineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(candidateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(baselineDir, "summary.json"), Summary{RunID: "base"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(candidateDir, "summary.json"), Summary{RunID: "candidate"}); err != nil {
		t.Fatal(err)
	}
	comparison, err := CompareRunDirs(baselineDir, candidateDir, "")
	if err != nil {
		t.Fatalf("CompareRunDirs() = %v", err)
	}
	if comparison.BaselineRunID != "base" || comparison.CandidateRunID != "candidate" {
		t.Fatalf("comparison = %+v", comparison)
	}
	manifestPath := filepath.Join(root, "manifest.json")
	manifest := &SeedManifest{SeedID: "fixture", ValidationReportFile: "validation.json"}
	seedHash := "hash"
	if err := writeJSONFile(filepath.Join(root, "validation.json"), seedValidationReport{SchemaVersion: "dense-mem.eval.validation.v1", SeedID: "fixture", Status: "passed", SeedHash: seedHash}); err != nil {
		t.Fatal(err)
	}
	if err := validateSeedValidationReport(manifestPath, manifest, seedHash); err != nil {
		t.Fatalf("valid validation report rejected: %v", err)
	}
	if err := validateSeedValidationReport(manifestPath, &SeedManifest{SeedID: "public_6axis_fixture"}, seedHash); err == nil {
		t.Fatal("public seed without validation report was accepted")
	}
}
