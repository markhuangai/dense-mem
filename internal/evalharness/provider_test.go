package evalharness

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func TestHTTPAnswerAdapterRegeneratesIncompleteEnvelopesAndKeepsDenominators(t *testing.T) {
	complete, err := json.Marshal(hostAction{Action: "answer", Answer: "Postgres", Citations: []Citation{}})
	require.NoError(t, err)
	valid, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(complete)}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 2, "completion_tokens": 3, "total_tokens": 5}})
	require.NoError(t, err)
	for name, broken := range map[string]string{
		"malformed envelope": `{"choices":`,
		"empty choices":      `{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`,
		"unfinished choice":  `{"choices":[{"message":{"content":"{}"},"finish_reason":"length"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`,
	} {
		for _, recovery := range []bool{true, false} {
			t.Run(name+map[bool]string{true: "/recover", false: "/exhaust"}[recovery], func(t *testing.T) {
				calls := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.Header().Set("Content-Type", "application/json")
					if recovery && calls == 2 {
						_, _ = w.Write(valid)
					} else {
						_, _ = w.Write([]byte(broken))
					}
				}))
				defer upstream.Close()
				question, observation, corpus, mapping, canonical := scoringFixture()
				result := RunHost(context.Background(), &HTTPClient{}, AnswerTransport{URL: upstream.URL, Key: "synthetic", Client: upstream.Client()}, Manifest{Model: "fixture", ContextWindowTokens: 300000}, HostQuestion{ID: question.ID, Query: question.Query}, observation.Initial, "single", "off")
				if recovery {
					require.Empty(t, result.Error)
					require.Equal(t, 2, calls)
				} else {
					require.Contains(t, result.Error, "regeneration exhausted")
					require.Equal(t, 3, calls)
				}
				require.Len(t, result.Attempts, calls)
				require.NotEmpty(t, result.Attempts[0].Error)
				if name == "malformed envelope" {
					require.Equal(t, "unavailable", result.Attempts[0].UsageState)
				} else {
					require.Equal(t, 5, result.Attempts[0].Total)
				}
				observation.Hosts[0] = result
				scores, err := ScoreObservation(question, observation, corpus, mapping, canonical)
				require.NoError(t, err)
				require.Equal(t, recovery, scores[0].Completed)
				report := NewReport(Manifest{}, 3, 1)
				report.Scores = scores
				report.CaptureComplete = true
				report.ImportedRows = 3
				require.NoError(t, FinalizeReport(&report))
				require.Equal(t, 4, report.ExpectedAnswers)
				if recovery {
					require.Equal(t, 2, report.CompletedAnswers)
				} else {
					require.Equal(t, 1, report.CompletedAnswers)
				}
			})
		}
	}
}

func TestHTTPAnswerAdapterKeepsPartialUsageUnavailableFields(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"total_tokens":5}}`))
	}))
	defer upstream.Close()
	result, err := (AnswerTransport{URL: upstream.URL, Key: "synthetic", Client: upstream.Client()}).Complete(context.Background(), modelprovider.StructuredRequest{})
	require.NoError(t, err)
	require.True(t, result.UsageAvailable)
	require.Equal(t, 2, result.PromptTokens)
	require.Equal(t, -1, result.CompletionTokens)
	require.Equal(t, 5, result.TotalTokens)
}

func TestAnswerWireCaptureProtectsFailedResponseAndPreservesExactBody(t *testing.T) {
	var output bytes.Buffer
	capture := captureFixture(&output)
	key := "dedicated-provider-canary"
	capture.Protector = observability.NewCredentialProtector(key)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+key, r.Header.Get("Authorization"))
		w.WriteHeader(503)
		_, _ = w.Write([]byte("public failure detail " + key))
	}))
	defer upstream.Close()
	_, err := (AnswerTransport{URL: upstream.URL, Key: key, Client: upstream.Client(), Capture: capture}).Complete(context.Background(), modelprovider.StructuredRequest{Model: "fixture"})
	require.Error(t, err)
	var protected strings.Builder
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		var envelope struct {
			Record string `json:"record"`
		}
		require.NoError(t, json.Unmarshal(line, &envelope))
		body, err := base64.StdEncoding.DecodeString(envelope.Record)
		require.NoError(t, err)
		var record captureRecord
		require.NoError(t, json.Unmarshal(body, &record))
		protected.WriteString(record.Chunk.Body)
	}
	require.Contains(t, protected.String(), "public failure detail")
	require.NotContains(t, protected.String(), key)
	require.NotContains(t, protected.String(), "Authorization")
	require.NotContains(t, protected.String(), upstream.URL)
}

func TestCanceledHostStartsNoFurtherProviderAttempts(t *testing.T) {
	_, observation, _, _, _ := scoringFixture()
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	transport := scriptedAnswer(func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		calls++
		cancel()
		return modelprovider.StructuredResult{Content: `{"action":"answer"}`}, nil
	})
	result := RunHost(ctx, &HTTPClient{}, transport, Manifest{Model: "fixture", ContextWindowTokens: 500000}, HostQuestion{ID: "q", Query: "question"}, observation.Initial, "single", "off")
	require.Equal(t, 1, calls)
	require.Len(t, result.Attempts, 1)
	require.Contains(t, result.Error, "canceled")
}
