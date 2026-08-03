package evalharness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateV2CohortDerivationProvesDeclaredCohortAndV2Identity(t *testing.T) {
	opts := writeV2CohortValidationFixture(t)

	report, err := ValidateV2CohortDerivation(opts)
	if err != nil {
		t.Fatalf("ValidateV2CohortDerivation: %v", err)
	}
	if report.Status != "passed" || report.RetainedCorpusCount != 1 || report.RetainedCaseCount != 1 {
		t.Fatalf("report = %+v", report)
	}
	if report.RelationshipCount != 1 || report.ContractValidatedCount != 1 {
		t.Fatalf("report = %+v", report)
	}
	if len(report.RemovedSourceDocIDs) != 1 || report.RemovedSourceDocIDs[0] != "doc-2" {
		t.Fatalf("removed source docs = %#v", report.RemovedSourceDocIDs)
	}
	if len(report.DroppedCaseIDs) != 1 || report.DroppedCaseIDs[0] != "case-2" {
		t.Fatalf("dropped cases = %#v", report.DroppedCaseIDs)
	}
}

func TestValidateV2CohortDerivationRejectsUndeclaredExclusion(t *testing.T) {
	opts := writeV2CohortValidationFixture(t)
	lock := cohortFilterLockForFixture(t, opts, nil, []string{"case-2"})
	if err := writeJSONFile(opts.CohortLockPath, lock); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	_, err := ValidateV2CohortDerivation(opts)
	if err == nil || !strings.Contains(err.Error(), "filtered corpus has") {
		t.Fatalf("undeclared exclusion error = %v", err)
	}
}

func TestValidateV2CohortDerivationRejectsCohortLockHashMismatch(t *testing.T) {
	opts := writeV2CohortValidationFixture(t)
	lock := cohortFilterLockForFixture(t, opts, []string{"doc-2"}, []string{"case-2"})
	lock.FilteredSeedHash = "sha256:does-not-match"
	if err := writeJSONFile(opts.CohortLockPath, lock); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	_, err := ValidateV2CohortDerivation(opts)
	if err == nil || !strings.Contains(err.Error(), "filtered_seed_hash") {
		t.Fatalf("cohort lock hash mismatch error = %v", err)
	}
}

func TestValidateV2CohortDerivationRejectsChangedRetainedEvidence(t *testing.T) {
	opts := writeV2CohortValidationFixture(t)
	filteredManifest, err := LoadSeedManifest(opts.FilteredManifestPath)
	if err != nil {
		t.Fatalf("load filtered manifest: %v", err)
	}
	if err := writeJSONL(filepath.Join(filepath.Dir(opts.FilteredManifestPath), filteredManifest.CorpusFile), []CorpusItem{{
		SourceDocID: "doc-1",
		Content:     "Alpha uses Delta.",
		Metadata:    map[string]any{"axis": "fixture"},
	}}); err != nil {
		t.Fatalf("rewrite filtered corpus: %v", err)
	}
	lock := cohortFilterLockForFixture(t, opts, []string{"doc-2"}, []string{"case-2"})
	if err := writeJSONFile(opts.CohortLockPath, lock); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	_, err = ValidateV2CohortDerivation(opts)
	if err == nil || !strings.Contains(err.Error(), "not byte-identical") {
		t.Fatalf("changed retained evidence error = %v", err)
	}
}

