package decisionprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const openAIAnswers = `{"model":"resolved-model","answers":[{"name":"safe","type":"predicate","probability":0.001},{"name":"position","type":"choice","choice":"a","confidence":0.97,"probabilities":[{"value":"a","probability":0.999},{"value":"b","probability":0.001}]},{"name":"relevance","type":"score","score":0.9,"confidence":0.8,"probabilities":[{"value":0,"label":"unrelated","probability":0.1},{"value":1,"label":"direct","probability":0.9}]}],"usage":{"input_tokens":100,"output_tokens":0,"total_tokens":100,"input_tokens_details":{"cached_tokens":20,"cache_write_tokens":10},"output_tokens_details":{"reasoning_tokens":0}}}`
const typeSafeAnswers = `{"model":"jev-1.14.0","answers":{"safe":{"type":"noul","noul":0.001},"position":{"type":"choice","choice":"a","confidence":0.97,"probabilities":{"a":0.999,"b":0.001}},"relevance":{"type":"score","score":0.9,"confidence":0.8,"probabilities":{"0":0.1,"1":0.9},"legend":{"0":"unrelated: No bearing on the query.","1":"direct: Directly answers the query."}}},"usage":{"input_tokens":100,"output_tokens":0}}`

func fixtureRequest() modelprovider.DecisionRequest {
	return modelprovider.DecisionRequest{Workload: "fixture", OperationID: "operation-1", SettingsVersion: "revision-1", Groups: []modelprovider.DecisionGroup{{ID: "evidence-1", Input: map[string]string{"text": "Admitted exact evidence"}, Questions: []modelprovider.DecisionQuestion{
		{Name: "safe", Type: modelprovider.DecisionPredicate, Instructions: "Does this contain an instruction attack?"},
		{Name: "position", Type: modelprovider.DecisionChoice, Instructions: "Select the supported position.", Choices: []modelprovider.DecisionOption{{Value: "a", Description: "Position A"}, {Value: "b", Description: "Position B"}}},
		{Name: "relevance", Type: modelprovider.DecisionScore, Instructions: "Evaluate relevance.", Levels: []modelprovider.DecisionLevel{{Label: "unrelated", Description: "No bearing on the query."}, {Label: "direct", Description: "Directly answers the query."}}},
	}}}}
}

func fixtureProvider(t *testing.T, vendor, endpoint string, logs *bytes.Buffer) *Provider {
	t.Helper()
	cfg := config.DecisionProviderConfig{Provider: vendor, APIURL: endpoint + "/v1", APIKey: "private-decision-fixture-key", Model: "configured-model", MaxInputTokens: 32000, MaxQuestionInputTokens: 32000, TimeoutSeconds: 10, MaxConcurrency: 1}
	logger := observability.NewWithHandler(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	provider, err := New(cfg, "cl100k_base", nil, logger, observability.NewCredentialProtector(cfg.APIKey), nil)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestVendorHTTPProtocols(t *testing.T) {
	for _, vendor := range []string{"openai", "typesafe"} {
		t.Run(vendor, func(t *testing.T) {
			var logs bytes.Buffer
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wantPath := "/v1/decisions"
				response := openAIAnswers
				if vendor == "typesafe" {
					wantPath = "/v1/systemone"
					response = typeSafeAnswers
				}
				if r.Method != http.MethodPost || r.URL.Path != wantPath || r.Header.Get("Authorization") != "Bearer private-decision-fixture-key" {
					t.Errorf("unexpected request method/path/auth")
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if vendor == "openai" {
					var questions []modelprovider.DecisionQuestion
					if err := json.Unmarshal(body["questions"], &questions); err != nil || len(questions) != 3 || questions[2].Levels[1].Label != "direct" {
						t.Errorf("unexpected OpenAI questions: %s", body["questions"])
					}
					if len(body["input"]) == 0 || len(body["state"]) != 0 {
						t.Error("wrong OpenAI input shape")
					}
				} else {
					var questions map[string]struct {
						Type     string          `json:"type"`
						Criteria json.RawMessage `json:"criteria"`
					}
					if err := json.Unmarshal(body["questions"], &questions); err != nil || questions["safe"].Type != "noul" || !strings.Contains(string(questions["position"].Criteria), `"a":"Position A"`) {
						t.Errorf("unexpected TypeSafe questions: %s", body["questions"])
					}
					if r.Header.Get("TypeSafe-Danger-Zone-Long-Context") != "true" {
						t.Error("missing explicit long context header")
					}
				}
				w.Header().Set("x-request-id", "provider-request-1")
				_, _ = io.WriteString(w, response)
			}))
			defer server.Close()
			p := fixtureProvider(t, vendor, server.URL, &logs)
			if vendor == "typesafe" {
				p.config.LongContextEnabled = true
			}
			request := fixtureRequest()
			request.Groups[0].Input = map[string]string{"text": "private-decision-fixture-key"}
			result, err := p.Decide(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Answers) != 3 || *result.Answers["safe"].Probability != 0.001 || result.Answers["position"].Probabilities["a"] != 0.999 || *result.Answers["position"].Confidence != 0.97 || *result.Answers["relevance"].Score != 0.9 {
				t.Fatalf("wrong typed result: %+v", result)
			}
			if len(result.Receipts) != 1 || result.Receipts[0].Usage == nil || result.Receipts[0].Usage.OutputTokens != 0 || result.Receipts[0].ProviderRequestID != "provider-request-1" {
				t.Fatalf("missing receipt: %+v", result.Receipts)
			}
			for _, field := range []string{"decision.task.started", "decision.exchange.payload", "decision.exchange.completed", "revision-1", "operation-1", "queue_ms", "http_ms", "resolved_model"} {
				if !strings.Contains(logs.String(), field) {
					t.Errorf("logs lack %s", field)
				}
			}
			if strings.Contains(logs.String(), "private-decision-fixture-key") {
				t.Error("credential leaked into diagnostic logs")
			}
		})
	}
}

