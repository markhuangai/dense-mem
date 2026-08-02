package verifier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIVerifierAssessSubmissionCorrectsClientProposalCorrespondence(t *testing.T) {
	var requests []openAIVerifierRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openAIVerifierRequest
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			http.Error(w, "invalid assessor request", http.StatusBadRequest)
			return
		}
		requests = append(requests, request)
		assert.Equal(t, SubmissionAssessmentSchemaName, request.ResponseFormat.JSONSchema.Name)
		assert.Contains(t, request.Messages[0].Content, "Return exactly one entity_result for every client proposal entity")

		response := submissionAssessmentTestResponse()
		if len(requests) > 1 {
			response.EntityResults[0].Ref = "subject"
			response.EntityResults[1].Ref = "object"
			response.RelationshipResults[0].SubjectRef = "subject"
			response.RelationshipResults[0].ObjectRef = stringPointer("object")
		}
		content, err := json.Marshal(response)
		if !assert.NoError(t, err) {
			http.Error(w, "invalid assessor response", http.StatusInternalServerError)
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(content)}}},
		}))
	}))
	defer srv.Close()

	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	req, _ := semanticAssessmentTestRequest(t)
	req.RequiredSubmissionProposal = submissionAssessmentTestRequiredProposal()
	req.ClientProposal = map[string]any{"entities": []any{}, "relationships": []any{}}
	response, err := v.AssessSubmission(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, requests, 2)
	assert.Equal(t, 2, response.ProviderTurns)
	assert.Equal(t, "subject", response.EntityResults[0].Ref)
	assert.Equal(t, "object", response.EntityResults[1].Ref)

	var firstPayload map[string]any
	require.NoError(t, json.Unmarshal([]byte(requests[0].Messages[1].Content), &firstPayload))
	assert.Contains(t, firstPayload, "client_proposal")
	assert.NotContains(t, firstPayload, "required_submission_proposal")
	var correction semanticAssessmentCorrection
	require.NoError(t, json.Unmarshal([]byte(requests[1].Messages[3].Content), &correction))
	assert.Contains(t, semanticAssessmentJoinedErrors(correction.ValidationErrors), "must retain a submitted entity ref")
	assert.Contains(t, correction.Instruction, "submitted-proposal correspondence errors")
}

func TestOpenAIVerifierAssessSubmissionCorrectsKnownPredicateMarkedNovel(t *testing.T) {
	var requests []openAIVerifierRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openAIVerifierRequest
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			http.Error(w, "invalid assessor request", http.StatusBadRequest)
			return
		}
		requests = append(requests, request)
		assert.Contains(t, request.Messages[0].Content, "genuinely new, evidence-grounded lower_snake_case normalization")

		response := submissionAssessmentTestResponse()
		if len(requests) == 1 {
			response.RelationshipResults[0].PredicateStatus = "needs_review"
			response.RelationshipResults[0].PredicateKey = nil
			response.RelationshipResults[0].PredicateVersion = nil
			response.RelationshipResults[0].PredicateCandidate = &SubmissionPredicateCandidate{
				PredicateKey: "works_on", RelationshipKind: "state",
			}
		}
		content, err := json.Marshal(response)
		if !assert.NoError(t, err) {
			http.Error(w, "invalid assessor response", http.StatusInternalServerError)
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(content)}}},
		}))
	}))
	defer srv.Close()

	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	req, _ := semanticAssessmentTestRequest(t)
	response, err := v.AssessSubmission(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, requests, 2)
	require.Equal(t, 2, response.ProviderTurns)

	var correction semanticAssessmentCorrection
	require.NoError(t, json.Unmarshal([]byte(requests[1].Messages[3].Content), &correction))
	require.Contains(t, semanticAssessmentJoinedErrors(correction.ValidationErrors), "must identify a new predicate")
}

