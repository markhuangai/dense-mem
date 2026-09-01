package verifier

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/observability"
)

type assessorRoundTripper func(*http.Request) (*http.Response, error)

func (f assessorRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type assessorErrorReader struct{}

func (assessorErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func runSemanticAssessmentSessionForTest(
	ctx context.Context,
	provider *OpenAIVerifier,
	request SemanticAssessmentRequest,
) (SemanticAssessmentResponse, error) {
	session, turn, err := provider.Assess(ctx, request)
	if err != nil {
		return SemanticAssessmentResponse{}, err
	}
	for {
		if len(turn.ValidationErrors) == 0 {
			return turn.Response, nil
		}
		if turn.Turn >= SemanticAssessmentMaxProviderTurns {
			return SemanticAssessmentResponse{}, &MalformedResponseError{
				Provider:                openAIVerifierProvider,
				Message:                 "semantic assessment response remained invalid after bounded correction",
				FailureClass:            "malformed_exhausted",
				Attempts:                turn.Turn,
				ValidationStage:         turn.ValidationStage,
				ValidationFieldFamilies: semanticAssessmentValidationFieldFamilies(turn.ValidationErrors),
			}
		}
		turn, err = provider.Repair(ctx, session, SemanticAssessmentRepairRequest{
			Request:          request,
			ValidationErrors: turn.ValidationErrors,
		})
		if err != nil {
			return SemanticAssessmentResponse{}, err
		}
	}
}

func TestOpenAIVerifierAssessSemanticUsesOneTurnForValidResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]json.RawMessage
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&raw)) {
			http.Error(w, "invalid assessor request", http.StatusBadRequest)
			return
		}
		assert.NotContains(t, raw, "max_tokens")
		assert.NotContains(t, raw, "max_completion_tokens")
		var request openAIVerifierRequest
		rawJSON, err := json.Marshal(raw)
		if !assert.NoError(t, err) || !assert.NoError(t, json.Unmarshal(rawJSON, &request)) {
			http.Error(w, "invalid assessor request", http.StatusBadRequest)
			return
		}
		assert.Equal(t, "assessor-model", request.Model)
		assert.Equal(t, "dense_mem_semantic_assessment_response", request.ResponseFormat.JSONSchema.Name)
		assert.Contains(t, request.Messages[0].Content, "structure, normalization, and evidence-security assessor")
		assert.Contains(t, request.Messages[0].Content, "registration_required predicate requires null predicate_key and predicate_version")
		var payload map[string]any
		if !assert.NoError(t, json.Unmarshal([]byte(request.Messages[1].Content), &payload)) {
			http.Error(w, "invalid assessor payload", http.StatusBadRequest)
			return
		}
		assert.NotContains(t, payload, "team_id")
		assert.NotContains(t, payload, "owner_profile_id")

		content, err := json.Marshal(semanticAssessmentTestResponse())
		if !assert.NoError(t, err) {
			http.Error(w, "invalid assessor response", http.StatusInternalServerError)
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(content)}}},
			"usage":   map[string]any{"prompt_tokens": 200, "completion_tokens": 100, "total_tokens": 300},
		}))
	}))
	defer srv.Close()

	cfg := newTestVerifierConfig(srv.URL, "sk-test", "assessor-model")
	v := NewOpenAIVerifier(cfg, srv.Client())
	req, _ := semanticAssessmentTestRequest(t)
	response, err := runSemanticAssessmentSessionForTest(context.Background(), v, req)
	require.NoError(t, err)
	assert.Equal(t, "assess-1", response.RequestID)
	require.Len(t, response.RelationshipResults, 1)
	assert.Equal(t, 1, response.ProviderTurns)
	assert.Equal(t, 200, response.InputTokens)
}

