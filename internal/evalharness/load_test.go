package evalharness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFunctionsValidateSeedFiles(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "seed_manifest.json")

	writeManifest := func(t *testing.T, manifest SeedManifest) {
		t.Helper()
		if err := writeJSONFile(manifestPath, manifest); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
	}
	expectManifestErr := func(t *testing.T, manifest SeedManifest, want string) {
		t.Helper()
		writeManifest(t, manifest)
		_, err := LoadSeedManifest(manifestPath)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("LoadSeedManifest err = %v; want %q", err, want)
		}
	}

	expectManifestErr(t, SeedManifest{}, "schema_version")
	expectManifestErr(t, SeedManifest{SchemaVersion: "wrong"}, "unsupported seed schema_version")
	expectManifestErr(t, SeedManifest{SchemaVersion: SeedSchemaVersion}, "seed_id")
	expectManifestErr(t, SeedManifest{SchemaVersion: SeedSchemaVersion, SeedID: "fixture"}, "corpus_file")

	manifest := SeedManifest{
		SchemaVersion:     SeedSchemaVersion,
		SeedID:            "fixture",
		CorpusFile:        "corpus.jsonl",
		CasesFile:         "cases.jsonl",
		QrelsFile:         "qrels.jsonl",
		AnswersFile:       "answers.jsonl",
		HardNegativesFile: "hard_negatives.jsonl",
		TransformsFile:    "transforms.jsonl",
		LicensesFile:      "licenses.md",
	}
	writeManifest(t, manifest)
	loaded, err := LoadSeedManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadSeedManifest valid: %v", err)
	}

	corpusPath := filepath.Join(dir, manifest.CorpusFile)
	if err := writeJSONL(corpusPath, []CorpusItem{{Content: "missing id"}}); err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	if _, err := LoadCorpus(manifestPath, loaded); err == nil || !strings.Contains(err.Error(), "missing source_doc_id") {
		t.Fatalf("LoadCorpus missing id err = %v", err)
	}
	if err := writeJSONL(corpusPath, []CorpusItem{{SourceDocID: "doc-1"}}); err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	if _, err := LoadCorpus(manifestPath, loaded); err == nil || !strings.Contains(err.Error(), "missing content") {
		t.Fatalf("LoadCorpus missing content err = %v", err)
	}
	if err := writeJSONL(corpusPath, []CorpusItem{{SourceDocID: "doc-1", Content: strings.Repeat("x", MaxCorpusContentCodepoints+1)}}); err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	if _, err := LoadCorpus(manifestPath, loaded); err == nil || !strings.Contains(err.Error(), "max is 999") {
		t.Fatalf("LoadCorpus oversized content err = %v", err)
	}
	if err := writeJSONL(corpusPath, []CorpusItem{{SourceDocID: "doc-1", Content: "one"}, {SourceDocID: "doc-1", Content: "dupe"}}); err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	if _, err := LoadCorpus(manifestPath, loaded); err == nil || !strings.Contains(err.Error(), "duplicate corpus") {
		t.Fatalf("LoadCorpus duplicate err = %v", err)
	}
	if err := writeJSONL(corpusPath, []CorpusItem{{SourceDocID: "doc-1", Content: "one"}}); err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	if corpus, err := LoadCorpus(manifestPath, loaded); err != nil || len(corpus) != 1 {
		t.Fatalf("LoadCorpus valid = %d, %v", len(corpus), err)
	}

	casesPath := filepath.Join(dir, manifest.CasesFile)
	if err := writeJSONL(casesPath, []Case{{Query: "missing id"}}); err != nil {
		t.Fatalf("write cases: %v", err)
	}
	if _, err := LoadCases(manifestPath, loaded); err == nil || !strings.Contains(err.Error(), "missing case_id") {
		t.Fatalf("LoadCases missing id err = %v", err)
	}
	if err := writeJSONL(casesPath, []Case{{CaseID: "case-1"}}); err != nil {
		t.Fatalf("write cases: %v", err)
	}
	if _, err := LoadCases(manifestPath, loaded); err == nil || !strings.Contains(err.Error(), "missing query") {
		t.Fatalf("LoadCases missing query err = %v", err)
	}
	if err := writeJSONL(casesPath, []Case{{CaseID: "case-1", Query: "one"}, {CaseID: "case-1", Query: "dupe"}}); err != nil {
		t.Fatalf("write cases: %v", err)
	}
	if _, err := LoadCases(manifestPath, loaded); err == nil || !strings.Contains(err.Error(), "duplicate case_id") {
		t.Fatalf("LoadCases duplicate err = %v", err)
	}
	if err := writeJSONL(casesPath, []Case{{CaseID: "case-1", Query: "one"}}); err != nil {
		t.Fatalf("write cases: %v", err)
	}
	if cases, err := LoadCases(manifestPath, loaded); err != nil || len(cases) != 1 {
		t.Fatalf("LoadCases valid = %d, %v", len(cases), err)
	}

	qrelsPath := filepath.Join(dir, manifest.QrelsFile)
	if err := writeJSONL(qrelsPath, []QRel{{RequiredRefs: []Ref{{Type: "fragment", ID: "frag-1"}}}}); err != nil {
		t.Fatalf("write qrels: %v", err)
	}
	if _, err := LoadQrels(manifestPath, loaded); err == nil || !strings.Contains(err.Error(), "missing case_id") {
		t.Fatalf("LoadQrels missing id err = %v", err)
	}
	if err := writeJSONL(qrelsPath, []QRel{{CaseID: "case-1"}}); err != nil {
		t.Fatalf("write qrels: %v", err)
	}
	if _, err := LoadQrels(manifestPath, loaded); err == nil || !strings.Contains(err.Error(), "no required_refs") {
		t.Fatalf("LoadQrels missing refs err = %v", err)
	}
	if err := writeJSONL(qrelsPath, []QRel{{CaseID: "case-1", RequiredRefs: []Ref{{Type: "fragment", ID: "frag-1"}}}}); err != nil {
		t.Fatalf("write qrels: %v", err)
	}
	if qrels, err := LoadQrels(manifestPath, loaded); err != nil || len(qrels) != 1 {
		t.Fatalf("LoadQrels valid = %d, %v", len(qrels), err)
	}

	suitePath := filepath.Join(dir, "suite.jsonl")
	if err := writeJSONL(suitePath, []SuiteCase{{}}); err != nil {
		t.Fatalf("write suite: %v", err)
	}
	if _, err := LoadSuite(suitePath); err == nil || !strings.Contains(err.Error(), "missing case_id") {
		t.Fatalf("LoadSuite missing id err = %v", err)
	}
	if err := writeJSONL(suitePath, []SuiteCase{{CaseID: "case-1"}, {CaseID: "case-1"}}); err != nil {
		t.Fatalf("write suite: %v", err)
	}
	if _, err := LoadSuite(suitePath); err == nil || !strings.Contains(err.Error(), "duplicate suite case_id") {
		t.Fatalf("LoadSuite duplicate err = %v", err)
	}
	if err := writeJSONL(suitePath, []SuiteCase{{CaseID: "case-1"}}); err != nil {
		t.Fatalf("write suite: %v", err)
	}
	if suite, err := LoadSuite(suitePath); err != nil || len(suite) != 1 {
		t.Fatalf("LoadSuite valid = %d, %v", len(suite), err)
	}

	for _, name := range []string{manifest.AnswersFile, manifest.HardNegativesFile, manifest.TransformsFile, manifest.LicensesFile} {
		if err := writeJSONFile(filepath.Join(dir, name), map[string]any{"fixture": name}); err != nil {
			t.Fatalf("write optional %s: %v", name, err)
		}
	}
	hash, err := SeedHash(manifestPath, loaded)
	if err != nil {
		t.Fatalf("SeedHash with optional files: %v", err)
	}
	if !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("SeedHash = %q", hash)
	}
}

