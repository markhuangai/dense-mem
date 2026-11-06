package serverapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/observability"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
)

func captureOntologyPublicationConsole(t *testing.T, run func()) (string, []map[string]any) {
	t.Helper()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	previous := os.Stdout
	os.Stdout = writer
	done := make(chan []byte, 1)
	go func() { body, _ := io.ReadAll(reader); done <- body }()
	defer func() { os.Stdout = previous; _ = writer.Close(); _ = reader.Close() }()
	run()
	require.NoError(t, writer.Close())
	os.Stdout = previous
	body := <-done
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record), line)
		records = append(records, record)
	}
	return string(body), records
}

func ontologyPublicationPayloads(t *testing.T, records []map[string]any) map[string][]byte {
	t.Helper()
	bodies := map[string][]byte{}
	counts := map[string]int{}
	var manifest map[string]any
	chunkCount := 0
	for _, record := range records {
		if record["msg"] != "ontology_semantic_publication" {
			continue
		}
		value, ok := record["payload_chunk"]
		if !ok {
			manifest = record
			continue
		}
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		var chunk observability.DiagnosticBodyChunk
		require.NoError(t, json.Unmarshal(encoded, &chunk))
		require.Equal(t, "captured", chunk.State)
		require.LessOrEqual(t, len([]byte(chunk.Body)), 2048)
		key := record["payload_kind"].(string) + ":" + record["record_id"].(string)
		require.Equal(t, counts[key], chunk.Index)
		counts[key]++
		chunkCount++
		bodies[key] = append(bodies[key], []byte(chunk.Body)...)
		if chunk.Index == chunk.Count-1 {
			require.Equal(t, chunk.Count, counts[key])
			digest := sha256.Sum256(bodies[key])
			require.Equal(t, hex.EncodeToString(digest[:]), chunk.SHA256)
		}
	}
	require.NotNil(t, manifest)
	require.Equal(t, "captured", manifest["capture_state"])
	require.EqualValues(t, len(bodies), manifest["payload_count"])
	require.EqualValues(t, chunkCount, manifest["chunk_count"])
	return bodies
}

func TestOntologySemanticPublicationRealLoggerLevelsAndProtection(t *testing.T) {
	for _, level := range []slog.Level{observability.LevelTrace, slog.LevelDebug, slog.LevelInfo} {
		t.Run(level.String(), func(t *testing.T) {
			const secret = "configured-publication-credential"
			const authentication = "authenticated-publication-credential"
			team, profile := uuid.New(), uuid.New()
			ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: team, OwnerID: profile})
			ctx = correlation.WithID(ctx, "semantic-protected-comparison")
			ctx = observability.WithAuthenticationSecrets(ctx, authentication)
			input := map[string]string{"token": "exact admitted token", "password": "exact admitted password", "secret": "exact admitted secret",
				"description": strings.Repeat("exact admitted fields ", 400) + secret + "/" + authentication + "/configured%2Dpublication%2Dcredential"}
			sink := &ontologyDiagnosticTestSink{}
			console, records := captureOntologyPublicationConsole(t, func() {
				protector := observability.NewCredentialProtector(secret)
				root := observability.NewWithProtector(level, protector)
				require.NoError(t, root.AttachSink(sink))
				root.ContextSlog().LogAttrs(ctx, observability.LevelTrace, "semantic-trace-filter")
				ctx, observation, finish := contract.ObservePublication(ctx, ontologyPublicationDiagnostics(root.ContextSlog(), protector), team.String(), "manager", input)
				_ = ctx
				observation.Rejection = &contract.PublicationRejection{Cause: contract.ErrInvalid, Reason: "hierarchy_cycle", RecordID: uuid.NewString()}
				finish(observation.Rejection)
			})
			require.NotContains(t, console, secret)
			require.NotContains(t, console, authentication)
			require.NotContains(t, console, "configured%2Dpublication%2Dcredential")
			for _, record := range records {
				require.Equal(t, team.String(), record["team_id"])
				require.Equal(t, profile.String(), record["profile_id"])
				require.Equal(t, "semantic-protected-comparison", record["correlation_id"])
			}
			for _, record := range sink.records {
				require.NotContains(t, record.Attrs, "payload_chunk", "complete bodies must remain stdout-only")
				require.Equal(t, team.String(), record.TeamID)
				require.Equal(t, profile.String(), record.ProfileID)
				encoded, err := json.Marshal(record.Attrs)
				require.NoError(t, err)
				require.LessOrEqual(t, len(encoded), observability.MaxOperationMetadataBytes)
			}
			require.Equal(t, level == observability.LevelTrace, strings.Contains(console, "semantic-trace-filter"))
			if level <= slog.LevelDebug {
				bodies := ontologyPublicationPayloads(t, records)
				var actual map[string]string
				require.NoError(t, json.Unmarshal(bodies["input:"], &actual))
				require.Equal(t, input["token"], actual["token"])
				require.Equal(t, input["password"], actual["password"])
				require.Equal(t, input["secret"], actual["secret"])
				require.Contains(t, actual["description"], observability.CredentialProtectionRedacted)
			} else {
				require.NotContains(t, console, "ontology_semantic_publication\"")
				require.NotContains(t, console, "exact admitted")
			}
			require.Contains(t, console, "ontology_semantic_publication_rejected")
			require.Contains(t, console, "not_committed")
		})
	}
}

func TestOntologySemanticPublicationExhaustionIsExplicit(t *testing.T) {
	console, _ := captureOntologyPublicationConsole(t, func() {
		protector := observability.NewCredentialProtector()
		root := observability.NewWithProtector(slog.LevelDebug, protector)
		_, _, finish := contract.ObservePublication(context.Background(), ontologyPublicationDiagnostics(root.ContextSlog(), protector), uuid.NewString(), "manager", strings.Repeat("x", contract.MaxPublicationBytes+1))
		finish(contract.ErrInvalid)
	})
	require.Contains(t, console, "publication_bound")
	require.Contains(t, console, "ontology_semantic_publication_capture_incomplete")
	require.NotContains(t, console, strings.Repeat("x", 100))
}
