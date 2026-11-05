package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEvalRunnerTokenDiagnosticsProcess(t *testing.T) {
	if os.Getenv("DENSE_MEM_TEST_DIAGNOSTICS_PROCESS") != "1" {
		return
	}
	for index, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"eval-runner"}, os.Args[index+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("eval-runner", flag.ExitOnError)
	main()
	os.Exit(0)
}

func TestEvalRunnerTokenDiagnosticsCLIProtectsArtifactsAndRefusesOverwrite(t *testing.T) {
	secrets := []string{"scrape-fixture", "embedding-fixture", "verifier-fixture", "control-fixture", "redis-fixture", "database-fixture", "eval-api-fixture", "eval-control-fixture"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scrape-fixture" {
			t.Error("CLI did not use runtime telemetry credential")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		for index, secret := range secrets {
			_, _ = fmt.Fprintf(w, "densemem_embedding_tokens_total{model=%q,kind=%q} %d\n", secret, fmt.Sprint(index), index+1)
		}
	}))
	defer server.Close()
	out := filepath.Join(t.TempDir(), "observation")
	env := tokenDiagnosticsEnvironment(server.URL)
	stdout, stderr, err := runTokenDiagnosticsCLI(t, env, "-mode", "token-diagnostics", "-out", out)
	require.NoError(t, err, "%s", stderr)
	require.Equal(t, "token diagnostics written\n", stdout)
	require.Empty(t, stderr)
	artifact := filepath.Join(out, "token_usage.json")
	body, err := os.ReadFile(artifact)
	require.NoError(t, err)
	require.True(t, json.Valid(body))
	for _, secret := range secrets {
		require.NotContains(t, string(body)+stdout+stderr, secret)
	}
	require.Equal(t, len(secrets), strings.Count(string(body), "[REDACTED]"))
	info, err := os.Stat(artifact)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	stdout, stderr, err = runTokenDiagnosticsCLI(t, env, "-mode", "token-diagnostics", "-metrics-url", server.URL, "-out", out)
	require.Error(t, err)
	require.Empty(t, stdout)
	require.Contains(t, stderr, "already exists")
	after, err := os.ReadFile(artifact)
	require.NoError(t, err)
	require.Equal(t, body, after)
}

func TestEvalRunnerTokenDiagnosticsCLIFailuresAreBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, "private-response-body")
	}))
	defer server.Close()
	for _, test := range []struct {
		name string
		env  []string
		want string
	}{
		{"configuration", []string{"POSTGRES_DSN=postgres://user:private%zz@localhost/db", "TELEMETRY_SCRAPE_TOKEN=scrape-fixture"}, "invalid operational credential configuration"},
		{"invalid setting", []string{"LOG_LEVEL=private-response-body"}, "invalid runtime configuration"},
		{"missing token", []string{"POSTGRES_DSN=postgres://user:password@localhost/db"}, "scrape token is required"},
		{"unauthorized", tokenDiagnosticsEnvironment(server.URL), "HTTP 401"},
	} {
		t.Run(test.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "observation")
			stdout, stderr, err := runTokenDiagnosticsCLI(t, test.env, "-mode", "token-diagnostics", "-metrics-url", server.URL, "-out", out)
			require.Error(t, err)
			require.Empty(t, stdout)
			require.Contains(t, stderr, test.want)
			for _, value := range []string{"private", "scrape-fixture", server.URL, out} {
				require.NotContains(t, stderr, value)
			}
			_, err = os.Stat(filepath.Join(out, "token_usage.json"))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestEvalRunnerHelpDoesNotExposeEnvironmentDefaults(t *testing.T) {
	env := append(tokenDiagnosticsEnvironment("http://fixture.test/private-endpoint"), "DENSE_MEM_BASE_URL=http://user:private-base-key@fixture.test")
	stdout, stderr, err := runTokenDiagnosticsCLI(t, env, "-help")
	require.NoError(t, err)
	require.Contains(t, stderr, "token-diagnostics")
	for _, value := range []string{"eval-api-fixture", "private-endpoint", "private-base-key", "scrape-fixture", "eval-control-fixture"} {
		require.NotContains(t, stdout+stderr, value)
	}
}

func TestEvalRunnerEnvironmentDefaultsPreserveExplicitEmptyFlags(t *testing.T) {
	t.Setenv("DENSE_MEM_API_KEY", "environment-key")
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	key := fs.String("api-key", "", "")
	require.NoError(t, fs.Parse([]string{"-api-key="}))
	resolveStringEnvironment(fs, "api-key", "DENSE_MEM_API_KEY", key)
	require.Empty(t, *key)
	fs = flag.NewFlagSet("fixture", flag.ContinueOnError)
	key = fs.String("api-key", "", "")
	require.NoError(t, fs.Parse(nil))
	resolveStringEnvironment(fs, "api-key", "DENSE_MEM_API_KEY", key)
	require.Equal(t, "environment-key", *key)
}

func TestEvalRunnerTokenDiagnosticsOutputFailuresPreserveExistingPaths(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(file, []byte("existing"), 0o600))
	err := writeTokenDiagnostics(file, []byte("{}\n"))
	require.ErrorContains(t, err, "output directory")
	linkDir := filepath.Join(root, "link")
	require.NoError(t, os.Mkdir(linkDir, 0o700))
	require.NoError(t, os.Symlink(file, filepath.Join(linkDir, "token_usage.json")))
	require.ErrorContains(t, writeTokenDiagnostics(linkDir, []byte("{}\n")), "already exists")
	body, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "existing", string(body))
}

