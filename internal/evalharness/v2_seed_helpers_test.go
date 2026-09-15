package evalharness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRelationshipLedgerValidationRejectsMalformedRows(t *testing.T) {
	if _, err := decodeRelationshipLedgerRow([]byte("{")); err == nil {
		t.Fatal("malformed ledger JSON was accepted")
	}
	if _, err := decodeRelationshipLedgerRow([]byte(`{"source_doc_id":"doc","support":"s","subject":"a","predicate":"uses","object":"b","extra":true}`)); err == nil || !strings.Contains(err.Error(), "unsupported field") {
		t.Fatalf("unsupported ledger field error = %v", err)
	}
	negative := -1
	for name, row := range map[string]relationshipLedgerRow{
		"source":     {Support: "s", Subject: "a", Predicate: "uses", Object: "b"},
		"support":    {SourceDocID: "doc", Subject: "a", Predicate: "uses", Object: "b"},
		"subject":    {SourceDocID: "doc", Support: "s", Predicate: "uses", Object: "b"},
		"predicate":  {SourceDocID: "doc", Support: "s", Subject: "a", Object: "b"},
		"object":     {SourceDocID: "doc", Support: "s", Subject: "a", Predicate: "uses"},
		"occurrence": {SourceDocID: "doc", Support: "s", Subject: "a", Predicate: "uses", Object: "b", PredicateOccurrence: &negative},
		"kind":       {SourceDocID: "doc", Support: "s", Subject: "a", Predicate: "uses", Object: "b", SubjectKind: "unsupported"},
		"polarity":   {SourceDocID: "doc", Support: "s", Subject: "a", Predicate: "uses", Object: "b", Polarity: "?"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateRelationshipLedgerRow(row); err == nil {
				t.Fatal("invalid ledger row was accepted")
			}
		})
	}
	valid := relationshipLedgerRow{SourceDocID: "doc", Support: "Alpha uses Beta.", Subject: "Alpha", Predicate: "uses", Object: "Beta", SupportOccurrence: intPtr(0)}
	if err := validateRelationshipLedgerRow(valid); err != nil {
		t.Fatalf("valid ledger row rejected: %v", err)
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRelationshipLedgerRow(encoded)
	if err != nil || decoded.SourceDocID != valid.SourceDocID {
		t.Fatalf("decoded ledger row = %+v, %v", decoded, err)
	}
}

func TestRelationshipLedgerSpanResolutionCoversAmbiguityAndFallback(t *testing.T) {
	content := []rune("Alpha uses Beta. Alpha uses Gamma.")
	if _, _, err := resolveLedgerSurface(content, "", nil, 0, len(content), "subject"); err == nil {
		t.Fatal("empty surface was accepted")
	}
	if _, _, err := resolveLedgerSurface(content, "uses", nil, 0, 5, "predicate"); err == nil {
		t.Fatal("missing surface was accepted")
	}
	if _, _, err := resolveLedgerSurface(content, "Alpha", nil, 0, len(content), "subject"); err == nil || !strings.Contains(err.Error(), "explicit occurrence") {
		t.Fatalf("ambiguous surface error = %v", err)
	}
	occurrence := 1
	start, end, err := resolveLedgerSurface(content, "Alpha", &occurrence, 0, len(content), "subject")
	if err != nil || string(content[start:end]) != "Alpha" {
		t.Fatalf("selected surface = %q, %v", string(content[start:end]), err)
	}
	outOfRange := 4
	if _, _, err := resolveLedgerSurface(content, "uses", &outOfRange, 0, len(content), "predicate"); err == nil {
		t.Fatal("out-of-range occurrence was accepted")
	}
	if _, _, err := resolveLedgerSurface(content, "uses", nil, -1, len(content), "predicate"); err == nil {
		t.Fatal("invalid scope was accepted")
	}
	if start, end, err := fallbackPredicateSpan(content, "uses", nil); err != nil || string(content[start:end]) != "uses" {
		t.Fatalf("fallback predicate = %q, %v", string(content[start:end]), err)
	}
	if _, _, err := fallbackPredicateSpan(content, "missing", nil); err == nil {
		t.Fatal("missing fallback predicate was accepted")
	}
	if _, _, err := fallbackPredicateSpan([]rune("uses only"), "uses", nil); err == nil {
		t.Fatal("predicate without endpoints was accepted")
	}
	if start, end := sentenceSupportSpan(content, 6, 10); string(content[start:end]) != "Alpha uses Beta." {
		t.Fatalf("sentence support = %q", string(content[start:end]))
	}
	if _, _, ok := nearestFallbackEntity([]rune("is to"), 0, len([]rune("is to")), true); ok {
		t.Fatal("stopwords were selected as a fallback entity")
	}
	if tokens := fallbackTokens([]rune("A-2"), 0, 3); len(tokens) != 2 {
		t.Fatalf("fallback tokens = %#v", tokens)
	}
}

