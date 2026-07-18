package evalharness

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsReleaseGatePolicyBeforeArtifacts(t *testing.T) {
	dir := writeEvalFixture(t)
	manifestPath := filepath.Join(dir, "seed_manifest.json")
	manifest, err := LoadSeedManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadSeedManifest: %v", err)
	}
	seedHash, err := SeedHash(manifestPath, manifest)
	if err != nil {
		t.Fatalf("SeedHash: %v", err)
	}
	suitePath := filepath.Join(dir, "suite.jsonl")
	suiteHash, err := FileHash(suitePath)
	if err != nil {
		t.Fatalf("FileHash suite: %v", err)
	}
	policy := releaseGateTestPolicy()
	policy.SeedID = manifest.SeedID
	policy.SeedHash = seedHash
	policy.SuiteHash = suiteHash
	policy.RequiredCaseCount = 3
	policy.RequiredScoredCaseCount = 3
	policy.BaselineSummary.CaseCount = 3
	policy.BaselineSummary.ScoredCaseCount = 3
	policyPath := filepath.Join(dir, "release_gate_policy.json")
	if err := writeJSONFile(policyPath, policy); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	out := filepath.Join(dir, "should-not-exist")

	_, err = Run(context.Background(), RunOptions{
		Mode:                  "baseline",
		SeedManifestPath:      manifestPath,
		SuitePath:             suitePath,
		TracesPath:            filepath.Join(dir, "traces.jsonl"),
		OutDir:                out,
		RunID:                 "release-gate-early",
		ToolTransport:         "mcp",
		ReleaseGatePolicyPath: policyPath,
	})

	if err == nil || !strings.Contains(err.Error(), "release gate policy case count") {
		t.Fatalf("Run err = %v; want early release gate policy case count failure", err)
	}
	if fileExists(out) {
		t.Fatalf("output directory %s exists; policy validation should run before artifacts", out)
	}
}

func TestRunWritesReleaseGateResultWhenLegacyGateFails(t *testing.T) {
	dir := writeEvalFixture(t)
	tracesPath := filepath.Join(dir, "traces.jsonl")
	if err := writeJSONL(tracesPath, []RecallTrace{
		{CaseID: "case-1", Query: "What is alpha?", RankedRefs: []Ref{{Type: "fragment", ID: "frag-alpha", Rank: 1}}},
		{CaseID: "case-2", Query: "What is beta?", RankedRefs: []Ref{{Type: "fragment", ID: "frag-beta", Rank: 1}}},
	}); err != nil {
		t.Fatalf("write traces: %v", err)
	}
	suitePath := filepath.Join(dir, "suite.jsonl")
	_, _, _, _, _, _, seedHash, suiteHash, err := loadRunInputs(filepath.Join(dir, "seed_manifest.json"), suitePath)
	if err != nil {
		t.Fatalf("load fixture inputs: %v", err)
	}
	policy := releaseGateTestPolicy()
	policy.SeedID = "fixture"
	policy.SeedHash = seedHash
	policy.SuiteHash = suiteHash
	policy.BaselineSummary = ReleaseGateBaseline{CaseCount: 2, ScoredCaseCount: 2, UnmappedSourceRefs: 2}
	policy.Minimums = ReleaseGateMetricMinimums{}
	policy.Maximums = ReleaseGateMetricMaximums{UnmappedSourceRefs: 2}
	policyPath := filepath.Join(dir, "release_gate.json")
	if err := writeJSONFile(policyPath, policy); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	out := filepath.Join(dir, "legacy-gated-run")
	minRecall := 0.5

	summary, err := Run(context.Background(), RunOptions{
		Mode:                  "baseline",
		SeedManifestPath:      filepath.Join(dir, "seed_manifest.json"),
		SuitePath:             suitePath,
		TracesPath:            tracesPath,
		OutDir:                out,
		RunID:                 "legacy-gated-test",
		Gates:                 GateOptions{MinRecallAtK: &minRecall},
		ToolTransport:         "mcp",
		ReleaseGatePolicyPath: policyPath,
	})
	if err == nil || !strings.Contains(err.Error(), "gate check failed") {
		t.Fatalf("Run err = %v; want legacy gate failure", err)
	}
	if strings.Contains(err.Error(), "release gate check failed") {
		t.Fatalf("Run err = %v; release gate should pass", err)
	}
	if summary.ScoredCaseCount != 2 {
		t.Fatalf("summary = %+v; want scored cases before returning gate failure", summary)
	}
	var releaseGate ReleaseGateResult
	if err := readJSONFile(filepath.Join(out, "release_gate_result.json"), &releaseGate); err != nil {
		t.Fatalf("read release gate result: %v", err)
	}
	if !releaseGate.Passed {
		t.Fatalf("release gate result = %+v; want pass", releaseGate)
	}
}

func TestRunValidationReportRequiresCurrentSuiteHash(t *testing.T) {
	dir := writeEvalFixture(t)
	manifestPath := filepath.Join(dir, "seed_manifest.json")
	manifest := SeedManifest{
		SchemaVersion:        SeedSchemaVersion,
		SeedID:               "public_6axis_1k_v1",
		CorpusFile:           "corpus.jsonl",
		CasesFile:            "cases.jsonl",
		QrelsFile:            "qrels.jsonl",
		ValidationReportFile: "validation_report.json",
	}
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	loaded, err := LoadSeedManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadSeedManifest: %v", err)
	}
	seedHash, err := SeedHash(manifestPath, loaded)
	if err != nil {
		t.Fatalf("SeedHash: %v", err)
	}
	report := seedValidationReport{
		SchemaVersion: "dense-mem.eval.validation.v1",
		SeedID:        "public_6axis_1k_v1",
		Status:        "passed",
		SeedHash:      seedHash,
	}
	if err := writeJSONFile(filepath.Join(dir, "validation_report.json"), report); err != nil {
		t.Fatalf("write validation report: %v", err)
	}
	_, err = Run(context.Background(), RunOptions{
		Mode:             "validate",
		SeedManifestPath: manifestPath,
		SuitePath:        filepath.Join(dir, "suite.jsonl"),
	})
	if err == nil || !strings.Contains(err.Error(), "missing suite_hash") {
		t.Fatalf("Run err = %v; want missing suite_hash", err)
	}

	report.SuiteHash = "sha256:stale"
	if err := writeJSONFile(filepath.Join(dir, "validation_report.json"), report); err != nil {
		t.Fatalf("rewrite validation report: %v", err)
	}
	_, err = Run(context.Background(), RunOptions{
		Mode:             "validate",
		SeedManifestPath: manifestPath,
		SuitePath:        filepath.Join(dir, "suite.jsonl"),
	})
	if err == nil || !strings.Contains(err.Error(), "does not match current suite hash") {
		t.Fatalf("Run err = %v; want suite hash mismatch", err)
	}
}
