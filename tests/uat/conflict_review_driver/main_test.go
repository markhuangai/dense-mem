package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func TestDriverDiagnosticsPreservePrivateProtectedHTTPExchange(t *testing.T) {
	const secret = "configured-driver-credential"
	const requestBody = `{"secret":"admitted exact fact"}`
	responseBody := `{"error":{"code":"provider_conflict","message":"` + secret + `"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, requestBody, string(body))
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, responseBody)
	}))
	defer server.Close()
	directory := t.TempDir()
	t.Setenv("DENSE_MEM_E2E_REMEMBER_FAILURE_DIR", directory)
	client, closeCapture, err := driverDiagnosticClient(&config.Config{AIAPIKey: secret})
	require.NoError(t, err)
	ctx := observability.WithExchangeScope(context.Background(), observability.ExchangeScope{TeamID: "team", RunID: "review", BatchID: "conflict"})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(requestBody))
	require.NoError(t, err)
	response, err := client(time.Second).Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, responseBody, string(body))
	require.NoError(t, response.Body.Close())
	closeCapture()
	files, err := filepath.Glob(filepath.Join(directory, "*.private.jsonl"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	info, err := os.Stat(files[0])
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	encoded, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.NotContains(t, string(encoded), secret)
	bodies := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(encoded)), "\n") {
		var row struct {
			TeamID    string                            `json:"team_id"`
			RunID     string                            `json:"run_id"`
			Direction string                            `json:"direction"`
			StartedAt string                            `json:"started_at"`
			ElapsedMS *int64                            `json:"elapsed_ms"`
			Chunk     observability.DiagnosticBodyChunk `json:"payload_chunk"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &row))
		require.Equal(t, "team", row.TeamID)
		require.Equal(t, "review", row.RunID)
		require.NotEmpty(t, row.StartedAt)
		require.NotNil(t, row.ElapsedMS)
		require.Equal(t, "captured", row.Chunk.State)
		digest := sha256.Sum256([]byte(row.Chunk.Body))
		require.Equal(t, hex.EncodeToString(digest[:]), row.Chunk.SHA256)
		bodies[row.Direction] += row.Chunk.Body
	}
	require.Equal(t, requestBody, bodies["request"])
	require.Equal(t, strings.ReplaceAll(responseBody, secret, observability.CredentialProtectionRedacted), bodies["response"])
}

func TestDriverDiagnosticsCaptureRealHTTPTransportCancellation(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("DENSE_MEM_E2E_REMEMBER_FAILURE_DIR", directory)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	client, closeCapture, err := driverDiagnosticClient(&config.Config{})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ctx = observability.WithExchangeScope(ctx, observability.ExchangeScope{TeamID: "team", RunID: "review", BatchID: "conflict"})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(`{"input":["admitted exact source"]}`))
	require.NoError(t, err)
	_, err = client(time.Second).Do(request)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	closeCapture()
	files, err := filepath.Glob(filepath.Join(directory, "*.private.jsonl"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	encoded, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"transport_capture_state":"no_response"`)
	require.Contains(t, string(encoded), `"direction":"transport_error"`)
	require.Contains(t, string(encoded), "context deadline exceeded")
}
