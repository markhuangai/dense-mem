package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/markhuangai/dense-mem/internal/evalharness"
)

func TestGenerateLocalEval1KProducesValidRememberOnlySeed(t *testing.T) {
	dir := t.TempDir()
	seedDir := filepath.Join(dir, "seed")
	suitePath := filepath.Join(dir, "suite.jsonl")
	if err := generatePreset(presetLocalEval1K, seedDir, suitePath); err != nil {
		t.Fatalf("generatePreset: %v", err)
	}

	manifestPath := filepath.Join(seedDir, "seed_manifest.json")
	manifest, err := evalharness.LoadSeedManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadSeedManifest: %v", err)
	}
	if manifest.SeedID != seedIdentityLocalEval {
		t.Fatalf("seed id = %q; want %q", manifest.SeedID, seedIdentityLocalEval)
	}
	corpus, err := evalharness.LoadCorpus(manifestPath, manifest)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(corpus) != 4000 {
		t.Fatalf("corpus count = %d; want 4000", len(corpus))
	}
	if corpus[0].SourceDataset != seedIdentityLocalEval || corpus[0].SourceDocID != seedIdentityLocalEval+"_0001_required" {
		t.Fatalf("first corpus provenance = dataset %q source_doc_id %q", corpus[0].SourceDataset, corpus[0].SourceDocID)
	}

	summary, err := evalharness.Run(context.Background(), evalharness.RunOptions{
		Mode:             "validate",
		SeedManifestPath: manifestPath,
		SuitePath:        suitePath,
	})
	if err != nil {
		t.Fatalf("validate generated seed: %v", err)
	}
	if summary.CaseCount != 1000 {
		t.Fatalf("case count = %d; want 1000", summary.CaseCount)
	}
}

func TestGenerateLocalEval1KAcceptsHistoricalPresetAlias(t *testing.T) {
	dir := t.TempDir()
	if err := generatePreset(seedIdentityLocalEval, filepath.Join(dir, "seed"), filepath.Join(dir, "suite.jsonl")); err != nil {
		t.Fatalf("generatePreset historical alias: %v", err)
	}
}
