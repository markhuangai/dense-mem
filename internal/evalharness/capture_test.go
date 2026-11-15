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
		for i := range observation.Hosts {
			observation.Hosts[i].ElapsedMS = float64((i + 1) * 10)
			if condition == "on" {
				observation.Hosts[i].ElapsedMS *= 2
			}
		}
		require.NoError(t, capture.Emit("observation", observation))
	}
	finished := completion{ImportedRows: len(corpus), ElapsedMS: 100000, CanonicalHash: "fixed-canonical", NativeMeasurements: []NativeObservation{{CanonicalHash: "fixed-canonical"}, {CanonicalHash: "fixed-canonical"}, {CanonicalHash: "fixed-canonical"}}}
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
	for i, expected := range []float64{100, 50, 50, 25} {
		require.NotNil(t, report.Matrix[i].ThroughputPerSecond)
		require.Equal(t, expected, *report.Matrix[i].ThroughputPerSecond)
	}
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

func TestReplayReportRejectsChangedOrIncompleteSemanticEvidence(t *testing.T) {
	q, observation, corpus, mapping, canonical := scoringFixture()
	manifest := Manifest{RunID: "synthetic-run", Dataset: "synthetic", Model: "fixture", SourceRevision: strings.Repeat("a", 40), RegistryHash: strings.Repeat("b", 64), Image: "immutable-image"}
	rows := []CorpusItem{}
	event := func(kind string, payload any) Event {
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		return Event{Kind: kind, Payload: body}
	}
	events := []Event{event("started", manifest)}
	for source, parts := range mapping.Partitions {
		rows = append(rows, corpus[source])
		events = append(events, event("mapping", map[string]any{"source_id": source, "partitions": parts}))
	}
	canonicalIndex := len(events)
	for id, evidence := range canonical {
		events = append(events, event("canonical", map[string]any{"evidence_id": id, "evidence": evidence}))
	}
	observationIndex := len(events)
	for _, condition := range []string{"off", "on"} {
		observation.Condition = condition
		events = append(events, event("observation", observation))
	}
	finished := completion{ImportedRows: len(rows), CanonicalHash: "fixed", NativeMeasurements: []NativeObservation{{CanonicalHash: "fixed"}, {CanonicalHash: "fixed"}, {CanonicalHash: "fixed"}}}
	events = append(events, event("finished", finished))
	for _, item := range []struct {
		name, kind string
		index      int
	}{
		{"manifest", "started", 0},
		{"mapping", "mapping", 1},
		{"canonical", "canonical", canonicalIndex},
		{"observation", "observation", observationIndex},
		{"completion", "finished", len(events) - 1},
	} {
		t.Run("malformed "+item.name, func(t *testing.T) {
			changed := append([]Event(nil), events...)
			changed[item.index] = Event{Kind: item.kind, Payload: json.RawMessage(`{`)}
			_, err := ReplayReport(changed, manifest, rows, []Question{q})
			require.Error(t, err)
		})
	}
	for _, item := range []struct {
		name, failure string
		index         int
		late          bool
	}{
		{"duplicate mapping", "duplicate replay mapping", 1, false},
		{"duplicate canonical", "duplicate replay canonical", canonicalIndex, false},
		{"late mapping", "mappings changed after observation", 1, true},
		{"late canonical", "canonical Evidence changed after observation", canonicalIndex, true},
	} {
		t.Run(item.name, func(t *testing.T) {
			index := item.index
			if item.late {
				index = len(events) - 1
			}
			changed := append([]Event(nil), events[:index]...)
			changed = append(changed, events[item.index])
			changed = append(changed, events[index:]...)
			_, err := ReplayReport(changed, manifest, rows, []Question{q})
			require.ErrorContains(t, err, item.failure)
		})
	}
	changed := append([]Event(nil), events...)
	observation.QuestionID = "unknown"
	changed[observationIndex] = event("observation", observation)
	_, err := ReplayReport(changed, manifest, rows, []Question{q})
	require.ErrorContains(t, err, "unknown question")
	_, err = ReplayReport(nil, manifest, rows, []Question{q})
	require.ErrorContains(t, err, "no complete capture")
	for _, count := range []int{2, 3} {
		changed = append([]Event(nil), events...)
		terminal := finished
		terminal.NativeMeasurements = append([]NativeObservation(nil), finished.NativeMeasurements[:count]...)
		if count == 3 {
			terminal.NativeMeasurements[2].CanonicalHash = "changed"
		}
		changed[len(changed)-1] = event("finished", terminal)
		_, err = ReplayReport(changed, manifest, rows, []Question{q})
		require.Error(t, err, "preservation evidence must be complete and unchanged")
	}
	partial := finished
	partial.Failure, partial.ImportedRows, partial.NativeMeasurements = "import incomplete", 0, nil
	report, err := ReplayReport([]Event{events[0], event("finished", partial)}, manifest, rows, []Question{q})
	require.NoError(t, err)
	require.False(t, report.Complete)
	require.Equal(t, 4, report.ExpectedAnswers)
	require.Zero(t, report.CompletedAnswers)
	require.Equal(t, "import incomplete", report.Failure)
}
