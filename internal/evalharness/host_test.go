package evalharness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/stretchr/testify/require"
)

type scriptedAnswer func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error)

func (f scriptedAnswer) Complete(ctx context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return f(ctx, request)
}

func TestConditionProgressCountsCompletedAndFailedAnswers(t *testing.T) {
	question, observation, bySource, mapping, canonical := scoringFixture()
	failedQuestion := question
	failedQuestion.ID, failedQuestion.Query = "failed-question", "Provider failure"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "recall_memory", r.Header.Get("Mcp-Name"))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"structuredContent": observation.Initial.Result}}))
	}))
	defer server.Close()
	var logs bytes.Buffer
	client := &HTTPClient{BaseURL: server.URL, APIKey: "synthetic", Client: server.Client(), Capture: captureFixture(&logs)}
	transport := scriptedAnswer(func(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		var input struct{ Question string }
		require.NoError(t, json.Unmarshal([]byte(request.Messages[1].Content), &input))
		if input.Question == failedQuestion.Query {
			return modelprovider.StructuredResult{}, errors.New("provider unavailable")
		}
		body, err := json.Marshal(hostAction{Action: "answer", Answer: "Postgres", Citations: []Citation{}})
		return modelprovider.StructuredResult{Content: string(body)}, err
	})
	corpus := []CorpusItem{}
	for _, item := range bySource {
		corpus = append(corpus, item)
	}
	scores, err := runCondition(context.Background(), Manifest{Model: "synthetic", ContextWindowTokens: 500000}, corpus, []Question{question, failedQuestion}, mapping, canonical, client, transport)
	require.NoError(t, err)
	require.Len(t, scores, 4)
	progress := capturedProgress(t, logs.Bytes())
	require.GreaterOrEqual(t, len(progress), 2)
	require.Equal(t, captureProgress{Stage: "questions", Condition: "off", Unit: "answers", Total: 4, Remaining: 4}, progress[0])
	require.Equal(t, captureProgress{Stage: "questions", Condition: "off", Unit: "answers", Total: 4, Completed: 2, Failed: 2}, progress[len(progress)-1])
}

func TestIndependentHostsShareInitialRecallAndEnforceEightCalls(t *testing.T) {
	additional := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		additional++
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"structuredContent": map[string]any{"results": []any{}, "related_topics": []any{}}}}))
	}))
	defer server.Close()
	client := &HTTPClient{BaseURL: server.URL, APIKey: "synthetic", Client: server.Client()}
	q, observation, _, _, _ := scoringFixture()
	q.Answers = []string{"gold-answer-canary"}
	q.GoldSources = []string{"gold-source-canary"}
	initial := observation.Initial
	requests := []modelprovider.StructuredRequest{}
	transport := scriptedAnswer(func(ctx context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		requests = append(requests, request)
		encoded, _ := json.Marshal(request.Messages)
		require.NotContains(t, string(encoded), "gold-answer-canary")
		require.NotContains(t, string(encoded), "gold-source-canary")
		var body struct {
			Policy    string `json:"policy"`
			Remaining int    `json:"remaining_memory_calls"`
		}
		for _, message := range request.Messages {
			if message.Role == "user" {
				_ = json.Unmarshal([]byte(message.Content), &body)
			}
		}
		action := hostAction{Action: "answer", Answer: "Postgres", Citations: []Citation{}}
		if body.Policy == "multi_turn" && body.Remaining > 0 {
			action.Action = "recall"
			action.Query = "additional question"
		}
		data, _ := json.Marshal(action)
		return modelprovider.StructuredResult{Content: string(data)}, nil
	})
	manifest := Manifest{Model: "synthetic-model", ContextWindowTokens: 300000}
	single := RunHost(context.Background(), client, transport, manifest, HostQuestion{ID: q.ID, Query: q.Query}, initial, "single", "off")
	multi := RunHost(context.Background(), client, transport, manifest, HostQuestion{ID: q.ID, Query: q.Query}, initial, "multi_turn", "off")
	require.Empty(t, single.Error)
	require.Empty(t, multi.Error)
	require.Len(t, single.Calls, 1)
	require.Len(t, multi.Calls, 8)
	require.Equal(t, 7, additional)
	require.Equal(t, initial, single.Calls[0])
	require.Equal(t, initial, multi.Calls[0])
	require.Equal(t, requests[0].Messages[0], requests[1].Messages[0])
	require.Len(t, requests[1].Messages, 2, "the second policy starts an independent conversation")
	for _, attempt := range multi.Attempts {
		require.Equal(t, "unavailable", attempt.UsageState)
	}
	_, present := initial.Result["unexpected_mutation"]
	require.False(t, present)
}

func TestHostRejectsInventedTopicsTraceAndMalformedCompleteResponses(t *testing.T) {
	_, observation, _, _, _ := scoringFixture()
	calls := []MemoryCall{observation.Initial}
	for _, action := range []hostAction{{Action: "trace", EvidenceID: "undelivered", Citations: []Citation{}}, {Action: "topic", TopicID: "invented", Citations: []Citation{}}} {
		require.Error(t, validateHostAction(action, calls, []string{"trace", "topic"}))
	}
	calls[0].Result["related_topics"] = []any{map[string]any{"topic_id": "returned"}}
	require.NoError(t, validateHostAction(hostAction{Action: "topic", TopicID: "returned", Citations: []Citation{}}, calls, []string{"topic"}))
	require.NoError(t, validateHostAction(hostAction{Action: "trace", EvidenceID: testEvidenceA1, Citations: []Citation{}}, calls, []string{"trace"}))
	count := 0
	transport := scriptedAnswer(func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		count++
		return modelprovider.StructuredResult{Content: `{"action":"answer"}`, UsageAvailable: true, PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}, nil
	})
	result := RunHost(context.Background(), &HTTPClient{}, transport, Manifest{Model: "fixture", ContextWindowTokens: 300000}, HostQuestion{ID: "q", Query: "question"}, observation.Initial, "single", "off")
	require.Equal(t, 3, count)
	require.Contains(t, result.Error, "regeneration exhausted")
	require.Len(t, result.Attempts, 3)
	require.Equal(t, 5, result.Attempts[0].Total)
	count = 0
	result = RunHost(context.Background(), &HTTPClient{}, transport, Manifest{Model: "fixture", ContextWindowTokens: 1}, HostQuestion{ID: "q", Query: strings.Repeat("overlong ", 100)}, observation.Initial, "single", "off")
	require.Contains(t, result.Error, "context")
	require.Zero(t, count)
}

