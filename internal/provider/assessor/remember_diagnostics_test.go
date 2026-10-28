package assessorprovider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

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

func TestRememberHTTPRepairPreservesSupportedClaimsAndPrivateCodes(t *testing.T) {
	for _, test := range []struct{ name, code, message string }{
		{"predicate kind", "predicate_subject_kind_mismatch", "same key cannot change"},
		{"unknown marker", "range_boundary_unknown", "boundary_text"},
		{"support containment", "range_not_covered", "same evidence_id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, _ := semanticAssessmentSubmissionContractTestRequest(t)
			if test.name == "predicate kind" {
				request.PredicateOptions[0].AllowedSubjectKinds = []string{"organization"}
			}
			var requests []openAIVerifierRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var wire openAIVerifierRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&wire))
				requests = append(requests, wire)
				response := semanticAssessmentTestResponse()
				split := &response.RelationshipResults[0].Splits[0]
				if len(requests) == 1 {
					switch test.name {
					case "unknown marker":
						split.PredicateRange.StartRef = "unknown-marker-canary"
					case "support containment":
						evidence := PrepareSemanticAssessmentEvidence(request.Evidence[0])
						split.SupportRanges = []SemanticAssessmentGroundedRange{semanticAssessmentTestRange(evidence, 0, 4)}
					}
				} else {
					var feedback struct {
						ValidationErrors []map[string]string `json:"validation_errors"`
						Instruction      string              `json:"instruction"`
					}
					require.NoError(t, json.Unmarshal([]byte(wire.Messages[len(wire.Messages)-1].Content), &feedback))
					require.NotEmpty(t, feedback.ValidationErrors)
					actionable := false
					for _, issue := range feedback.ValidationErrors {
						require.NotContains(t, issue, "code")
						actionable = actionable || strings.Contains(issue["message"], test.message)
					}
					require.True(t, actionable, "repair feedback must describe the rejected field: %v", feedback.ValidationErrors)
					require.Contains(t, feedback.Instruction, "known_predicate_key remains binding")
					require.Contains(t, feedback.Instruction, "distinct canonical key")
					require.Contains(t, feedback.Instruction, "Preserve valid unrelated results")
					require.Equal(t, requests[0].Messages, wire.Messages[:2])
					require.Equal(t, requests[0].ResponseFormat, wire.ResponseFormat)
					if test.name == "predicate kind" {
						split.PredicateStatus, split.PredicateKey, split.PredicateVersion = "registration_required", nil, nil
						split.PredicateRegistration = &SemanticAssessmentPredicateRegistration{PredicateKey: "person_works_on", RelationshipKind: "state", CurrentCardinality: "many"}
					}
				}
				content, err := json.Marshal(response)
				require.NoError(t, err)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(content)}}}}))
			}))
			defer server.Close()
			provider := NewOpenAIAssessor(newTestVerifierConfig(server.URL, "key", "assessor-model"), server.Client())
			session, first, err := provider.Assess(context.Background(), request)
			require.NoError(t, err)
			require.NotEmpty(t, first.ValidationErrors)
			require.Equal(t, test.code, first.ValidationErrors[0].Code)
			second, err := provider.Repair(context.Background(), session, SemanticAssessmentRepairRequest{Request: request, ValidationErrors: first.ValidationErrors})
			require.NoError(t, err)
			require.Empty(t, second.ValidationErrors)
			require.Equal(t, 2, second.Turn)
			require.Len(t, second.Response.RelationshipResults, 1)
			require.Equal(t, "stored", second.Response.RelationshipResults[0].Disposition)
			require.Equal(t, first.Response.EntityResults, second.Response.EntityResults)
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
