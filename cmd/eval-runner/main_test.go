package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
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
	manifest := evalharness.Manifest{Version: 1, RunID: "123-hotpotqa-off", Condition: "off", SeedImage: "sha256:" + strings.Repeat("d", 64), WorkflowRunID: "123", SourceRevision: strings.Repeat("a", 40), Image: "ghcr.io/markhuangai/dense-mem@sha256:" + strings.Repeat("b", 64), RegistryHash: registryHash, Dataset: "hotpotqa", Mode: "fixtures", Operation: "run", SeedDir: "seed", Model: "dense-mem-testing", ContextWindowTokens: 500000, EmbeddingModel: "text-embedding-3-small", EmbeddingDimensions: 1536, ModelDeadline: time.Now().Add(time.Minute), DataDir: filepath.Join(root, "tests/eval/fixtures"), OutputDir: filepath.Join(t.TempDir(), "public")}
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
	require.NoError(t, execute(context.Background(), []string{"validate", path}, logger, protector, &console))
	require.Contains(t, console.String(), "evaluation_validated")
	require.NotContains(t, console.String(), "synthetic-cli-key")
	require.NotContains(t, console.String(), "provider-canary")
	t.Run("main emits validation through a pipe", func(t *testing.T) {
		read, write, err := os.Pipe()
		require.NoError(t, err)
		defer read.Close()
		defer write.Close()
		originalArgs := os.Args
		t.Cleanup(func() { os.Args = originalArgs })
		os.Args = []string{"eval-runner", "validate", path}
		originalStdout, err := unix.Dup(unix.Stdout)
		require.NoError(t, err)
		defer unix.Close(originalStdout)
		func() {
			require.NoError(t, unix.Dup3(int(write.Fd()), unix.Stdout, 0))
			defer func() { require.NoError(t, unix.Dup3(originalStdout, unix.Stdout, 0)) }()
			main()
		}()
		require.NoError(t, write.Close())
		data, err := io.ReadAll(read)
		require.NoError(t, err)
		require.Contains(t, string(data), `"msg":"evaluation_validated"`)
	})
	require.ErrorContains(t, execute(context.Background(), []string{"run", path}, logger, protector, &console), "required runner setting missing: DENSE_MEM_EVAL_API_KEY")
	manifest.ModelDeadline = time.Now().Add(-time.Second)
	writeManifest()
	require.ErrorContains(t, execute(context.Background(), []string{"run", path}, logger, protector, &console), "model deadline is expired")
	manifest.ModelDeadline = time.Now().Add(time.Minute)
	manifest.RegistryHash = strings.Repeat("d", 64)
	writeManifest()
	require.ErrorContains(t, execute(context.Background(), []string{"validate", path}, logger, protector, &console), "registry digest differs")

	manifest.RegistryHash = registryHash
	manifest.Operation, manifest.Dataset, manifest.RunID = "bootstrap", "bootstrap", "123-bootstrap"
	manifest.Condition, manifest.SeedImage = "", ""
	manifest.ImportWorkers, manifest.BaseRelease = 20, "v2.8.1-rc.5"
	manifest.SeedDir = t.TempDir()
	writeManifest()
	t.Setenv("EVAL_API_KEY", "synthetic-cli-bootstrap-key")
	t.Setenv("EVAL_TEAM_ID", "invalid-bootstrap-team")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorContains(t, execute(cancelled, []string{"bootstrap", path}, logger, protector, &console), "runner container identity cannot be verified")
	require.NoFileExists(t, filepath.Join(manifest.SeedDir, "scope.json"))

	manifest.Mode, manifest.Operation, manifest.Dataset, manifest.RunID = "intake", "intake", "intake", "123-intake"
	writeManifest()
	require.ErrorContains(t, execute(t.Context(), []string{"validate", path}, logger, protector, &console), "invalid runner campaign mode")

	manifest.Mode, manifest.Operation, manifest.Dataset, manifest.RunID = "fixtures", "run", "hotpotqa", "123-hotpotqa-off"
	manifest.Condition, manifest.SeedImage = "off", "sha256:"+strings.Repeat("d", 64)
	manifest.ImportWorkers = 0
	writeManifest()
	logPath := filepath.Join(t.TempDir(), "server.log")
	require.NoError(t, os.WriteFile(logPath, []byte("startup failure at https://provider-canary.example with synthetic-cli-key\n"), 0o600))
	t.Setenv("EVAL_SERVER_LOG_FILE", logPath)
	console.Reset()
	require.NoError(t, execute(t.Context(), []string{"validate", path}, logger, protector, &console))
	require.Contains(t, console.String(), "startup failure")
	require.NotContains(t, console.String(), "synthetic-cli-key")
	require.NotContains(t, console.String(), "provider-canary")
	t.Setenv("RUNNER_ENVIRONMENT", "self-hosted")
	require.ErrorContains(t, execute(t.Context(), []string{"validate", path}, logger, protector, &console), "GitHub-hosted")

	require.ErrorContains(t, execute(t.Context(), []string{"freeze-intake-proof", t.TempDir()}, logger, protector, &console), "requires local Docker")
	t.Setenv("GITHUB_ACTIONS", "")
	require.ErrorIs(t, execute(t.Context(), []string{"freeze-intake-proof", t.TempDir()}, logger, protector, &console), os.ErrNotExist)

	localCheckout := t.TempDir()
	registryFile := filepath.Join(localCheckout, "tests/eval/datasets.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(registryFile), 0700))
	fixtureRegistry, err := os.ReadFile("tests/eval/fixtures/datasets.json")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(registryFile, fixtureRegistry, 0600))
	t.Chdir(localCheckout)
	t.Setenv("EVAL_SERVER_LOG_FILE", "")
	manifest.SeedBuildID = "local-" + strings.Repeat("a", 32)
	t.Setenv("EVAL_LOCAL_SEED_ID", manifest.SeedBuildID)
	manifest.RunID, manifest.WorkflowRunID = manifest.SeedBuildID+"-hotpotqa", ""
	manifest.Mode, manifest.Operation, manifest.ImportWorkers = "live", "prepare", 1
	manifest.Condition = ""
	manifest.SeedImage = "ghcr.io/markhuangai/dense-mem-eval-postgres@sha256:" + strings.Repeat("c", 64)
	manifest.SkipAISecurityReview = true
	writeManifest()
	admissionPath := filepath.Join(filepath.Dir(path), "seed-admission.json")
	scopePath := filepath.Join(filepath.Dir(path), "seed-admission-scope.json")
	require.ErrorIs(t, execute(t.Context(), []string{"validate", path}, logger, protector, &console), os.ErrNotExist)
	require.NoFileExists(t, admissionPath)
	scope := evalharness.Scope{TeamID: uuid.NewString(), CredentialID: uuid.NewString(), OwnerID: uuid.NewString(), SpaceID: uuid.NewString()}
	body, err := json.Marshal(scope)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(scopePath, body, 0600))
	require.ErrorContains(t, execute(t.Context(), []string{"validate", path}, logger, protector, &console), "incomplete public seed admission manifest")
	require.NoFileExists(t, admissionPath)
	scope.Generation = 1
	body, err = json.Marshal(scope)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(scopePath, body, 0600))
	require.NoError(t, execute(t.Context(), []string{"validate", path}, logger, protector, &console))
	admission, err := os.ReadFile(admissionPath)
	require.NoError(t, err)
	require.Contains(t, string(admission), scope.CredentialID)
	require.Contains(t, string(admission), "eval:hotpotqa:row:0")
	require.ErrorContains(t, execute(t.Context(), []string{"bake-ontology", path}, logger, protector, &console), "workflow-internal")

	manifest.IntakeProof, manifest.Image = true, "sha256:"+strings.Repeat("e", 64)
	manifest.DataDir = filepath.Join(root, "tests/eval/fixtures")
	writeManifest()
	require.ErrorIs(t, execute(t.Context(), []string{"validate", path}, logger, protector, &console), os.ErrNotExist)
}

