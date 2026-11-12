package assessorprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

func TestRememberHTTPRoutedRawValidationRepair(t *testing.T) {
	for _, route := range []struct {
		name                  string
		security, equivalence bool
	}{{"unrouted", false, false}, {"security", true, false}, {"equivalence", false, true}, {"both", true, true}} {
		for _, exhausted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/exhausted=%t", route.name, exhausted), func(t *testing.T) {
				request, _ := semanticAssessmentPreparedTestRequest(t)
				valid := semanticAssessmentTestResponse()
				if route.security || route.equivalence {
					request.DecisionRouting = &assessor.SemanticAssessmentRouting{}
					if route.security {
						request.DecisionRouting.Security = valid.EvidenceSecurityResults
					}
					if route.equivalence {
						request.DecisionRouting.Equivalence = valid.EvidenceEquivalenceResults
					}
				}
				routingBefore, err := json.Marshal(request.DecisionRouting)
				require.NoError(t, err)
				var requests []openAIVerifierRequest
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var wire openAIVerifierRequest
					require.NoError(t, json.NewDecoder(r.Body).Decode(&wire))
					requests = append(requests, wire)
					if len(requests) > 1 {
						var feedback semanticAssessmentSessionRepair
						last := wire.Messages[len(wire.Messages)-1].Content
						require.NoError(t, json.Unmarshal([]byte(last), &feedback))
						require.Equal(t, []assessor.SemanticValidationError{{Field: "request_id", Message: "is required"}}, feedback.ValidationErrors)
						require.NotContains(t, last, "private-code-canary")
						require.Equal(t, requests[0].Messages, wire.Messages[:2])
						require.Equal(t, requests[0].ResponseFormat, wire.ResponseFormat)
					}
					var fields map[string]any
					require.NoError(t, json.Unmarshal(mustMarshalJSON(t, valid), &fields))
					if route.security {
						delete(fields, "evidence_security_results")
					}
					if route.equivalence {
						delete(fields, "evidence_equivalence_results")
					}
					if len(requests) == 1 || exhausted {
						delete(fields, "request_id")
					}
					require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(mustMarshalJSON(t, fields))}}}}))
				}))
				defer server.Close()
				provider := NewOpenAIAssessor(newTestVerifierConfig(server.URL, "key", "assessor-model"), server.Client())
				session, turn, err := provider.Assess(context.Background(), request)
				require.NoError(t, err)
				for {
					require.Equal(t, assessor.SemanticAssessmentResponse{}, turn.Response)
					require.Equal(t, "response_json", turn.ValidationStage)
					require.Equal(t, []assessor.SemanticValidationError{{Field: "request_id", Message: "is required"}}, turn.ValidationErrors)
					turn.ValidationErrors[0].Code = "private-code-canary"
					turn, err = provider.Repair(context.Background(), session, assessor.SemanticAssessmentRepairRequest{Request: request, ValidationErrors: turn.ValidationErrors})
					if err != nil || len(turn.ValidationErrors) == 0 {
						break
					}
				}
				if exhausted {
					var malformed *MalformedResponseError
					require.ErrorAs(t, err, &malformed)
					require.Equal(t, "malformed_exhausted", malformed.FailureClass)
					require.Equal(t, 3, malformed.Attempts)
					require.Len(t, requests, 3)
					require.Equal(t, assessor.SemanticAssessmentResponse{}, turn.Response)
				} else {
					require.NoError(t, err)
					require.Len(t, requests, 2)
					require.Equal(t, valid.RequestID, turn.Response.RequestID)
					require.Empty(t, turn.Response.EvidenceConflictResults)
					require.Equal(t, valid.EvidenceSecurityResults, turn.Response.EvidenceSecurityResults)
					require.Equal(t, valid.EvidenceEquivalenceResults, turn.Response.EvidenceEquivalenceResults)
					changed := request
					changed.RequestID = "different-request"
					_, err = provider.Repair(context.Background(), session, assessor.SemanticAssessmentRepairRequest{Request: changed})
					require.ErrorContains(t, err, "changed the submitted envelope")
					changed = request
					changed.DecisionRouting = &assessor.SemanticAssessmentRouting{Security: []assessor.SemanticAssessmentEvidenceSecurityResult{}}
					_, err = provider.Repair(context.Background(), session, assessor.SemanticAssessmentRepairRequest{Request: changed})
					require.ErrorContains(t, err, "changed the submitted envelope")
					require.Len(t, requests, 2)
				}
				routingAfter, err := json.Marshal(request.DecisionRouting)
				require.NoError(t, err)
				require.Equal(t, routingBefore, routingAfter)
			})
		}
	}
}

