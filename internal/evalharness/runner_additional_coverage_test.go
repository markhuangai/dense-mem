package evalharness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvaluationRunnerLoaderAndInputBoundaryErrors(t *testing.T) {
	root := writeEvalFixture(t)
	manifestPath := filepath.Join(root, "seed_manifest.json")
	manifest, err := LoadSeedManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, field := range map[string]*string{
		"corpus": &manifest.CorpusFile,
		"cases":  &manifest.CasesFile,
		"qrels":  &manifest.QrelsFile,
	} {
		t.Run("missing "+name, func(t *testing.T) {
			copyManifest := *manifest
			copyManifest.CorpusFile, copyManifest.CasesFile, copyManifest.QrelsFile = manifest.CorpusFile, manifest.CasesFile, manifest.QrelsFile
			switch name {
			case "corpus":
				copyManifest.CorpusFile = "missing-corpus.jsonl"
			case "cases":
				copyManifest.CasesFile = "missing-cases.jsonl"
			case "qrels":
				copyManifest.QrelsFile = "missing-qrels.jsonl"
			}
			_ = field
			path := filepath.Join(root, name+"-manifest.json")
			if err := writeJSONFile(path, copyManifest); err != nil {
				t.Fatal(err)
			}
			if _, _, _, _, _, _, _, err := loadRunInputs(path, filepath.Join(root, "suite.jsonl")); err == nil {
				t.Fatal("missing loader input was accepted")
			}
		})
	}

	loaded, corpus, cases, qrels, dreams, suite, seedHash, err := loadRunInputs(manifestPath, filepath.Join(root, "suite.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRunInputs(manifestPath, loaded, corpus, cases, append(qrels, QRel{CaseID: "missing-qrel"}), dreams, suite, seedHash); err == nil || !strings.Contains(err.Error(), "missing from seed cases") {
		t.Fatal("qrel case missing from cases was accepted")
	}
	missingSuiteQrel := append([]SuiteCase(nil), suite...)
	missingSuiteQrel[0].CaseID = "missing-qrel"
	if err := validateRunInputs(manifestPath, loaded, corpus, cases, qrels, dreams, missingSuiteQrel, seedHash); err == nil || !strings.Contains(err.Error(), "missing from seed cases") {
		t.Fatal("suite case missing from cases was accepted")
	}
	expected := []ExpectedDream{{SourceDocID: "dream", CaseID: "missing-case", SourceRefs: []Ref{{SourceDocID: corpus[0].SourceDocID}, {SourceDocID: corpus[0].SourceDocID}}}}
	if err := validateRunInputs(manifestPath, loaded, corpus, cases, qrels, expected, suite, seedHash); err == nil || !strings.Contains(err.Error(), "missing case") {
		t.Fatal("expected dream missing case was accepted")
	}

	counts := &SeedManifest{Counts: map[string]int{"qrels": 2}}
	if err := validateManifestCounts(manifestPath, counts, nil, nil, []QRel{{CaseID: "case"}}); err == nil || !strings.Contains(err.Error(), "qrels") {
		t.Fatal("qrel count mismatch was accepted")
	}
	if err := validateManifestCounts(manifestPath, &SeedManifest{Counts: map[string]int{"docs_per_case": 1}}, []CorpusItem{{SourceDocID: "a"}, {SourceDocID: "b"}, {SourceDocID: "c"}}, []Case{{CaseID: "a"}, {CaseID: "b"}}, nil); err == nil || !strings.Contains(err.Error(), "docs_per_case") {
		t.Fatal("non-divisible docs_per_case was accepted")
	}
}

func TestEvaluationRunnerMappingAndArtifactWriteFailures(t *testing.T) {
	root := writeEvalFixture(t)
	manifestPath := filepath.Join(root, "seed_manifest.json")
	suitePath := filepath.Join(root, "suite.jsonl")
	if _, err := Run(context.Background(), RunOptions{Mode: "baseline", SeedManifestPath: manifestPath, SuitePath: suitePath, TracesPath: filepath.Join(root, "traces.jsonl"), MappingPath: filepath.Join(root, "missing-mapping.json")}); err == nil {
		t.Fatal("missing mapping was accepted")
	}
	tracesPath := filepath.Join(root, "traces.jsonl")
	if err := writeJSONL(tracesPath, []RecallTrace{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), RunOptions{Mode: "baseline", SeedManifestPath: manifestPath, SuitePath: suitePath, TracesPath: tracesPath, MappingPath: filepath.Join(root, "missing-mapping.json")}); err == nil {
		t.Fatal("missing trace mapping was accepted")
	}

	manifest, suite, runConfig := &SeedManifest{}, []SuiteCase{}, RunConfig{}
	fileTargets := []struct {
		name string
		call func(string) error
	}{
		{"validation config", func(out string) error {
			return writeValidationArtifacts(out, manifest, suite, runConfig, Summary{})
		}},
		{"run mapping", func(out string) error {
			return writeRunArtifacts(out, manifest, suite, runConfig, KnowledgeMapping{}, nil, nil, Summary{})
		}},
		{"import mapping", func(out string) error {
			return writeImportArtifacts(out, manifest, suite, runConfig, KnowledgeMapping{}, Summary{})
		}},
	}
	for _, target := range fileTargets {
		t.Run(target.name, func(t *testing.T) {
			out := filepath.Join(root, target.name)
			if err := os.MkdirAll(out, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(out, "run_config.json"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := target.call(out); err == nil {
				t.Fatal("artifact writer accepted a directory target")
			}
		})
	}
}