func TestRelationshipLedgerProposalAndArtifactHelpers(t *testing.T) {
	if got, err := predicateProposalKey("works-with"); err != nil || got != "works_with" {
		t.Fatalf("predicate key = %q, %v", got, err)
	}
	if got, err := predicateProposalKey(" "); err != nil || got != "related_to" {
		t.Fatalf("empty predicate key = %q, %v", got, err)
	}
	if _, err := predicateProposalKey(strings.Repeat("x", 129)); err == nil {
		t.Fatal("overlong predicate key was accepted")
	}
	if !sameRunes([]rune("a"), []rune("a")) || sameRunes([]rune("a"), []rune("b")) {
		t.Fatal("sameRunes result was incorrect")
	}
	root := t.TempDir()
	manifest := filepath.Join(root, "seed_manifest.json")
	if err := os.WriteFile(manifest, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := safeSeedFilePath(manifest, "../escape"); err == nil {
		t.Fatal("unsafe seed path was accepted")
	}
	if got, err := derivedSeedPath(root, "corpus.jsonl"); err != nil || got != filepath.Join(root, "corpus.jsonl") {
		t.Fatalf("derived seed path = %q, %v", got, err)
	}
	if _, err := derivedSeedPath(root, "../escape"); err == nil {
		t.Fatal("unsafe derived path was accepted")
	}
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	if err := copyFileExact(manifest, left); err != nil {
		t.Fatal(err)
	}
	if err := copyFileExact(manifest, right); err != nil {
		t.Fatal(err)
	}
	if equal, err := sameFileBytes(left, right); err != nil || !equal {
		t.Fatalf("sameFileBytes equal = %v, %v", equal, err)
	}
	if err := os.WriteFile(right, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if equal, err := sameFileBytes(left, right); err != nil || equal {
		t.Fatalf("sameFileBytes changed = %v, %v", equal, err)
	}
	if _, err := sha256File(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing file hash succeeded")
	}
}

func TestValidateV2CorpusRelationshipsRequiresFlatRelationship(t *testing.T) {
	manifest := &SeedManifest{SchemaVersion: SeedSchemaVersionV2}
	if err := validateV2CorpusRelationships(manifest, []CorpusItem{{SourceDocID: "doc", Content: "claim"}}); err == nil || !strings.Contains(err.Error(), "has no relationships") {
		t.Fatalf("missing relationship error = %v", err)
	}
	if err := validateV2CorpusRelationships(&SeedManifest{SchemaVersion: SeedSchemaVersion}, []CorpusItem{{SourceDocID: "doc"}}); err != nil {
		t.Fatalf("V1 corpus validation should be skipped: %v", err)
	}
}

func TestRelationshipLedgerFallbackAndCohortBindingFailures(t *testing.T) {
	row := relationshipLedgerRow{Predicate: "uses", Polarity: "-"}
	relationship, err := flatRelationshipFallback("Alpha uses Beta.", row)
	if err != nil {
		t.Fatalf("fallback relationship: %v", err)
	}
	if relationship["polarity"] != "-" {
		t.Fatalf("fallback polarity = %#v", relationship["polarity"])
	}
	if _, err := flatRelationshipFallback("uses only", row); err == nil {
		t.Fatal("fallback without endpoints was accepted")
	}
	if _, _, err := fallbackPredicateSpan([]rune("Alpha uses Beta. Alpha uses Gamma."), "uses", nil); err != nil {
		t.Fatalf("fallback predicate with usable sentence: %v", err)
	}
	occurrence := 1
	if start, end, err := fallbackPredicateSpan([]rune("Alpha uses Beta. Alpha uses Gamma."), "uses", &occurrence); err != nil || start >= end {
		t.Fatalf("explicit fallback occurrence = %d:%d, %v", start, end, err)
	}
	manifest := &SeedManifest{SeedID: "seed"}
	lockRoot := t.TempDir()
	lockPath := filepath.Join(lockRoot, "lock.json")
	if err := writeJSONFile(lockPath, cohortFilterLock{SchemaVersion: cohortFilterLockSchema, SeedID: "other", FilteredSeedHash: "hash", ParentSeedHash: "parent", RemovedSourceDocIDs: []string{"doc"}, ExpectedCounts: map[string]int{"corpus": 1, "cases": 1, "qrels": 1, "answers": 1, "transforms": 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := allowedExtraLedgerRows(manifest, "hash", lockPath); err == nil || !strings.Contains(err.Error(), "does not bind") {
		t.Fatalf("unbound cohort lock was accepted: %v", err)
	}
	if _, err := allowedExtraLedgerRows(manifest, "hash", filepath.Join(lockRoot, "missing.json")); err == nil {
		t.Fatal("missing cohort lock was accepted")
	}
}

func TestRelationshipLedgerAndArtifactCopyFailures(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	manifest := &SeedManifest{SeedID: "seed", CasesFile: "cases.jsonl", CorpusFile: "corpus.jsonl"}
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(root, "ledger.jsonl")
	if err := os.WriteFile(ledgerPath, []byte("# comment\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadRelationshipLedger(ledgerPath, manifest, "hash", []CorpusItem{{SourceDocID: "doc"}}, ""); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing ledger row was accepted: %v", err)
	}
	if err := os.WriteFile(ledgerPath, []byte("{\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadRelationshipLedger(ledgerPath, manifest, "hash", nil, ""); err == nil {
		t.Fatal("malformed ledger was accepted")
	}
	if err := copyFileExact(filepath.Join(root, "missing"), filepath.Join(root, "copy")); err == nil {
		t.Fatal("missing source copy succeeded")
	}
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFileExact(directory, filepath.Join(root, "directory-copy")); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory copy error = %v", err)
	}
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := copyFileExact(source, target); err != nil {
		t.Fatal(err)
	}
	if err := copyFileExact(source, target); err == nil {
		t.Fatal("existing destination copy succeeded")
	}
	if _, err := validateCopiedFile(source, filepath.Join(root, "missing-target"), "fixture"); err == nil {
		t.Fatal("missing copied artifact was accepted")
	}
	if err := os.WriteFile(target, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateCopiedFile(source, target, "fixture"); err == nil || !strings.Contains(err.Error(), "byte-identical") {
		t.Fatalf("changed copied artifact was accepted: %v", err)
	}
	if _, err := sameFileBytes(source, filepath.Join(root, "missing-right")); err == nil {
		t.Fatal("missing right file comparison succeeded")
	}
}

func TestRelationshipLedgerManifestArtifactHelpersCoverNilAndDefaults(t *testing.T) {
	if got := manifestArtifactFiles(nil); got != nil {
		t.Fatalf("nil manifest artifacts = %#v", got)
	}
	manifest := &SeedManifest{CasesFile: " cases.jsonl ", QrelsFile: "qrels.jsonl", AnswersFile: "", LicensesFile: "licenses.txt"}
	files := manifestArtifactFiles(manifest)
	if len(files) != 3 || files[0] != " cases.jsonl " {
		t.Fatalf("manifest artifacts = %#v", files)
	}
	for _, name := range []string{"", "/absolute", "../escape", "."} {
		if _, err := safeSeedFilePath("/tmp/manifest.json", name); err == nil {
			t.Fatalf("unsafe source artifact %q was accepted", name)
		}
		if _, err := derivedSeedPath("/tmp/output", name); err == nil {
			t.Fatalf("unsafe derived artifact %q was accepted", name)
		}
	}
	if hash, err := sha256File(filepath.Join(t.TempDir(), "missing")); err == nil || hash != "" {
		t.Fatalf("missing hash = %q, %v", hash, err)
	}
}

func TestV2DerivationRejectsMissingAndMismatchedInputs(t *testing.T) {
	if _, err := DeriveV2Seed(DeriveV2SeedOptions{SourceManifestPath: filepath.Join(t.TempDir(), "missing"), SourceSuitePath: "suite", RelationshipLedgerPath: "ledger", OutputDir: filepath.Join(t.TempDir(), "out"), SeedID: "v2"}); err == nil {
		t.Fatal("missing source manifest was accepted")
	}
	sourceManifest, sourceSuite, ledger, output := writeV2DerivationFixture(t, "Alpha uses Beta.")
	manifest, err := LoadSeedManifest(sourceManifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion = "wrong"
	if err := writeJSONFile(sourceManifest, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := DeriveV2Seed(DeriveV2SeedOptions{SourceManifestPath: sourceManifest, SourceSuitePath: sourceSuite, RelationshipLedgerPath: ledger, OutputDir: output, SeedID: "v2"}); err == nil || !strings.Contains(err.Error(), "unsupported seed schema_version") {
		t.Fatalf("wrong source schema error = %v", err)
	}

	sourceManifest, sourceSuite, ledger, output = writeV2DerivationFixture(t, "Alpha uses Beta.")
	if _, err := DeriveV2Seed(DeriveV2SeedOptions{SourceManifestPath: sourceManifest, SourceSuitePath: sourceSuite, RelationshipLedgerPath: filepath.Join(filepath.Dir(ledger), "missing-ledger"), OutputDir: output, SeedID: "v2"}); err == nil {
		t.Fatal("missing relationship ledger was accepted")
	}
	if _, err := DeriveV2Seed(DeriveV2SeedOptions{SourceManifestPath: sourceManifest, SourceSuitePath: filepath.Join(filepath.Dir(sourceSuite), "missing-suite"), RelationshipLedgerPath: ledger, OutputDir: output, SeedID: "v2"}); err == nil {
		t.Fatal("missing source suite was accepted")
	}
}

func TestValidateV2DerivationRejectsArtifactAndCorpusDrift(t *testing.T) {
	for name, mutate := range map[string]func(*SeedManifest, []CorpusItem, string){
		"schema":             func(m *SeedManifest, _ []CorpusItem, _ string) { m.SchemaVersion = SeedSchemaVersion },
		"corpus count":       func(m *SeedManifest, _ []CorpusItem, _ string) { m.Counts["corpus"]++ },
		"relationship count": func(m *SeedManifest, _ []CorpusItem, _ string) { m.RelationshipCount++ },
		"seed id":            func(m *SeedManifest, _ []CorpusItem, _ string) { m.ParentSeedID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			sourceManifest, sourceSuite, ledger, output := writeV2DerivationFixture(t, "Alpha uses Beta.")
			if _, err := DeriveV2Seed(DeriveV2SeedOptions{SourceManifestPath: sourceManifest, SourceSuitePath: sourceSuite, RelationshipLedgerPath: ledger, OutputDir: output, SeedID: "v2"}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(output, "seed_manifest.json")
			manifest, err := LoadSeedManifest(path)
			if err != nil {
				t.Fatal(err)
			}
			corpus, err := LoadCorpus(path, manifest)
			if err != nil {
				t.Fatal(err)
			}
			mutate(manifest, corpus, output)
			if err := writeJSONFile(path, manifest); err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateV2Derivation(sourceManifest, sourceSuite, path, filepath.Join(output, derivedSuiteFileName)); err == nil {
				t.Fatal("manifest drift was accepted")
			}
		})
	}
	if _, err := ValidateV2Derivation(filepath.Join(t.TempDir(), "missing"), "suite", "derived", "derived-suite"); err == nil {
		t.Fatal("missing source derivation inputs were accepted")
	}
}

func TestDeriveV2SeedRejectsInvalidOptionsAndExistingOutput(t *testing.T) {
	if _, err := DeriveV2Seed(DeriveV2SeedOptions{}); err == nil {
		t.Fatal("empty derivation options were accepted")
	}
	sourceManifest, sourceSuite, ledger, output := writeV2DerivationFixture(t, "Alpha uses Beta.")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := DeriveV2Seed(DeriveV2SeedOptions{SourceManifestPath: sourceManifest, SourceSuitePath: sourceSuite, RelationshipLedgerPath: ledger, OutputDir: output, SeedID: "v2"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output error = %v", err)
	}
}

func TestValidateV2DerivationRejectsBoundedManifestDrift(t *testing.T) {
	for name, mutate := range map[string]func(*SeedManifest){
		"parent id":     func(m *SeedManifest) { m.ParentSeedID = "wrong" },
		"parent hash":   func(m *SeedManifest) { m.ParentSeedHash = "sha256:wrong" },
		"counts":        func(m *SeedManifest) { m.Counts["corpus"]++ },
		"relationships": func(m *SeedManifest) { m.RelationshipCount++ },
		"schema":        func(m *SeedManifest) { m.SchemaVersion = SeedSchemaVersion },
	} {
		t.Run(name, func(t *testing.T) {
			sourceManifest, sourceSuite, ledger, output := writeV2DerivationFixture(t, "Alpha uses Beta.")
			if _, err := DeriveV2Seed(DeriveV2SeedOptions{SourceManifestPath: sourceManifest, SourceSuitePath: sourceSuite, RelationshipLedgerPath: ledger, OutputDir: output, SeedID: "fixture_v2"}); err != nil {
				t.Fatal(err)
			}
			derivedPath := filepath.Join(output, "seed_manifest.json")
			manifest, err := LoadSeedManifest(derivedPath)
			if err != nil {
				t.Fatal(err)
			}
			mutate(manifest)
			if err := writeJSONFile(derivedPath, manifest); err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateV2Derivation(sourceManifest, sourceSuite, derivedPath, filepath.Join(output, derivedSuiteFileName)); err == nil {
				t.Fatal("manifest drift was accepted")
			}
		})
	}
}

func intPtr(value int) *int { return &value }