func TestConsoleReportsAnUnavailableOutputDescriptor(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "console")
	require.NoError(t, err)
	originalStdout := os.Stdout
	t.Cleanup(func() { os.Stdout = originalStdout })
	os.Stdout = file
	writer, closeConsole, err := evaluationConsole()
	require.NoError(t, err)
	defer closeConsole()
	require.Same(t, file, writer)
	require.NoError(t, file.Close())
	_, _, err = evaluationConsole()
	require.ErrorIs(t, err, os.ErrClosed)
	_, err = (evaluationPipe{file: file}).Write([]byte("record after exit"))
	require.Error(t, err)
}

func TestCLIRecoversOnlyTheCurrentHostedDataset(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	t.Chdir(root)
	parent := t.TempDir()
	for name, value := range map[string]string{"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted", "GITHUB_RUN_ID": "832", "GITHUB_RUN_ATTEMPT": "1", "RUNNER_TEMP": parent} {
		t.Setenv(name, value)
	}
	const fixture = "tests/eval/fixtures"
	registry, err := evalharness.ReadRegistry(filepath.Join(fixture, "datasets.json"))
	require.NoError(t, err)
	dataset, err := registry.Dataset("hotpotqa")
	require.NoError(t, err)
	digest, err := evalharness.FileHash(filepath.Join(fixture, "datasets.json"))
	require.NoError(t, err)
	dir := filepath.Join(parent, "dense-mem-eval-832-hotpotqa-off")
	manifest := evalharness.Manifest{
		Version: 1, RunID: "832-hotpotqa-off", WorkflowRunID: "832", Dataset: "hotpotqa", Mode: "fixtures", Operation: "run",
		Condition: "off", SourceRevision: strings.Repeat("a", 40), RegistryHash: digest,
		Image: "ghcr.io/markhuangai/dense-mem@sha256:" + strings.Repeat("b", 64), SeedImage: "sha256:" + strings.Repeat("c", 64),
		SeedDir: filepath.Join(dir, "seed"), DataDir: filepath.Join(dir, "data"), OutputDir: filepath.Join(dir, "public"),
		Model: "fixture", EmbeddingModel: "text-embedding-3-small", EmbeddingDimensions: 1536, ContextWindowTokens: 300_000,
		ModelDeadline: time.Now().Add(-time.Second),
	}
	private := filepath.Join(dir, "score-checkpoint")
	for _, name := range []string{manifest.DataDir, manifest.OutputDir, private} {
		require.NoError(t, os.MkdirAll(name, 0o700))
	}
	for _, name := range []string{dataset.Corpus.Path, dataset.Questions.Path} {
		bytes, err := os.ReadFile(filepath.Join(fixture, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(manifest.DataDir, name), bytes, 0o600))
	}
	corpus, questions, err := evalharness.LoadDataset(dataset, manifest.DataDir)
	require.NoError(t, err)
	writeFixture := func(path string, value any) {
		data, err := json.Marshal(value)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0o600))
	}
	header := evalharness.NewReport(manifest, len(corpus), len(questions))
	header.ImportedRows = len(corpus)
	header.Identity.CanonicalHash = strings.Repeat("d", 64)
	headerPath := filepath.Join(private, "verified-seed.json")
	writeFixture(headerPath, header)
	headerDigest, err := evalharness.FileHash(headerPath)
	require.NoError(t, err)
	score := evalharness.CaseScore{QuestionID: questions[0].ID, Condition: "off", Policy: "single", Completed: true, EM: 1, F1: 1}
	scoreBody, err := json.Marshal(score)
	require.NoError(t, err)
	writeFixture(filepath.Join(private, "0000-single.json"), map[string]any{
		"verified_seed_sha256": headerDigest, "question_ordinal": 0, "score": score, "score_sha256": hashBytes(scoreBody),
	})
	path := filepath.Join(dir, "manifest.json")
	writeFixture(path, manifest)
	foreign := filepath.Join(parent, "manifest.json")
	writeFixture(foreign, manifest)
	var output bytes.Buffer
	protector := evalharness.NewRunnerCredentialProtector()
	logger := observability.NewConsoleWithProtector(slog.LevelDebug, protector, &output)
	require.ErrorContains(t, execute(t.Context(), []string{"recover", foreign}, logger, protector, &output), "current hosted dataset directory")
	require.NoFileExists(t, filepath.Join(manifest.OutputDir, "scores.json"))
	require.NoError(t, execute(t.Context(), []string{"recover", path}, logger, protector, &output))
	require.Contains(t, output.String(), `"msg":"eval_scores_recovered"`)
	data, err := os.ReadFile(filepath.Join(manifest.OutputDir, "scores.json"))
	require.NoError(t, err)
	var recovered evalharness.Report
	require.NoError(t, json.Unmarshal(data, &recovered))
	require.Equal(t, 4, recovered.ExpectedAnswers)
	require.Equal(t, 1, recovered.CompletedAnswers)
	require.False(t, recovered.Complete)
	require.False(t, recovered.CaptureComplete)
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
		require.ErrorContains(t, execute(context.Background(), []string{mode, "manifest"}, logger, protector, io.Discard), "workflow-internal")
	}
	t.Setenv("GITHUB_ACTIONS", "")
	require.ErrorContains(t, execute(context.Background(), []string{"run", "manifest"}, logger, protector, io.Discard), "GitHub-hosted")
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