func TestRememberTurnDiagnosticsSurviveHTTPFailuresAndRepair(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				if status != http.StatusOK {
					_, _ = w.Write([]byte("upstream-canary"))
					return
				}
				var request openAIVerifierRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				if len(request.Messages) > 2 {
					require.NotContains(t, request.Messages[len(request.Messages)-1].Content, "diagnostic-canary")
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "{"}}}}))
			}))
			defer server.Close()
			provider := NewOpenAIAssessor(newTestVerifierConfig(server.URL, "key", "assessor-model"), server.Client())
			request, _ := semanticAssessmentTestRequest(t)
			session, turn, err := provider.Assess(context.Background(), request)
			require.NotNil(t, turn.Diagnostics)
			require.NotNil(t, turn.Diagnostics.QueueMS)
			require.NotNil(t, turn.Diagnostics.HTTPMS)
			if status != http.StatusOK {
				require.Error(t, err)
				require.Equal(t, "response_status", turn.Diagnostics.FailurePhase)
				require.Nil(t, turn.ValidationMS)
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, turn.ValidationErrors)
			require.NotNil(t, turn.ValidationMS)
			turn.ValidationErrors[0].Code = "diagnostic-canary"
			repaired, err := provider.Repair(context.Background(), session, assessor.SemanticAssessmentRepairRequest{Request: request, ValidationErrors: turn.ValidationErrors})
			require.NoError(t, err)
			require.NotNil(t, repaired.Diagnostics)
			require.NotNil(t, repaired.ValidationMS)
			require.Equal(t, int32(2), calls.Load())
		})
	}
}

func TestRememberTurnDiagnosticsDistinguishQueueAndHTTPTimeout(t *testing.T) {
	started := make(chan struct{}, 1)
	shutdown := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case started <- struct{}{}:
		case <-shutdown:
			return
		}
		select {
		case <-r.Context().Done():
		case <-shutdown:
		}
	}))
	t.Cleanup(func() { close(shutdown); server.Close() })
	provider := NewOpenAIAssessor(newTestVerifierConfig(server.URL, "key", "assessor-model"), server.Client())
	request, _ := semanticAssessmentTestRequest(t)
	provider.sem = make(chan struct{}, 1)
	provider.sem <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, queued, err := provider.Assess(ctx, request)
	require.Error(t, err)
	require.NotNil(t, queued.Diagnostics)
	require.Equal(t, "queue", queued.Diagnostics.FailurePhase)
	require.NotNil(t, queued.Diagnostics.QueueMS)
	require.Nil(t, queued.Diagnostics.HTTPMS)
	require.Empty(t, started)
	<-provider.sem

	for _, cancelRequest := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		t.Cleanup(cancel)
		if !cancelRequest {
			provider.httpClient.Timeout = 250 * time.Millisecond
		}
		type result struct {
			turn assessor.SemanticAssessmentTurn
			err  error
		}
		completed := make(chan result, 1)
		go func() {
			_, turn, err := provider.Assess(ctx, request)
			completed <- result{turn, err}
		}()
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("HTTP fixture did not receive the request")
		}
		if cancelRequest {
			cancel()
		}
		var got result
		select {
		case got = <-completed:
		case <-time.After(5 * time.Second):
			t.Fatal("provider did not release the cancelled HTTP request")
		}
		cancel()
		require.ErrorIs(t, got.err, assessor.ErrVerifierTimeout)
		require.NotNil(t, got.turn.Diagnostics.HTTPMS)
		require.Equal(t, "request", got.turn.Diagnostics.FailurePhase)
		require.Nil(t, got.turn.ValidationMS)
		require.Empty(t, provider.sem)
		if !cancelRequest {
			provider.httpClient.Timeout = 0
		}
	}
}