func TestHostReturnedTopicAndAuthorizedTraceRemainIndependentAndBounded(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "delivered Evidence", true: "wrong Trace identity"}[mismatch], func(t *testing.T) {
			_, observation, _, _, _ := scoringFixture()
			initial := observation.Initial
			initial.Result["related_topics"] = []any{map[string]any{"topic_id": "returned-topic"}}
			var logs bytes.Buffer
			seen := make(chan map[string]any, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var rpc struct {
					Params struct {
						Name      string
						Arguments map[string]any
					}
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&rpc))
				seen <- rpc.Params.Arguments
				var result map[string]any
				if rpc.Params.Name == "recall_memory" {
					require.Equal(t, []any{"returned-topic"}, rpc.Params.Arguments["topic_ids"])
					require.Contains(t, rpc.Params.Arguments["known_evidence_ids"], testEvidenceA1)
					result = map[string]any{"results": []any{}, "related_topics": []any{}}
				} else {
					require.Equal(t, "trace_memory", rpc.Params.Name)
					require.Equal(t, testEvidenceA1, rpc.Params.Arguments["evidence_id"])
					require.Equal(t, true, rpc.Params.Arguments["include_evidence_content"])
					require.EqualValues(t, 16384, rpc.Params.Arguments["max_fragment_content_runes"])
					id := testEvidenceA1
					if mismatch {
						id = testEvidenceA2
					}
					result = map[string]any{"evidence": map[string]any{"evidence_id": id, "content": "Exact canonical Evidence."}, "occurrences": []any{map[string]any{"content": "Exact source occurrence."}}}
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"structuredContent": result}}))
			}))
			defer server.Close()
			client := &HTTPClient{BaseURL: server.URL, APIKey: "synthetic", Client: server.Client(), Capture: captureFixture(&logs)}
			turns := 0
			transport := scriptedAnswer(func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
				turns++
				action := hostAction{Citations: []Citation{}}
				switch turns {
				case 1:
					action.Action, action.TopicID = "topic", "returned-topic"
				case 2:
					action.Action, action.EvidenceID = "trace", testEvidenceA1
				default:
					action.Action, action.Answer = "answer", "PostgreSQL"
					action.Citations = []Citation{{EvidenceID: testEvidenceA1, Quote: "Exact canonical Evidence."}}
				}
				body, err := json.Marshal(action)
				return modelprovider.StructuredResult{Content: string(body), UsageAvailable: true, PromptTokens: 2, CompletionTokens: 3, TotalTokens: 8}, err
			})
			result := RunHost(context.Background(), client, transport, Manifest{Model: "synthetic", ContextWindowTokens: 300000}, HostQuestion{ID: "question", Query: "public question"}, initial, "multi_turn", "off")
			require.Len(t, result.Calls, 3)
			require.Equal(t, 2, len(seen))
			if mismatch {
				require.Contains(t, result.Error, "Trace response differs")
				require.Equal(t, 2, turns)
				require.Empty(t, deliveredText(result.Calls[2]))
			} else {
				require.Empty(t, result.Error)
				require.Equal(t, 3, turns)
				require.Equal(t, []string{"Exact canonical Evidence.", "Exact source occurrence."}, deliveredText(result.Calls[2])[testEvidenceA1])
			}
			for _, attempt := range result.Attempts {
				require.Equal(t, "unreconciled", attempt.UsageState)
			}
			require.Equal(t, initial, result.Calls[0])
		})
	}
}

func TestHostRetainsFailedAttemptsAndStopsAfterInitialRecallFailure(t *testing.T) {
	_, observation, _, _, _ := scoringFixture()
	var logs bytes.Buffer
	capture := captureFixture(&logs)
	const key = "host-key-canary-4193"
	capture.Protector = NewRunnerCredentialProtector(key)
	client := &HTTPClient{Capture: capture}
	calls := 0
	transport := scriptedAnswer(func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		calls++
		return modelprovider.StructuredResult{}, errors.New("provider unavailable " + key)
	})
	manifest := Manifest{Model: "synthetic", ContextWindowTokens: 300000}
	result := RunHost(context.Background(), client, transport, manifest, HostQuestion{ID: "question", Query: "public question"}, observation.Initial, "single", "off")
	require.Equal(t, 1, calls)
	require.Contains(t, result.Error, "provider unavailable")
	require.NotContains(t, result.Error, key)
	require.Len(t, result.Attempts, 1)
	require.Equal(t, "unavailable", result.Attempts[0].UsageState)
	failed := observation.Initial
	failed.Error = "Recall unavailable"
	result = RunHost(context.Background(), client, transport, manifest, HostQuestion{}, failed, "single", "off")
	require.Equal(t, 1, calls)
	require.Equal(t, "initial Recall failed", result.Error)
	require.Empty(t, result.Attempts)
	result = RunHost(context.Background(), client, nil, manifest, HostQuestion{}, observation.Initial, "single", "off")
	require.Equal(t, "answer transport unavailable", result.Error)
}