func TestOpenAIVerifierRememberSessionRepairsWithRefreshedCandidates(t *testing.T) {
	var requests []openAIVerifierRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openAIVerifierRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		requests = append(requests, request)
		response := semanticAssessmentTestResponse()
		if len(requests) == 1 {
			response.EntityResults[0].GroundingRef = nil
		}
		content, err := json.Marshal(response)
		require.NoError(t, err)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(content)}}},
		}))
	}))
	defer srv.Close()

	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	request, _ := semanticAssessmentSubmissionContractTestRequest(t)
	session, first, err := v.Assess(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, session)
	require.NotEmpty(t, first.ValidationErrors)
	assert.True(t, semanticAssessmentTestHasValidationField(first.ValidationErrors, "entity_results[0].grounding_ref"))
	assert.Equal(t, 1, first.Turn)

	refreshed := request
	refreshed.EntityCandidateGroups = append([]SemanticAssessmentEntityCandidateGroup(nil), request.EntityCandidateGroups...)
	refreshed.EntityCandidateGroups[0].Candidates[0].IdentityContext = map[string]any{"source": "refreshed-catalog"}
	second, err := v.Repair(context.Background(), session, SemanticAssessmentRepairRequest{
		Request:          refreshed,
		ValidationErrors: first.ValidationErrors,
	})
	require.NoError(t, err)
	assert.Empty(t, second.ValidationErrors)
	assert.Equal(t, 2, second.Turn)
	assert.Equal(t, session.SessionID(), session.(*openAISemanticAssessmentSession).SessionID())
	require.Len(t, requests, 2)
	assert.Equal(t, []string{"system", "user", "assistant", "user"}, assessmentMessageRoles(requests[1].Messages))
	assert.Contains(t, requests[1].Messages[3].Content, "refreshed_candidate_context")
	assert.Contains(t, requests[1].Messages[3].Content, "refreshed-catalog")
}

func TestOpenAIVerifierRememberSessionRepairsMultipleCandidatesAsAmbiguous(t *testing.T) {
	var requests []openAIVerifierRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openAIVerifierRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		requests = append(requests, request)

		response := semanticAssessmentTestResponse()
		if len(requests) > 1 {
			response.EntityResults[0].Action = "ambiguous"
			response.EntityResults[0].CandidateEntityID = nil
			reason := "not_supported_by_evidence"
			response.RelationshipResults[0].Disposition = "not_supported"
			response.RelationshipResults[0].Reason = &reason
			response.RelationshipResults[0].Splits = []SemanticAssessmentRelationshipSplit{}
		}
		content, err := json.Marshal(response)
		require.NoError(t, err)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(content)}}},
		}))
	}))
	defer srv.Close()

	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	request, _ := semanticAssessmentSubmissionContractTestRequest(t)
	request.EntityCandidateGroups[0].Candidates = append(request.EntityCandidateGroups[0].Candidates, SemanticAssessmentEntityCandidate{
		EntityID:      "entity-other-mark",
		CanonicalName: "Mark Other",
		Kind:          "person",
	})
	response, err := runSemanticAssessmentSessionForTest(context.Background(), v, request)
	require.NoError(t, err)
	require.Len(t, requests, 2)
	assert.Equal(t, 2, response.ProviderTurns)
	assert.Equal(t, "ambiguous", response.EntityResults[0].Action)
	assert.Nil(t, response.EntityResults[0].CandidateEntityID)
	assert.Equal(t, "not_supported", response.RelationshipResults[0].Disposition)

	var correction semanticAssessmentSessionRepair
	require.NoError(t, json.Unmarshal([]byte(requests[1].Messages[3].Content), &correction))
	require.True(t, semanticAssessmentTestHasValidationField(correction.ValidationErrors, "entity_results[0].candidate_entity_id"))
	assert.Contains(t, correction.Instruction, "without known_entity_id")
	assert.Contains(t, correction.Instruction, "multiple compatible candidates")
	assert.Contains(t, correction.Instruction, "mark every dependent Relationship not_supported")
}

