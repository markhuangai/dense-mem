package generation_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/provider/structured"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/generation"
	"github.com/stretchr/testify/require"
)

func testTask() generation.Task[string] {
	return generation.Task[string]{
		ID: "evidence-generation", Capability: "structured_fixture", Tokenizer: "o200k_base",
		Correction: "Return one complete object with every requested field.",
		Request: modelprovider.StructuredRequest{
			Model: "fixture-model", SchemaName: "fixture_schema", Temperature: modelprovider.StructuredTemperature(false),
			Messages:       []modelprovider.Message{{Role: "system", Content: "Return one closed object."}, {Role: "user", Content: "Real Evidence 你好 <>& \\n \"quoted\"."}},
			Schema:         map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []string{"value"}, "additionalProperties": false},
			MaxInputTokens: 16000, MaxOutputTokens: 1000,
		},
		Validate: func(_ context.Context, raw []byte) (string, error) {
			object, issues := jsonstrict.ValidateResponseObject(raw, "", []string{"value"}, nil, nil)
			if len(issues) > 0 {
				return "", &jsonstrict.ValidationError{Stage: "response_json", Errors: issues}
			}
			var result string
			if err := json.Unmarshal(object["value"], &result); err != nil {
				return "", &jsonstrict.ValidationError{Stage: "response_json", Errors: []jsonstrict.Issue{jsonstrict.ResponseJSONValidationError(err, "value")}}
			}
			if result != "complete" {
				return "", &jsonstrict.ValidationError{Stage: "response_contract", Errors: []jsonstrict.Issue{{Field: "value", Code: "exact_grounding", Message: "must equal complete"}}}
			}
			return result, nil
		},
	}
}

func TestStructuredGenerationHTTPCompleteHistoryAndBudgets(t *testing.T) {
	for _, tc := range []struct {
		name           string
		content        []string
		prompt, output []int
		offset, calls  int
		failure        string
	}{
		{name: "two complete repairs", content: []string{"{}", "{\"value\":null}", "{\"value\":\"complete\"}"}, calls: 3},
		{name: "exhaustion and no response splicing", content: []string{"{}", "{\"value\":2}", "{\"value\":\"complete\",\"value\":\"extra\"}"}, calls: 3, failure: "malformed_exhausted"},
		{name: "previous response counts", content: []string{"{}", "{\"unrecognized\":true}"}, calls: 2, offset: 1, failure: "malformed_exhausted"},
		{name: "reported and measured output overruns", content: []string{"{\"value\":\"complete\"}", strings.Repeat(" word", 1500), "{\"value\":\"complete\"}"}, output: []int{1001, 0, 0}, calls: 3},
		{name: "reported input terminates even after repair", content: []string{"{}", "{\"value\":\"complete\"}"}, prompt: []int{0, 16001}, calls: 2, failure: "input_budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := testTask()
			task.ResponsesConsumed = tc.offset
			if tc.offset != 0 {
				task.Request.Temperature = nil
			}
			var requests [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, err := io.ReadAll(r.Body)
				if err != nil || len(requests) >= len(tc.content) {
					http.Error(w, "unexpected fixture request", http.StatusBadRequest)
					return
				}
				turn := len(requests)
				requests = append(requests, data)
				reply := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": tc.content[turn]}}}}
				if len(tc.prompt) > turn || len(tc.output) > turn {
					var prompt, output int
					if len(tc.prompt) > turn {
						prompt = tc.prompt[turn]
					}
					if len(tc.output) > turn {
						output = tc.output[turn]
					}
					reply["usage"] = map[string]int{"prompt_tokens": prompt, "completion_tokens": output, "total_tokens": prompt + output}
				}
				_ = json.NewEncoder(w).Encode(reply)
			}))
			defer server.Close()
			transport := structured.NewOpenAI(&config.Config{AIVerifierAPIURL: server.URL}, server.Client())
			originalBody, err := modelprovider.MarshalStructuredRequest(task.Request)
			require.NoError(t, err)
			result, err := (generation.Service[string]{Transport: transport}).Run(t.Context(), task)
			if tc.failure == "" {
				require.NoError(t, err)
				require.Equal(t, "complete", result.Value)
			} else {
				var terminal *modelprovider.MalformedResponseError
				require.ErrorAs(t, err, &terminal)
				require.Equal(t, tc.failure, terminal.FailureClass)
				require.Empty(t, result.Value)
			}
			require.Len(t, requests, tc.calls)
			require.Len(t, result.Attempts, tc.calls)
			require.Equal(t, originalBody, requests[0])
			for index, body := range requests {
				count, err := modelprovider.CountTokens(string(body), task.Tokenizer)
				require.NoError(t, err)
				attempt := result.Attempts[index]
				require.Equal(t, count, attempt.InputTokens)
				require.Equal(t, tc.offset+index+1, attempt.Number)
				require.Equal(t, len(tc.prompt) > index || len(tc.output) > index, attempt.Provider.UsageAvailable)
				var received struct{ Messages []modelprovider.Message }
				require.NoError(t, json.Unmarshal(body, &received))
				require.Equal(t, task.Request.Messages, received.Messages[:2])
				for last := range index {
					require.Equal(t, tc.content[last], received.Messages[2+2*last].Content)
				}
			}
		})
	}
}

