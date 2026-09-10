package main

import (
	"encoding/json"
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
		cases = append(cases, databaseCase{ID: fmt.Sprintf("case-%03d", index), Package: "./internal/repository"})
	}
	cases = append(cases, databaseCase{ID: "other", Package: "./internal/http"})

	batches := groupCases(cases)
	if len(batches) != 3 {
		t.Fatalf("expected three batches, got %d", len(batches))
	}
	if batches[0].Package != "./internal/http" || len(batches[0].Cases) != 1 {
		t.Fatalf("unexpected first batch: %+v", batches[0])
	}
	if batches[1].Package != "./internal/repository" || len(batches[1].Cases) != maxDatabaseCasesPerBatch {
		t.Fatalf("unexpected first repository batch: %+v", batches[1])
	}
	if batches[2].Package != "./internal/repository" || len(batches[2].Cases) != 1 {
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
		{name: "command failure", output: `{"Action":"fail","Test":"TestRequired"}`, exitStatus: 1, want: "batch ./internal/repository failed"},
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
				Package: "./internal/repository",
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
	if _, err := loadCases(root, "precheck", "repository", "", ""); err == nil || !strings.Contains(err.Error(), "has no declaration") {
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
		if capability != "index-retirement" && len(fragment.Cases) == 0 {
			t.Fatalf("wave 7 fragment %s is unexpectedly empty", capability)
		}
		if capability == "index-retirement" && len(fragment.Cases) != 0 {
			t.Fatalf("index-retirement fragment unexpectedly owns cases")
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
