package evalharness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func captureFixture(buffer *bytes.Buffer) *Capture {
	return &Capture{Logger: observability.NewConsoleWithHandler(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug})).ContextSlog(), Protector: observability.NewCredentialProtector(), RunID: "synthetic-run"}
}

func TestCaptureEmitsReadableJSON(t *testing.T) {
	var output bytes.Buffer
	capture := captureFixture(&output)
	require.NoError(t, capture.Emit("import_attempt", map[string]any{"content": "Exact public Evidence.", "elapsed_ms": 123}))
	var record struct {
		RunID    string `json:"run_id"`
		Sequence int    `json:"sequence"`
		Kind     string `json:"kind"`
		Payload  struct {
			Content   string `json:"content"`
			ElapsedMS int    `json:"elapsed_ms"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &record))
	require.Equal(t, capture.RunID, record.RunID)
	require.Equal(t, 1, record.Sequence)
	require.Equal(t, "import_attempt", record.Kind)
	require.Equal(t, "Exact public Evidence.", record.Payload.Content)
	require.Equal(t, 123, record.Payload.ElapsedMS)
	require.NotContains(t, output.String(), `"record":`)
}

func TestProgressIsReadableAtInfoAndRejectsInvalidCounts(t *testing.T) {
	var output bytes.Buffer
	capture := captureFixture(&output)
	capture.importStart = time.Now().Add(-2 * time.Minute)
	require.NoError(t, capture.emitProgress(captureProgress{Stage: "import", Unit: "documents", Total: 10, Completed: 6, Failed: 1}))
	var line struct {
		Level   string `json:"level"`
		Message string `json:"msg"`
		Payload struct {
			captureProgress
			ElapsedMS          int64   `json:"elapsed_ms"`
			DocumentsPerMinute float64 `json:"documents_per_minute"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &line))
	require.Equal(t, "INFO", line.Level)
	require.Equal(t, "eval_progress", line.Message)
	require.Equal(t, 3, line.Payload.Remaining)
	require.InDelta(t, 3, line.Payload.DocumentsPerMinute, 0.01)
	require.GreaterOrEqual(t, line.Payload.ElapsedMS, int64(120000))
	for _, invalid := range []captureProgress{{Total: -1}, {Total: 1, Completed: -1}, {Total: 1, Failed: -1}, {Total: 1, Completed: 2}, {Total: 1, Completed: 1, Failed: 1}} {
		require.Error(t, capture.emitProgress(invalid))
	}
	require.Len(t, capturedProgress(t, output.Bytes()), 1)
}

func TestResumedImportRateCountsOnlyNewDocuments(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var output bytes.Buffer
		capture := captureFixture(&output)
		progress := captureProgress{Stage: "import", Unit: "documents", Total: 10, Completed: 8}
		require.NoError(t, capture.emitProgress(progress))
		time.Sleep(time.Minute)
		require.NoError(t, capture.emitProgress(progress))
		time.Sleep(time.Minute)
		progress.Completed = 10
		require.NoError(t, capture.emitProgress(progress))
		events := capturedEvents(t, output.Bytes())
		require.Len(t, events, 3)
		for i, expected := range []struct{ completed, remaining, rate int }{{8, 2, 0}, {8, 2, 0}, {10, 0, 1}} {
			var payload struct {
				captureProgress
				Rate float64 `json:"documents_per_minute"`
			}
			require.NoError(t, json.Unmarshal(events[i].Payload, &payload))
			require.Equal(t, expected.completed, payload.Completed)
			require.Equal(t, expected.remaining, payload.Remaining)
			require.Equal(t, float64(expected.rate), payload.Rate)
		}
	})
}

func TestCapturePreservesNestedProviderLogsAndProtectsCredentials(t *testing.T) {
	const secret = "nested-provider-key-canary-763"
	const admitted = `{"items":[{"text":"Atlas uses \"quoted\" Evidence."}]}`
	wire, err := json.Marshal(map[string]any{"messages": []map[string]string{{"role": "user", "content": admitted}}, "api_key": secret, "password": "ordinary public text"})
	require.NoError(t, err)
	line, err := json.Marshal(map[string]any{"msg": "ai_exchange_payload", "payload_chunk": map[string]string{"body": string(wire)}})
	require.NoError(t, err)
	var output bytes.Buffer
	capture := captureFixture(&output)
	capture.Protector = NewRunnerCredentialProtector(secret)
	require.NoError(t, capture.Emit("runtime_log", map[string]string{"line": string(line)}))
	events := capturedEvents(t, output.Bytes())
	require.Len(t, events, 1)
	event := events[0]
	require.Equal(t, "runtime_log", event.Kind)
	var retained struct{ Line string }
	require.NoError(t, json.Unmarshal(event.Payload, &retained))
	var provider struct {
		Chunk struct{ Body string } `json:"payload_chunk"`
	}
	require.NoError(t, json.Unmarshal([]byte(retained.Line), &provider))
	var request struct {
		Messages []struct{ Content string }
		APIKey   string `json:"api_key"`
		Password string
	}
	require.NoError(t, json.Unmarshal([]byte(provider.Chunk.Body), &request))
	require.Len(t, request.Messages, 1)
	require.Equal(t, admitted, request.Messages[0].Content)
	require.Equal(t, "ordinary public text", request.Password)
	require.Equal(t, observability.CredentialProtectionRedacted, request.APIKey)
	require.NotContains(t, output.String(), secret)
	require.NotContains(t, string(event.Payload), secret)
}

