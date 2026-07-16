package evalharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunAIJudgeUsesGPT55AndRepairsInSameConversation(t *testing.T) {
	var requests []aiJudgeChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer judge-key", r.Header.Get("Authorization"))
		var request aiJudgeChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		requests = append(requests, request)
		require.Equal(t, "gpt-5.5", request.Model)
		require.Equal(t, "dense_mem_recall_judge", request.ResponseFormat.JSONSchema.Name)
		if len(requests) == 1 {
			writeAIJudgeResponse(t, w, `{
				"verdict":"pass",
				"answerability_score":0,
				"relevance_score":5,
				"completeness_score":5,
				"faithfulness_score":5,
				"generated_answer":"Dense-Mem uses Postgres.",
				"missing_information":[],
				"misleading_information":[],
				"rationale":"The response contains the answer."
			}`)
			return
		}
		require.Len(t, request.Messages, 4)
		require.Equal(t, requests[0].Messages[:2], request.Messages[:2])
		require.Equal(t, "assistant", request.Messages[2].Role)
		require.Contains(t, request.Messages[2].Content, `"answerability_score":0`)
		require.Equal(t, "user", request.Messages[3].Role)
		require.Contains(t, request.Messages[3].Content, "answerability_score must be between 1 and 5")
		require.Contains(t, request.Messages[3].Content, "complete replacement response")
		writeAIJudgeResponse(t, w, `{
			"verdict":"pass",
			"answerability_score":5,
			"relevance_score":5,
			"completeness_score":5,
			"faithfulness_score":5,
			"generated_answer":"Dense-Mem uses Postgres.",
			"missing_information":[],
			"misleading_information":[],
			"rationale":"The initial response directly contains the required fact."
		}`)
	}))
	defer server.Close()

	scores, summary, err := RunAIJudge(context.Background(), JudgeOptions{
		Enabled:     true,
		BaseURL:     server.URL,
		APIKey:      "judge-key",
		Model:       "gpt-5.5",
		Concurrency: 1,
		Timeout:     time.Second,
	}, []CorpusItem{{
		SourceDocID: "doc-1",
		Content:     "Dense-Mem uses Postgres.",
	}}, map[string]Case{
		"case-1": {CaseID: "case-1", Query: "What does Dense-Mem use?"},
	}, map[string]QRel{
		"case-1": {CaseID: "case-1", RequiredRefs: []Ref{{Type: "source_doc", SourceDocID: "doc-1"}}},
	}, map[string]AnswerLabel{
		"case-1": {CaseID: "case-1"},
	}, []RecallTrace{{
		CaseID: "case-1",
		InitialResponse: map[string]any{
			"results": []any{map[string]any{
				"evidence_id": "evidence-1",
				"context":     "Dense-Mem uses Postgres.",
			}},
			"discovery_paths": []any{},
		},
	}})

	require.NoError(t, err)
	require.Len(t, requests, 2)
	require.Len(t, scores, 1)
	require.Equal(t, "gpt-5.5", scores[0].Model)
	require.NotEmpty(t, scores[0].InputSHA256)
	require.Equal(t, 2, scores[0].Attempts)
	require.Equal(t, "pass", scores[0].Verdict)
	require.Equal(t, 1.0, summary.PassRate)
	require.Equal(t, 5.0, summary.AverageCompletenessScore)
}

func TestRunAIJudgeResumesOnlyExactModelAndInputHash(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		writeAIJudgeResponse(t, w, `{
			"verdict":"pass",
			"answerability_score":5,
			"relevance_score":5,
			"completeness_score":5,
			"faithfulness_score":5,
			"generated_answer":"answer",
			"missing_information":[],
			"misleading_information":[],
			"rationale":"The response contains the required answer."
		}`)
	}))
	defer server.Close()

	opts := JudgeOptions{Enabled: true, BaseURL: server.URL, APIKey: "key", Model: "gpt-5.5", Concurrency: 1}
	corpus := []CorpusItem{{SourceDocID: "doc-1", Content: "gold"}}
	cases := map[string]Case{"case-1": {CaseID: "case-1", Query: "query"}}
	qrels := map[string]QRel{"case-1": {CaseID: "case-1", RequiredRefs: []Ref{{SourceDocID: "doc-1"}}}}
	trace := RecallTrace{CaseID: "case-1", InitialResponse: map[string]any{"results": []any{map[string]any{"evidence_id": "evidence-1", "context": "answer"}}}}

	scores, _, err := RunAIJudge(context.Background(), opts, corpus, cases, qrels, nil, []RecallTrace{trace})
	require.NoError(t, err)
	require.Equal(t, 1, calls)

	opts.ResumeScores = scores
	resumed, _, err := RunAIJudge(context.Background(), opts, corpus, cases, qrels, nil, []RecallTrace{trace})
	require.NoError(t, err)
	require.Equal(t, scores, resumed)
	require.Equal(t, 1, calls, "matching score should not call the provider")

	trace.InitialResponse["results"] = []any{map[string]any{"evidence_id": "evidence-1", "context": "changed answer"}}
	changed, _, err := RunAIJudge(context.Background(), opts, corpus, cases, qrels, nil, []RecallTrace{trace})
	require.NoError(t, err)
	require.Equal(t, 2, calls, "changed input must be rejudged")
	require.NotEqual(t, scores[0].InputSHA256, changed[0].InputSHA256)
}

func TestRunAIJudgeFailsBeforeProviderCallWhenInitialResponseMissing(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()

	_, _, err := RunAIJudge(context.Background(), JudgeOptions{
		Enabled: true,
		BaseURL: server.URL,
		APIKey:  "judge-key",
		Model:   "gpt-5.5",
	}, nil, map[string]Case{
		"case-1": {CaseID: "case-1", Query: "query"},
	}, map[string]QRel{
		"case-1": {CaseID: "case-1", RequiredRefs: []Ref{{SourceDocID: "doc-1"}}},
	}, nil, []RecallTrace{{CaseID: "case-1"}})

	require.ErrorContains(t, err, `case "case-1" is missing the exact initial_response`)
	require.False(t, called)
}

func writeAIJudgeResponse(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{
			"message": map[string]any{"content": content},
		}},
	}))
}