func tokenDiagnosticsEnvironment(endpoint string) []string {
	return []string{
		"POSTGRES_DSN=postgres://user:database-fixture@localhost/db", "TELEMETRY_SCRAPE_TOKEN=scrape-fixture",
		"AI_API_URL=https://provider.example.test/v1", "AI_API_EMBEDDING_MODEL=fixture-embedding", "AI_API_EMBEDDING_DIMENSIONS=1024",
		"AI_API_KEY=embedding-fixture", "AI_VERIFIER_API_KEY=verifier-fixture", "CONTROL_PORTAL_TOKEN=control-fixture", "REDIS_PASSWORD=redis-fixture",
		"DENSE_MEM_API_KEY=eval-api-fixture", "DENSE_MEM_CONTROL_TOKEN=eval-control-fixture", "DENSE_MEM_EVAL_METRICS_URL=" + endpoint,
	}
}

func runTokenDiagnosticsCLI(t *testing.T, env []string, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestEvalRunnerTokenDiagnosticsProcess$", "--"}, args...)...)
	command.Env = append([]string{"DENSE_MEM_TEST_DIAGNOSTICS_PROCESS=1"}, env...)
	// Forward Go's coverage directory so CLI subprocess counters join the parent profile.
	if coverage := flag.Lookup("test.gocoverdir"); coverage != nil && coverage.Value.String() != "" {
		command.Env = append(command.Env, "GOCOVERDIR="+coverage.Value.String())
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return stdout.String(), stderr.String(), err
}

func TestEvaluationDiagnosticsUsesExistingLevelParser(t *testing.T) {
	t.Setenv("LOG_LEVEL", "debug")
	logger, protector, err := evaluationDiagnostics("synthetic-evaluator-key")
	require.NoError(t, err)
	require.NotNil(t, logger)
	require.NotNil(t, protector)
	t.Setenv("LOG_LEVEL", "invalid-level")
	_, _, err = evaluationDiagnostics("synthetic-evaluator-key")
	require.ErrorContains(t, err, "invalid LOG_LEVEL")
}

func TestEvaluationDiagnosticsStartupFailureDoesNotEchoCredentials(t *testing.T) {
	const presented = "private-startup-presented-credential"
	const configured = "private-startup-configured-credential"
	const control = "private-startup-control-credential"
	for _, test := range []struct{ name, level string }{
		{"presented", presented}, {"configured", configured}, {"control", control},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr, err := runTokenDiagnosticsCLI(t, []string{
				"DENSE_MEM_API_KEY=" + configured, "DENSE_MEM_CONTROL_TOKEN=" + control, "LOG_LEVEL=" + test.level,
			}, "--mode", "candidate", "--api-key", presented)
			require.Error(t, err)
			require.Empty(t, stdout)
			require.Contains(t, stderr, "invalid LOG_LEVEL")
			for _, secret := range []string{presented, configured, control} {
				require.NotContains(t, stderr, secret)
			}
		})
	}
}

func TestEvalRunnerFailureProtectsCredentialsInBothOutputStreams(t *testing.T) {
	const key = "private-cli-presented-credential"
	const configured = "private-cli-environment-credential"
	const control = "private-cli-control-credential"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "Bearer "+key, r.Header.Get("Authorization"))
		http.Error(w, fmt.Sprintf("admitted rejection: %s %s %s", key, configured, control), http.StatusBadRequest)
	}))
	defer server.Close()
	root := t.TempDir()
	for name, contents := range map[string]string{
		"seed_manifest.json": `{"schema_version":"dense-mem.eval.seed.v1","seed_id":"cli-failure","corpus_file":"corpus.jsonl","cases_file":"cases.jsonl","qrels_file":"qrels.jsonl"}`,
		"corpus.jsonl":       `{"source_doc_id":"doc-1","content":"fixture content"}` + "\n",
		"cases.jsonl":        `{"case_id":"case-1","query":"fixture query"}` + "\n",
		"qrels.jsonl":        `{"case_id":"case-1","required_refs":[{"type":"fragment","source_doc_id":"doc-1"}]}` + "\n",
		"suite.jsonl":        `{"case_id":"case-1","weight":1}` + "\n",
		"mapping.json":       `{"by_source_doc_id":{"doc-1":{"type":"fragment","id":"frag-1","source_doc_id":"doc-1"}}}`,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600))
	}
	stdout, stderr, err := runTokenDiagnosticsCLI(t, []string{"DENSE_MEM_API_KEY=" + configured, "DENSE_MEM_CONTROL_TOKEN=" + control, "LOG_LEVEL=debug"},
		"-mode", "candidate", "-seed", filepath.Join(root, "seed_manifest.json"), "-suite", filepath.Join(root, "suite.jsonl"),
		"-mapping", filepath.Join(root, "mapping.json"), "-base-url", server.URL, "-api-key", key, "-out", filepath.Join(root, "capture"))
	require.Error(t, err)
	require.Equal(t, int32(1), calls.Load())
	require.Contains(t, stderr, "returned 400")
	require.Contains(t, stderr, "admitted rejection")
	require.Contains(t, stderr, "[REDACTED]")
	retained := stdout + stderr
	entries, err := os.ReadDir(filepath.Join(root, "capture"))
	require.NoError(t, err)
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(root, "capture", entry.Name()))
		require.NoError(t, err)
		retained += string(body)
	}
	for _, secret := range []string{key, configured, control} {
		require.NotContains(t, retained, secret)
	}
}
