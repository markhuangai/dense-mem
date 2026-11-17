package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func TestCLIValidatesPinnedFixturesAndRejectsIncompleteSetup(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	t.Chdir(root)
	for name, value := range map[string]string{"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted", "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "1", "EVAL_SERVER_LOG_FILE": "", "DENSE_MEM_EVAL_API_KEY": ""} {
		t.Setenv(name, value)
	}
	registryHash, err := evalharness.FileHash("tests/eval/fixtures/datasets.json")
	require.NoError(t, err)
	manifest := evalharness.Manifest{Version: 1, RunID: "123-hotpotqa", WorkflowRunID: "123", SourceRevision: strings.Repeat("a", 40), Image: "ghcr.io/markhuangai/dense-mem@sha256:" + strings.Repeat("b", 64), RegistryHash: registryHash, Dataset: "hotpotqa", Mode: "fixtures", Operation: "run", SeedDir: "seed", Model: "dense-mem-testing", ContextWindowTokens: 500000, EmbeddingModel: "text-embedding-3-small", EmbeddingDimensions: 1536, ModelDeadline: time.Now().Add(time.Minute), DataDir: filepath.Join(root, "tests/eval/fixtures"), OutputDir: filepath.Join(t.TempDir(), "public")}
	path := filepath.Join(t.TempDir(), "manifest.json")
	writeManifest := func() {
		body, err := json.Marshal(manifest)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, body, 0o600))
	}
	writeManifest()
	var console bytes.Buffer
	logger := observability.NewConsoleWithHandler(slog.NewJSONHandler(&console, &slog.HandlerOptions{Level: slog.LevelDebug}))
	protector := evalharness.NewRunnerCredentialProtector("synthetic-cli-key", "https://provider-canary.example")
	require.NoError(t, execute(context.Background(), []string{"validate", path}, logger, protector))
	require.Contains(t, console.String(), "evaluation_validated")
	require.NotContains(t, console.String(), "synthetic-cli-key")
	require.NotContains(t, console.String(), "provider-canary")
	require.ErrorContains(t, execute(context.Background(), []string{"run", path}, logger, protector), "required runner setting missing: DENSE_MEM_EVAL_API_KEY")
	manifest.ModelDeadline = time.Now().Add(-time.Second)
	writeManifest()
	require.ErrorContains(t, execute(context.Background(), []string{"run", path}, logger, protector), "model deadline is expired")
	manifest.ModelDeadline = time.Now().Add(time.Minute)
	manifest.RegistryHash = strings.Repeat("d", 64)
	writeManifest()
	require.ErrorContains(t, execute(context.Background(), []string{"validate", path}, logger, protector), "registry digest differs")

	manifest.RegistryHash = registryHash
	manifest.Operation, manifest.Dataset, manifest.RunID = "bootstrap", "bootstrap", "123-bootstrap"
	manifest.ImportWorkers, manifest.BaseRelease = 20, "v2.8.1-rc.5"
	manifest.SeedDir = t.TempDir()
	writeManifest()
	t.Setenv("EVAL_API_KEY", "synthetic-cli-bootstrap-key")
	t.Setenv("EVAL_TEAM_ID", "invalid-bootstrap-team")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorContains(t, execute(cancelled, []string{"bootstrap", path}, logger, protector), "runner container identity cannot be verified")
	require.NoFileExists(t, filepath.Join(manifest.SeedDir, "scope.json"))
}

func TestBootstrapRejectsIncompleteSetupBeforeWritingScope(t *testing.T) {
	manifest := evalharness.Manifest{Dataset: "hotpotqa", SeedDir: t.TempDir()}
	t.Setenv("EVAL_API_KEY", "synthetic-cli-bootstrap-key")
	require.ErrorContains(t, bootstrapCredential(context.Background(), manifest), "bootstrap requires")
	manifest.Dataset = "bootstrap"
	t.Setenv("EVAL_API_KEY", "")
	require.ErrorContains(t, bootstrapCredential(context.Background(), manifest), "configured team key")
	t.Setenv("EVAL_API_KEY", "synthetic-cli-bootstrap-key")
	t.Setenv("EVAL_TEAM_ID", "invalid-bootstrap-team")
	require.ErrorContains(t, bootstrapCredential(context.Background(), manifest), "invalid bootstrap team")
	t.Setenv("EVAL_TEAM_ID", "239f6b35-a934-4f3a-a075-da6836f5debc")
	t.Setenv("EVAL_DATABASE_URL", "invalid-bootstrap-dsn")
	require.Error(t, bootstrapCredential(context.Background(), manifest))
	require.NoFileExists(t, filepath.Join(manifest.SeedDir, "scope.json"))
}