func TestActualConsoleCaptureProtectsCredentialsAndPreservesAdmittedText(t *testing.T) {
	secrets := []string{"provider-key-canary-7d2ac", "https://provider-url-canary.example/v1", "database-password-canary-981", "control-token-canary-762", "postgres://user:password-canary@database/private"}
	var encoded []string
	for _, secret := range secrets {
		encoded = append(encoded, url.QueryEscape(secret), base64.StdEncoding.EncodeToString([]byte(secret)), hex.EncodeToString([]byte(secret)), strings.ToUpper(hex.EncodeToString([]byte(secret))))
	}
	read, write, err := os.Pipe()
	require.NoError(t, err)
	original := os.Stdout
	os.Stdout = write
	protector := NewRunnerCredentialProtector(secrets...)
	logger := observability.NewWithProtector(slog.LevelDebug, protector)
	capture := NewConsoleCapture(write, logger.ContextSlog(), protector, "console-protection")
	emitErr := capture.Emit("started", map[string]any{"admitted": "public benchmark text; password is a normal field name", "password": "ordinary public text", "public_hex": "deadbeef", "operational": secrets, "encoded": encoded})
	logger.ErrorContext(context.Background(), "dependency_failed", &HTTPStatusError{StatusCode: 503})
	require.NoError(t, write.Close())
	os.Stdout = original
	body, err := io.ReadAll(read)
	require.NoError(t, err)
	require.NoError(t, read.Close())
	require.NoError(t, emitErr)
	for _, secret := range secrets {
		require.NotContains(t, string(body), secret)
	}
	for _, value := range encoded {
		require.NotContains(t, string(body), value)
	}
	events := capturedEvents(t, body)
	require.Len(t, events, 1)
	require.Contains(t, string(events[0].Payload), "ordinary public text")
	require.Contains(t, string(events[0].Payload), "deadbeef")
	for _, secret := range secrets {
		require.NotContains(t, string(events[0].Payload), secret)
	}
	for _, value := range encoded {
		require.NotContains(t, string(events[0].Payload), value)
	}
	report := NewReport(Manifest{Model: "selected-model", EmbeddingModel: "text-embedding-3-small"}, 3, 2)
	data, err := json.Marshal(report)
	require.NoError(t, err)
	for _, secret := range secrets {
		require.NotContains(t, string(data), secret)
	}
	require.NotContains(t, string(data), "provider_url")
	require.Contains(t, string(data), "selected-model")
	require.Contains(t, string(data), "text-embedding-3-small")
}

const runnerCaptureCanary = "answer-provider-credential-canary-832"

func newRunnerCaptureFixture(writer io.Writer, runID string) *Capture {
	protector := NewRunnerCredentialProtector(runnerCaptureCanary, "embedding-canary-key", "https://answer-canary.example/v1",
		"https://embedding-canary.example/v1", "https://answer-canary.example/v1", "https://embedding-canary.example/v1",
		"mcp-credential-canary", "control-credential-canary", "database-credential-canary",
		"postgres://densemem:database-credential-canary@postgres/densemem")
	logger := observability.NewConsoleWithProtector(slog.LevelDebug, protector, writer)
	return NewConsoleCapture(writer, logger.ContextSlog(), protector, runID)
}

func largeAnswerRequest() string {
	admitted := strings.Repeat(`{\"question\":\"A public question?\",\"content\":\"Public text.\\nPreserve password and token fields.\"}`, 2430)
	return `{"messages":[{"role":"user","content":"` + admitted + `"}]}`
}

