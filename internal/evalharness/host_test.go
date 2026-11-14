package evalharness

import (
	"context"
	"encoding/json"
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
