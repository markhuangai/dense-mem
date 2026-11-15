package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestCLIValidatesPinnedFixturesAndReportsSyntheticFailure(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	t.Chdir(root)
	for name, value := range map[string]string{"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted", "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "1", "EVAL_SERVER_LOG_FILE": "", "DENSE_MEM_EVAL_API_KEY": ""} {
		t.Setenv(name, value)
	}
	registryHash, err := evalharness.FileHash("tests/eval/fixtures/datasets.json")
	require.NoError(t, err)
	manifest := evalharness.Manifest{Version: 1, RunID: "123-hotpotqa", WorkflowRunID: "123", SourceRevision: strings.Repeat("a", 40), Image: "ghcr.io/markhuangai/dense-mem@sha256:" + strings.Repeat("b", 64), RegistryHash: registryHash, Dataset: "hotpotqa", Mode: "fixtures", Model: "dense-mem-testing", ContextWindowTokens: 500000, EmbeddingModel: "@cf/baai/bge-m3", EmbeddingDimensions: 1024, ModelDeadline: time.Now().Add(time.Minute), DataDir: filepath.Join(root, "tests/eval/fixtures"), OutputDir: filepath.Join(t.TempDir(), "public")}
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
	var captureOutput bytes.Buffer
	capture := &evalharness.Capture{RunID: manifest.RunID, Logger: slog.New(slog.NewJSONHandler(&captureOutput, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: protector}
	require.NoError(t, capture.Emit("started", manifest))
	require.NoError(t, capture.Emit("finished", map[string]any{"imported_rows": 0, "canonical_hash": "", "elapsed_ms": 1, "failure": "startup incomplete", "native_measurements": []any{}, "server_usage": map[string]any{}}))
	logsDir := t.TempDir()
	t.Setenv("EVAL_LOGS_DIR", logsDir)
	require.NoError(t, os.WriteFile(filepath.Join(logsDir, "synthetic.txt"), captureOutput.Bytes(), 0o600))
	manifest.Mode, manifest.WorkflowRunID = "replay", "456"
	t.Setenv("GITHUB_RUN_ID", "456")
	writeManifest()
	require.ErrorContains(t, execute(context.Background(), []string{"run", path}, logger, protector), "replay cannot start a runtime")
	require.NoError(t, execute(context.Background(), []string{"report", path}, logger, protector))
	body, err := os.ReadFile(filepath.Join(manifest.OutputDir, "scores.json"))
	require.NoError(t, err)
	var report evalharness.Report
	require.NoError(t, json.Unmarshal(body, &report))
	require.False(t, report.Complete)
	require.True(t, report.CaptureComplete)
	require.Equal(t, 8, report.ExpectedAnswers)
	require.Zero(t, report.CompletedAnswers)
	require.Equal(t, "123", report.Identity.WorkflowRunID)
	require.Equal(t, "dense-mem-testing", report.Identity.Model)
	require.NotContains(t, string(body), "provider-canary")
	require.NotContains(t, string(body), "synthetic-cli-key")
	require.ErrorIs(t, execute(context.Background(), []string{"report", path}, logger, protector), os.ErrExist)
	require.NoError(t, os.Remove(filepath.Join(manifest.OutputDir, "scores.json")))
	require.NoError(t, os.Remove(filepath.Join(logsDir, "synthetic.txt")))
	require.Error(t, execute(context.Background(), []string{"report", path}, logger, protector))
	require.NoFileExists(t, filepath.Join(manifest.OutputDir, "scores.json"))
}

func TestCLIRejectsRetiredInterfacesAndLocalCampaigns(t *testing.T) {
	logger := observability.NewConsoleWithHandler(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	protector := observability.NewCredentialProtector()
	for _, mode := range []string{"paired", "import", "baseline", "candidate", "resume", "token-diagnostics", "runtime"} {
		require.ErrorContains(t, execute(context.Background(), []string{mode, "manifest"}, logger, protector), "workflow-internal")
	}
	t.Setenv("GITHUB_ACTIONS", "")
	require.ErrorContains(t, execute(context.Background(), []string{"run", "manifest"}, logger, protector), "GitHub-hosted")
}

func TestRuntimeLogCollectorProtectsOutputAndExposesIncompleteCapture(t *testing.T) {
	var output bytes.Buffer
	protector := observability.NewCredentialProtector("secret-canary", "https://provider-canary.example")
	capture := &evalharness.Capture{Logger: slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: protector, RunID: "test"}
	usage := map[string]evalharness.UsageTotals{}
	require.NoError(t, logReader(strings.NewReader("provider failed secret-canary at https://provider-canary.example\n"), capture, usage))
	require.NotContains(t, output.String(), "secret-canary")
	require.NotContains(t, output.String(), "https://provider-canary.example")
	broken := `{"msg":"ai_exchange_payload","exchange_id":"exchange","direction":"request","payload_chunk":{"state":"captured","count":2,"index":0,"body":"partial","sha256":"hash"}}` + "\n"
	require.ErrorContains(t, logReader(strings.NewReader(broken), capture, usage), "incomplete")
}
