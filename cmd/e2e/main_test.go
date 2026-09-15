package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
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
			argsFile := filepath.Join(t.TempDir(), "args")
			t.Setenv("ARGS_FILE", argsFile)
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > \"$ARGS_FILE\"\nprintf '%%s\\n' '%s'\nexit %d\n", tc.output, tc.exitStatus)
			if err := os.WriteFile(fakeGo, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			err := runBatch(t.TempDir(), packageBatch{
				Package: "./internal/knowledge/postgres",
				Cases:   []databaseCase{{ID: "required", Run: "^TestRequired$"}},
			}, 10*time.Second)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("runBatch() error = %v", err)
				}
				args, readErr := os.ReadFile(argsFile)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !strings.Contains(string(args), "-tags=integration") {
					t.Fatalf("runBatch() args = %q, missing integration build tag", args)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runBatch() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestRunBatchUsesIntegrationTagWithRealGoPackage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/runner-fixture\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(root, "fixture")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "//go:build integration\n\npackage fixture\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestIntegrationFixture(t *testing.T) {}\n\nfunc TestSkippedIntegrationFixture(t *testing.T) { t.Skip(\"intentional runner fixture skip\") }\n\nfunc TestFailedIntegrationFixture(t *testing.T) { t.Fatal(\"intentional runner fixture failure\") }\n\nfunc TestSlowIntegrationFixture(t *testing.T) {\n\ttime.Sleep(10 * time.Second)\n}\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "fixture_integration_test.go"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "fixture_test.go"), []byte("package fixture\n\nimport \"testing\"\n\nfunc TestOrdinaryFixture(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runBatch(root, packageBatch{
		Package: "./fixture",
		Cases:   []databaseCase{{ID: "fixture/TestIntegrationFixture", Run: "^TestIntegrationFixture$"}},
	}, 10*time.Second); err != nil {
		t.Fatalf("tagged runBatch() error = %v", err)
	}

	ordinary := exec.Command("go", "test", "-json", "-run", "^TestIntegrationFixture$", "./fixture")
	ordinary.Dir = root
	ordinaryOutput, err := ordinary.CombinedOutput()
	if err != nil {
		t.Fatalf("ordinary go test error = %v, output = %s", err, ordinaryOutput)
	}
	if strings.Contains(string(ordinaryOutput), "TestIntegrationFixture") {
		t.Fatalf("ordinary go test unexpectedly selected integration test: %s", ordinaryOutput)
	}

	if err := runBatch(root, packageBatch{
		Package: "./fixture",
		Cases:   []databaseCase{{ID: "fixture/TestMissingIntegrationFixture", Run: "^TestMissingIntegrationFixture$"}},
	}, 10*time.Second); err == nil || !strings.Contains(err.Error(), "did not execute") {
		t.Fatalf("missing tagged case error = %v, want did-not-execute failure", err)
	}
	if err := runBatch(root, packageBatch{
		Package: "./fixture",
		Cases:   []databaseCase{{ID: "fixture/TestSkippedIntegrationFixture", Run: "^TestSkippedIntegrationFixture$"}},
	}, 10*time.Second); err == nil || !strings.Contains(err.Error(), "ended with skip") {
		t.Fatalf("skipped tagged case error = %v, want skip failure", err)
	}
	if err := runBatch(root, packageBatch{
		Package: "./fixture",
		Cases:   []databaseCase{{ID: "fixture/TestFailedIntegrationFixture", Run: "^TestFailedIntegrationFixture$"}},
	}, 10*time.Second); err == nil || !strings.Contains(err.Error(), "batch ./fixture failed") {
		t.Fatalf("failed tagged case error = %v, want batch failure", err)
	}
	if err := runBatch(root, packageBatch{
		Package: "./fixture",
		Cases:   []databaseCase{{ID: "fixture/TestSlowIntegrationFixture", Run: "^TestSlowIntegrationFixture$"}},
	}, 500*time.Millisecond); err == nil {
		t.Fatal("slow tagged case unexpectedly completed")
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

func TestE2ERunnerSmallHelpers(t *testing.T) {
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
}

func TestReconcileCaseRegistryAcceptsDeclaredFixture(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "internal", "fixture_integration_test.go")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("//go:build integration\n\npackage fixture\n\nfunc TestFixture(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	caseDef := databaseCase{ID: "fixture/TestFixture", Package: "./internal", Run: "^TestFixture$", Source: "internal/fixture_integration_test.go"}
	if err := reconcileCaseRegistry(root, []databaseCase{caseDef}); err != nil {
		t.Fatalf("reconcileCaseRegistry() = %v", err)
	}
	bad := caseDef
	bad.Run = "^TestMissing$"
	if err := reconcileCaseRegistry(root, []databaseCase{bad}); err == nil || !strings.Contains(err.Error(), "no entry") {
		t.Fatalf("unregistered declaration error = %v", err)
	}
}

func TestReconcileCaseRegistryAllowsHelperSourceWithoutTestDeclaration(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "internal", "fixture_integration_test.go")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("//go:build integration\n\npackage fixture\n\nfunc helper() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reconcileCaseRegistry(root, nil); err != nil {
		t.Fatalf("reconcileCaseRegistry() error = %v, want helper source acceptance", err)
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
	source := filepath.Join(root, "internal", "sample", "fixture_integration_test.go")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(root, "scripts", "e2e-db-cases", "repository.json")
	if err := os.MkdirAll(filepath.Dir(registry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("//go:build integration\n\npackage sample\n\nfunc TestRegistered(t *testing.T) {}\n"), 0o600); err != nil {
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
    "source": "internal/sample/fixture_integration_test.go"
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
    "source": "internal/sample/fixture_integration_test.go"
  }]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("//go:build integration\n\npackage sample\n\nfunc TestRegistered(t *testing.T) {}\nfunc TestOtherRegistered(t *testing.T) {}\n"), 0o600); err != nil {
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
	generated := filepath.Join(root, "tests", "eval", ".runtime", "fixture_integration_test.go")
	if err := os.MkdirAll(filepath.Dir(generated), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(generated, []byte("//go:build integration\n\npackage generated\n\nfunc TestIgnored(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCases(root, "precheck", "repository", "", ""); err != nil {
		t.Fatalf("loadCases() should ignore generated evaluation trees: %v", err)
	}
	if err := os.WriteFile(source, []byte("//go:build integration\n\npackage sample\n\nfunc TestRegistered(t *testing.T) {}\nfunc TestUnregistered(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCases(root, "precheck", "repository", "", ""); err == nil || !strings.Contains(err.Error(), "TestUnregistered") {
		t.Fatalf("loadCases() error = %v, want unregistered declaration failure", err)
	}

	if err := os.WriteFile(source, []byte("//go:build integration\n\npackage sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCases(root, "precheck", "repository", "", ""); err == nil || !strings.Contains(err.Error(), "has no declaration") {
		t.Fatalf("loadCases() error = %v, want missing declaration failure", err)
	}

	if err := os.WriteFile(source, []byte("//go:build integration\n\npackage sample\n\nfunc TestRegistered(t *testing.T) {}\nfunc TestRegistered(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCases(root, "precheck", "repository", "", ""); err == nil || !strings.Contains(err.Error(), "duplicate test declaration") {
		t.Fatalf("loadCases() error = %v, want duplicate declaration failure", err)
	}

	if err := os.WriteFile(source, []byte("//go:build integration\n\npackage sample\n\nfunc TestRegistered(t *testing.T) {}\nfunc TestOtherRegistered(t *testing.T) {}\n"), 0o600); err != nil {
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
    "source": "internal/sample/fixture_integration_test.go"
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
    "source": "internal/sample/fixture_integration_test.go"
  }, {
    "id": "repository/TestRegistered",
    "package": "./internal/sample",
    "run": "^TestRegistered$",
    "phase": "precheck",
    "source": "internal/sample/fixture_integration_test.go"
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
		{ID: "repository/TestExisting", Run: "^TestExisting$", Phase: "precheck", Package: "./internal/conflict", Source: "internal/conflict/fixture_integration_test.go", Capability: "conflict"},
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