func TestActualConsoleCaptureReconstructsLargeAnswerAndQueuedCredentials(t *testing.T) {
	var output bytes.Buffer
	capture := newRunnerCaptureFixture(&output, "runner-832")
	ctx, cancel := context.WithCancel(observability.WithAuthenticationSecrets(t.Context(), "presented-credential-canary-832"))
	defer cancel()
	capture.startQueue(cancel)
	request := largeAnswerRequest()
	require.Greater(t, len(request), 240_000)
	require.NoError(t, capture.EmitContext(ctx, "answer_wire_request", map[string]string{
		"body": request, "authorization": runnerCaptureCanary, "presented": "presented-credential-canary-832",
	}))
	require.NoError(t, capture.finishQueue(ctx))
	events := capturedEvents(t, output.Bytes())
	require.Len(t, events, 1)
	require.Equal(t, "answer_wire_request", events[0].Kind)
	var result map[string]string
	require.NoError(t, json.Unmarshal(events[0].Payload, &result))
	require.Equal(t, request, result["body"])
	require.Equal(t, observability.CredentialProtectionRedacted, result["authorization"])
	require.Equal(t, observability.CredentialProtectionRedacted, result["presented"])
	require.NotContains(t, output.String(), runnerCaptureCanary)
	require.NotContains(t, output.String(), "presented-credential-canary-832")
}

func TestActualConsoleCaptureFailsClosedForMetadataAndSink(t *testing.T) {
	var output bytes.Buffer
	capture := newRunnerCaptureFixture(&output, runnerCaptureCanary)
	require.ErrorContains(t, capture.Emit("started", "public"), "payload protection unavailable")
	require.NotContains(t, output.String(), runnerCaptureCanary)
	require.Contains(t, output.String(), "eval_capture_unavailable")
	require.Equal(t, "formatting_failed", capture.FirstFailure().Reason)
	capture = newRunnerCaptureFixture(failedCaptureWriter{}, "runner-832")
	require.ErrorContains(t, capture.Emit("started", "public"), "console write failed")
	require.Equal(t, "console_write_failed", capture.FirstFailure().Reason)
}

func BenchmarkRunnerCapture(b *testing.B) {
	capture := newRunnerCaptureFixture(io.Discard, "runner-832")
	body := largeAnswerRequest()
	payload := map[string]string{"body": body, "authorization": runnerCaptureCanary}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if err := capture.Emit("answer_wire_request", payload); err != nil {
			b.Fatal(err)
		}
	}
}

func TestRunnerCaptureRejectsOversizedEncodedCredentials(t *testing.T) {
	secret := strings.Repeat("k", observability.MaxCredentialSecretBytes/2+1)
	var output bytes.Buffer
	capture := captureFixture(&output)
	capture.Protector = NewRunnerCredentialProtector(secret)
	require.ErrorContains(t, capture.Emit("runtime_log", map[string]string{"line": secret}), "protection unavailable")
	require.Contains(t, output.String(), "secret_too_long")
	require.NotContains(t, output.String(), secret)
	require.NotContains(t, output.String(), hex.EncodeToString([]byte(secret)))
}

func TestRawCapturePreservesPublicSettings(t *testing.T) {
	var output bytes.Buffer
	capture := captureFixture(&output)
	settings := map[string]any{"model": "selected-model", "context_window_tokens": 500000, "public_text": "Exact public fixture text."}
	require.NoError(t, capture.Emit("started", settings))
	require.NoError(t, capture.Emit("finished", map[string]any{"complete": true}))
	events := capturedEvents(t, output.Bytes())
	var restored map[string]any
	require.NoError(t, json.Unmarshal(events[0].Payload, &restored))
	require.Equal(t, "selected-model", restored["model"])
	require.Equal(t, float64(500000), restored["context_window_tokens"])
	require.Equal(t, settings["public_text"], restored["public_text"])
}

func TestRawCaptureChunksLargeEventsWithoutLosingPublicText(t *testing.T) {
	var output bytes.Buffer
	capture := captureFixture(&output)
	const secret = "chunk-boundary-secret-canary"
	capture.Protector = NewRunnerCredentialProtector(secret)
	text := strings.Repeat("Exact quoted \"Evidence\" 第二。 ", 1000)
	require.NoError(t, capture.Emit("runtime_log", map[string]string{"public_text": text, "operational": secret}))
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	require.Greater(t, len(lines), 1)
	for _, line := range lines {
		require.Less(t, len(line), observability.MaxOperationMetadataBytes)
		require.NotContains(t, string(line), `"record":`)
		require.NotContains(t, string(line), secret)
	}
	events := capturedEvents(t, output.Bytes())
	require.Len(t, events, 1)
	var value map[string]string
	require.NoError(t, json.Unmarshal(events[0].Payload, &value))
	require.Equal(t, text, value["public_text"])
	require.Equal(t, observability.CredentialProtectionRedacted, value["operational"])
}