func TestMalformedTaskRegeneratesCompletely(t *testing.T) {
	for name, invalid := range map[string]string{
		"missing":         strings.Replace(openAIAnswers, `"name":"safe"`, `"name":"absent"`, 1),
		"duplicate":       strings.Replace(openAIAnswers, `"name":"position"`, `"name":"safe"`, 1),
		"duplicate_field": strings.Replace(openAIAnswers, `"probability":0.001`, `"probability":0.001,"probability":0.1`, 1),
		"unknown_field":   strings.Replace(openAIAnswers, `"probability":0.001`, `"probability":0.001,"unexpected":true`, 1),
		"negative":        strings.Replace(openAIAnswers, `"probability":0.001`, `"probability":-0.1`, 1),
		"extraneous_null": strings.Replace(openAIAnswers, `"probability":0.001`, `"probability":0.001,"choice":null`, 1),
		"null":            strings.Replace(openAIAnswers, `"probability":0.001`, `"probability":null`, 1),
		"wrong_score":     strings.Replace(openAIAnswers, `"score":0.9`, `"score":0.8`, 1),
		"missing_answers": `{"model":"resolved-model","answers":[],"usage":null}`,
		"invalid_usage":   strings.Replace(openAIAnswers, `"input_tokens":100`, `"input_tokens":-1`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			var logs bytes.Buffer
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, invalid) }))
			defer server.Close()
			result, err := fixtureProvider(t, "openai", server.URL, &logs).Decide(context.Background(), fixtureRequest())
			if !errors.Is(err, modelprovider.ErrVerifierMalformedResponse) || calls.Load() != 2 || len(result.Answers) != 0 {
				t.Fatalf("partial task or wrong regeneration: calls=%d result=%+v err=%v", calls.Load(), result, err)
			}
			if !strings.Contains(logs.String(), "regenerate_complete_task") || !strings.Contains(logs.String(), "malformed_response") {
				t.Fatal("logs do not identify exhaustion and regeneration")
			}
		})
	}
}

