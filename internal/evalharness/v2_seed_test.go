package evalharness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeriveV2SeedRetainsV1EvidenceAndProducesFlatRelationships(t *testing.T) {
	sourceManifestPath, sourceSuitePath, ledgerPath, outputDir := writeV2DerivationFixture(t, "Māori uses PostgreSQL.")

	report, err := DeriveV2Seed(DeriveV2SeedOptions{
		SourceManifestPath:     sourceManifestPath,
		SourceSuitePath:        sourceSuitePath,
		RelationshipLedgerPath: ledgerPath,
		OutputDir:              outputDir,
		SeedID:                 "fixture_v2",
	})
	if err != nil {
		t.Fatalf("DeriveV2Seed: %v", err)
	}
	if report.Status != "passed" || report.CorpusCount != 1 || report.RelationshipCount != 1 || report.ContractValidatedCount != 1 {
		t.Fatalf("derivation report = %+v", report)
	}

	targetManifestPath := filepath.Join(outputDir, "seed_manifest.json")
	targetManifest, err := LoadSeedManifest(targetManifestPath)
	if err != nil {
		t.Fatalf("LoadSeedManifest target: %v", err)
	}
	if targetManifest.SchemaVersion != SeedSchemaVersionV2 || targetManifest.ParentSeedID != "fixture_v1" || targetManifest.EvidenceIdentityHash != report.EvidenceIdentityHash {
		t.Fatalf("target manifest = %+v", targetManifest)
	}
	targetCorpus, err := LoadCorpus(targetManifestPath, targetManifest)
	if err != nil {
		t.Fatalf("LoadCorpus target: %v", err)
	}
	if len(targetCorpus) != 1 || targetCorpus[0].Content != "Māori uses PostgreSQL." || len(targetCorpus[0].Relationships) != 1 {
		t.Fatalf("target corpus = %+v", targetCorpus)
	}
	relationship := targetCorpus[0].Relationships[0].(map[string]any)
	if relationship["ref"] != "relationship_1" || relationship["polarity"] != "+" {
		t.Fatalf("relationship = %#v", relationship)
	}
	subject := relationship["subject"].(map[string]any)
	if subject["name"] != "Māori" {
		t.Fatalf("subject = %#v", subject)
	}
	predicate := relationship["predicate"].(map[string]any)
	if predicate["proposed_key"] != "uses" {
		t.Fatalf("predicate = %#v", predicate)
	}
	object := relationship["object"].(map[string]any)["entity"].(map[string]any)
	if object["name"] != "PostgreSQL" {
		t.Fatalf("object = %#v", object)
	}
	indices, ok := relationship["evidence_indices"].([]any)
	if !ok || len(indices) != 1 || indices[0] != float64(0) {
		t.Fatalf("evidence_indices = %#v", relationship["evidence_indices"])
	}
	if equal, err := sameFileBytes(filepath.Join(filepath.Dir(sourceManifestPath), "cases.jsonl"), filepath.Join(outputDir, "cases.jsonl")); err != nil || !equal {
		t.Fatalf("copied cases byte equality = %v, %v", equal, err)
	}
	validated, err := ValidateV2Derivation(sourceManifestPath, sourceSuitePath, targetManifestPath, filepath.Join(outputDir, derivedSuiteFileName))
	if err != nil {
		t.Fatalf("ValidateV2Derivation: %v", err)
	}
	if validated.DerivedSeedHash != report.DerivedSeedHash || validated.EvidenceIdentityHash != report.EvidenceIdentityHash {
		t.Fatalf("validated report = %+v; derived report = %+v", validated, report)
	}
}

func TestDeriveV2SeedRejectsUnresolvableLedgerSelection(t *testing.T) {
	sourceManifestPath, sourceSuitePath, ledgerPath, outputDir := writeV2DerivationFixture(t, "Alpha uses Beta. Alpha uses Gamma.")
	if err := writeJSONL(ledgerPath, []relationshipLedgerRow{{
		SourceDocID: "doc-1",
		Support:     "Alpha uses",
		Subject:     "Alpha",
		Predicate:   "does not occur",
		Object:      "Beta",
	}}); err != nil {
		t.Fatalf("write ambiguous ledger: %v", err)
	}

	_, err := DeriveV2Seed(DeriveV2SeedOptions{
		SourceManifestPath:     sourceManifestPath,
		SourceSuitePath:        sourceSuitePath,
		RelationshipLedgerPath: ledgerPath,
		OutputDir:              outputDir,
		SeedID:                 "fixture_v2",
	})
	if err == nil || !strings.Contains(err.Error(), "predicate surface does not occur") {
		t.Fatalf("DeriveV2Seed unresolvable ledger error = %v", err)
	}
	if _, err := os.Stat(outputDir); !os.IsNotExist(err) {
		t.Fatalf("derived output exists after rejected ledger: %v", err)
	}
}

