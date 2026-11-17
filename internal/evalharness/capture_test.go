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
	"testing"

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
	require.NoError(t, capture.emitProgress(captureProgress{Stage: "import", Unit: "documents", Total: 10, Completed: 6, Failed: 1}))
	var line struct {
		Level   string          `json:"level"`
		Message string          `json:"msg"`
		Payload captureProgress `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &line))
	require.Equal(t, "INFO", line.Level)
	require.Equal(t, "eval_progress", line.Message)
	require.Equal(t, 3, line.Payload.Remaining)
	for _, invalid := range []captureProgress{{Total: -1}, {Total: 1, Completed: -1}, {Total: 1, Failed: -1}, {Total: 1, Completed: 2}, {Total: 1, Completed: 1, Failed: 1}} {
		require.Error(t, capture.emitProgress(invalid))
	}
	require.Len(t, capturedProgress(t, output.Bytes()), 1)
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
	capture := &Capture{Logger: logger.ContextSlog(), Protector: protector, RunID: "console-protection"}
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