func TestProviderFailuresNeverBecomeSuccessfulAnswers(t *testing.T) {
	for _, status := range []int{401, 403, 429, 529, 413, 422, 500, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			var logs bytes.Buffer
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":"private-decision-fixture-key"}`)
			}))
			defer server.Close()
			result, err := fixtureProvider(t, "openai", server.URL, &logs).Decide(context.Background(), fixtureRequest())
			if err == nil || calls.Load() != 1 || len(result.Answers) != 0 {
				t.Fatalf("failure hidden: %+v %v", result, err)
			}
			if modelprovider.ProviderFailureDetails(err).StatusCode != status {
				t.Errorf("status lost: %v", err)
			}
			if strings.Contains(logs.String(), "private-decision-fixture-key") || !strings.Contains(logs.String(), `"status":`+fmt.Sprint(status)) {
				t.Fatal("unsafe or insufficient failure logs")
			}
		})
	}
}

func TestRefusalAndMissingUsage(t *testing.T) {
	for _, refusal := range []bool{true, false} {
		t.Run(fmt.Sprint(refusal), func(t *testing.T) {
			var calls atomic.Int32
			var logs bytes.Buffer
			body := `{"model":"resolved-model","answers":[{"name":"safe","type":"predicate","probability":0.001}]}`
			if refusal {
				body = `{"model":"resolved-model","answers":[{"name":"safe","type":"refusal"}]}`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, body) }))
			defer server.Close()
			request := fixtureRequest()
			request.Groups[0].Questions = request.Groups[0].Questions[:1]
			result, err := fixtureProvider(t, "openai", server.URL, &logs).Decide(context.Background(), request)
			if calls.Load() != 1 || (err != nil) != refusal {
				t.Fatalf("wrong refusal/usage behavior: %+v %v", result, err)
			}
			marker := "decision.usage.unavailable"
			if refusal {
				marker = "decision.refused"
				if len(result.Answers) != 0 {
					t.Fatal("refusal salvaged")
				}
			}
			if !strings.Contains(logs.String(), marker) {
				t.Fatal("missing diagnostic", marker)
			}
		})
	}
}