func writeV2CohortValidationFixture(t *testing.T) V2CohortValidationOptions {
	t.Helper()
	root := t.TempDir()
	parentDir := filepath.Join(root, "parent-v1")
	filteredDir := filepath.Join(root, "filtered-v1")
	parentManifestPath := filepath.Join(parentDir, "seed_manifest.json")
	filteredManifestPath := filepath.Join(filteredDir, "seed_manifest.json")
	parentManifest := cohortFixtureManifest(2)
	filteredManifest := cohortFixtureManifest(1)
	if err := writeJSONFile(parentManifestPath, parentManifest); err != nil {
		t.Fatalf("write parent manifest: %v", err)
	}
	if err := writeJSONFile(filteredManifestPath, filteredManifest); err != nil {
		t.Fatalf("write filtered manifest: %v", err)
	}
	parentCorpus := []CorpusItem{
		{SourceDocID: "doc-1", Content: "Alpha uses Beta.", Metadata: map[string]any{"axis": "fixture"}},
		{SourceDocID: "doc-2", Content: "Gamma uses Delta.", Metadata: map[string]any{"axis": "fixture"}},
	}
	parentCases := []Case{{CaseID: "case-1", Query: "Alpha"}, {CaseID: "case-2", Query: "Gamma"}}
	parentQrels := []QRel{
		{CaseID: "case-1", RequiredRefs: []Ref{{Type: "fragment", SourceDocID: "doc-1"}}},
		{CaseID: "case-2", RequiredRefs: []Ref{{Type: "fragment", SourceDocID: "doc-2"}}},
	}
	parentAnswers := []AnswerLabel{{CaseID: "case-1", ReferenceAnswer: "Beta"}, {CaseID: "case-2", ReferenceAnswer: "Delta"}}
	parentTransforms := []map[string]string{{"case_id": "case-1", "operation": "fixture"}, {"case_id": "case-2", "operation": "fixture"}}
	writeFixtureV1 := func(dir string, corpus []CorpusItem, cases []Case, qrels []QRel, answers []AnswerLabel, transforms []map[string]string) {
		t.Helper()
		if err := writeJSONL(filepath.Join(dir, "corpus.jsonl"), corpus); err != nil {
			t.Fatalf("write corpus: %v", err)
		}
		if err := writeJSONL(filepath.Join(dir, "cases.jsonl"), cases); err != nil {
			t.Fatalf("write cases: %v", err)
		}
		if err := writeJSONL(filepath.Join(dir, "qrels.jsonl"), qrels); err != nil {
			t.Fatalf("write qrels: %v", err)
		}
		if err := writeJSONL(filepath.Join(dir, "answers.jsonl"), answers); err != nil {
			t.Fatalf("write answers: %v", err)
		}
		if err := writeJSONL(filepath.Join(dir, "transforms.jsonl"), transforms); err != nil {
			t.Fatalf("write transforms: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "licenses.md"), []byte("fixture license\n"), 0o644); err != nil {
			t.Fatalf("write licenses: %v", err)
		}
	}
	writeFixtureV1(parentDir, parentCorpus, parentCases, parentQrels, parentAnswers, parentTransforms)
	writeFixtureV1(
		filteredDir,
		parentCorpus[:1],
		parentCases[:1],
		parentQrels[:1],
		parentAnswers[:1],
		parentTransforms[:1],
	)
	parentSuitePath := filepath.Join(root, "parent-suite.jsonl")
	filteredSuitePath := filepath.Join(root, "filtered-suite.jsonl")
	if err := writeJSONL(parentSuitePath, []SuiteCase{{CaseID: "case-1"}, {CaseID: "case-2"}}); err != nil {
		t.Fatalf("write parent suite: %v", err)
	}
	if err := writeJSONL(filteredSuitePath, []SuiteCase{{CaseID: "case-1"}}); err != nil {
		t.Fatalf("write filtered suite: %v", err)
	}
	ledgerPath := filepath.Join(root, "relationship_ledger.jsonl")
	if err := writeJSONL(ledgerPath, []relationshipLedgerRow{{
		SourceDocID: "doc-1",
		Support:     "Alpha uses Beta.",
		Subject:     "Alpha",
		Predicate:   "uses",
		Object:      "Beta",
	}}); err != nil {
		t.Fatalf("write relationship ledger: %v", err)
	}
	derivedDir := filepath.Join(root, "derived-v2")
	if _, err := DeriveV2Seed(DeriveV2SeedOptions{
		SourceManifestPath:     filteredManifestPath,
		SourceSuitePath:        filteredSuitePath,
		RelationshipLedgerPath: ledgerPath,
		OutputDir:              derivedDir,
		SeedID:                 "fixture_v2",
	}); err != nil {
		t.Fatalf("derive V2: %v", err)
	}
	opts := V2CohortValidationOptions{
		ParentManifestPath:   parentManifestPath,
		ParentSuitePath:      parentSuitePath,
		FilteredManifestPath: filteredManifestPath,
		FilteredSuitePath:    filteredSuitePath,
		DerivedManifestPath:  filepath.Join(derivedDir, "seed_manifest.json"),
		DerivedSuitePath:     filepath.Join(derivedDir, "suite.jsonl"),
		CohortLockPath:       filepath.Join(root, "cohort_lock.json"),
	}
	if err := writeJSONFile(opts.CohortLockPath, cohortFilterLockForFixture(t, opts, []string{"doc-2"}, []string{"case-2"})); err != nil {
		t.Fatalf("write cohort lock: %v", err)
	}
	return opts
}

func cohortFixtureManifest(count int) SeedManifest {
	return SeedManifest{
		SchemaVersion:  SeedSchemaVersion,
		SeedID:         "fixture_v1",
		CorpusFile:     "corpus.jsonl",
		CasesFile:      "cases.jsonl",
		QrelsFile:      "qrels.jsonl",
		AnswersFile:    "answers.jsonl",
		TransformsFile: "transforms.jsonl",
		LicensesFile:   "licenses.md",
		Counts: map[string]int{
			"corpus": count, "cases": count, "qrels": count, "answers": count, "transforms": count,
		},
	}
}

func cohortFilterLockForFixture(t *testing.T, opts V2CohortValidationOptions, removed, dropped []string) cohortFilterLock {
	t.Helper()
	parentManifest, err := LoadSeedManifest(opts.ParentManifestPath)
	if err != nil {
		t.Fatalf("load parent manifest: %v", err)
	}
	filteredManifest, err := LoadSeedManifest(opts.FilteredManifestPath)
	if err != nil {
		t.Fatalf("load filtered manifest: %v", err)
	}
	parentHash, err := SeedHash(opts.ParentManifestPath, parentManifest)
	if err != nil {
		t.Fatalf("hash parent: %v", err)
	}
	filteredHash, err := SeedHash(opts.FilteredManifestPath, filteredManifest)
	if err != nil {
		t.Fatalf("hash filtered: %v", err)
	}
	return cohortFilterLock{
		SchemaVersion:       cohortFilterLockSchema,
		SeedID:              "fixture_v1",
		ParentSeedHash:      parentHash,
		FilteredSeedHash:    filteredHash,
		RemovedSourceDocIDs: removed,
		DroppedCaseIDs:      dropped,
		ExpectedCounts:      filteredManifest.Counts,
		Invariant:           "fixture",
	}
}
