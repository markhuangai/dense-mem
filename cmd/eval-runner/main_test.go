package main

import (
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/markhuangai/dense-mem/internal/evalharness"
)

func TestImportConcurrencyDefault(t *testing.T) {
	t.Setenv("DENSE_MEM_EVAL_IMPORT_CONCURRENCY", "")
	if got := importConcurrencyDefault(); got != evalharness.DefaultImportConcurrency {
		t.Fatalf("import concurrency default = %d, want %d", got, evalharness.DefaultImportConcurrency)
	}
}

func TestImportConcurrencyDefaultUsesEnvironment(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int
	}{{"1", 1}, {"4", 4}, {"30", 30}} {
		t.Setenv("DENSE_MEM_EVAL_IMPORT_CONCURRENCY", test.value)
		if got := importConcurrencyDefault(); got != test.want {
			t.Fatalf("import concurrency default = %d, want %d", got, test.want)
		}
	}
}

func TestEvalRunnerValidationAndEnvironmentHelpers(t *testing.T) {
	for _, test := range []struct {
		name  string
		value float64
		valid bool
	}{
		{name: "rate lower", value: -0.1},
		{name: "rate upper", value: 1.1},
		{name: "rate valid", value: 0.5, valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateRate(test.value)
			if (err == nil) != test.valid {
				t.Fatalf("validateRate(%v) = %v, valid=%t", test.value, err, test.valid)
			}
		})
	}
	if err := validateNonNegative(-1); err == nil {
		t.Fatal("negative value accepted")
	}
	if err := validateNonNegative(math.SmallestNonzeroFloat64); err != nil {
		t.Fatalf("positive value rejected: %v", err)
	}

	t.Setenv("EVAL_RUNNER_TEST_VALUE", " configured ")
	if got := env("EVAL_RUNNER_TEST_VALUE", "fallback"); got != " configured " {
		t.Fatalf("env configured = %q", got)
	}
	t.Setenv("EVAL_RUNNER_TEST_VALUE", "")
	if got := env("EVAL_RUNNER_TEST_VALUE", "fallback"); got != "fallback" {
		t.Fatalf("env fallback = %q", got)
	}
	t.Setenv("EVAL_RUNNER_TEST_INT", "9")
	if got := envInt("EVAL_RUNNER_TEST_INT", 3); got != 9 {
		t.Fatalf("envInt configured = %d", got)
	}
	t.Setenv("EVAL_RUNNER_TEST_INT", "bad")
	if got := envInt("EVAL_RUNNER_TEST_INT", 3); got != 3 {
		t.Fatalf("envInt malformed = %d", got)
	}
	t.Setenv("EVAL_RUNNER_TEST_INT", " ")
	if got := envInt("EVAL_RUNNER_TEST_INT", 3); got != 3 {
		t.Fatalf("envInt blank = %d", got)
	}

	floatTarget := new(float64)
	floatName := "eval-runner-test-float"
	registerFloatGate(floatName, "test", &floatTarget, validateRate)
	if err := flag.CommandLine.Set(floatName, "0.25"); err != nil || floatTarget == nil || *floatTarget != 0.25 {
		t.Fatalf("float gate = %v/%v", err, floatTarget)
	}
	intTarget := new(int)
	intName := "eval-runner-test-int"
	registerIntGate(intName, "test", &intTarget)
	if err := flag.CommandLine.Set(intName, "4"); err != nil || intTarget == nil || *intTarget != 4 {
		t.Fatalf("int gate = %v/%v", err, intTarget)
	}
	invalidFloatName := "eval-runner-test-invalid-float"
	registerFloatGate(invalidFloatName, "test", &floatTarget, validateRate)
	if err := flag.CommandLine.Set(invalidFloatName, "bad"); err == nil {
		t.Fatal("invalid float gate was accepted")
	}
	if err := flag.CommandLine.Set(invalidFloatName, "2"); err == nil {
		t.Fatal("out-of-range float gate was accepted")
	}
	invalidIntName := "eval-runner-test-invalid-int"
	registerIntGate(invalidIntName, "test", &intTarget)
	if err := flag.CommandLine.Set(invalidIntName, "bad"); err == nil {
		t.Fatal("invalid int gate was accepted")
	}
	if err := flag.CommandLine.Set(invalidIntName, "-1"); err == nil {
		t.Fatal("negative int gate was accepted")
	}
}