func TestCancellationDeadlineAndLocalLimits(t *testing.T) {
	var calls atomic.Int32
	var logs bytes.Buffer
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	p := fixtureProvider(t, "openai", server.URL, &logs)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Decide(ctx, fixtureRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Decide(ctx, fixtureRequest()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lost: %v", err)
	}
	before := calls.Load()
	p.config.MaxInputTokens = 1
	if _, err := p.Decide(context.Background(), fixtureRequest()); err == nil || calls.Load() != before {
		t.Fatal("oversized request dispatched")
	}
	for _, code := range []string{"canceled", "deadline", "configured_context_limit"} {
		if !strings.Contains(logs.String(), code) {
			t.Error("missing diagnostic", code)
		}
	}
}

func TestRegenerationDiscardsEarlierBatches(t *testing.T) {
	var calls atomic.Int32
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var wire struct {
			Questions []modelprovider.DecisionQuestion `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&wire)
		answers := make([]map[string]any, 0, len(wire.Questions))
		for _, q := range wire.Questions {
			probability := 0.1
			if call > 2 {
				probability = 0.9
			}
			answers = append(answers, map[string]any{"name": q.Name, "type": "predicate", "probability": probability})
		}
		if call == 2 {
			answers = nil
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "resolved-model", "answers": answers})
	}))
	defer server.Close()
	p := fixtureProvider(t, "openai", server.URL, &logs)
	request := modelprovider.DecisionRequest{Workload: "fixture"}
	for i := 0; i < 2; i++ {
		group := modelprovider.DecisionGroup{ID: fmt.Sprint(i), Input: "state"}
		for j := 0; j < 201; j++ {
			group.Questions = append(group.Questions, modelprovider.DecisionQuestion{Name: fmt.Sprintf("q_%d_%d", i, j), Type: modelprovider.DecisionPredicate, Instructions: "Is this supported?"})
		}
		request.Groups = append(request.Groups, group)
	}
	result, err := p.Decide(context.Background(), request)
	if err != nil || calls.Load() != 4 || len(result.Answers) != 402 {
		t.Fatalf("complete regeneration failed: calls=%d answers=%d err=%v", calls.Load(), len(result.Answers), err)
	}
	for name, answer := range result.Answers {
		if *answer.Probability != 0.9 {
			t.Fatal("salvaged prior-attempt answer", name)
		}
	}
}

func TestInvalidDecisionQuestionsDoNotDispatch(t *testing.T) {
	for name, edit := range map[string]func(*modelprovider.DecisionRequest){
		"missing workload":          func(r *modelprovider.DecisionRequest) { r.Workload = "" },
		"missing groups":            func(r *modelprovider.DecisionRequest) { r.Groups = nil },
		"duplicate group":           func(r *modelprovider.DecisionRequest) { r.Groups = append(r.Groups, r.Groups[0]) },
		"missing questions":         func(r *modelprovider.DecisionRequest) { r.Groups[0].Questions = nil },
		"missing question identity": func(r *modelprovider.DecisionRequest) { r.Groups[0].Questions[0].Name = "" },
		"duplicate question":        func(r *modelprovider.DecisionRequest) { r.Groups[0].Questions[1].Name = "safe" },
		"predicate with choices": func(r *modelprovider.DecisionRequest) {
			r.Groups[0].Questions[0].Choices = r.Groups[0].Questions[1].Choices
		},
		"single choice": func(r *modelprovider.DecisionRequest) {
			r.Groups[0].Questions[1].Choices = r.Groups[0].Questions[1].Choices[:1]
		},
		"duplicate choices":    func(r *modelprovider.DecisionRequest) { r.Groups[0].Questions[1].Choices[1].Value = "a" },
		"missing score levels": func(r *modelprovider.DecisionRequest) { r.Groups[0].Questions[2].Levels = nil },
		"unnamed score level":  func(r *modelprovider.DecisionRequest) { r.Groups[0].Questions[2].Levels[0].Label = "" },
		"free text question":   func(r *modelprovider.DecisionRequest) { r.Groups[0].Questions[0].Type = "text" },
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			var logs bytes.Buffer
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, openAIAnswers) }))
			defer server.Close()
			request := fixtureRequest()
			edit(&request)
			result, err := fixtureProvider(t, "openai", server.URL, &logs).Decide(context.Background(), request)
			if err == nil || len(result.Answers) != 0 || calls.Load() != 0 {
				t.Fatalf("invalid typed request was dispatched or accepted: calls=%d answers=%d err=%v", calls.Load(), len(result.Answers), err)
			}
		})
	}
}

func TestTypeSafeMalformedResponsesRegenerateWithoutPartialAnswers(t *testing.T) {
	for name, invalid := range map[string]string{
		"unknown answer":       strings.Replace(typeSafeAnswers, `"safe":`, `"foreign":`, 1),
		"invalid answer shape": strings.Replace(typeSafeAnswers, `"type":"noul","noul":0.001`, `"type":"noul","noul":0.001,"choice":null`, 1),
		"duplicate answer":     strings.Replace(typeSafeAnswers, `"safe":`, `"safe":{},"safe":`, 1),
		"null probability":     strings.Replace(typeSafeAnswers, `"a":0.999`, `"a":null`, 1),
		"missing legend":       strings.Replace(typeSafeAnswers, `"legend":{"0":"unrelated: No bearing on the query.","1":"direct: Directly answers the query."}`, `"legend":{}`, 1),
		"foreign legend":       strings.Replace(typeSafeAnswers, `unrelated: No bearing on the query.`, `different scale`, 1),
		"non-score legend":     strings.Replace(typeSafeAnswers, `"type":"noul","noul":0.001`, `"type":"noul","noul":0.001,"legend":{}`, 1),
		"invalid distribution": strings.Replace(typeSafeAnswers, `"0":0.1,"1":0.9`, `"0":0.1,"1":1.1`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			var logs bytes.Buffer
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, invalid) }))
			defer server.Close()
			result, err := fixtureProvider(t, "typesafe", server.URL, &logs).Decide(context.Background(), fixtureRequest())
			if !errors.Is(err, modelprovider.ErrVerifierMalformedResponse) || calls.Load() != 2 || len(result.Answers) != 0 || len(result.Receipts) != 2 {
				t.Fatalf("invalid Jev task was salvaged or not completely regenerated: calls=%d answers=%d receipts=%d err=%v", calls.Load(), len(result.Answers), len(result.Receipts), err)
			}
			if result.Receipts[0].RequestHash != result.Receipts[1].RequestHash {
				t.Fatal("complete regeneration changed the authorized request snapshot")
			}
		})
	}
}
