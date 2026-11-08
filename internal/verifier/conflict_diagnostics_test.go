package verifier

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func TestConflictSharedTransportPreservesHTTPFailures(t *testing.T) {
	for _, mode := range []string{"429", "503", "interrupted", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_, _ = io.Copy(io.Discard, r.Body)
				switch mode {
				case "cancelled":
					<-r.Context().Done()
				case "interrupted":
					w.Header().Set("Content-Length", "9999")
					_, _ = io.WriteString(w, `{"choices":[`)
				case "429":
					w.Header().Set("Retry-After", "17")
					w.WriteHeader(429)
					_, _ = io.WriteString(w, "non-JSON rate limit body")
				case "503":
					w.WriteHeader(503)
					_, _ = io.WriteString(w, "non-JSON server failure")
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			provider := newTestOpenAIVerifier(newTestVerifierConfig(server.URL, "fixture-key", "conflict-model"), server.Client())
			_, err := provider.AssessRelationshipConflict(ctx, conflictAssessmentTestRequest(t))
			require.Error(t, err)
			require.Equal(t, 1, calls)
			details := ProviderFailureDetails(err)
			switch mode {
			case "429":
				require.ErrorIs(t, err, ErrVerifierRateLimit)
				require.Equal(t, 17*time.Second, details.RetryAfter)
			case "503":
				require.ErrorIs(t, err, ErrVerifierProvider)
				require.Equal(t, ProviderFailureClassHTTPServer, details.Class)
			case "interrupted":
				require.ErrorIs(t, err, ErrVerifierProvider)
				require.Equal(t, ProviderFailureClassProtocol, details.Class)
			case "cancelled":
				require.ErrorIs(t, err, ErrVerifierTimeout)
			}
		})
	}
}

func TestConflictFailureCanBeDiagnosedFromLogsAlone(t *testing.T) {
	for _, mode := range []string{"provider_failure", "repair_exhausted"} {
		for _, level := range []slog.Level{observability.LevelTrace, slog.LevelDebug, slog.LevelInfo} {
			t.Run(mode+"/"+level.String(), func(t *testing.T) {
				const secret = "conflict-provider-credential-canary"
				responseBody := `{"error":{"message":"configured Conflict credential expired ` + secret + `"}}`
				if mode == "repair_exhausted" {
					responseBody = `{"choices":[{"message":{"content":"invalid complete response ` + secret + `"}}]}`
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					if mode == "provider_failure" {
						w.WriteHeader(http.StatusUnauthorized)
					}
					_, _ = io.WriteString(w, responseBody)
				}))
				defer server.Close()
				var output bytes.Buffer
				logger := observability.NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: level}))
				protector := observability.NewCredentialProtector(secret)
				client := observability.DiagnosticHTTPClient(server.Client(), logger.ContextSlog(), protector)
				provider := newTestOpenAIVerifier(newTestVerifierConfig(server.URL, secret, "conflict-model"), client)
				provider.logger = logger
				request := conflictAssessmentTestRequest(t)
				request.Evidence[0].Content = `{"token":"admitted exact conflict evidence ` + strings.Repeat("α", 1100) + `"}`
				ctx := correlation.WithID(context.Background(), "conflict-diagnostic-correlation")
				ctx = observability.WithAuthenticationSecrets(ctx, secret)
				ctx = observability.WithExchangeScope(ctx, observability.ExchangeScope{TeamID: "team", RunID: "review-run", BatchID: "assessment-attempt"})
				_, err := provider.AssessRelationshipConflict(ctx, request)
				require.Error(t, err)
				require.NotContains(t, output.String(), secret)
				terminal, responses, invalid := 0, 0, 0
				bodies := map[string]string{}
				counts, totals := map[string]int{}, map[string]int{}
				hashes := map[string]string{}
				directions := map[string]string{}
				for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
					var row map[string]any
					require.NoError(t, json.Unmarshal(line, &row))
					switch row["msg"] {
					case "conflict_assessment_failed":
						terminal++
						require.Equal(t, "conflict-diagnostic-correlation", row["correlation_id"])
						require.Equal(t, request.RequestID, row["request_id"])
						require.Equal(t, request.CaseID, row["case_id"])
						require.Equal(t, "conflict-model", row["model"])
						require.Equal(t, ConflictAssessmentSchemaName, row["schema"])
						want := 1
						if mode == "repair_exhausted" {
							want = 5
							require.NotEmpty(t, row["validation_errors"])
						}
						require.Equal(t, float64(want), row["attempts"])
						require.Contains(t, row, "duration_ms")
					case "conflict_assessment_provider_response":
						responses++
						measured := row["transport"].(map[string]any)
						require.Contains(t, measured, "queue_ms")
						require.Contains(t, measured, "http_ms")
						require.Equal(t, false, row["usage_available"])
					case "conflict_assessment_response_invalid":
						invalid++
					case "ai_exchange_payload":
						key := fmt.Sprint(row["exchange_id"], ":", row["direction"])
						directions[key] = row["direction"].(string)
						require.Equal(t, "review-run", row["run_id"])
						require.Equal(t, "assessment-attempt", row["batch_id"])
						var chunk observability.DiagnosticBodyChunk
						encoded, _ := json.Marshal(row["payload_chunk"])
						require.NoError(t, json.Unmarshal(encoded, &chunk))
						require.Equal(t, counts[key], chunk.Index)
						require.Equal(t, "captured", chunk.State)
						counts[key]++
						totals[key], hashes[key] = chunk.Count, chunk.SHA256
						bodies[key] += chunk.Body
					}
				}
				require.Equal(t, 1, terminal)
				if level == slog.LevelInfo {
					require.Empty(t, bodies)
					require.Zero(t, responses)
					return
				}
				want := 1
				if mode == "repair_exhausted" {
					want = 5
					require.Equal(t, 5, invalid)
				}
				require.Equal(t, want, responses)
				require.Len(t, bodies, want*2)
				for key, body := range bodies {
					require.Equal(t, totals[key], counts[key])
					digest := sha256.Sum256([]byte(body))
					require.Equal(t, hashes[key], hex.EncodeToString(digest[:]))
					if directions[key] == "response" {
						require.Equal(t, strings.ReplaceAll(responseBody, secret, observability.CredentialProtectionRedacted), body)
					} else {
						require.Greater(t, counts[key], 1)
						var wire openAIVerifierRequest
						require.NoError(t, json.Unmarshal([]byte(body), &wire))
						require.Equal(t, "conflict-model", wire.Model)
						require.Contains(t, wire.Messages[1].Content, "admitted exact conflict evidence")
					}
				}
				t.Log("Log-only diagnosis: correlated Conflict exchanges show the effective model/schema, complete dossier and response history, measured queue/HTTP timings, missing usage, and terminal attempts. An HTTP 401 requires replacing the configured credential; five invalid complete responses require correcting the provider's schema output before the next permitted review.")
			})
		}
	}
}