func TestOpenAIVerifierRememberSessionRejectsInvalidRepairState(t *testing.T) {
	var nilSession *openAISemanticAssessmentSession
	assert.Empty(t, nilSession.SessionID())

	v := NewOpenAIVerifier(newTestVerifierConfig("", "key", "assessor-model"), nil)
	request, _ := semanticAssessmentSubmissionContractTestRequest(t)
	prepared, validationErrors := PrepareSemanticAssessmentRequest(request, v.assessmentLimits)
	require.Empty(t, validationErrors)

	_, err := v.Repair(context.Background(), nil, SemanticAssessmentRepairRequest{Request: prepared})
	require.Error(t, err)
	var providerErr *ProviderError
	require.ErrorAs(t, err, &providerErr)
	assert.Equal(t, ProviderFailureClassRequestInvalid, providerErr.FailureClass)

	maxed := &openAISemanticAssessmentSession{id: "maxed", turn: SemanticAssessmentMaxProviderTurns}
	_, err = v.Repair(context.Background(), maxed, SemanticAssessmentRepairRequest{Request: prepared})
	var malformed *MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	assert.Equal(t, "malformed_exhausted", malformed.FailureClass)

	_, err = v.Repair(context.Background(), &openAISemanticAssessmentSession{id: "valid"}, SemanticAssessmentRepairRequest{})
	require.ErrorAs(t, err, &providerErr)
	assert.Equal(t, ProviderFailureClassRequestInvalid, providerErr.FailureClass)

	changed := prepared
	changed.RequestID = "changed-request"
	_, err = v.Repair(context.Background(), &openAISemanticAssessmentSession{id: "valid", prepared: prepared}, SemanticAssessmentRepairRequest{Request: changed})
	require.ErrorAs(t, err, &providerErr)
	assert.Equal(t, ProviderFailureClassRequestInvalid, providerErr.FailureClass)
}

func TestOpenAIVerifierRememberSessionEnforcesConversationInputBudget(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1
	v := NewOpenAIVerifierWithAssessmentLimits(newTestVerifierConfig("", "key", "assessor-model"), nil, limits)
	_, _, err := v.runRememberAssessmentTurn(context.Background(), &openAISemanticAssessmentSession{id: "budget"}, SemanticAssessmentRequest{}, []openAIVerifierMessage{{Role: "user", Content: "this cannot fit"}})
	var malformed *MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	assert.Equal(t, "input_budget", malformed.FailureClass)
}

func TestOpenAIVerifierRememberSessionRejectsProviderReportedInputOverflow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		content, err := json.Marshal(semanticAssessmentTestResponse())
		require.NoError(t, err)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(content)}}},
			"usage":   map[string]any{"prompt_tokens": 999999, "completion_tokens": 1, "total_tokens": 1000000},
		}))
	}))
	defer srv.Close()

	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	request, _ := semanticAssessmentSubmissionContractTestRequest(t)
	_, _, err := v.Assess(context.Background(), request)
	var malformed *MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	assert.Equal(t, "input_budget", malformed.FailureClass)
}

func TestOpenAIStructuredChatRecordsProviderUsageBeforeRejectingContent(t *testing.T) {
	rate := 1_000_000.0
	metrics := observability.NewPrometheusMetrics(observability.AIPricingResolverFunc(func(context.Context) (observability.AIPricing, error) {
		return observability.AIPricing{
			VerifierInputUSDPerMillionTokens:  &rate,
			VerifierOutputUSDPerMillionTokens: &rate,
		}, nil
	}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		}))
	}))
	defer srv.Close()

	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	v.SetMetrics(metrics)
	ctx := observability.WithAIOperation(context.Background(), observability.AIOperationSemanticAssessment, 1)
	_, err := v.openAIStructuredChatJSONWithUsage(ctx, "assessor-model", "schema", map[string]any{}, "system", map[string]any{})
	require.ErrorIs(t, err, ErrVerifierProvider)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "densemem_ai_operation_cost_usd_total{") {
			continue
		}
		assert.Contains(t, line, `operation="semantic_assessment"`)
		assert.Contains(t, line, `component="verifier"`)
		assert.Contains(t, line, `model="assessor-model"`)
		assert.True(t, strings.HasSuffix(line, " 15"), "cost line = %q; want 15 USD", line)
		return
	}
	t.Fatal("provider usage did not produce an AI operation cost sample")
}

