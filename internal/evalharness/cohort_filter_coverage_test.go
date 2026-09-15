package evalharness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCohortFilterLockValidationRejectsInvalidDeclarations(t *testing.T) {
	if _, _, err := loadCohortFilterLock(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing cohort lock was accepted")
	}
	root := t.TempDir()
	path := filepath.Join(root, "lock.json")
	base := cohortFilterLock{
		SchemaVersion: cohortFilterLockSchema, SeedID: "seed", ParentSeedHash: "parent", FilteredSeedHash: "filtered",
		ExpectedCounts: map[string]int{"corpus": 1, "cases": 1, "qrels": 1, "answers": 1, "transforms": 1},
	}
	for name, mutate := range map[string]func(*cohortFilterLock){
		"schema":            func(lock *cohortFilterLock) { lock.SchemaVersion = "wrong" },
		"missing seed":      func(lock *cohortFilterLock) { lock.SeedID = "" },
		"empty removal":     func(lock *cohortFilterLock) { lock.RemovedSourceDocIDs = []string{""} },
		"duplicate removal": func(lock *cohortFilterLock) { lock.RemovedSourceDocIDs = []string{"doc", "doc"} },
		"negative count":    func(lock *cohortFilterLock) { lock.ExpectedCounts["corpus"] = -1 },
		"missing count":     func(lock *cohortFilterLock) { delete(lock.ExpectedCounts, "answers") },
		"zero cases":        func(lock *cohortFilterLock) { lock.ExpectedCounts["cases"] = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			lock := base
			lock.ExpectedCounts = map[string]int{}
			for key, value := range base.ExpectedCounts {
				lock.ExpectedCounts[key] = value
			}
			mutate(&lock)
			if err := writeJSONFile(path, lock); err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadCohortFilterLock(path); err == nil {
				t.Fatal("invalid cohort lock was accepted")
			}
		})
	}
	if err := writeJSONFile(path, base); err != nil {
		t.Fatal(err)
	}
	if lock, hash, err := loadCohortFilterLock(path); err != nil || lock.SeedID != "seed" || hash == "" {
		t.Fatalf("valid cohort lock = %+v, %q, %v", lock, hash, err)
	}
}