func TestCLIRejectsRetiredInterfacesAndLocalCampaigns(t *testing.T) {
	logger := observability.NewConsoleWithHandler(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	protector := observability.NewCredentialProtector()
	for _, mode := range []string{"paired", "import", "baseline", "candidate", "resume", "token-diagnostics", "runtime", "report", "replay"} {
		require.ErrorContains(t, execute(context.Background(), []string{mode, "manifest"}, logger, protector), "workflow-internal")
	}
	t.Setenv("GITHUB_ACTIONS", "")
	require.ErrorContains(t, execute(context.Background(), []string{"run", "manifest"}, logger, protector), "GitHub-hosted")
}

func TestRuntimeLogCollectorProtectsOutputAndExposesIncompleteCapture(t *testing.T) {
	var output bytes.Buffer
	protector := evalharness.NewRunnerCredentialProtector("secret-canary", "https://provider-canary.example")
	capture := &evalharness.Capture{Logger: slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: protector, RunID: "test"}
	usage := map[string]evalharness.UsageTotals{}
	lines := "provider failed secret-canary at https://provider-canary.example\n" +
		`{"level":"DEBUG","msg":"successful document detail"}` + "\n" +
		`{"level":"WARN","msg":"provider retry secret-canary"}` + "\n" +
		`{"level":"ERROR","msg":"provider terminal failure secret-canary"}` + "\n"
	encoded := []string{base64.StdEncoding.EncodeToString([]byte("secret-canary")), hex.EncodeToString([]byte("secret-canary")), strings.ToUpper(hex.EncodeToString([]byte("secret-canary")))}
	failure, err := json.MarshalIndent(map[string]string{"message": "seed publication rejected secret-canary", "diagnostic": strings.Join(encoded, " ")}, "", "  ")
	require.NoError(t, err)
	lines += string(failure) + "\n"
	require.NoError(t, logReader(context.Background(), strings.NewReader(lines), capture, usage))
	protected := runtimeCapturePayloads(t, output.Bytes())
	require.NotContains(t, protected, "secret-canary")
	require.NotContains(t, protected, "https://provider-canary.example")
	for _, value := range encoded {
		require.NotContains(t, protected, value)
		require.NotContains(t, output.String(), value)
	}
	require.Contains(t, protected, "provider failed")
	require.Contains(t, protected, "provider retry")
	require.Contains(t, protected, "provider terminal failure")
	require.Contains(t, protected, "seed publication rejected")
	require.NotContains(t, protected, "successful document detail")
	broken := `{"msg":"ai_exchange_payload","exchange_id":"exchange","direction":"request","payload_chunk":{"state":"captured","count":2,"index":0,"body":"partial","sha256":"hash"}}` + "\n"
	require.ErrorContains(t, logReader(context.Background(), strings.NewReader(broken), capture, usage), "incomplete")
}

type cancellingLogWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancellingLogWriter) Write(data []byte) (int, error) {
	n, err := w.Buffer.Write(data)
	w.cancel()
	return n, err
}

func TestRuntimeLogCollectorHonorsCancellationBetweenRecords(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &cancellingLogWriter{cancel: cancel}
	capture := &evalharness.Capture{Logger: slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: evalharness.NewRunnerCredentialProtector(), RunID: "cancelled"}
	err := logReader(ctx, strings.NewReader("first failure\nsecond failure\n"), capture, map[string]evalharness.UsageTotals{})
	require.ErrorIs(t, err, context.Canceled)
	require.Contains(t, output.String(), "first failure")
	require.NotContains(t, output.String(), "second failure")
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	require.ErrorIs(t, logReader(ctx, reader, capture, map[string]evalharness.UsageTotals{}), context.Canceled)
}

const runtimeLogKeyCanary = "provider-log-key-canary-1739"
const runtimeLogURLCanary = "https://provider-log-canary.example/v1"

func runtimeCapturePayloads(t *testing.T, logs []byte) string {
	t.Helper()
	var protected strings.Builder
	for _, line := range bytes.Split(bytes.TrimSpace(logs), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record struct {
			Kind    string                             `json:"kind"`
			Payload json.RawMessage                    `json:"payload"`
			Chunk   *observability.DiagnosticBodyChunk `json:"payload_chunk"`
		}
		require.NoError(t, json.Unmarshal(line, &record))
		if record.Chunk != nil {
			protected.WriteString(record.Chunk.Body)
		} else {
			body, err := json.Marshal(evalharness.Event{Kind: record.Kind, Payload: record.Payload})
			require.NoError(t, err)
			protected.Write(body)
		}
	}
	return protected.String()
}