func TestOpenAIStructuredChatMarksMissingUsageBeforeRejectingContent(t *testing.T) {
	metrics := observability.NewPrometheusMetrics()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{}}))
	}))
	defer srv.Close()

	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	v.SetMetrics(metrics)
	ctx := observability.WithAIOperation(context.Background(), observability.AIOperationSemanticAssessment, 1)
	_, err := v.openAIStructuredChatJSONWithUsage(ctx, "assessor-model", "schema", map[string]any{}, "system", map[string]any{})
	require.ErrorIs(t, err, ErrVerifierProvider)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if !strings.HasPrefix(line, "densemem_ai_operation_unpriced_total{") {
			continue
		}
		if strings.Contains(line, `operation="semantic_assessment"`) &&
			strings.Contains(line, `component="verifier"`) &&
			strings.Contains(line, `model="assessor-model"`) &&
			strings.Contains(line, `reason="missing_usage"`) {
			assert.True(t, strings.HasSuffix(line, " 1"), "unpriced line = %q; want one missing-usage observation", line)
			return
		}
	}
	t.Fatal("missing provider usage did not produce an unpriced operation sample")
}

func TestOpenAIVerifierAssessSemanticCorrectsMalformedContentInSameHistory(t *testing.T) {
	var requests []openAIVerifierRequest
	var invalidContent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openAIVerifierRequest
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			http.Error(w, "invalid assessor request", http.StatusBadRequest)
			return
		}
		requests = append(requests, request)

		response := semanticAssessmentTestResponse()
		if len(requests) == 1 {
			response.RequestID = "wrong-request"
		}
		content, err := json.Marshal(response)
		if !assert.NoError(t, err) {
			http.Error(w, "invalid assessor response", http.StatusInternalServerError)
			return
		}
		if len(requests) == 1 {
			invalidContent = string(content)
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(content)}}},
		}))
	}))
	defer srv.Close()

	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	req, _ := semanticAssessmentTestRequest(t)
	response, err := runSemanticAssessmentSessionForTest(context.Background(), v, req)
	require.NoError(t, err)
	require.Len(t, requests, 2)
	assert.Equal(t, 2, response.ProviderTurns)
	assert.Greater(t, response.InputTokens, req.InputTokens)
	assert.Equal(t, []string{"system", "user"}, assessmentMessageRoles(requests[0].Messages))
	assert.Equal(t, []string{"system", "user", "assistant", "user"}, assessmentMessageRoles(requests[1].Messages))
	assert.Equal(t, invalidContent, requests[1].Messages[2].Content)

	var correction semanticAssessmentSessionRepair
	require.NoError(t, json.Unmarshal([]byte(requests[1].Messages[3].Content), &correction))
	require.Len(t, correction.ValidationErrors, 1)
	assert.Equal(t, "request_id", correction.ValidationErrors[0].Field)
	assert.Contains(t, correction.ValidationErrors[0].Message, `expected "assess-1"`)
	assert.Contains(t, correction.Instruction, "complete replacement JSON object")
	assert.Contains(t, correction.Instruction, "complete predicate_registration")
}

func TestOpenAIVerifierAssessSemanticRegeneratesInvalidGroundingReference(t *testing.T) {
	var requests []openAIVerifierRequest
	var invalidContent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openAIVerifierRequest
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			http.Error(w, "invalid assessor request", http.StatusBadRequest)
			return
		}
		requests = append(requests, request)

		response := semanticAssessmentTestResponse()
		if len(requests) == 1 {
			invalidGrounding := "invented-grounding"
			response.EntityResults[0].GroundingRef = &invalidGrounding
		}
		content, err := json.Marshal(response)
		if !assert.NoError(t, err) {
			http.Error(w, "invalid assessor response", http.StatusInternalServerError)
			return
		}
		if len(requests) == 1 {
			invalidContent = string(content)
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(content)}}},
		}))
	}))
	defer srv.Close()

	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	req, _ := semanticAssessmentSubmissionContractTestRequest(t)
	response, err := runSemanticAssessmentSessionForTest(context.Background(), v, req)
	require.NoError(t, err)
	require.Len(t, requests, 2)
	assert.Equal(t, 2, response.ProviderTurns)
	assert.Equal(t, invalidContent, requests[1].Messages[2].Content)

	var correction semanticAssessmentSessionRepair
	require.NoError(t, json.Unmarshal([]byte(requests[1].Messages[3].Content), &correction))
	assert.True(t, semanticAssessmentTestHasValidationField(correction.ValidationErrors, "entity_results[0].grounding_ref"))
	assert.NotContains(t, requests[1].Messages[3].Content, "span_hints")
	assert.NotContains(t, requests[1].Messages[3].Content, "entity_selection_hints")
	assert.Contains(t, correction.Instruction, "one complete replacement JSON object")
	assert.Contains(t, correction.Instruction, "Copy only grounding_ref, start_ref, and end_ref")
}