func TestCohortFilterJSONLAndManifestHelpers(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent.jsonl")
	filtered := filepath.Join(root, "filtered.jsonl")
	if err := os.WriteFile(parent, []byte("{\"id\":\"a\",\"value\":1}\n# comment\n{\"id\":\"b\",\"value\":2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filtered, []byte("{\"id\":\"a\",\"value\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateFilteredJSONL(parent, filtered, "id", map[string]struct{}{"b": {}}, "fixture"); err != nil {
		t.Fatalf("valid filtered JSONL rejected: %v", err)
	}
	for name, contents := range map[string]string{
		"malformed":   "{\n",
		"missing key": "{\"value\":1}\n",
		"duplicate":   "{\"id\":\"a\"}\n{\"id\":\"a\"}\n",
		"invalid key": "{\"id\":1}\n",
	} {
		t.Run(name, func(t *testing.T) {
			bad := filepath.Join(root, name+".jsonl")
			if err := os.WriteFile(bad, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadCohortJSONLRows(bad, "id"); err == nil {
				t.Fatal("invalid cohort JSONL was accepted")
			}
		})
	}
	if err := validateFilteredJSONL(parent, filtered, "id", map[string]struct{}{"missing": {}}, "fixture"); err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("missing declared removal error = %v", err)
	}
	if err := validateFilteredJSONL(parent, filepath.Join(root, "missing.jsonl"), "id", map[string]struct{}{"b": {}}, "fixture"); err == nil || !strings.Contains(err.Error(), "read filtered") {
		t.Fatalf("missing filtered file error = %v", err)
	}
	if err := os.WriteFile(filtered, []byte("{\"id\":\"b\",\"value\":2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateFilteredJSONL(parent, filtered, "id", map[string]struct{}{"b": {}}, "fixture"); err == nil || !strings.Contains(err.Error(), "row 1 key") {
		t.Fatalf("wrong retained key was accepted: %v", err)
	}
	if err := os.WriteFile(filtered, []byte("{\"id\":\"a\",\"value\":9}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateFilteredJSONL(parent, filtered, "id", map[string]struct{}{"b": {}}, "fixture"); err == nil || !strings.Contains(err.Error(), "byte-identical") {
		t.Fatalf("changed retained row was accepted: %v", err)
	}
	if err := os.WriteFile(filtered, []byte("{\"id\":\"a\",\"value\":1}\n{\"id\":\"b\",\"value\":2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateFilteredJSONL(parent, filtered, "id", map[string]struct{}{"b": {}}, "fixture"); err == nil || !strings.Contains(err.Error(), "expected 1") {
		t.Fatalf("unfiltered rows were accepted: %v", err)
	}
	if err := validateCohortManifestDeclarations(&SeedManifest{CorpusFile: "a"}, &SeedManifest{CorpusFile: "b"}); err == nil {
		t.Fatal("changed manifest declaration was accepted")
	}
	if _, err := cohortIDSet([]string{"x", "x"}, "ids"); err == nil {
		t.Fatal("duplicate cohort IDs were accepted")
	}
	if ids, err := cohortIDSet([]string{" x "}, "ids"); err != nil || len(ids) != 1 {
		t.Fatalf("cohort IDs = %#v, %v", ids, err)
	}
}

func TestCohortFilterCountsAndDeclarationsRejectDrift(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	manifest := &SeedManifest{
		CorpusFile: "corpus.jsonl", CasesFile: "cases.jsonl", QrelsFile: "qrels.jsonl", AnswersFile: "answers.jsonl", TransformsFile: "transforms.jsonl",
		Counts: map[string]int{"corpus": 1, "cases": 1, "qrels": 1, "answers": 1, "transforms": 1},
	}
	for _, file := range []string{"corpus.jsonl", "cases.jsonl", "qrels.jsonl", "answers.jsonl", "transforms.jsonl"} {
		key := "source_doc_id"
		if file != "corpus.jsonl" {
			key = "case_id"
		}
		if err := os.WriteFile(filepath.Join(root, file), []byte(`{"`+key+`":"one"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	opts := V2CohortValidationOptions{FilteredManifestPath: manifestPath}
	lock := cohortFilterLock{ExpectedCounts: map[string]int{"corpus": 1, "cases": 1, "qrels": 1, "answers": 1, "transforms": 1}}
	parent := &SeedManifest{Counts: map[string]int{"corpus": 2, "cases": 1}}
	if err := validateCohortCounts(opts, parent, manifest, lock); err != nil {
		t.Fatalf("valid cohort counts rejected: %v", err)
	}
	for name, mutate := range map[string]func(*SeedManifest, *cohortFilterLock){
		"missing file":   func(m *SeedManifest, _ *cohortFilterLock) { m.AnswersFile = "" },
		"manifest count": func(m *SeedManifest, _ *cohortFilterLock) { m.Counts["cases"] = 2 },
		"lock count":     func(_ *SeedManifest, l *cohortFilterLock) { l.ExpectedCounts["qrels"] = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			copyManifest := *manifest
			copyManifest.Counts = map[string]int{}
			for key, value := range manifest.Counts {
				copyManifest.Counts[key] = value
			}
			copyLock := lock
			copyLock.ExpectedCounts = map[string]int{}
			for key, value := range lock.ExpectedCounts {
				copyLock.ExpectedCounts[key] = value
			}
			mutate(&copyManifest, &copyLock)
			if err := validateCohortCounts(opts, parent, &copyManifest, copyLock); err == nil {
				t.Fatal("cohort count drift was accepted")
			}
		})
	}
	if err := validateCohortManifestDeclarations(manifest, &SeedManifest{CorpusFile: "other"}); err == nil {
		t.Fatal("manifest declaration drift was accepted")
	}
}

func TestCohortFilterOptionAndBindingValidation(t *testing.T) {
	if err := validateV2CohortValidationOptions(V2CohortValidationOptions{}); err == nil {
		t.Fatal("empty cohort options were accepted")
	}
	parent := &SeedManifest{SeedID: "parent"}
	filtered := &SeedManifest{SeedID: "filtered"}
	lock := cohortFilterLock{SeedID: "lock", ParentSeedHash: "parent", FilteredSeedHash: "filtered"}
	if err := validateCohortFilterLockBinding(lock, parent, filtered, "parent", "filtered"); err == nil {
		t.Fatal("mismatched cohort seed IDs were accepted")
	}
	lock.SeedID = "parent"
	if err := validateCohortFilterLockBinding(lock, parent, parent, "wrong", "filtered"); err == nil {
		t.Fatal("mismatched parent hash was accepted")
	}
}