func TestDeriveV2SeedUsesDocumentedFallbackForStaleLedgerSelection(t *testing.T) {
	sourceManifestPath, sourceSuitePath, ledgerPath, outputDir := writeV2DerivationFixture(t, "Alpha uses Beta.")
	if err := writeJSONL(ledgerPath, []relationshipLedgerRow{{
		SourceDocID: "doc-1",
		Support:     "Stale support.",
		Subject:     "Stale",
		Predicate:   "uses",
		Object:      "Stale",
	}}); err != nil {
		t.Fatalf("write stale ledger: %v", err)
	}

	report, err := DeriveV2Seed(DeriveV2SeedOptions{
		SourceManifestPath:     sourceManifestPath,
		SourceSuitePath:        sourceSuitePath,
		RelationshipLedgerPath: ledgerPath,
		OutputDir:              outputDir,
		SeedID:                 "fixture_v2",
	})
	if err != nil {
		t.Fatalf("DeriveV2Seed: %v", err)
	}
	if report.FallbackRelationshipCount != 1 || len(report.FallbackSourceDocIDs) != 1 || report.FallbackSourceDocIDs[0] != "doc-1" {
		t.Fatalf("fallback report = %+v", report)
	}
	manifest, err := LoadSeedManifest(filepath.Join(outputDir, "seed_manifest.json"))
	if err != nil {
		t.Fatalf("LoadSeedManifest: %v", err)
	}
	corpus, err := LoadCorpus(filepath.Join(outputDir, "seed_manifest.json"), manifest)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	relationship := corpus[0].Relationships[0].(map[string]any)
	if relationship["subject"].(map[string]any)["name"] != "Alpha" || relationship["object"].(map[string]any)["entity"].(map[string]any)["name"] != "Beta" {
		t.Fatalf("fallback relationship = %#v", relationship)
	}
}