func TestOpenAIVerifierAssessSemanticRegeneratesInvalidEntitySelection(t *testing.T) {
	var requests []openAIVerifierRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openAIVerifierRequest
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			http.Error(w, "invalid assessor request", http.StatusBadRequest)
			return
		}
		requests = append(requests, request)

		response := semanticAssessmentTestResponse()
		if len(requests) == 1 {
			response.EntityResults[0].Action = "create"
			response.EntityResults[0].CandidateEntityID = nil
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
	req, _ := semanticAssessmentSubmissionContractTestRequest(t)
	response, err := runSemanticAssessmentSessionForTest(context.Background(), v, req)
	require.NoError(t, err)
	require.Len(t, requests, 2)
	assert.Equal(t, 2, response.ProviderTurns)

	var correction semanticAssessmentSessionRepair
	require.NoError(t, json.Unmarshal([]byte(requests[1].Messages[3].Content), &correction))
	assert.True(t, semanticAssessmentTestHasValidationField(correction.ValidationErrors, "entity_results[0].action"))
	assert.NotContains(t, requests[1].Messages[3].Content, "entity_selection_hints")
}

func semanticAssessmentTestHasValidationField(errs []SemanticValidationError, field string) bool {
	for _, validationError := range errs {
		if validationError.Field == field {
			return true
		}
	}
	return false
}
func TestSemanticAssessmentValidationFieldFamilyIsBounded(t *testing.T) {
	testCases := []struct {
		field string
		want  string
	}{
		{field: "request_id", want: "request_id"},
		{field: "evidence_security_results[4].signals[0].evidence_id", want: "evidence_security_results"},
		{field: "entity_results[12].surface", want: "entity_results.span"},
		{field: "entity_results[12].candidate_entity_id", want: "entity_results.selection"},
		{field: "entity_results[12].invented_field", want: "entity_results.other"},
		{field: "relationship_results[7].predicate_key", want: "relationship_results.predicate"},
		{field: "relationship_results[7].evidence[2].evidence_id", want: "relationship_results.evidence"},
		{field: "relationship_results[7].valid_to", want: "relationship_results.temporal"},
		{field: "relationship_results[7].invented_field", want: "relationship_results.other"},
		{field: "provider supplied arbitrary field", want: "other"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.field, func(t *testing.T) {
			assert.Equal(t, testCase.want, semanticAssessmentValidationFieldFamily(testCase.field))
		})
	}
}

func TestSemanticAssessmentInstructionsExposeRequestDependentRules(t *testing.T) {
	for _, expected := range []string{
		"Never return numeric text offsets",
		"start_ref is inclusive and end_ref is exclusive",
		"return exactly one result for each submitted ref",
		"Every stored split requires exactly one of object_ref and object_value",
		`otherwise use predicate_status "registration_required" with a complete predicate_registration`,
		"registration_required predicate requires null predicate_key and predicate_version",
		"contiguous split_index entries starting at zero",
		"not_supported_by_evidence",
		"Pronouns and inferred coreference are not grounding options",
		"RFC3339",
		"hidden control rune or active markup",
	} {
		assert.Contains(t, semanticAssessmentSystemPrompt, expected)
	}
	for _, expected := range []string{
		"Never return numeric offsets",
		"Copy only grounding_ref, start_ref, and end_ref values present in the immutable request",
		"same array index",
		"Preserve every submitted ref, endpoint, typed value, polarity, and submitted temporal bounds",
		"registration_required with a complete predicate_registration",
		"one complete replacement JSON object",
	} {
		assert.Contains(t, semanticAssessmentCorrectionInstruction, expected)
	}
}