func capturedEvents(t *testing.T, logs []byte) []Event {
	t.Helper()
	var events []Event
	var payload strings.Builder
	pending := false
	for _, line := range bytes.Split(bytes.TrimSpace(logs), []byte("\n")) {
		var envelope struct {
			Kind    string                             `json:"kind"`
			Payload json.RawMessage                    `json:"payload"`
			Chunk   *observability.DiagnosticBodyChunk `json:"payload_chunk"`
		}
		require.NoError(t, json.Unmarshal(line, &envelope))
		if envelope.Kind == "" {
			continue
		}
		if envelope.Chunk == nil {
			require.NotEmpty(t, envelope.Payload)
			events = append(events, Event{Kind: envelope.Kind, Payload: envelope.Payload})
			continue
		}
		require.Equal(t, "captured", envelope.Chunk.State)
		if envelope.Chunk.Index == 0 {
			payload.Reset()
			pending = true
		}
		payload.WriteString(envelope.Chunk.Body)
		if envelope.Chunk.Index == envelope.Chunk.Count-1 {
			sum := sha256.Sum256([]byte(payload.String()))
			require.Equal(t, envelope.Chunk.SHA256, hex.EncodeToString(sum[:]))
			var event Event
			require.NoError(t, json.Unmarshal([]byte(payload.String()), &event))
			require.Equal(t, envelope.Kind, event.Kind)
			events = append(events, event)
			pending = false
		}
	}
	require.False(t, pending, "capture ended inside a chunked event")
	return events
}

func capturedProgress(t *testing.T, logs []byte) []captureProgress {
	t.Helper()
	var updates []captureProgress
	for _, event := range capturedEvents(t, logs) {
		if event.Kind == "progress" {
			var update captureProgress
			require.NoError(t, json.Unmarshal(event.Payload, &update))
			updates = append(updates, update)
		}
	}
	return updates
}

func TestQueuedCaptureReconstructsScoringAndRetainsImmutableInputs(t *testing.T) {
	var output bytes.Buffer
	capture := captureFixture(&output)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	capture.startQueue(cancel)
	question, observation, corpus, mapping, canonical := scoringFixture()
	expected, err := ScoreObservation(question, observation, corpus, mapping, canonical)
	require.NoError(t, err)
	require.NoError(t, capture.EmitContext(ctx, "initial_recall", map[string]any{"question_id": question.ID, "condition": observation.Condition, "call": observation.Initial}))
	require.NoError(t, capture.EmitContext(ctx, "observation", observationReceipt(observation)))
	observation.Hosts[0].Answer = "changed after queueing"
	require.NoError(t, capture.finishQueue(ctx))
	events := capturedEvents(t, output.Bytes())
	require.Len(t, events, 2)
	var initial struct {
		Call MemoryCall `json:"call"`
	}
	require.NoError(t, json.Unmarshal(events[0].Payload, &initial))
	var restored Observation
	require.NoError(t, json.Unmarshal(events[1].Payload, &restored))
	restored.Initial = initial.Call
	for i := range restored.Hosts {
		restored.Hosts[i].Calls = append([]MemoryCall{initial.Call}, restored.Hosts[i].Calls...)
	}
	measured, err := ScoreObservation(question, restored, corpus, mapping, canonical)
	require.NoError(t, err)
	require.Equal(t, expected, measured)
	require.NotContains(t, string(events[1].Payload), "changed after queueing")
	require.NotContains(t, string(events[1].Payload), "\"initial\"")
}

type failedCaptureWriter struct{}

