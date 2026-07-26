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
	if err := generateLocalEval1K(seedDir, suitePath); err != nil {
		t.Fatalf("generateLocalEval1K: %v", err)
	}

	manifestPath := filepath.Join(seedDir, "seed_manifest.json")
	manifest, err := evalharness.LoadSeedManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadSeedManifest: %v", err)
	}
	corpus, err := evalharness.LoadCorpus(manifestPath, manifest)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(corpus) != 4000 {
		t.Fatalf("corpus count = %d; want 4000", len(corpus))
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