func TestEvalRunnerMainValidatesASeedThroughTheCLIPath(t *testing.T) {
	oldCommandLine := flag.CommandLine
	oldArgs := os.Args
	t.Cleanup(func() {
		flag.CommandLine = oldCommandLine
		os.Args = oldArgs
	})
	root := t.TempDir()
	seed := filepath.Join(root, "seed_manifest.json")
	if err := os.WriteFile(seed, []byte(`{
  "schema_version": "dense-mem.eval.seed.v1",
  "seed_id": "cli-fixture",
  "corpus_file": "corpus.jsonl",
  "cases_file": "cases.jsonl",
  "qrels_file": "qrels.jsonl"
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"corpus.jsonl": `{"source_doc_id":"doc-1","content":"fixture content"}
`,
		"cases.jsonl": `{"case_id":"case-1","query":"fixture query"}
`,
		"qrels.jsonl": `{"case_id":"case-1","required_refs":[{"type":"fragment","source_doc_id":"doc-1"}]}
`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	suite := filepath.Join(root, "suite.jsonl")
	if err := os.WriteFile(suite, []byte(`{"case_id":"case-1","weight":1}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, environment, override string
		want                        int
	}{
		{name: "default", want: 4},
		{name: "environment minimum", environment: "1", want: 1},
		{name: "environment maximum", environment: "30", want: 30},
		{name: "flag overrides environment", environment: "30", override: "4", want: 4},
		{name: "flag maximum", environment: "1", override: "30", want: 30},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DENSE_MEM_EVAL_IMPORT_CONCURRENCY", test.environment)
			out := t.TempDir()
			flag.CommandLine = flag.NewFlagSet("eval-runner-test", flag.ContinueOnError)
			os.Args = []string{"eval-runner", "-mode", "validate", "-seed", seed, "-suite", suite, "-out", out}
			if test.override != "" {
				os.Args = append(os.Args, "--import-concurrency", test.override)
			}
			main()
			data, err := os.ReadFile(filepath.Join(out, "run_config.json"))
			if err != nil {
				t.Fatal(err)
			}
			var config evalharness.RunConfig
			if err := json.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			if config.ImportConcurrency != test.want {
				t.Fatalf("recorded import concurrency = %d, want %d", config.ImportConcurrency, test.want)
			}
		})
	}
}

func TestEvalRunnerMainCompareModeWritesComparison(t *testing.T) {
	oldCommandLine := flag.CommandLine
	oldArgs := os.Args
	t.Cleanup(func() {
		flag.CommandLine = oldCommandLine
		os.Args = oldArgs
	})
	root := t.TempDir()
	baseline := filepath.Join(root, "baseline")
	candidate := filepath.Join(root, "candidate")
	if err := os.MkdirAll(baseline, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(candidate, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSummary := func(path, runID string) {
		t.Helper()
		contents := `{"run_id":"` + runID + `","seed_hash":"hash","case_count":1,"scored_case_count":1}`
		if err := os.WriteFile(filepath.Join(path, "summary.json"), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSummary(baseline, "baseline")
	writeSummary(candidate, "candidate")
	out := filepath.Join(root, "comparison")
	flag.CommandLine = flag.NewFlagSet("eval-runner-compare", flag.ContinueOnError)
	os.Args = []string{"eval-runner", "-mode", "compare", "-baseline-run", baseline, "-candidate-run", candidate, "-out", out}
	main()
	if _, err := os.Stat(filepath.Join(out, "comparison.json")); err != nil {
		t.Fatalf("comparison artifact missing: %v", err)
	}
}