func TestLoadCorpusRejectsLegacyTypedImportFields(t *testing.T) {
	for _, field := range []string{"claims", "auto_promote"} {
		t.Run(field, func(t *testing.T) {
			dir := t.TempDir()
			manifestPath := filepath.Join(dir, "seed_manifest.json")
			manifest := SeedManifest{
				SchemaVersion: SeedSchemaVersion,
				SeedID:        "remember-only",
				CorpusFile:    "corpus.jsonl",
			}
			if err := writeJSONFile(manifestPath, manifest); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			line := `{"source_doc_id":"doc-1","content":"content","` + field + `":null}` + "\n"
			if err := os.WriteFile(filepath.Join(dir, manifest.CorpusFile), []byte(line), 0o644); err != nil {
				t.Fatalf("write corpus: %v", err)
			}

			_, err := LoadCorpus(manifestPath, &manifest)
			if err == nil || !strings.Contains(err.Error(), `legacy corpus field "`+field+`" is not supported`) {
				t.Fatalf("LoadCorpus err = %v", err)
			}
		})
	}
}

func TestLoadErrorAndCommentBranches(t *testing.T) {
	dir := t.TempDir()

	var traces []RecallTrace
	if err := readJSONL(filepath.Join(dir, "missing.jsonl"), &traces); err == nil {
		t.Fatal("readJSONL missing file error = nil")
	}
	if _, err := LoadRecallTraces(filepath.Join(dir, "missing-traces.jsonl")); err == nil {
		t.Fatal("LoadRecallTraces missing file error = nil")
	}

	badJSONL := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(badJSONL, []byte("{bad\n"), 0o644); err != nil {
		t.Fatalf("write bad jsonl: %v", err)
	}
	if err := readJSONL(badJSONL, &traces); err == nil || !strings.Contains(err.Error(), "bad.jsonl:1") {
		t.Fatalf("readJSONL bad json err = %v", err)
	}

	commented := filepath.Join(dir, "commented.jsonl")
	if err := os.WriteFile(commented, []byte("# comment\n\n{\"case_id\":\"case-1\"}\n"), 0o644); err != nil {
		t.Fatalf("write commented jsonl: %v", err)
	}
	var suite []SuiteCase
	if err := readJSONL(commented, &suite); err != nil || len(suite) != 1 {
		t.Fatalf("readJSONL comments = %d, %v", len(suite), err)
	}

	var manifest SeedManifest
	if err := readJSONFile(filepath.Join(dir, "missing.json"), &manifest); err == nil {
		t.Fatal("readJSONFile missing file error = nil")
	}
	badJSON := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badJSON, []byte("{bad"), 0o644); err != nil {
		t.Fatalf("write bad json: %v", err)
	}
	if err := readJSONFile(badJSON, &manifest); err == nil || !strings.Contains(err.Error(), "bad.json") {
		t.Fatalf("readJSONFile bad json err = %v", err)
	}

	fixture := writeEvalFixture(t)
	manifestPath := filepath.Join(fixture, "seed_manifest.json")
	loaded, err := LoadSeedManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadSeedManifest fixture: %v", err)
	}
	loaded.AnswersFile = "missing-answers.json"
	if _, err := SeedHash(manifestPath, loaded); err == nil {
		t.Fatal("SeedHash missing optional file error = nil")
	}
}