func TestStructuredGenerationFitsHistoryWithoutEditingRequiredInput(t *testing.T) {
	task := testTask()
	task.Request.MaxInputTokens, task.Request.MaxOutputTokens = 400, 2000
	call := 0
	transport := transportFunc(func(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		call++
		require.Equal(t, task.Request.Messages, request.Messages[:2])
		for i := 2; i+1 < len(request.Messages); i += 2 {
			require.Contains(t, request.Messages[i].Content, "exceeded the repair history budget")
			require.Contains(t, request.Messages[i+1].Content, "validation_errors")
		}
		// Transport receives its own copy each time. Changes cannot affect repair.
		request.Messages[1].Content = "mutated"
		request.Schema["type"] = "mutated"
		*request.Temperature = 1
		if call < 3 {
			return modelprovider.StructuredResult{Content: strings.Repeat(" escaped\\\"你好", 400)}, nil
		}
		return modelprovider.StructuredResult{Content: "{\"value\":\"complete\"}"}, nil
	})
	result, err := (generation.Service[string]{Transport: transport}).Run(t.Context(), task)
	require.NoError(t, err)
	require.Equal(t, "complete", result.Value)
	require.Equal(t, 3, call)
	require.Equal(t, "object", task.Request.Schema["type"])
	require.Zero(t, *task.Request.Temperature)

	stop := errors.New("domain overflow")
	task.Validate = func(context.Context, []byte) (string, error) { return "", stop }
	call = 2
	_, err = (generation.Service[string]{Transport: transport}).Run(t.Context(), task)
	require.ErrorIs(t, err, stop)
	require.Equal(t, 3, call)
}

type transportFunc func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error)

func (f transportFunc) Complete(ctx context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return f(ctx, request)
}

