package serverapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	"github.com/stretchr/testify/require"
)

func TestOntologyReconstructedBoundedRequestReportsProtectionBudget(t *testing.T) {
	const targetBytes = 61728
	const presented = "synthetic-presented-credential"
	wire := make(chan []byte, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		wire <- body
		var envelope struct{ Messages []modelprovider.Message }
		require.NoError(t, json.Unmarshal(body, &envelope))
		var request assessment.Request
		require.NoError(t, json.Unmarshal([]byte(envelope.Messages[1].Content), &request))
		response := assessment.Response{HierarchyUpdates: []assessment.HierarchyUpdate{}, RequestID: request.RequestID, Definitions: []assessment.Definition{{Ref: "new-topic", Kind: contract.Topic,
			Key: "diagnostic-witness", Label: "Diagnostic witness", Description: "Reconstructed synthetic diagnostic input", Aliases: []string{}}},
			Items: []assessment.Decision{}, Equivalence: []assessment.Equivalence{}}
		for _, item := range request.Items {
			response.Items = append(response.Items, assessment.Decision{Ref: item.Ref, Status: "classified", DefinitionRef: "new-topic"})
		}
		encoded, err := json.Marshal(response)
		require.NoError(t, err)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(encoded)}}}}))
	}))
	defer upstream.Close()
	request := assessment.Request{RequestID: "reconstructed-budget-witness", Definitions: []assessment.Definition{}, Pairs: []assessment.Pair{}}
	for index := range 20 {
		request.Items = append(request.Items, assessment.Item{Ref: fmt.Sprintf("s%d", index), Kind: contract.EvidenceSource,
			Text: "Frozen reconstructed public-shape source.", OwnerRef: "owner", Context: map[string]string{}})
	}
	request.Items[19].Text += " " + presented
	run := func(secret string) ([]byte, string, []observability.DiagnosticBodyChunk) {
		t.Helper()
		var output bytes.Buffer
		protector := observability.NewCredentialProtector(secret)
		logger := observability.NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
		client := observability.DiagnosticHTTPClient(upstream.Client(), logger.ContextSlog(), protector)
		cfg := &config.Config{AIVerifierAPIURL: upstream.URL, AIVerifierAPIKey: secret, AIVerifierModel: "fixture-model"}
		provider := assessment.NewProvider(assessorprovider.NewOpenAIAssessor(cfg, client), cfg.AIVerifierModel, assessor.DefaultSemanticAssessmentLimits(), 300000)
		ctx := observability.WithAuthenticationSecrets(context.Background(), presented)
		ctx = observability.WithExchangeScope(ctx, observability.ExchangeScope{TeamID: "synthetic-team", RunID: "synthetic-run", BatchID: "synthetic-batch"})
		started := time.Now()
		_, attempts, err := provider.Assess(ctx, request)
		require.NoError(t, err)
		require.Len(t, attempts, 1)
		body := <-wire
		require.NotContains(t, output.String(), secret)
		require.NotContains(t, output.String(), presented)
		var chunks []observability.DiagnosticBodyChunk
		for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
			var record struct {
				Message    string                            `json:"msg"`
				Direction  string                            `json:"direction"`
				ExchangeID string                            `json:"exchange_id"`
				Chunk      observability.DiagnosticBodyChunk `json:"payload_chunk"`
			}
			require.NoError(t, json.Unmarshal(line, &record))
			if record.Message == "ai_exchange_payload" && record.Direction == "request" {
				require.NotEmpty(t, record.ExchangeID)
				chunks = append(chunks, record.Chunk)
			}
		}
		t.Logf("reconstructed native request bytes=%d capture_chunks=%d elapsed_ms=%d", len(body), len(chunks), time.Since(started).Milliseconds())
		_, err = fmt.Print(output.String())
		require.NoError(t, err)
		return body, output.String(), chunks
	}
	longSecret := strings.Repeat("a", 3000) + "b"
	sizing, _, chunks := run(longSecret)
	require.NotEmpty(t, chunks)
	require.Equal(t, "captured", chunks[0].State)
	require.Less(t, len(sizing), targetBytes)
	request.Items[0].Text += strings.Repeat("a", targetBytes-len(sizing))
	bounded, logs, chunks := run(longSecret)
	require.Len(t, bounded, targetBytes)
	require.Len(t, chunks, 1)
	require.Equal(t, "unavailable", chunks[0].State)
	require.Equal(t, int(observability.CredentialProtectionMatchingBudgetExceeded), chunks[0].Reason)
	require.Equal(t, "matching_work_exceeded", chunks[0].ReasonCode)
	require.Equal(t, 8<<20, chunks[0].Limit)
	require.Equal(t, "matching_work", chunks[0].LimitUnit)
	require.Empty(t, chunks[0].Body)
	require.Empty(t, chunks[0].SHA256)
	require.Contains(t, logs, "diagnostic_body_unavailable")
	require.Contains(t, logs, `"level":"WARN"`)
	comparable, _, chunks := run("synthetic-nonmatching-provider-key")
	require.Equal(t, bounded, comparable)
	var protected strings.Builder
	for index, chunk := range chunks {
		require.Equal(t, "captured", chunk.State)
		require.Equal(t, index, chunk.Index)
		require.Equal(t, len(chunks), chunk.Count)
		protected.WriteString(chunk.Body)
	}
	expected := strings.ReplaceAll(string(bounded), presented, observability.CredentialProtectionRedacted)
	require.Equal(t, expected, protected.String())
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(expected))), chunks[0].SHA256)
}