func TestOpenAIVerifierAssessSubmissionAcceptsValidatedSecurityConcernBeforeSemanticNormalization(t *testing.T) {
	var requests []openAIVerifierRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openAIVerifierRequest
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			http.Error(w, "invalid assessor request", http.StatusBadRequest)
			return
		}
		requests = append(requests, request)

		response := submissionAssessmentTestResponse()
		response.SecurityAssessments[0] = SubmissionSecurityAssessment{
			EvidenceID: "ev-1",
			Verdict:    "concern",
			Signals: []SemanticSecuritySignal{{
				EvidenceID: "ev-1",
				Kind:       "tool_exfiltration",
				Start:      0,
				End:        4,
			}},
			Justification: "The evidence contains an active request to use a tool for data exfiltration.",
		}
		response.RelationshipResults[0].PredicateStatus = "needs_review"
		response.RelationshipResults[0].PredicateKey = nil
		response.RelationshipResults[0].PredicateVersion = nil
		response.RelationshipResults[0].PredicateCandidate = &SubmissionPredicateCandidate{
			PredicateKey:     "works_on",
			RelationshipKind: "state",
		}
		content, err := json.Marshal(response)
		if !assert.NoError(t, err) {
			http.Error(w, "invalid assessor response", http.StatusInternalServerError)
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(content)}}},
		}))
	}))
	defer srv.Close()

	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	req, _ := semanticAssessmentTestRequest(t)
	req.RequiredSubmissionProposal = submissionAssessmentTestRequiredProposal()
	response, err := v.AssessSubmission(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.True(t, response.HasSecurityConcern())
	require.Empty(t, response.EntityResults)
	require.Empty(t, response.RelationshipResults)
}

func TestOpenAIVerifierAssessSubmissionRejectsUnpricedReportedInputOverage(t *testing.T) {
	response := submissionAssessmentTestResponse()
	content, err := json.Marshal(response)
	require.NoError(t, err)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(content)}}},
			"usage": map[string]any{
				"prompt_tokens":     DefaultSemanticAssessmentLimits().MaxInputTokens + 1,
				"completion_tokens": 0,
				"total_tokens":      DefaultSemanticAssessmentLimits().MaxInputTokens + 1,
			},
		}))
	}))
	defer srv.Close()

	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	req, _ := semanticAssessmentTestRequest(t)
	_, err = v.AssessSubmission(context.Background(), req)
	var malformed *MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	require.Equal(t, "input_budget", malformed.FailureClass)
	require.Equal(t, 1, malformed.Attempts)
	require.Equal(t, 1, requests)
}

func TestOpenAIVerifierAssessSubmissionFailsClosedAtProviderBoundary(t *testing.T) {
	t.Run("invalid request does not call provider", func(t *testing.T) {
		v := NewOpenAIVerifier(newTestVerifierConfig("https://example.invalid", "key", "assessor-model"), nil)
		_, err := v.AssessSubmission(context.Background(), SemanticAssessmentRequest{})
		var provider *ProviderError
		require.ErrorAs(t, err, &provider)
		require.Contains(t, provider.Message, "invalid submission assessment request")
	})

	t.Run("provider failure stops the conversation", func(t *testing.T) {
		requests := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests++
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
		req, _ := semanticAssessmentTestRequest(t)
		_, err := v.AssessSubmission(context.Background(), req)
		var provider *ProviderError
		require.ErrorAs(t, err, &provider)
		require.Equal(t, ProviderFailureClassHTTPServer, provider.FailureClass)
		require.Equal(t, http.StatusServiceUnavailable, provider.StatusCode)
		require.Equal(t, 1, requests)
	})

	t.Run("malformed output exhausts bounded correction", func(t *testing.T) {
		requests := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests++
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": "not-json"}}},
			}))
		}))
		defer srv.Close()

		v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
		req, _ := semanticAssessmentTestRequest(t)
		_, err := v.AssessSubmission(context.Background(), req)
		var malformed *MalformedResponseError
		require.ErrorAs(t, err, &malformed)
		require.Equal(t, "malformed_exhausted", malformed.FailureClass)
		require.Equal(t, SemanticAssessmentMaxProviderTurns, malformed.Attempts)
		require.Equal(t, SemanticAssessmentMaxProviderTurns, requests)
	})
}