func TestStructuredGenerationLogsProveRepairAndTransportFailures(t *testing.T) {
	const providerSecret, actorSecret = "synth-provider-canary", "synth-actor+canary/=?"
	encodedSecret := url.QueryEscape(actorSecret)
	for _, runtime := range []string{"", "info"} {
		t.Run("level_"+runtime, func(t *testing.T) {
			output, err := os.Create(filepath.Join(t.TempDir(), "stdout.jsonl"))
			require.NoError(t, err)
			defer output.Close()
			savedStdout := os.Stdout
			os.Stdout = output
			defer func() { os.Stdout = savedStdout }()
			level, err := observability.ParseLevel(runtime)
			require.NoError(t, err)
			protector := observability.NewCredentialProtector(providerSecret)
			root := observability.NewWithProtector(level, protector)
			logger := root.ContextSlog()

			team, owner, credential := uuid.New(), uuid.New(), uuid.New()
			ctx := requestctx.WithActor(t.Context(), requestctx.Actor{TeamID: team, OwnerID: owner, CredentialID: &credential})
			ctx = correlation.WithID(ctx, "generation-log-witness")
			ctx = observability.WithAuthenticationSecrets(ctx, actorSecret)
			ctx = observability.WithExchangeScope(ctx, observability.ExchangeScope{RunID: "fixture-run", OperationID: "fixture-operation"})
			task := testTask()
			var admitted string
			// Force multiple protected chunks. Secret-like field names alone do
			// not cause masking. Fixtures never use operational credentials.
			task.Request.Messages[1].Content = "token=admitted-value password=also-admitted-value " + providerSecret + " " + encodedSecret + strings.Repeat(" deliberately retained exact text.", 900)
			seen := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := io.ReadAll(r.Body)
				seen++
				switch seen {
				case 1:
					admitted = string(data)
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "{}"}}}})
				case 2:
					w.Header().Set("Retry-After", "12")
					http.Error(w, "fixture upstream is at capacity", http.StatusTooManyRequests)
				case 3:
					_, _ = w.Write([]byte("invalid response envelope"))
				default:
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"value":"complete"}`}}}})
				}
			}))
			defer server.Close()
			client := observability.DiagnosticHTTPClient(server.Client(), logger, protector)
			transport := structured.NewOpenAIWithModel(&config.Config{AIVerifierAPIURL: server.URL, AIVerifierAPIKey: providerSecret}, client, modelprovider.NewConcurrencyGate(1), "fixture-model")
			service := generation.Service[string]{Transport: transport, Logger: logger}
			_, err = service.Run(ctx, task)
			require.ErrorIs(t, err, modelprovider.ErrVerifierRateLimit)
			task.ID = "invalid-envelope"
			_, err = service.Run(ctx, task)
			require.ErrorIs(t, err, modelprovider.ErrVerifierProvider)
			task.ID = "validated-after-failures"
			result, err := service.Run(ctx, task)
			require.NoError(t, err)
			require.Equal(t, "complete", result.Value)

			_, err = output.Seek(0, io.SeekStart)
			require.NoError(t, err)
			data, err := io.ReadAll(output)
			require.NoError(t, err)
			require.False(t, bytes.Contains(data, []byte(providerSecret)), "capture included the provider authentication canary")
			require.False(t, bytes.Contains(data, []byte(encodedSecret)), "capture included the encoded actor authentication canary")
			var repairExchange string
			var sawRateLimit, sawProtocol bool
			failedExchanges := map[string]string{}
			var successExchange string
			chunks := map[string][]observability.DiagnosticBodyChunk{}
			decoder := json.NewDecoder(bytes.NewReader(data))
			for decoder.More() {
				var event struct {
					Message, Team, Profile, Credential, Correlation, Exchange, Direction, Level string
					Fields                                                                      map[string]json.RawMessage
				}
				require.NoError(t, decoder.Decode(&event.Fields))
				field := func(k string) string { var value string; _ = json.Unmarshal(event.Fields[k], &value); return value }
				event.Message, event.Level, event.Exchange, event.Direction = field("msg"), field("level"), field("exchange_id"), field("direction")
				switch event.Message {
				case "structured_ai.repair.requested":
					require.Equal(t, "WARN", event.Level)
					require.Equal(t, team.String(), field("team_id"))
					require.Equal(t, owner.String(), field("profile_id"))
					require.Equal(t, credential.String(), field("credential_id"))
					require.Equal(t, "generation-log-witness", field("correlation_id"))
					require.Equal(t, "fixture-run", field("run_id"))
					require.Equal(t, "fixture-operation", field("operation_id"))
					require.Equal(t, "request_complete_replacement", field("next_action"))
					require.Contains(t, string(event.Fields["issues"]), "is required")
					repairExchange = event.Exchange
				case "structured_ai.attempt.completed":
					if field("failure_class") == modelprovider.ProviderFailureClassRateLimited {
						require.JSONEq(t, "12000", string(event.Fields["retry_after_ms"]))
						require.JSONEq(t, "429", string(event.Fields["http_status"]))
						sawRateLimit = true
					} else if field("failure_class") == modelprovider.ProviderFailureClassProtocol {
						require.Contains(t, string(event.Fields["transport"]), "response_decode")
						sawProtocol = true
					}
				case "structured_ai.failed":
					require.Equal(t, "ERROR", event.Level)
					require.Equal(t, team.String(), field("team_id"))
					require.Equal(t, "fixture-operation", field("operation_id"))
					require.NotEmpty(t, event.Exchange)
					require.Contains(t, string(event.Fields["attempts"]), event.Exchange)
					failedExchanges[field("assessment_id")] = event.Exchange
				case "structured_ai.completed":
					require.Equal(t, "INFO", event.Level)
					require.Equal(t, "validated-after-failures", field("assessment_id"))
					require.Equal(t, "validated", field("outcome"))
					require.Contains(t, string(event.Fields["attempts"]), `"usage_available":false`)
					successExchange = event.Exchange
				case "ai_exchange_payload":
					require.Equal(t, "DEBUG", event.Level)
					var chunk observability.DiagnosticBodyChunk
					require.NoError(t, json.Unmarshal(event.Fields["payload_chunk"], &chunk))
					key := event.Exchange + ":" + event.Direction
					chunks[key] = append(chunks[key], chunk)
				}
			}
			require.NotEmpty(t, repairExchange)
			require.True(t, sawRateLimit && sawProtocol)
			require.Len(t, failedExchanges, 2)
			require.NotEmpty(t, successExchange)
			if level == slog.LevelInfo {
				require.Empty(t, chunks)
				return
			}
			got := chunks[repairExchange+":request"]
			require.Greater(t, len(got), 1)
			sort.Slice(got, func(i, j int) bool { return got[i].Index < got[j].Index })
			var reconstructed strings.Builder
			for i, chunk := range got {
				require.Equal(t, i, chunk.Index)
				require.Equal(t, len(got), chunk.Count)
				require.Equal(t, "captured", chunk.State)
				reconstructed.WriteString(chunk.Body)
			}
			sum := sha256.Sum256([]byte(reconstructed.String()))
			require.Equal(t, hex.EncodeToString(sum[:]), got[0].SHA256)
			protected, reason := observability.ProtectDiagnosticText(ctx, protector, []byte(admitted), modelprovider.MaxProviderDiagnosticBodyBytes)
			require.Equal(t, observability.CredentialProtectionAvailable, reason)
			require.Equal(t, string(protected), reconstructed.String())
			require.Contains(t, reconstructed.String(), "token=admitted-value password=also-admitted-value")
			require.NotEmpty(t, chunks[repairExchange+":response"])
			for _, exchange := range failedExchanges {
				require.NotEmpty(t, chunks[exchange+":response"])
			}
			require.NotEmpty(t, chunks[successExchange+":response"])
		})
	}
}
