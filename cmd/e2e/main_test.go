package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGroupCasesCapsPackageBatches(t *testing.T) {
	cases := make([]databaseCase, 0, maxDatabaseCasesPerBatch+1)
	for index := 0; index < maxDatabaseCasesPerBatch+1; index++ {
		cases = append(cases, databaseCase{ID: fmt.Sprintf("case-%03d", index), Package: "./internal/knowledge/postgres"})
	}
	cases = append(cases, databaseCase{ID: "other", Package: "./internal/http"})

	batches := groupCases(cases)
	if len(batches) != 3 {
		t.Fatalf("expected three batches, got %d", len(batches))
	}
	if batches[0].Package != "./internal/http" || len(batches[0].Cases) != 1 {
		t.Fatalf("unexpected first batch: %+v", batches[0])
	}
	if batches[1].Package != "./internal/knowledge/postgres" || len(batches[1].Cases) != maxDatabaseCasesPerBatch {
		t.Fatalf("unexpected first repository batch: %+v", batches[1])
	}
	if batches[2].Package != "./internal/knowledge/postgres" || len(batches[2].Cases) != 1 {
		t.Fatalf("unexpected second repository batch: %+v", batches[2])
	}
}

func TestRunBatchRequiresEveryCaseToPass(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake go command uses a POSIX shell")
	}

	tests := []struct {
		name       string
		output     string
		exitStatus int
		want       string
	}{
		{name: "pass", output: `{"Action":"pass","Test":"TestRequired"}`, want: ""},
		{name: "skip", output: `{"Action":"skip","Test":"TestRequired"}`, want: "ended with skip"},
		{name: "missing", output: `{"Action":"pass","Test":"TestOther"}`, want: "did not execute"},
		{name: "command failure", output: `{"Action":"fail","Test":"TestRequired"}`, exitStatus: 1, want: "batch ./internal/knowledge/postgres failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			fakeGo := filepath.Join(binDir, "go")
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '%s'\nexit %d\n", tc.output, tc.exitStatus)
			if err := os.WriteFile(fakeGo, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			err := runBatch(t.TempDir(), filepath.Join(t.TempDir(), "overlay.json"), packageBatch{
				Package: "./internal/knowledge/postgres",
				Cases:   []databaseCase{{ID: "required", Run: "^TestRequired$"}},
			}, 10*time.Second)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("runBatch() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runBatch() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestDatabaseCaseBaselineValidationRejectsDriftAndDuplicates(t *testing.T) {
	baseline := []databaseCaseBaseline{{ID: "case-1", Run: "^TestOne$", Phase: "precheck"}}
	if err := validateDatabaseCaseBaseline(baseline, []databaseCase{{ID: "case-1", Run: "^TestOne$", Phase: "precheck"}}); err != nil {
		t.Fatalf("matching baseline rejected: %v", err)
	}
	for name, tc := range map[string]struct {
		current []databaseCase
		want    string
	}{
		"missing":   {current: nil, want: "is missing"},
		"changed":   {current: []databaseCase{{ID: "case-1", Run: "^TestTwo$", Phase: "precheck"}}, want: "changed execution"},
		"duplicate": {current: []databaseCase{{ID: "case-1", Run: "^TestOne$", Phase: "precheck"}, {ID: "case-1", Run: "^TestOther$", Phase: "precheck"}}, want: "duplicate"},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateDatabaseCaseBaseline(baseline, tc.current)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateDatabaseCaseBaseline() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDatabaseCaseBaselineLoaderRejectsInvalidFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "cmd", "e2e", "testdata", "database-case-baseline.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		contents string
		want     string
	}{
		"malformed":     {contents: `{`, want: "decode"},
		"wrong version": {contents: `{"version":2,"cases":[{"id":"x","run":"TestX","phase":"precheck"}]}`, want: "invalid version"},
		"empty":         {contents: `{"version":1,"cases":[]}`, want: "invalid version"},
		"incomplete":    {contents: `{"version":1,"cases":[{"id":"","run":"TestX","phase":"precheck"}]}`, want: "incomplete"},
		"duplicate":     {contents: `{"version":1,"cases":[{"id":"x","run":"TestX","phase":"precheck"},{"id":"x","run":"TestY","phase":"precheck"}]}`, want: "duplicate"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadDatabaseCaseBaseline(root); err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("loadDatabaseCaseBaseline() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestE2ERunnerSmallHelpersAndOverlay(t *testing.T) {
	root := t.TempDir()
	if got, err := repositoryRoot(root); err != nil || got != root {
		t.Fatalf("repositoryRoot explicit = %q, %v", got, err)
	}
	if skip, err := skipGeneratedEvaluationTree(root, filepath.Join(root, "tests", "eval")); err != nil || !skip {
		t.Fatalf("skipGeneratedEvaluationTree root = %v, %v", skip, err)
	}
	if skip, err := skipGeneratedEvaluationTree(root, filepath.Join(root, "internal")); err != nil || skip {
		t.Fatalf("skipGeneratedEvaluationTree unrelated = %v, %v", skip, err)
	}
	if got := splitFilter(" a, ,b "); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("splitFilter = %#v", got)
	}
	if !contains([]string{"a", "b"}, "b") || contains([]string{"a"}, "b") {
		t.Fatal("contains returned the wrong result")
	}
	for expression, want := range map[string]string{"^TestOne$": "TestOne", "TestOne|TestTwo": "TestOne", "^TestOne": "TestOne"} {
		if got := testName(expression); got != want {
			t.Errorf("testName(%q) = %q, want %q", expression, got, want)
		}
	}
	if _, err := writeOverlay(root); err == nil || !strings.Contains(err.Error(), "no E2E test sources") {
		t.Fatalf("writeOverlay(empty) error = %v", err)
	}
	source := filepath.Join(root, "internal", "fixture.e2e")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("package fixture\n\nfunc TestFixture(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	overlayPath, err := writeOverlay(root)
	if err != nil {
		t.Fatalf("writeOverlay() = %v", err)
	}
	defer os.Remove(overlayPath)
	var got overlay
	contents, err := os.ReadFile(overlayPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contents, &got); err != nil {
		t.Fatal(err)
	}
	if got.Replace[filepath.Join(root, "internal", "fixture_test.go")] != source {
		t.Fatalf("overlay = %#v", got.Replace)
	}
}

func TestReconcileCaseRegistryAcceptsDeclaredFixture(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "internal", "fixture.e2e")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("package fixture\n\nfunc TestFixture(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	caseDef := databaseCase{ID: "fixture/TestFixture", Package: "./internal", Run: "^TestFixture$", Source: "internal/fixture.e2e"}
	if err := reconcileCaseRegistry(root, []databaseCase{caseDef}); err != nil {
		t.Fatalf("reconcileCaseRegistry() = %v", err)
	}
	bad := caseDef
	bad.Run = "^TestMissing$"
	if err := reconcileCaseRegistry(root, []databaseCase{bad}); err == nil || !strings.Contains(err.Error(), "no entry") {
		t.Fatalf("unregistered declaration error = %v", err)
	}
}

func TestReconcileCaseRegistryRejectsHelperSourceWithoutTestDeclaration(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "internal", "fixture.e2e")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("package fixture\n\nfunc helper() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reconcileCaseRegistry(root, nil); err == nil || !strings.Contains(err.Error(), "has no test declaration") {
		t.Fatalf("reconcileCaseRegistry() error = %v, want helper declaration failure", err)
	}
}

func TestE2ERunnerMainListsRegisteredCasesWithoutRunningBatches(t *testing.T) {
	oldArgs := os.Args
	oldCommandLine := flag.CommandLine
	t.Cleanup(func() {
		os.Args = oldArgs
		flag.CommandLine = oldCommandLine
	})
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	flag.CommandLine = flag.NewFlagSet("e2e-list", flag.ContinueOnError)
	os.Args = []string{"e2e", "-root", root, "-list-capabilities"}
	main()
	flag.CommandLine = flag.NewFlagSet("e2e-case-list", flag.ContinueOnError)
	os.Args = []string{"e2e", "-root", root, "-list", "-capability", "postgres"}
	main()
}

func TestLoadCasesReconcilesRegistryDeclarations(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "internal", "sample", "fixture.e2e")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(root, "scripts", "e2e-db-cases", "repository.json")
	if err := os.MkdirAll(filepath.Dir(registry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("package sample\n\nfunc TestRegistered(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, []byte(`{
  "version": 1,
  "capability": "repository",
  "cases": [{
    "id": "repository/TestRegistered",
    "package": "./internal/sample",
    "run": "^TestRegistered$",
    "phase": "precheck",
    "source": "internal/sample/fixture.e2e"
  }]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	otherRegistry := filepath.Join(root, "scripts", "e2e-db-cases", "postgres.json")
	if err := os.WriteFile(otherRegistry, []byte(`{
  "version": 1,
  "capability": "postgres",
  "cases": [{
    "id": "postgres/TestOtherRegistered",
    "package": "./internal/sample",
    "run": "^TestOtherRegistered$",
    "phase": "precheck",
    "source": "internal/sample/fixture.e2e"
  }]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("package sample\n\nfunc TestRegistered(t *testing.T) {}\nfunc TestOtherRegistered(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases, err := loadCases(root, "precheck", "repository", "", "")
	if err != nil {
		t.Fatalf("loadCases() error = %v", err)
	}
	if len(cases) != 1 || cases[0].ID != "repository/TestRegistered" {
		t.Fatalf("loadCases() = %+v", cases)
	}
	allCases, err := loadCases(root, "precheck", "", "", "")
	if err != nil {
		t.Fatalf("loadCases() without capability filter error = %v", err)
	}
	if len(allCases) != 2 || allCases[0].Capability != "postgres" || allCases[1].Capability != "repository" {
		t.Fatalf("unfiltered loadCases() = %+v, want deterministic capability ownership", allCases)
	}
	generated := filepath.Join(root, "tests", "eval", ".runtime", "fixture.e2e")
	if err := os.MkdirAll(filepath.Dir(generated), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(generated, []byte("package generated\n\nfunc TestIgnored(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCases(root, "precheck", "repository", "", ""); err != nil {
		t.Fatalf("loadCases() should ignore generated evaluation trees: %v", err)
	}
	overlayPath, err := writeOverlay(root)
	if err != nil {
		t.Fatalf("writeOverlay() = %v", err)
	}
	defer os.Remove(overlayPath)
	overlayContents, err := os.ReadFile(overlayPath)
	if err != nil {
		t.Fatal(err)
	}
	var generatedOverlay overlay
	if err := json.Unmarshal(overlayContents, &generatedOverlay); err != nil {
		t.Fatal(err)
	}
	if _, ok := generatedOverlay.Replace[strings.TrimSuffix(generated, ".e2e")+"_test.go"]; ok {
		t.Fatal("writeOverlay() included generated evaluation source")
	}
	if _, ok := generatedOverlay.Replace[strings.TrimSuffix(source, ".e2e")+"_test.go"]; !ok {
		t.Fatal("writeOverlay() omitted the registered fixture source")
	}

	if err := os.WriteFile(source, []byte("package sample\n\nfunc TestRegistered(t *testing.T) {}\nfunc TestUnregistered(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCases(root, "precheck", "repository", "", ""); err == nil || !strings.Contains(err.Error(), "TestUnregistered") {
		t.Fatalf("loadCases() error = %v, want unregistered declaration failure", err)
	}

	if err := os.WriteFile(source, []byte("package sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCases(root, "precheck", "repository", "", ""); err == nil || !strings.Contains(err.Error(), "has no test declaration") {
		t.Fatalf("loadCases() error = %v, want missing declaration failure", err)
	}

	if err := os.WriteFile(source, []byte("package sample\n\nfunc TestRegistered(t *testing.T) {}\nfunc TestRegistered(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCases(root, "precheck", "repository", "", ""); err == nil || !strings.Contains(err.Error(), "duplicate test declaration") {
		t.Fatalf("loadCases() error = %v, want duplicate declaration failure", err)
	}

	if err := os.WriteFile(source, []byte("package sample\n\nfunc TestRegistered(t *testing.T) {}\nfunc TestOtherRegistered(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, []byte(`{
  "version": 1,
  "capability": "repository",
  "cases": [{
    "id": "repository/TestRegistered",
    "package": "./wrong/package",
    "run": "^TestRegistered$",
    "phase": "precheck",
    "source": "internal/sample/fixture.e2e"
  }]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCases(root, "precheck", "repository", "", ""); err == nil || !strings.Contains(err.Error(), "uses package ./wrong/package") {
		t.Fatalf("loadCases() error = %v, want package mismatch failure", err)
	}

	if err := os.WriteFile(registry, []byte(`{
  "version": 1,
  "capability": "repository",
  "cases": []
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherRegistry, []byte(`{
  "version": 1,
  "capability": "postgres",
  "cases": [{
    "id": "postgres/TestOtherRegistered",
    "package": "./internal/sample",
    "run": "^TestOtherRegistered$",
    "phase": "precheck",
    "source": "internal/sample/fixture.e2e"
  }, {
    "id": "repository/TestRegistered",
    "package": "./internal/sample",
    "run": "^TestRegistered$",
    "phase": "precheck",
    "source": "internal/sample/fixture.e2e"
  }]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	movedCases, err := loadCases(root, "precheck", "postgres", "", "")
	if err != nil {
		t.Fatalf("loadCases() after fragment relocation error = %v", err)
	}
	var moved databaseCase
	for _, item := range movedCases {
		if item.ID == "repository/TestRegistered" {
			moved = item
			break
		}
	}
	if moved.ID != "repository/TestRegistered" || moved.Capability != "postgres" {
		t.Fatalf("relocated case = %+v, want postgres-owned repository/TestRegistered", moved)
	}
}

func TestDatabaseCaseFragmentsPreserveInventoryAndWave6Partition(t *testing.T) {
	root, err := repositoryRoot("")
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	allCases := make([]databaseCase, 0)
	capabilities := make(map[string]int)
	for _, phase := range []string{"precheck", "scenario"} {
		cases, err := loadCases(root, phase, "", "", "")
		if err != nil {
			t.Fatalf("loadCases(%s) error = %v", phase, err)
		}
		for _, item := range cases {
			if seen[item.ID] {
				t.Fatalf("duplicate database case %s", item.ID)
			}
			seen[item.ID] = true
			if strings.TrimSpace(item.Capability) == "" {
				t.Fatalf("database case %s has no capability", item.ID)
			}
			allCases = append(allCases, item)
			capabilities[item.Capability]++
		}
	}
	baseline, err := loadDatabaseCaseBaseline(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(allCases) < len(baseline) {
		t.Fatalf("database case inventory contains %d cases, baseline contains %d", len(allCases), len(baseline))
	}
	if err := validateDatabaseCaseBaseline(baseline, allCases); err != nil {
		t.Fatal(err)
	}
	if capabilities["operations"] < 1 || capabilities["repository"] < 1 {
		t.Fatalf("baseline capabilities lost: %+v", capabilities)
	}
	for _, capability := range []string{"access", "operations", "remember", "search", "lifecycle", "memorypack"} {
		path := filepath.Join(root, "scripts", "e2e-db-cases", capability+".json")
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var fragment caseFragment
		if err := json.Unmarshal(contents, &fragment); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if fragment.Capability != capability {
			t.Fatalf("fragment %s declares capability %q", capability, fragment.Capability)
		}
		reserved := capability == "lifecycle" || capability == "memorypack"
		if !reserved && len(fragment.Cases) == 0 {
			t.Fatalf("wave 6 fragment %s is unexpectedly empty", capability)
		}
		if reserved && len(fragment.Cases) != 0 {
			t.Fatalf("reserved fragment %s unexpectedly owns cases", capability)
		}
	}
	for _, capability := range []string{"recall", "conflict", "index-retirement"} {
		path := filepath.Join(root, "scripts", "e2e-db-cases", capability+".json")
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var fragment caseFragment
		if err := json.Unmarshal(contents, &fragment); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if fragment.Capability != capability {
			t.Fatalf("fragment %s declares capability %q", capability, fragment.Capability)
		}
		if len(fragment.Cases) == 0 {
			t.Fatalf("wave 7 fragment %s is unexpectedly empty", capability)
		}
	}
}

func TestDatabaseCaseBaselineAllowsAdditionsAndRelocation(t *testing.T) {
	baseline := []databaseCaseBaseline{{ID: "repository/TestExisting", Run: "^TestExisting$", Phase: "precheck"}}
	current := []databaseCase{
		{ID: "repository/TestExisting", Run: "^TestExisting$", Phase: "precheck", Package: "./internal/conflict", Source: "internal/conflict/fixture.e2e", Capability: "conflict"},
		{ID: "recall/TestAdded", Run: "^TestAdded$", Phase: "scenario", Scenario: "space_aware_recall"},
	}
	if err := validateDatabaseCaseBaseline(baseline, current); err != nil {
		t.Fatalf("baseline rejected valid addition and relocation: %v", err)
	}
	current[0].Run = "^TestChanged$"
	if err := validateDatabaseCaseBaseline(baseline, current); err == nil || !strings.Contains(err.Error(), "changed execution selection") {
		t.Fatalf("baseline change error = %v, want execution-selection failure", err)
	}
	current[0].Run = "^TestExisting$"
	current = current[1:]
	if err := validateDatabaseCaseBaseline(baseline, current); err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("baseline missing error = %v, want missing-case failure", err)
	}
}

func TestE2ERunnerMainListModesUseRegisteredInventory(t *testing.T) {
	root, err := repositoryRoot("")
	if err != nil {
		t.Fatal(err)
	}
	oldCommandLine := flag.CommandLine
	oldArgs := os.Args
	t.Cleanup(func() {
		flag.CommandLine = oldCommandLine
		os.Args = oldArgs
	})
	flag.CommandLine = flag.NewFlagSet("e2e-test", flag.ContinueOnError)
	os.Args = []string{"e2e", "-root", root, "-phase", "precheck", "-capability", "operations", "-list"}
	main()

	flag.CommandLine = flag.NewFlagSet("e2e-test-capabilities", flag.ContinueOnError)
	os.Args = []string{"e2e", "-root", root, "-phase", "scenario", "-list-capabilities"}
	main()
}