func (failedCaptureWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestQueuedCapturePropagatesWriteFailure(t *testing.T) {
	capture := &Capture{Logger: slog.New(slog.NewJSONHandler(failedCaptureWriter{}, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: observability.NewCredentialProtector()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	capture.startQueue(cancel)
	require.NoError(t, capture.EmitContext(ctx, "observation", "public"))
	<-ctx.Done()
	require.ErrorContains(t, capture.finishQueue(context.Background()), "did not drain")
	require.True(t, capture.incomplete())
	failure := capture.firstFailure.Load()
	require.NotNil(t, failure)
	require.Equal(t, "observation", failure.Kind)
	require.Equal(t, 1, failure.Sequence)
	require.Equal(t, "console_write_failed", failure.Reason)
}

func TestQueuedCaptureRetainsProtectionBudgetFailure(t *testing.T) {
	var output bytes.Buffer
	const canary = "capture-credential-canary"
	capture := captureFixture(&output)
	capture.Protector = observability.NewCredentialProtector(canary, strings.Repeat("a", 3000)+"b")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	capture.startQueue(cancel)
	capture.setPhase("ontology off")
	require.NoError(t, capture.EmitContext(ctx, "answer_wire_request", map[string]string{
		"content": strings.Repeat("%61", 3000), "credential": canary,
	}))
	require.ErrorContains(t, capture.finishQueue(t.Context()), "did not drain")
	failure := capture.firstFailure.Load()
	require.NotNil(t, failure)
	require.Equal(t, "ontology off", failure.Phase)
	require.Equal(t, "answer_wire_request", failure.Kind)
	require.Equal(t, 1, failure.Sequence)
	require.Equal(t, "matching_work_exceeded", failure.Reason)
	require.Contains(t, failure.Protection, "matching_work=")
	require.Contains(t, output.String(), `"msg":"eval_capture_unavailable"`)
	require.Contains(t, output.String(), `"reason":"matching_work_exceeded"`)
	require.NotContains(t, output.String(), canary)
}

func TestQueuedCapturePreservesTerminalExchangesAfterWorkCancellation(t *testing.T) {
	var output bytes.Buffer
	capture := captureFixture(&output)
	ctx, cancel := context.WithCancel(t.Context())
	capture.startQueue(cancel)
	cancel()
	require.NoError(t, capture.EmitTerminal(ctx, "answer_wire_unavailable", map[string]string{"exchange_id": "interrupted-answer"}))
	progress := captureProgress{Stage: "questions", Unit: "answers", Total: 4, Completed: 2}
	require.NoError(t, capture.emitProgressContext(ctx, progress, true))
	require.NoError(t, capture.finishQueue(t.Context()))
	defer capture.queue.cancelPreserve()
	events := capturedEvents(t, output.Bytes())
	require.Len(t, events, 2)
	require.Equal(t, "answer_wire_unavailable", events[0].Kind)
	require.JSONEq(t, `{"exchange_id":"interrupted-answer"}`, string(events[0].Payload))
	progress.Remaining = 2
	require.Equal(t, []captureProgress{progress}, capturedProgress(t, output.Bytes()))
}

func TestQueuedCaptureReportsUnserializableRecords(t *testing.T) {
	var output bytes.Buffer
	capture := captureFixture(&output)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	capture.startQueue(cancel)
	require.ErrorContains(t, capture.EmitContext(ctx, "observation", make(chan string)), "unsupported type")
	require.ErrorContains(t, capture.finishQueue(ctx), "did not drain")
	require.True(t, capture.incomplete())
}

func TestQueuedCaptureReportsAnOversizedRecordWithoutEmittingIt(t *testing.T) {
	var output bytes.Buffer
	capture := captureFixture(&output)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	capture.startQueue(cancel)
	require.ErrorContains(t, capture.EmitContext(ctx, "observation", strings.Repeat("x", captureLimit)), "payload exceeds limit")
	require.ErrorContains(t, capture.finishQueue(ctx), "did not drain")
	require.Empty(t, output.String())
}

type heldCaptureWriter struct {
	started chan struct{}
	release chan struct{}
	output  *bytes.Buffer
}

func (w heldCaptureWriter) Write(data []byte) (int, error) {
	select {
	case <-w.started:
	default:
		close(w.started)
	}
	<-w.release
	return w.output.Write(data)
}

func TestQueuedCaptureBoundsBlockedWriter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var output bytes.Buffer
		sink := heldCaptureWriter{started: make(chan struct{}), release: make(chan struct{}), output: &output}
		release := sync.OnceFunc(func() { close(sink.release) })
		defer release()
		capture := &Capture{Logger: slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: observability.NewCredentialProtector()}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		capture.startQueue(cancel)
		require.NoError(t, capture.EmitContext(ctx, "observation", "public"))
		<-sink.started
		for range cap(capture.queue.events) {
			require.NoError(t, capture.EmitContext(ctx, "observation", "waiting on the sink"))
		}
		require.ErrorContains(t, capture.EmitContext(ctx, "observation", "beyond queue capacity"), "queue unavailable")
		require.ErrorContains(t, capture.finishQueue(ctx), "did not drain")
		require.True(t, capture.incomplete())
		failure := capture.firstFailure.Load()
		require.NotNil(t, failure)
		require.Equal(t, "enqueue_interrupted", failure.Reason)
		release()
		<-capture.queue.done
		require.Len(t, capturedEvents(t, output.Bytes()), 1)
	})
}