func TestOpenAIVerifierAssessSemanticStopsConversationOnProviderFailure(t *testing.T) {
	var requests []openAIVerifierRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openAIVerifierRequest
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			http.Error(w, "invalid assessor request", http.StatusBadRequest)
			return
		}
		requests = append(requests, request)
		if len(requests) == 1 {
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": "not-json"}}},
			}))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, err := w.Write([]byte("provider-private-body"))
		assert.NoError(t, err)
	}))
	defer srv.Close()

	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	req, _ := semanticAssessmentTestRequest(t)
	_, err := runSemanticAssessmentSessionForTest(context.Background(), v, req)
	require.ErrorIs(t, err, ErrVerifierProvider)
	var provider *ProviderError
	require.ErrorAs(t, err, &provider)
	assert.Equal(t, ProviderFailureClassHTTPServer, provider.FailureClass)
	assert.Equal(t, http.StatusServiceUnavailable, provider.StatusCode)
	assert.NotContains(t, err.Error(), "provider-private-body")
	require.Len(t, requests, 2)
	assert.Equal(t, []string{"system", "user", "assistant", "user"}, assessmentMessageRoles(requests[1].Messages))
}

func assessmentMessageRoles(messages []openAIVerifierMessage) []string {
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message.Role)
	}
	return roles
}

func TestSemanticAssessmentResponseForCorrectionClassifiesLocalFailures(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, validationErrors := PrepareSemanticAssessmentRequest(req, limits)
	require.Empty(t, validationErrors)
	validContent := string(mustMarshalJSON(t, semanticAssessmentTestResponse()))

	t.Run("local output budget", func(t *testing.T) {
		limited := limits
		limited.MaxOutputTokens = 1
		_, errs, stage := semanticAssessmentResponseForCorrection(
			prepared,
			openAIStructuredChatResult{Content: validContent},
			limited,
		)
		require.NotEmpty(t, errs)
		assert.Equal(t, "response_output_tokens", stage)
		assert.Equal(t, "output_tokens", errs[0].Field)
	})

	t.Run("json type", func(t *testing.T) {
		content := strings.Replace(validContent, `"request_id":"assess-1"`, `"request_id":123`, 1)
		_, errs, stage := semanticAssessmentResponseForCorrection(
			prepared,
			openAIStructuredChatResult{Content: content},
			limits,
		)
		require.NotEmpty(t, errs)
		assert.Equal(t, "response_json", stage)
		assert.Equal(t, "request_id", errs[0].Field)
		assert.Contains(t, errs[0].Message, "required JSON type")
	})

	t.Run("tokenizer", func(t *testing.T) {
		unsupported := limits
		unsupported.Tokenizer = "unsupported"
		_, errs, stage := semanticAssessmentResponseForCorrection(
			prepared,
			openAIStructuredChatResult{Content: validContent},
			unsupported,
		)
		require.NotEmpty(t, errs)
		assert.Equal(t, "response_json", stage)
		assert.Equal(t, "response", errs[0].Field)
	})

	assert.Equal(t, ProviderFailureClassHTTPUnexpected, openAIHTTPFailureClass(http.StatusFound))
}