func runtimeProviderLogFixture(t *testing.T) string {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	protector := evalharness.NewRunnerCredentialProtector(runtimeLogKeyCanary, runtimeLogURLCanary)
	for _, item := range []struct {
		name, schema string
		usage        map[string]int
	}{
		{"partition", "dense_mem_semantic_partition_response", map[string]int{"prompt_tokens": 2, "completion_tokens": 3, "total_tokens": 5}},
		{"assessment", "dense_mem_evidence_assessment_response", map[string]int{"prompt_tokens": 4, "completion_tokens": 3}},
		{"ontology", "ontology_evidence_organization_v3", nil},
		{"embedding", "", map[string]int{"prompt_tokens": 7, "completion_tokens": 0, "total_tokens": 7}},
		{"other", "unknown_schema", map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 5}},
	} {
		request := map[string]any{"response_format": map[string]any{"json_schema": map[string]any{"name": item.schema}}, "public_text": strings.Repeat("Exact public Evidence. ", 1500), "api_key": runtimeLogKeyCanary, "endpoint": runtimeLogURLCanary, "password": "ordinary benchmark text"}
		if item.name == "embedding" {
			request = map[string]any{"input": []string{"Exact public Evidence."}}
		}
		for _, direction := range []string{"request", "response"} {
			var payload any = request
			if direction == "response" {
				payload = map[string]any{"usage": item.usage}
			}
			body, err := json.Marshal(payload)
			require.NoError(t, err)
			observability.LogDiagnosticBody(context.Background(), logger, protector, "ai_exchange_payload", body, 16<<20,
				slog.String("exchange_id", item.name), slog.String("direction", direction))
		}
	}
	return logs.String()
}

func TestRuntimeLogCollectorReconstructsProtectedExchangesAndUsage(t *testing.T) {
	logs := runtimeProviderLogFixture(t)
	var output bytes.Buffer
	capture := &evalharness.Capture{RunID: "collector", Logger: slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: evalharness.NewRunnerCredentialProtector(runtimeLogKeyCanary, runtimeLogURLCanary)}
	usage := map[string]evalharness.UsageTotals{}
	require.NoError(t, logReader(context.Background(), strings.NewReader(logs), capture, usage))
	require.Len(t, usage, 5)
	require.EqualValues(t, 1, usage["intake_partition"].Available)
	require.EqualValues(t, 5, usage["intake_partition"].Total)
	require.EqualValues(t, 1, usage["intake_assessment"].Partial)
	require.EqualValues(t, 4, usage["intake_assessment"].Input)
	require.EqualValues(t, 1, usage["ontology"].Unavailable)
	require.EqualValues(t, 1, usage["embedding"].Available)
	require.EqualValues(t, 7, usage["embedding"].Total)
	require.EqualValues(t, 1, usage["other"].Partial)
	for _, total := range usage {
		require.EqualValues(t, 1, total.Attempts)
	}
	for _, text := range []string{logs, output.String()} {
		require.NotContains(t, text, runtimeLogKeyCanary)
		require.NotContains(t, text, runtimeLogURLCanary)
	}
	protected := runtimeCapturePayloads(t, output.Bytes())
	require.NotContains(t, protected, runtimeLogKeyCanary)
	require.NotContains(t, protected, runtimeLogURLCanary)
	require.NotContains(t, protected, "ordinary benchmark text", "successful provider payloads are parsed for usage without being printed")
	require.Contains(t, logs, "ordinary benchmark text")
	path := filepath.Join(t.TempDir(), "server.log")
	require.NoError(t, os.WriteFile(path, []byte(logs), 0o600))
	require.NoError(t, logFile(context.Background(), path, capture))
	require.ErrorIs(t, logFile(context.Background(), path+"-missing", capture), os.ErrNotExist)
}

func TestRuntimeLogCollectorRejectsCorruptionAndIncompleteExchanges(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(runtimeProviderLogFixture(t)), "\n")
	for _, item := range []struct {
		name, field string
		value       any
		outer       bool
	}{
		{"capture unavailable", "capture_state", "unavailable", true},
		{"transport interrupted", "transport_capture_state", "interrupted", true},
		{"chunk unavailable", "state", "unavailable", false},
		{"zero chunks", "count", 0, false},
		{"unbounded chunks", "count", 1 << 20, false},
		{"invalid index", "index", -1, false},
		{"altered payload", "body", "altered public text", false},
	} {
		t.Run(item.name, func(t *testing.T) {
			var first map[string]any
			require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
			if item.outer {
				first[item.field] = item.value
			} else {
				first["payload_chunk"].(map[string]any)[item.field] = item.value
			}
			body, err := json.Marshal(first)
			require.NoError(t, err)
			broken := append([]string{string(body)}, lines[1:]...)
			require.Error(t, logReader(context.Background(), strings.NewReader(strings.Join(broken, "\n")), nil, map[string]evalharness.UsageTotals{}))
		})
	}
	for _, item := range []struct {
		name string
		logs []string
	}{
		{"missing chunk", lines[1:]},
		{"duplicate completed chunk", append(append([]string{}, lines...), lines[0])},
		{"missing provider response", lines[:len(lines)-1]},
		{"unbounded line", []string{strings.Repeat("x", (1<<20)+1)}},
	} {
		t.Run(item.name, func(t *testing.T) {
			require.Error(t, logReader(context.Background(), strings.NewReader(strings.Join(item.logs, "\n")), nil, map[string]evalharness.UsageTotals{}))
		})
	}
}
