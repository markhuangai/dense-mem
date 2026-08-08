package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/markhuangai/dense-mem/internal/evalharness"
)

func TestGenerateLocalEval100IsDeterministicAndValid(t *testing.T) {
	dir := t.TempDir()
	firstSeed := filepath.Join(dir, "first", "seed")
	firstSuite := filepath.Join(dir, "first", "suite.jsonl")
	secondSeed := filepath.Join(dir, "second", "seed")
	secondSuite := filepath.Join(dir, "second", "suite.jsonl")
	if err := generatePreset(presetLocalEval100, firstSeed, firstSuite); err != nil {
		t.Fatalf("generate first preset: %v", err)
	}
	if err := generatePreset(seedIdentityLocal100, secondSeed, secondSuite); err != nil {
		t.Fatalf("generate second preset by identity: %v", err)
	}

	manifestPath := filepath.Join(firstSeed, "seed_manifest.json")
	manifest, err := evalharness.LoadSeedManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadSeedManifest: %v", err)
	}
	if manifest.SchemaVersion != evalharness.SeedSchemaVersionV2 || manifest.SeedID != seedIdentityLocal100 || manifest.Counts["cases"] != 25 || manifest.Counts["corpus"] != 100 || manifest.Counts["hard_negatives"] != 75 || manifest.RelationshipCount != 100 {
		t.Fatalf("smoke manifest identity/counts = %q/%v", manifest.SeedID, manifest.Counts)
	}
	corpus, err := evalharness.LoadCorpus(manifestPath, manifest)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(corpus) != 100 || corpus[0].SourceDocID != seedIdentityLocal100+"_0001_required" || corpus[99].SourceDocID != seedIdentityLocal100+"_0025_negative_03" {
		t.Fatalf("smoke corpus boundaries = count %d first %q last %q", len(corpus), corpus[0].SourceDocID, corpus[len(corpus)-1].SourceDocID)
	}
	cases, err := evalharness.LoadCases(manifestPath, manifest)
	if err != nil {
		t.Fatalf("LoadCases: %v", err)
	}
	for index, item := range cases {
		if item.KnownAt != "" {
			t.Fatalf("smoke case %d known_at = %q; want current knowledge time", index+1, item.KnownAt)
		}
	}
	if corpus[0].SourceDataset != seedIdentityLocal100 {
		t.Fatalf("smoke source dataset = %q", corpus[0].SourceDataset)
	}
	for index, item := range corpus {
		if len(item.Relationships) != 1 {
			t.Fatalf("smoke corpus row %d relationships = %d; want 1", index+1, len(item.Relationships))
		}
	}
	summary, err := evalharness.Run(context.Background(), evalharness.RunOptions{
		Mode:             "validate",
		SeedManifestPath: manifestPath,
		SuitePath:        firstSuite,
	})
	if err != nil {
		t.Fatalf("validate generated smoke seed: %v", err)
	}
	if summary.CaseCount != 25 {
		t.Fatalf("smoke case count = %d; want 25", summary.CaseCount)
	}

	for _, name := range []string{"seed_manifest.json", "corpus.jsonl", "cases.jsonl", "qrels.jsonl", "answers.jsonl", "hard_negatives.jsonl", "transforms.jsonl", "licenses.md"} {
		assertSameFile(t, filepath.Join(firstSeed, name), filepath.Join(secondSeed, name))
	}
	assertSameFile(t, firstSuite, secondSuite)
}

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
	if manifest.SchemaVersion != evalharness.SeedSchemaVersion || manifest.SeedID != seedIdentityLocalEval || manifest.RelationshipCount != 0 {
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

func assertSameFile(t *testing.T, firstPath, secondPath string) {
	t.Helper()
	first, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatalf("read %s: %v", firstPath, err)
	}
	second, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatalf("read %s: %v", secondPath, err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("generated files differ: %s and %s", firstPath, secondPath)
	}
}
