package evalharness

import (
	"bytes"
	"context"
	"encoding/base64"
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
	var envelope struct{ Record string }
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &envelope))
	recordBytes, err := base64.StdEncoding.DecodeString(envelope.Record)
	require.NoError(t, err)
	var record captureRecord
	require.NoError(t, json.Unmarshal(recordBytes, &record))
	require.Equal(t, capture.RunID, record.RunID)
	require.Equal(t, "captured", record.Chunk.State)
	require.Equal(t, 1, record.Chunk.Count)
	require.Zero(t, record.Chunk.Index)
	var event Event
	require.NoError(t, json.Unmarshal([]byte(record.Chunk.Body), &event))
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
	read, write, err := os.Pipe()
	require.NoError(t, err)
	original := os.Stdout
	os.Stdout = write
	protector := NewRunnerCredentialProtector(secrets...)
	logger := observability.NewWithProtector(slog.LevelDebug, protector)
	capture := &Capture{Logger: logger.ContextSlog(), Protector: protector, RunID: "console-protection"}
	emitErr := capture.Emit("started", map[string]any{"admitted": "public benchmark text; password is a normal field name", "password": "ordinary public text", "operational": secrets, "encoded": []string{url.QueryEscape(secrets[0]), base64.StdEncoding.EncodeToString([]byte(secrets[0]))}})
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
	require.NotContains(t, string(body), base64.StdEncoding.EncodeToString([]byte(secrets[0])))
	var envelope struct {
		Record string `json:"record"`
	}
	require.NoError(t, json.Unmarshal(bytes.Split(body, []byte("\n"))[0], &envelope))
	recordBytes, err := base64.StdEncoding.DecodeString(envelope.Record)
	require.NoError(t, err)
	var record captureRecord
	require.NoError(t, json.Unmarshal(recordBytes, &record))
	require.Contains(t, record.Chunk.Body, "ordinary public text")
	for _, secret := range secrets {
		require.NotContains(t, record.Chunk.Body, secret)
	}
	require.NotContains(t, record.Chunk.Body, base64.StdEncoding.EncodeToString([]byte(secrets[0])))
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

func TestProtectedCaptureSurvivesActionsMaskingOfOrdinarySecretSettings(t *testing.T) {
	var output bytes.Buffer
	capture := captureFixture(&output)
	settings := map[string]any{"model": "gpt-6-luna", "context_window_tokens": 300000, "public_text": "Exact public fixture text."}
	require.NoError(t, capture.Emit("started", settings))
	require.NoError(t, capture.Emit("finished", map[string]any{"complete": true}))
	masked := strings.NewReplacer("gpt-6-luna", "***", "300000", "***").Replace(output.String())
	events := capturedEvents(t, []byte(masked))
	var restored map[string]any
	require.NoError(t, json.Unmarshal(events[0].Payload, &restored))
	require.Equal(t, "gpt-6-luna", restored["model"])
	require.Equal(t, float64(300000), restored["context_window_tokens"])
	require.Equal(t, settings["public_text"], restored["public_text"])
}

func capturedEvents(t *testing.T, logs []byte) []Event {
	t.Helper()
	var events []Event
	var payload strings.Builder
	for _, line := range bytes.Split(bytes.TrimSpace(logs), []byte("\n")) {
		var envelope struct{ Record string }
		require.NoError(t, json.Unmarshal(line, &envelope))
		if envelope.Record == "" {
			continue
		}
		body, err := base64.StdEncoding.DecodeString(envelope.Record)
		require.NoError(t, err)
		var record captureRecord
		require.NoError(t, json.Unmarshal(body, &record))
		if record.Chunk.Index == 0 {
			payload.Reset()
		}
		payload.WriteString(record.Chunk.Body)
		if record.Chunk.Index == record.Chunk.Count-1 {
			var event Event
			require.NoError(t, json.Unmarshal([]byte(payload.String()), &event))
			events = append(events, event)
		}
	}
	return events
}
