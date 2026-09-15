package evalharness

import (
	"path/filepath"
	"testing"
)

func TestEvaluationRunnerLoadsInputsAndWritesAllArtifactProfiles(t *testing.T) {
	root := writeEvalFixture(t)
	manifestPath := filepath.Join(root, "seed_manifest.json")
	suitePath := filepath.Join(root, "suite.jsonl")
	manifest, corpus, cases, qrels, dreams, suite, seedHash, err := loadRunInputs(manifestPath, suitePath)
	if err != nil {
		t.Fatalf("loadRunInputs: %v", err)
	}
	if err := validateRunInputs(manifestPath, manifest, corpus, cases, qrels, dreams, suite, seedHash); err != nil {
		t.Fatalf("validateRunInputs: %v", err)
	}
	runConfig := RunConfig{Mode: "validate", SeedManifest: manifestPath, SeedHash: seedHash, SuitePath: suitePath}
	summary := Summary{RunID: "fixture", Mode: "validate", SeedHash: seedHash, CaseCount: len(cases)}
	if err := writeValidationArtifacts(filepath.Join(root, "validation"), manifest, suite, runConfig, summary); err != nil {
		t.Fatalf("writeValidationArtifacts: %v", err)
	}
	if err := writeRunArtifacts(filepath.Join(root, "run"), manifest, suite, runConfig, KnowledgeMapping{}, nil, nil, summary); err != nil {
		t.Fatalf("writeRunArtifacts: %v", err)
	}
	if err := writeImportArtifacts(filepath.Join(root, "import"), manifest, suite, runConfig, KnowledgeMapping{}, summary); err != nil {
		t.Fatalf("writeImportArtifacts: %v", err)
	}
}

func TestEvaluationRunnerQrelAndManifestFailureBranches(t *testing.T) {
	root := writeEvalFixture(t)
	manifestPath := filepath.Join(root, "seed_manifest.json")
	manifest, corpus, cases, qrels, dreams, suite, seedHash, err := loadRunInputs(manifestPath, filepath.Join(root, "suite.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRunInputs(manifestPath, manifest, corpus, cases, append(qrels, QRel{CaseID: "unknown", RequiredRefs: []Ref{{SourceDocID: "doc-alpha"}}}), dreams, suite, seedHash); err == nil {
		t.Fatal("qrel for unknown case was accepted")
	}
	if err := validateRequiredQRelMappings(map[string]QRel{"case-1": {RequiredRefs: []Ref{{Type: "evidence", SourceDocID: "missing"}}}}, []SuiteCase{{CaseID: "case-1"}}, KnowledgeMapping{}); err == nil {
		t.Fatal("empty mapping was accepted")
	}
}
