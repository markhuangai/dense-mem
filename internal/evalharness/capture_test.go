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
	report := NewReport(Manifest{Model: "selected-model", EmbeddingModel: "@cf/baai/bge-m3"}, 3, 2)
	data, err := json.Marshal(report)
	require.NoError(t, err)
	for _, secret := range secrets {
		require.NotContains(t, string(data), secret)
	}
	require.NotContains(t, string(data), "provider_url")
	require.Contains(t, string(data), "selected-model")
}

func TestRuntimeFreeReplayRecomputesNumericScoresAndRejectsIncompleteLogs(t *testing.T) {
	q, observation, corpus, mapping, canonical := scoringFixture()
	manifest := Manifest{RunID: "synthetic-run", Dataset: "synthetic", Model: "fixture", SourceRevision: strings.Repeat("a", 40), RegistryHash: strings.Repeat("b", 64), Image: "immutable-image"}
	var log bytes.Buffer
	capture := captureFixture(&log)
	require.NoError(t, capture.Emit("started", manifest))
	for source, parts := range mapping.Partitions {
		require.NoError(t, capture.Emit("mapping", map[string]any{"source_id": source, "partitions": parts}))
	}
	for id, evidence := range canonical {
		require.NoError(t, capture.Emit("canonical", map[string]any{"evidence_id": id, "evidence": evidence}))
	}
	for _, condition := range []string{"off", "on"} {
		observation.Condition = condition
		require.NoError(t, capture.Emit("observation", observation))
	}
	finished := completion{ImportedRows: len(corpus), CanonicalHash: "fixed-canonical", NativeMeasurements: []NativeObservation{{CanonicalHash: "fixed-canonical"}, {CanonicalHash: "fixed-canonical"}, {CanonicalHash: "fixed-canonical"}}}
	require.NoError(t, capture.Emit("finished", finished))
	events, err := ReplayEvents([]io.Reader{strings.NewReader(log.String())}, manifest.RunID)
	require.NoError(t, err)
	rows := []CorpusItem{}
	for _, item := range corpus {
		rows = append(rows, item)
	}
	report, err := ReplayReport(events, manifest, rows, []Question{q})
	require.NoError(t, err)
	require.True(t, report.Complete)
	require.Equal(t, 4, report.CompletedAnswers)
	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	for _, broken := range []string{strings.Join(lines[:len(lines)-1], "\n"), strings.Join(append(lines, lines[0]), "\n"), strings.Replace(log.String(), `"record":"`, `"record":"A`, 1), ""} {
		_, err := ReplayEvents([]io.Reader{strings.NewReader(broken)}, manifest.RunID)
		require.Error(t, err)
	}
	wrong := manifest
	wrong.SourceRevision = strings.Repeat("c", 40)
	_, err = ReplayReport(events, wrong, rows, []Question{q})
	require.ErrorContains(t, err, "identity")
}

func TestProtectedCaptureSurvivesActionsMaskingOfOrdinarySecretSettings(t *testing.T) {
	var output bytes.Buffer
	capture := captureFixture(&output)
	settings := map[string]any{"model": "gpt-6-luna", "context_window_tokens": 300000, "public_text": "Exact public fixture text."}
	require.NoError(t, capture.Emit("started", settings))
	require.NoError(t, capture.Emit("finished", map[string]any{"complete": true}))
	masked := strings.NewReplacer("gpt-6-luna", "***", "300000", "***").Replace(output.String())
	events, err := ReplayEvents([]io.Reader{strings.NewReader(masked)}, capture.RunID)
	require.NoError(t, err)
	var restored map[string]any
	require.NoError(t, json.Unmarshal(events[0].Payload, &restored))
	require.Equal(t, "gpt-6-luna", restored["model"])
	require.Equal(t, float64(300000), restored["context_window_tokens"])
	require.Equal(t, settings["public_text"], restored["public_text"])
}
