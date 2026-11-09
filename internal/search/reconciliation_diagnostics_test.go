package search

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/embedding"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
	"github.com/stretchr/testify/require"
)

func TestSearchReconciliationFailureCanBeDiagnosedFromLogsAlone(t *testing.T) {
	for _, mode := range []string{"authentication", "timeout"} {
		for _, level := range []slog.Level{observability.LevelTrace, slog.LevelDebug, slog.LevelInfo} {
			t.Run(mode+"/"+level.String(), func(t *testing.T) {
				const secret = "search-provider-credential-canary"
				const requestSecret = "search-request-credential-canary"
				admitted := `{"secret":"admitted exact source ` + strings.Repeat("α", 1600) + `"}`
				responseBody := `{"error":{"code":"invalid_api_key","message":"configured deployment credential expired ` + secret + `"}}`
				providerTimeout := searchReconciliationProviderCap
				if mode == "timeout" {
					providerTimeout = 50 * time.Millisecond
					responseBody = `{"interrupted":"waiting for provider`
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					if mode == "timeout" {
						w.Header().Set("Content-Length", "9999")
						_, _ = io.WriteString(w, responseBody)
						w.(http.Flusher).Flush()
						<-r.Context().Done()
						return
					}
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = io.WriteString(w, responseBody)
				}))
				defer server.Close()
				var output bytes.Buffer
				logger := observability.NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: level}))
				protector := observability.NewCredentialProtector(secret)
				client := observability.DiagnosticHTTPClient(server.Client(), logger.ContextSlog(), protector)
				provider := embedding.NewOpenAIEmbeddingProvider(&config.Config{AIAPIURL: server.URL, AIAPIKey: secret, AIEmbeddingModel: "model", AIEmbeddingDimensions: 2}, client)
				repo := &searchReconciliationRepositoryStub{
					contract: &searchcontract.ActiveSearchContract{EmbeddingContractID: "contract", EmbeddingDimensions: 2, EmbeddingModel: "model", SearchIndexGenerationID: "generation", IndexGeneration: 7},
					run:      &knowledgecontract.SearchReconciliationRun{RunID: "system-reconciliation-run"},
					documents: []knowledgecontract.SearchDocumentForEmbedding{
						{SearchDocumentResult: knowledgecontract.SearchDocumentResult{TeamID: "team-a", OwnerProfileID: "owner-a", SourceKind: "evidence", SourceID: "source-a"}, DocumentHash: "hash", DocumentText: admitted},
						{SearchDocumentResult: knowledgecontract.SearchDocumentResult{TeamID: "team-b", OwnerProfileID: "owner-b", SourceKind: "evidence", SourceID: "source-b"}, DocumentHash: "hash", DocumentText: admitted},
					},
				}
				ctx := correlation.WithID(context.Background(), "search-diagnostic-correlation")
				ctx = observability.WithAuthenticationSecrets(ctx, secret, requestSecret)
				_, err := NewSearchReconciliationService(SearchReconciliationDependencies{Repository: repo, Projection: repo, Executor: semanticwrite.NewProviderExecutor(provider), Logger: logger, ProviderTimeout: providerTimeout}).Run(ctx)
				require.ErrorIs(t, err, ErrSearchReconciliationFailed)
				require.NotContains(t, output.String(), secret)
				require.NotContains(t, output.String(), requestSecret)
				failures := 0
				bodies := map[string]string{}
				counts := map[string]int{}
				totals := map[string]int{}
				digests := map[string]string{}
				for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
					var row map[string]any
					require.NoError(t, json.Unmarshal(line, &row))
					if row["msg"] == "search_reconciliation_failed" {
						failures++
						require.Equal(t, "ERROR", row["level"])
						require.Equal(t, "system-reconciliation-run", row["run_id"])
						require.Equal(t, "search-diagnostic-correlation", row["correlation_id"])
						if mode == "timeout" {
							require.Equal(t, "embedding_timeout", row["error_code"])
							require.Equal(t, "provider_timeout", row["failure_code"])
							require.Equal(t, "transient", row["failure_class"])
						} else {
							require.Equal(t, "embedding_provider_failed", row["error_code"])
							require.Equal(t, "provider_authentication_failed", row["failure_code"])
						}
						require.Equal(t, float64(2), row["selected_count"])
						require.Equal(t, float64(0), row["embedded_count"])
						require.Contains(t, row, "duration_ms")
						require.Len(t, row["sources"], 2)
						require.NotContains(t, row, "profile_id")
						require.Equal(t, "model", row["embedding_fence"].(map[string]any)["model"])
					}
					if row["msg"] != "ai_exchange_payload" {
						continue
					}
					require.Equal(t, "system-reconciliation-run", row["run_id"])
					var chunk observability.DiagnosticBodyChunk
					encoded, _ := json.Marshal(row["payload_chunk"])
					require.NoError(t, json.Unmarshal(encoded, &chunk))
					direction := row["direction"].(string)
					if mode == "timeout" && direction == "response" {
						require.Equal(t, "interrupted", row["transport_capture_state"])
					}
					require.Equal(t, counts[direction], chunk.Index)
					require.Equal(t, "captured", chunk.State)
					counts[direction]++
					totals[direction], digests[direction] = chunk.Count, chunk.SHA256
					bodies[direction] += chunk.Body
				}
				require.Equal(t, 1, failures)
				if level == slog.LevelInfo {
					require.Empty(t, bodies)
					return
				}
				for direction, body := range bodies {
					require.Equal(t, totals[direction], counts[direction])
					digest := sha256.Sum256([]byte(body))
					require.Equal(t, digests[direction], hex.EncodeToString(digest[:]))
				}
				require.Greater(t, counts["request"], 1)
				var request struct {
					Input []string `json:"input"`
				}
				require.NoError(t, json.Unmarshal([]byte(bodies["request"]), &request))
				require.Equal(t, []string{admitted}, request.Input)
				require.Equal(t, strings.ReplaceAll(responseBody, secret, observability.CredentialProtectionRedacted), bodies["response"])
				if mode == "timeout" {
					t.Log("Log-only diagnosis: the provider body was interrupted at the embedding phase deadline; embedding_timeout and transient/provider_timeout identify a slow provider, not permanent input rejection. Restore provider responsiveness, then rerun reconciliation; no documents were committed.")
				} else {
					t.Log("Log-only diagnosis: the two source owners shared one batch under generation 7; HTTP 401 and provider_authentication_failed identify an expired configured embedding credential. Replace that credential, then rerun reconciliation. No documents were committed.")
				}
			})
		}
	}
}