func TestDeriveV2SeedRejectsDuplicateLedgerSourceDocID(t *testing.T) {
	sourceManifestPath, sourceSuitePath, ledgerPath, outputDir := writeV2DerivationFixture(t, "Alpha uses Beta.")
	row := relationshipLedgerRow{
		SourceDocID: "doc-1",
		Support:     "Alpha uses Beta.",
		Subject:     "Alpha",
		Predicate:   "uses",
		Object:      "Beta",
	}
	if err := writeJSONL(ledgerPath, []relationshipLedgerRow{row, row}); err != nil {
		t.Fatalf("write duplicate ledger: %v", err)
	}

	_, err := DeriveV2Seed(DeriveV2SeedOptions{
		SourceManifestPath:     sourceManifestPath,
		SourceSuitePath:        sourceSuitePath,
		RelationshipLedgerPath: ledgerPath,
		OutputDir:              outputDir,
		SeedID:                 "fixture_v2",
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate source_doc_id") {
		t.Fatalf("DeriveV2Seed duplicate ledger error = %v", err)
	}
}

func TestDeriveV2SeedRejectsExtraLedgerRowWithoutCohortLock(t *testing.T) {
	sourceManifestPath, sourceSuitePath, ledgerPath, outputDir := writeV2DerivationFixture(t, "Alpha uses Beta.")
	if err := writeJSONL(ledgerPath, []relationshipLedgerRow{
		{
			SourceDocID: "doc-1", Support: "Alpha uses Beta.", Subject: "Alpha", Predicate: "uses", Object: "Beta",
		},
		{
			SourceDocID: "doc-excluded", Support: "Excluded uses Ledger.", Subject: "Excluded", Predicate: "uses", Object: "Ledger",
		},
	}); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	_, err := DeriveV2Seed(DeriveV2SeedOptions{
		SourceManifestPath:     sourceManifestPath,
		SourceSuitePath:        sourceSuitePath,
		RelationshipLedgerPath: ledgerPath,
		OutputDir:              outputDir,
		SeedID:                 "fixture_v2",
	})
	if err == nil || !strings.Contains(err.Error(), "outside the retained V1 cohort") {
		t.Fatalf("DeriveV2Seed extra ledger row error = %v", err)
	}
}

func TestDeriveV2SeedAllowsCohortLockDeclaredExtraLedgerRow(t *testing.T) {
	sourceManifestPath, sourceSuitePath, ledgerPath, outputDir := writeV2DerivationFixture(t, "Alpha uses Beta.")
	if err := writeJSONL(ledgerPath, []relationshipLedgerRow{
		{
			SourceDocID: "doc-1", Support: "Alpha uses Beta.", Subject: "Alpha", Predicate: "uses", Object: "Beta",
		},
		{
			SourceDocID: "doc-excluded", Support: "Excluded uses Ledger.", Subject: "Excluded", Predicate: "uses", Object: "Ledger",
		},
	}); err != nil {
		t.Fatalf("write ledger: %v", err)
	}
	manifest, err := LoadSeedManifest(sourceManifestPath)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	seedHash, err := SeedHash(sourceManifestPath, manifest)
	if err != nil {
		t.Fatalf("hash manifest: %v", err)
	}
	cohortLockPath := filepath.Join(filepath.Dir(outputDir), "cohort_lock.json")
	if err := writeJSONFile(cohortLockPath, cohortFilterLock{
		SchemaVersion:       cohortFilterLockSchema,
		SeedID:              manifest.SeedID,
		ParentSeedHash:      seedHash,
		FilteredSeedHash:    seedHash,
		RemovedSourceDocIDs: []string{"doc-excluded"},
		ExpectedCounts:      manifest.Counts,
		Invariant:           "fixture",
	}); err != nil {
		t.Fatalf("write cohort lock: %v", err)
	}

	report, err := DeriveV2Seed(DeriveV2SeedOptions{
		SourceManifestPath:     sourceManifestPath,
		SourceSuitePath:        sourceSuitePath,
		RelationshipLedgerPath: ledgerPath,
		CohortLockPath:         cohortLockPath,
		OutputDir:              outputDir,
		SeedID:                 "fixture_v2",
	})
	if err != nil {
		t.Fatalf("DeriveV2Seed: %v", err)
	}
	if report.ExcludedLedgerRows != 1 {
		t.Fatalf("excluded ledger rows = %d, want 1", report.ExcludedLedgerRows)
	}
}

func TestValidateV2DerivationRejectsChangedEvidence(t *testing.T) {
	sourceManifestPath, sourceSuitePath, ledgerPath, outputDir := writeV2DerivationFixture(t, "Alpha uses Beta.")
	if _, err := DeriveV2Seed(DeriveV2SeedOptions{
		SourceManifestPath:     sourceManifestPath,
		SourceSuitePath:        sourceSuitePath,
		RelationshipLedgerPath: ledgerPath,
		OutputDir:              outputDir,
		SeedID:                 "fixture_v2",
	}); err != nil {
		t.Fatalf("DeriveV2Seed: %v", err)
	}
	targetManifestPath := filepath.Join(outputDir, "seed_manifest.json")
	targetManifest, err := LoadSeedManifest(targetManifestPath)
	if err != nil {
		t.Fatalf("LoadSeedManifest: %v", err)
	}
	targetCorpus, err := LoadCorpus(targetManifestPath, targetManifest)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	targetCorpus[0].Content = "Alpha uses Delta."
	if err := writeJSONL(filepath.Join(outputDir, targetManifest.CorpusFile), targetCorpus); err != nil {
		t.Fatalf("write changed target corpus: %v", err)
	}
	_, err = ValidateV2Derivation(sourceManifestPath, sourceSuitePath, targetManifestPath, filepath.Join(outputDir, derivedSuiteFileName))
	if err == nil || !strings.Contains(err.Error(), "evidence identity hash") {
		t.Fatalf("ValidateV2Derivation changed evidence error = %v", err)
	}
}

func TestValidateV2DerivationRejectsChangedSidecar(t *testing.T) {
	sourceManifestPath, sourceSuitePath, ledgerPath, outputDir := writeV2DerivationFixture(t, "Alpha uses Beta.")
	if _, err := DeriveV2Seed(DeriveV2SeedOptions{
		SourceManifestPath:     sourceManifestPath,
		SourceSuitePath:        sourceSuitePath,
		RelationshipLedgerPath: ledgerPath,
		OutputDir:              outputDir,
		SeedID:                 "fixture_v2",
	}); err != nil {
		t.Fatalf("DeriveV2Seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "licenses.md"), []byte("changed license\n"), 0o644); err != nil {
		t.Fatalf("write changed sidecar: %v", err)
	}
	_, err := ValidateV2Derivation(sourceManifestPath, sourceSuitePath, filepath.Join(outputDir, "seed_manifest.json"), filepath.Join(outputDir, derivedSuiteFileName))
	if err == nil || !strings.Contains(err.Error(), "licenses.md is not byte-identical") {
		t.Fatalf("ValidateV2Derivation changed sidecar error = %v", err)
	}
}

func TestPredicateProposalKeyUsesGenericKeyForPunctuationSurface(t *testing.T) {
	key, err := predicateProposalKey("-")
	if err != nil {
		t.Fatalf("predicateProposalKey: %v", err)
	}
	if key != "related_to" {
		t.Fatalf("predicateProposalKey = %q, want related_to", key)
	}
}

func writeV2DerivationFixture(t *testing.T, content string) (string, string, string, string) {
	t.Helper()
	root := t.TempDir()
	sourceDir := filepath.Join(root, "v1")
	manifestPath := filepath.Join(sourceDir, "seed_manifest.json")
	manifest := SeedManifest{
		SchemaVersion:  SeedSchemaVersion,
		SeedID:         "fixture_v1",
		CorpusFile:     "corpus.jsonl",
		CasesFile:      "cases.jsonl",
		QrelsFile:      "qrels.jsonl",
		AnswersFile:    "answers.jsonl",
		TransformsFile: "transforms.jsonl",
		LicensesFile:   "licenses.md",
		Counts: map[string]int{
			"corpus":     1,
			"cases":      1,
			"qrels":      1,
			"answers":    1,
			"transforms": 1,
		},
	}
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		t.Fatalf("write source manifest: %v", err)
	}
	if err := writeJSONL(filepath.Join(sourceDir, manifest.CorpusFile), []CorpusItem{{
		SourceDocID: "doc-1",
		Title:       "fixture",
		Content:     content,
		Metadata:    map[string]any{"axis": "fixture"},
	}}); err != nil {
		t.Fatalf("write source corpus: %v", err)
	}
	if err := writeJSONL(filepath.Join(sourceDir, manifest.CasesFile), []Case{{CaseID: "case-1", Query: "fixture"}}); err != nil {
		t.Fatalf("write source cases: %v", err)
	}
	if err := writeJSONL(filepath.Join(sourceDir, manifest.QrelsFile), []QRel{{CaseID: "case-1", RequiredRefs: []Ref{{Type: "fragment", SourceDocID: "doc-1"}}}}); err != nil {
		t.Fatalf("write source qrels: %v", err)
	}
	if err := writeJSONL(filepath.Join(sourceDir, manifest.AnswersFile), []AnswerLabel{{CaseID: "case-1", ReferenceAnswer: "fixture"}}); err != nil {
		t.Fatalf("write source answers: %v", err)
	}
	if err := writeJSONL(filepath.Join(sourceDir, manifest.TransformsFile), []map[string]string{{"case_id": "case-1", "operation": "fixture"}}); err != nil {
		t.Fatalf("write source transforms: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, manifest.LicensesFile), []byte("fixture license\n"), 0o644); err != nil {
		t.Fatalf("write source licenses: %v", err)
	}
	suitePath := filepath.Join(root, "suite.jsonl")
	if err := writeJSONL(suitePath, []SuiteCase{{CaseID: "case-1"}}); err != nil {
		t.Fatalf("write source suite: %v", err)
	}
	ledgerPath := filepath.Join(root, "relationship_ledger.jsonl")
	if err := writeJSONL(ledgerPath, []relationshipLedgerRow{{
		SourceDocID: "doc-1",
		Support:     content,
		Subject:     strings.Fields(content)[0],
		Predicate:   "uses",
		Object:      strings.TrimSuffix(strings.Fields(content)[2], "."),
		SubjectKind: "organization",
		ObjectKind:  "product",
	}}); err != nil {
		t.Fatalf("write relationship ledger: %v", err)
	}
	return manifestPath, suitePath, ledgerPath, filepath.Join(root, "v2")
}