func TestOpenAIVerifierAssessSemanticRejectsProviderBoundaries(t *testing.T) {
	testCases := []struct {
		name         string
		handler      http.HandlerFunc
		wantType     any
		wantDetail   string
		wantCalls    int
		wantAttempts int
		wantClass    string
		wantStatus   int
		forbidDetail string
	}{
		{
			name: "reported input token overage",
			handler: assessorResponseHandler(t, semanticAssessmentTestResponse(), &openAIVerifierUsage{
				PromptTokens: int64(DefaultSemanticAssessmentLimits().MaxInputTokens + 1),
			}),
			wantType:     &MalformedResponseError{},
			wantDetail:   "input tokens beyond semantic assessment limit",
			wantCalls:    1,
			wantAttempts: 1,
			wantClass:    "input_budget",
		},
		{
			name: "reported output token overage",
			handler: assessorResponseHandler(t, semanticAssessmentTestResponse(), &openAIVerifierUsage{
				CompletionTokens: int64(DefaultSemanticAssessmentLimits().MaxOutputTokens + 1),
			}),
			wantType:     &MalformedResponseError{},
			wantDetail:   "remained invalid after bounded correction",
			wantCalls:    SemanticAssessmentMaxProviderTurns,
			wantAttempts: SemanticAssessmentMaxProviderTurns,
			wantClass:    "malformed_exhausted",
		},
		{
			name:         "invalid structured content",
			handler:      assessorRawResponseHandler(t, "not-json", nil),
			wantType:     &MalformedResponseError{},
			wantDetail:   "remained invalid after bounded correction",
			wantCalls:    SemanticAssessmentMaxProviderTurns,
			wantAttempts: SemanticAssessmentMaxProviderTurns,
			wantClass:    "malformed_exhausted",
		},
		{
			name: "request dependent invalid response",
			handler: func() http.HandlerFunc {
				response := semanticAssessmentTestResponse()
				response.RequestID = "other-request"
				return assessorResponseHandler(t, response, nil)
			}(),
			wantType:     &MalformedResponseError{},
			wantDetail:   "remained invalid after bounded correction",
			wantCalls:    SemanticAssessmentMaxProviderTurns,
			wantAttempts: SemanticAssessmentMaxProviderTurns,
			wantClass:    "malformed_exhausted",
		},
		{
			name: "rate limited",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusTooManyRequests)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "slow down"}}))
			},
			wantType:     &RateLimitError{},
			wantDetail:   "provider returned HTTP 429",
			wantCalls:    1,
			forbidDetail: "slow down",
		},
		{
			name: "provider status",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "upstream unavailable"}}))
			},
			wantType:     &ProviderError{},
			wantDetail:   "provider returned HTTP 502",
			wantCalls:    1,
			wantClass:    ProviderFailureClassHTTPServer,
			wantStatus:   http.StatusBadGateway,
			forbidDetail: "upstream unavailable",
		},
		{
			name: "bad request",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "do not expose me"}}))
			},
			wantType:     &ProviderError{},
			wantDetail:   "provider returned HTTP 400",
			wantCalls:    1,
			wantClass:    ProviderFailureClassHTTPClient,
			wantStatus:   http.StatusBadRequest,
			forbidDetail: "do not expose me",
		},
		{
			name: "unauthorized",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "secret diagnostic"}}))
			},
			wantType:     &ProviderError{},
			wantDetail:   "provider returned HTTP 401",
			wantCalls:    1,
			wantClass:    ProviderFailureClassHTTPClient,
			wantStatus:   http.StatusUnauthorized,
			forbidDetail: "secret diagnostic",
		},
		{
			name: "empty choices",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{}}))
			},
			wantType:   &ProviderError{},
			wantDetail: "no choices",
			wantCalls:  1,
			wantClass:  ProviderFailureClassProtocol,
		},
		{
			name: "invalid outer envelope",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, err := io.WriteString(w, "not-json")
				require.NoError(t, err)
			},
			wantType:   &ProviderError{},
			wantDetail: "invalid provider response envelope",
			wantCalls:  1,
			wantClass:  ProviderFailureClassProtocol,
		},
		{
			name: "non-json service unavailable",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, err := io.WriteString(w, "not-json")
				require.NoError(t, err)
			},
			wantType:   &ProviderError{},
			wantDetail: "provider returned HTTP 503",
			wantCalls:  1,
			wantClass:  ProviderFailureClassHTTPServer,
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				testCase.handler(w, r)
			}))
			defer srv.Close()
			v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
			req, _ := semanticAssessmentTestRequest(t)
			_, err := runSemanticAssessmentSessionForTest(context.Background(), v, req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), testCase.wantDetail)
			if testCase.forbidDetail != "" {
				assert.NotContains(t, err.Error(), testCase.forbidDetail)
			}
			assert.Equal(t, testCase.wantCalls, calls)
			switch testCase.wantType.(type) {
			case *MalformedResponseError:
				var malformed *MalformedResponseError
				require.ErrorAs(t, err, &malformed)
				assert.Equal(t, testCase.wantAttempts, malformed.Attempts)
				assert.Equal(t, testCase.wantClass, malformed.FailureClass)
			case *RateLimitError:
				var rateLimited *RateLimitError
				require.ErrorAs(t, err, &rateLimited)
			case *ProviderError:
				var provider *ProviderError
				require.ErrorAs(t, err, &provider)
				assert.Equal(t, testCase.wantClass, provider.FailureClass)
				assert.Equal(t, testCase.wantStatus, provider.StatusCode)
			default:
				t.Fatalf("unsupported expected error type %T", testCase.wantType)
			}
		})
	}
}