func TestRuntimeLogCollectorReportsAnUnavailableOutputSink(t *testing.T) {
	for _, line := range []string{"unstructured Docker warning", `{"level":"WARN","msg":"owned server stopped unexpectedly"}`} {
		read, write := io.Pipe()
		require.NoError(t, read.Close())
		capture := &evalharness.Capture{Logger: slog.New(slog.NewJSONHandler(write, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: observability.NewCredentialProtector()}
		require.ErrorContains(t, logReader(t.Context(), strings.NewReader(line+"\n"), capture, map[string]evalharness.UsageTotals{}), "benchmark console write failed")
		require.NoError(t, write.Close())
	}
}

const runtimeLogKeyCanary = "provider-log-key-canary-1739"
const runtimeLogURLCanary = "https://provider-log-canary.example/v1"

func TestRuntimeLogCollectorKeepsFirstProtectionFailureWhenTheSinkStops(t *testing.T) {
	read, write := io.Pipe()
	require.NoError(t, read.Close())
	defer write.Close()
	capture := &evalharness.Capture{Logger: slog.New(slog.NewJSONHandler(write, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: observability.NewCredentialProtector()}
	exchange := uuid.NewString()
	reason := observability.CredentialProtectionMatchingBudgetExceeded
	body, err := json.Marshal(map[string]any{"msg": "ai_exchange_payload", "exchange_id": exchange, "direction": "request",
		"payload_chunk": observability.DiagnosticBodyChunk{State: "unavailable", Reason: int(reason), ReasonCode: reason.Code(), OriginalBytes: 100}})
	require.NoError(t, err)
	logs := string(body) + "\n" + `{"level":"WARN","msg":"a later operator diagnostic"}` + "\n"
	err = logReader(t.Context(), strings.NewReader(logs), capture, map[string]evalharness.UsageTotals{})
	require.ErrorContains(t, err, "matching_work_exceeded")
	require.ErrorContains(t, err, "benchmark console write failed")
	failure := capture.FirstFailure()
	require.NotNil(t, failure)
	require.Equal(t, "server_log", failure.Source)
	require.Equal(t, "ai_exchange_payload", failure.Kind)
	require.Equal(t, exchange, failure.ExchangeID)
	require.Equal(t, 1, failure.Sequence)
	require.Equal(t, reason.Code(), failure.Reason)
}

func TestRuntimeLogCollectorReportsProtectedTransportFailure(t *testing.T) {
	const host = "provider-log-canary.example"
	const correlationID = "synthetic-correlation"
	const invocationID = "synthetic-invocation"
	const admittedText = "ordinary benchmark request"
	encodedKey := strings.ToUpper(hex.EncodeToString([]byte(runtimeLogKeyCanary)))
	failure := &net.DNSError{Err: "no such host " + runtimeLogKeyCanary + " " + encodedKey, Name: host}
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, failure }}
	var logs bytes.Buffer
	logger := observability.NewConsoleWithHandler(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := observability.DiagnosticHTTPClient(&http.Client{Transport: transport}, logger.ContextSlog(), observability.NewCredentialProtector(runtimeLogKeyCanary))
	ctx := requestctx.WithRememberInvocationIDSink(correlation.WithID(context.Background(), correlationID))
	requestctx.SetRememberInvocationID(ctx, invocationID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, runtimeLogURLCanary+"/chat/completions", strings.NewReader(`{"messages":"`+admittedText+`"}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+runtimeLogKeyCanary)
	_, err = client.Do(request)
	require.ErrorIs(t, err, failure)
	require.Contains(t, logs.String(), host, "the server-side error can contain the bare endpoint")

	for _, incomplete := range []bool{false, true} {
		name, prefix := "complete", ""
		if incomplete {
			name = "missing earlier exchange"
			prefix = `{"level":"DEBUG","msg":"earlier capture","capture_state":"unavailable"}` + "\n"
		}
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			capture := &evalharness.Capture{RunID: "collector", Logger: slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: evalharness.NewRunnerCredentialProtector(runtimeLogKeyCanary, runtimeLogURLCanary)}
			usage := map[string]evalharness.UsageTotals{}
			err := logReader(ctx, io.MultiReader(strings.NewReader(prefix), bytes.NewReader(logs.Bytes())), capture, usage)
			if incomplete {
				require.ErrorContains(t, err, "capture is incomplete")
				require.Empty(t, usage)
			} else {
				require.NoError(t, err)
				require.EqualValues(t, 1, usage["other"].Unavailable)
			}
			events := runtimeCaptureEvents(t, output.Bytes())
			require.Len(t, events, 3)
			var exchangeID string
			for index, direction := range []string{"request", "response", "transport_error"} {
				var payload runtimeWireFixture
				require.NoError(t, json.Unmarshal(events[index].Payload, &payload))
				require.Equal(t, direction, payload.Identity.Direction)
				require.Equal(t, correlationID, payload.Identity.CorrelationID)
				require.Equal(t, invocationID, payload.Identity.InvocationID)
				if index == 0 {
					exchangeID = payload.Identity.ExchangeID
					require.NotEmpty(t, exchangeID)
					require.Equal(t, `{"messages":"`+admittedText+`"}`, payload.Body)
				}
				require.Equal(t, exchangeID, payload.Identity.ExchangeID)
				if direction == "response" {
					require.Equal(t, "no_response", payload.Identity.TransportState)
					require.Empty(t, payload.Body)
				}
				if direction == "transport_error" {
					require.Equal(t, "runtime_transport_failure", events[index].Kind)
					require.Contains(t, payload.Body, "no such host")
				}
			}
			for _, forbidden := range []string{runtimeLogKeyCanary, encodedKey, runtimeLogURLCanary, host, "Authorization"} {
				require.NotContains(t, output.String(), forbidden)
			}
		})
	}
}

type runtimeWireFixture struct {
	Identity struct {
		ExchangeID     string `json:"exchange_id"`
		Direction      string `json:"direction"`
		InvocationID   string `json:"invocation_id"`
		CorrelationID  string `json:"correlation_id"`
		TransportState string `json:"transport_capture_state"`
	} `json:"identity"`
	Body string `json:"body"`
}

func runtimeCaptureEvents(t *testing.T, logs []byte) []evalharness.Event {
	t.Helper()
	var events []evalharness.Event
	var pending []observability.DiagnosticBodyChunk
	var sequence int
	for _, line := range bytes.Split(bytes.TrimSpace(logs), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record struct {
			Kind     string                             `json:"kind"`
			Sequence int                                `json:"sequence"`
			Payload  json.RawMessage                    `json:"payload"`
			Chunk    *observability.DiagnosticBodyChunk `json:"payload_chunk"`
		}
		require.NoError(t, json.Unmarshal(line, &record))
		if record.Chunk == nil {
			require.Empty(t, pending)
			events = append(events, evalharness.Event{Kind: record.Kind, Payload: record.Payload})
			continue
		}
		if len(pending) == 0 {
			sequence = record.Sequence
		}
		require.Equal(t, sequence, record.Sequence)
		require.Equal(t, len(pending), record.Chunk.Index)
		pending = append(pending, *record.Chunk)
		if len(pending) == pending[0].Count {
			var protected strings.Builder
			for _, chunk := range pending {
				require.Equal(t, "captured", chunk.State)
				require.Equal(t, pending[0].Count, chunk.Count)
				require.Equal(t, pending[0].SHA256, chunk.SHA256)
				protected.WriteString(chunk.Body)
			}
			require.Equal(t, pending[0].SHA256, hashBytes([]byte(protected.String())))
			var event evalharness.Event
			require.NoError(t, json.Unmarshal([]byte(protected.String()), &event))
			require.Equal(t, record.Kind, event.Kind)
			events = append(events, event)
			pending = nil
		}
	}
	require.Empty(t, pending)
	return events
}

func runtimeCapturePayloads(t *testing.T, logs []byte) string {
	t.Helper()
	var protected strings.Builder
	for _, event := range runtimeCaptureEvents(t, logs) {
		body, err := json.Marshal(event)
		require.NoError(t, err)
		protected.Write(body)
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
		{"ontology-previous", "ontology_evidence_organization_v3", nil},
		{"ontology", "ontology_evidence_organization_v4", nil},
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
				if item.name == "embedding" {
					payload.(map[string]any)["data"] = json.RawMessage("[" + strings.Repeat("0.00012345678,", 2800) + "0]")
				}
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
	sql := strings.Repeat("\n\t\tSELECT source_id FROM memory_sources WHERE team_id = $1 ", 64)
	line, err := json.Marshal(map[string]string{"level": "WARN", "msg": "slow postgres query", "sql": sql})
	require.NoError(t, err)
	logs := strings.Repeat(string(line)+"\n", 700) + runtimeProviderLogFixture(t)
	var output bytes.Buffer
	protector := evalharness.NewRunnerCredentialProtector(runtimeLogKeyCanary, runtimeLogURLCanary, "fixture-control-token-832",
		"https://fixture-embedding.example/v1", "fixture-embedding-secret-832", "fixture-db-password-832", "fixture-mcp-token-832")
	logger := observability.NewConsoleWithProtector(slog.LevelDebug, protector, &output)
	capture := evalharness.NewConsoleCapture(&output, logger.ContextSlog(), protector, "collector")
	usage := map[string]evalharness.UsageTotals{}
	require.NoError(t, logReader(context.Background(), strings.NewReader(logs), capture, usage))
	require.Len(t, usage, 5)
	require.EqualValues(t, 1, usage["intake_partition"].Available)
	require.EqualValues(t, 5, usage["intake_partition"].Total)
	require.EqualValues(t, 1, usage["intake_assessment"].Partial)
	require.EqualValues(t, 4, usage["intake_assessment"].Input)
	require.EqualValues(t, 2, usage["ontology"].Unavailable)
	require.EqualValues(t, 1, usage["embedding"].Available)
	require.EqualValues(t, 7, usage["embedding"].Total)
	require.EqualValues(t, 1, usage["other"].Partial)
	for phase, total := range usage {
		if phase == "ontology" {
			require.EqualValues(t, 2, total.Attempts)
		} else {
			require.EqualValues(t, 1, total.Attempts)
		}
	}
	for _, text := range []string{logs, output.String()} {
		require.NotContains(t, text, runtimeLogKeyCanary)
		require.NotContains(t, text, runtimeLogURLCanary)
	}
	protected := runtimeCapturePayloads(t, output.Bytes())
	require.NotContains(t, protected, runtimeLogKeyCanary)
	require.NotContains(t, protected, runtimeLogURLCanary)
	require.Contains(t, protected, "ordinary benchmark text")
	events := runtimeCaptureEvents(t, output.Bytes())
	require.Len(t, events, 712)
	for _, event := range events[:700] {
		require.Equal(t, "runtime_log", event.Kind)
		var payload map[string]string
		require.NoError(t, json.Unmarshal(event.Payload, &payload))
		require.Equal(t, string(line), payload["line"])
	}
	pairs := make(map[string]map[string]string)
	for _, event := range events[700:] {
		require.Equal(t, "runtime_wire_exchange", event.Kind)
		var payload runtimeWireFixture
		require.NoError(t, json.Unmarshal(event.Payload, &payload))
		if pairs[payload.Identity.ExchangeID] == nil {
			pairs[payload.Identity.ExchangeID] = make(map[string]string)
		}
		_, duplicate := pairs[payload.Identity.ExchangeID][payload.Identity.Direction]
		require.False(t, duplicate)
		pairs[payload.Identity.ExchangeID][payload.Identity.Direction] = payload.Body
	}
	require.Len(t, pairs, 6)
	require.Equal(t, `{"input":["Exact public Evidence."]}`, pairs["embedding"]["request"])
	require.Greater(t, len(pairs["embedding"]["response"]), 36_000)
	for id, pair := range pairs {
		require.Len(t, pair, 2, id)
		require.Contains(t, pair["response"], `"usage":`)
	}
	var request struct {
		PublicText string `json:"public_text"`
	}
	require.NoError(t, json.Unmarshal([]byte(pairs["partition"]["request"]), &request))
	require.Equal(t, strings.Repeat("Exact public Evidence. ", 1500), request.PublicText)
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
