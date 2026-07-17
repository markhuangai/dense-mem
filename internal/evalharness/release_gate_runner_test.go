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
		ReleaseGatePolicyPath: policyPath,
	})

	if err == nil || !strings.Contains(err.Error(), "release gate policy case count") {
		t.Fatalf("Run err = %v; want early release gate policy case count failure", err)
	}
	if fileExists(out) {
		t.Fatalf("output directory %s exists; policy validation should run before artifacts", out)
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