func TestOpenAIVerifierAssessSemanticRejectsInvalidRequestBeforeProviderCall(t *testing.T) {
	v := NewOpenAIVerifier(newTestVerifierConfig("https://example.invalid", "key", "assessor-model"), nil)
	_, err := runSemanticAssessmentSessionForTest(context.Background(), v, SemanticAssessmentRequest{})
	var provider *ProviderError
	require.ErrorAs(t, err, &provider)
	assert.Contains(t, provider.Message, "invalid semantic assessment request")
}

func TestOpenAIVerifierWithUsageSurfacesMarshalRequestAndTransportFailures(t *testing.T) {
	v := NewOpenAIVerifier(newTestVerifierConfig("https://example.invalid", "key", "assessor-model"), nil)

	_, err := v.openAIStructuredChatJSONWithUsage(context.Background(), "model", "schema", map[string]any{"invalid": func() {}}, "system", map[string]any{})
	var provider *ProviderError
	require.ErrorAs(t, err, &provider)
	assert.Contains(t, provider.Message, "failed to marshal structured schema")

	_, err = v.openAIStructuredChatJSONWithUsage(context.Background(), "model", "schema", map[string]any{}, "system", func() {})
	require.ErrorAs(t, err, &provider)
	assert.Contains(t, provider.Message, "failed to marshal user payload")

	v.baseURL = "://invalid"
	_, err = v.openAIStructuredChatJSONWithUsage(context.Background(), "model", "schema", map[string]any{}, "system", map[string]any{})
	require.ErrorAs(t, err, &provider)
	assert.Contains(t, provider.Message, "failed to create HTTP request")

	v = NewOpenAIVerifier(newTestVerifierConfig("https://example.invalid", "key", "assessor-model"), &http.Client{
		Transport: assessorRoundTripper(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed")
		}),
	})
	_, err = v.openAIStructuredChatJSONWithUsage(context.Background(), "model", "schema", map[string]any{}, "system", map[string]any{})
	require.ErrorAs(t, err, &provider)
	assert.Contains(t, provider.Message, "HTTP request failed")
	assert.NotContains(t, err.Error(), "dial failed")

	v = NewOpenAIVerifier(newTestVerifierConfig("https://example.invalid", "key", "assessor-model"), &http.Client{
		Transport: assessorRoundTripper(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	})
	_, err = v.openAIStructuredChatJSONWithUsage(context.Background(), "model", "schema", map[string]any{}, "system", map[string]any{})
	var timeout *TimeoutError
	require.ErrorAs(t, err, &timeout)

	v.sem = make(chan struct{}, 1)
	v.sem <- struct{}{}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	err = v.acquire(canceled)
	require.ErrorAs(t, err, &timeout)
}

func TestDecodeOpenAIVerifierAPIResponseBoundsTransport(t *testing.T) {
	_, err := decodeOpenAIVerifierAPIResponse(assessorErrorReader{})
	require.ErrorContains(t, err, "read failed")

	_, err = decodeOpenAIVerifierAPIResponse(strings.NewReader("not-json"))
	require.Error(t, err)

	_, err = decodeOpenAIVerifierAPIResponse(strings.NewReader(strings.Repeat("x", openAIVerifierMaxResponseBytes+1)))
	require.ErrorContains(t, err, "transport limit")
}

func assessorResponseHandler(t *testing.T, response SemanticAssessmentResponse, usage *openAIVerifierUsage) http.HandlerFunc {
	t.Helper()
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	return assessorRawResponseHandler(t, string(encoded), usage)
}

func assessorRawResponseHandler(t *testing.T, content string, usage *openAIVerifierUsage) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		response := map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": content}}}}
		if usage != nil {
			response["usage"] = usage
		}
		require.NoError(t, json.NewEncoder(w).Encode(response))
	}
}