func TestCanonicalJSONHashIsStableAndDetectsMappingDrift(t *testing.T) {
	first := map[string]any{
		"by_source_doc_id": map[string]any{
			"doc-b": map[string]any{"type": "fragment", "id": "fragment-b"},
			"doc-a": map[string]any{"id": "fragment-a", "type": "fragment"},
		},
	}
	second := map[string]any{
		"by_source_doc_id": map[string]any{
			"doc-a": map[string]any{"type": "fragment", "id": "fragment-a"},
			"doc-b": map[string]any{"id": "fragment-b", "type": "fragment"},
		},
	}
	firstHash, err := canonicalJSONHash(first)
	if err != nil {
		t.Fatalf("hash first mapping: %v", err)
	}
	secondHash, err := canonicalJSONHash(second)
	if err != nil {
		t.Fatalf("hash second mapping: %v", err)
	}
	if firstHash != secondHash {
		t.Fatalf("canonical hashes differ: %s != %s", firstHash, secondHash)
	}
	second["by_source_doc_id"].(map[string]any)["doc-b"].(map[string]any)["id"] = "fragment-changed"
	driftedHash, err := canonicalJSONHash(second)
	if err != nil {
		t.Fatalf("hash drifted mapping: %v", err)
	}
	if driftedHash == firstHash {
		t.Fatalf("mapping drift retained hash %s", firstHash)
	}
}
