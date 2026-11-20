package evalharness

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	for _, event := range capturedEvents(t, output.Bytes()) {
		body, err := json.Marshal(event)
		require.NoError(t, err)
		protected.Write(body)
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

const completeFixtureAnswer = `{"choices":[{"message":{"content":"{\"action\":\"answer\",\"query\":\"\",\"topic_id\":\"\",\"evidence_id\":\"\",\"answer\":\"Postgres\",\"citations\":[]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`

func TestAnswerTransportRetriesKeepOneTurnAndOneBudget(t *testing.T) {
	for _, scenario := range []string{"reset and gateway timeout", "budget across regeneration", "permanent status"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var requests []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("reading answer request: %v", err)
					return
				}
				mu.Lock()
				requests = append(requests, string(body))
				count := len(requests)
				mu.Unlock()
				switch {
				case scenario == "permanent status":
					w.WriteHeader(http.StatusUnauthorized)
				case count == 1:
					w.WriteHeader(524)
				case scenario == "budget across regeneration" && count == 2:
					_, _ = w.Write([]byte(`{"choices":[]}`))
				case scenario == "budget across regeneration":
					w.WriteHeader(http.StatusServiceUnavailable)
				case count == 2:
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Errorf("hijacking fixture connection: %v", err)
						return
					}
					if tcp, ok := conn.(*net.TCPConn); ok {
						_ = tcp.SetLinger(0)
					}
					_ = conn.Close()
				default:
					_, _ = w.Write([]byte(completeFixtureAnswer))
				}
			}))
			defer upstream.Close()
			question, observation, _, _, _ := scoringFixture()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			started := time.Now()
			client := &HTTPClient{Capture: captureFixture(&bytes.Buffer{})}
			result := RunHost(ctx, client, AnswerTransport{URL: upstream.URL, Client: upstream.Client()},
				Manifest{Model: "fixture", ContextWindowTokens: 300000}, HostQuestion{ID: question.ID, Query: question.Query}, observation.Initial, "single", "off")
			mu.Lock()
			defer mu.Unlock()
			switch scenario {
			case "reset and gateway timeout":
				require.Empty(t, result.Error)
				require.Equal(t, "Postgres", result.Answer)
				require.Len(t, result.Attempts, 3)
				require.Equal(t, requests[0], requests[1])
				require.Equal(t, requests[0], requests[2])
				require.Equal(t, "unavailable", result.Attempts[0].UsageState)
				require.Contains(t, result.Attempts[1].Error, "HTTP transport failed")
				require.Equal(t, "observed", result.Attempts[2].UsageState)
				require.GreaterOrEqual(t, time.Since(started), 3*time.Second)
			case "budget across regeneration":
				require.Contains(t, result.Error, "503")
				require.Len(t, result.Attempts, 4)
				require.Equal(t, requests[0], requests[1])
				require.Equal(t, requests[2], requests[3])
				require.NotEqual(t, requests[1], requests[2])
			case "permanent status":
				require.Contains(t, result.Error, "401")
				require.Len(t, result.Attempts, 1)
			}
			require.Len(t, result.Calls, 1)
			require.Len(t, requests, len(result.Attempts))
			for i, attempt := range result.Attempts {
				require.Equal(t, i+1, attempt.Number)
			}
		})
	}
}

type observedAnswerWait struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *observedAnswerWait) Deadline() (time.Time, bool) {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Deadline()
}

func TestAnswer429HoldsOtherWorkersAndCancellationInterruptsTheHold(t *testing.T) {
	var count atomic.Int32
	dispatched := make(chan time.Time, 4)
	releaseBody := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dispatched <- time.Now()
		if count.Add(1) == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-releaseBody:
				_, _ = w.Write([]byte("provider rate limited the answer"))
			}
			return
		}
		_, _ = w.Write([]byte(completeFixtureAnswer))
	}))
	defer upstream.Close()
	question, observation, _, _, _ := scoringFixture()
	client := &HTTPClient{Capture: captureFixture(&bytes.Buffer{})}
	transport := AnswerTransport{URL: upstream.URL, Client: upstream.Client()}
	manifest := Manifest{Model: "fixture", ContextWindowTokens: 300000}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run := func(ctx context.Context) HostResult {
		return RunHost(ctx, client, transport, manifest, HostQuestion{ID: question.ID, Query: question.Query}, observation.Initial, "single", "off")
	}
	results := make(chan HostResult, 2)
	go func() { results <- run(ctx) }()
	var heldUntil time.Time
	require.Eventually(t, func() bool {
		client.answerAdmission.mu.Lock()
		defer client.answerAdmission.mu.Unlock()
		heldUntil = client.answerAdmission.until
		return !heldUntil.IsZero()
	}, 4*time.Second, 5*time.Millisecond)
	first := <-dispatched
	require.GreaterOrEqual(t, heldUntil.Sub(first), 3*time.Second)
	cancelled, stop := context.WithCancel(ctx)
	defer stop()
	waiting := &observedAnswerWait{Context: cancelled, waiting: make(chan struct{})}
	stopped := make(chan HostResult, 1)
	go func() { stopped <- run(waiting) }()
	select {
	case <-waiting.waiting:
	case <-ctx.Done():
		t.Fatal("answer worker did not enter the 429 wait")
	}
	stop()
	select {
	case result := <-stopped:
		require.Contains(t, result.Error, "canceled")
		require.Empty(t, result.Attempts)
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt the 429 wait")
	}
	go func() { results <- run(ctx) }()
	select {
	case <-dispatched:
		t.Fatal("another worker dispatched while the 429 body was still open")
	case <-time.After(100 * time.Millisecond):
	}
	require.EqualValues(t, 1, count.Load())
	close(releaseBody)
	for range 2 {
		require.Empty(t, (<-results).Error)
		require.False(t, (<-dispatched).Before(heldUntil))
	}
	require.EqualValues(t, 3, count.Load())
}

func TestAnswerRetryAfterAcceptsDeltaAndHTTPDate(t *testing.T) {
	now := time.Date(2026, 11, 20, 4, 0, 0, 0, time.UTC)
	require.Equal(t, now.Add(7*time.Second), answerRetryAfter("7", now))
	require.True(t, now.Add(time.Minute).Equal(answerRetryAfter(now.Add(time.Minute).Format(http.TimeFormat), now)))
	for _, header := range []string{"9223372037", "999999999999999999999"} {
		require.Equal(t, now.Add(time.Duration(1<<63-1)), answerRetryAfter(header, now))
	}
	for _, header := range []string{"", "-1", "+1", "garbage", "999999999999999999999junk"} {
		require.True(t, answerRetryAfter(header, now).IsZero())
	}
}
