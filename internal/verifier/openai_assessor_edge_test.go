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
		assert.Contains(t, request.Messages[0].Content, "integrated structure and support assessor")
		assert.Contains(t, request.Messages[0].Content, `predicate_status "needs_review" requires predicate_key and predicate_version both null`)
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
	cfg.AIReviewerModel = ""
	v := NewOpenAIVerifier(cfg, srv.Client())
	req, _ := semanticAssessmentTestRequest(t)
	response, err := v.AssessSemantic(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "assess-1", response.RequestID)
	require.Len(t, response.RelationshipResults, 1)
	assert.Equal(t, "entailed", response.RelationshipResults[0].EvidenceVerdict)
	assert.Equal(t, 1, response.ProviderTurns)
	assert.Equal(t, 200, response.InputTokens)
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

	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	v := NewOpenAIVerifier(newTestVerifierConfig(srv.URL, "key", "assessor-model"), srv.Client())
	v.SetMetrics(metrics)
	req, _ := semanticAssessmentTestRequest(t)
	response, err := v.AssessSemantic(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, requests, 2)
	assert.Equal(t, 2, response.ProviderTurns)
	assert.Greater(t, response.InputTokens, req.InputTokens)
	assert.Equal(t, []string{"system", "user"}, assessmentMessageRoles(requests[0].Messages))
	assert.Equal(t, []string{"system", "user", "assistant", "user"}, assessmentMessageRoles(requests[1].Messages))
	assert.Equal(t, invalidContent, requests[1].Messages[2].Content)

	var correction semanticAssessmentCorrection
	require.NoError(t, json.Unmarshal([]byte(requests[1].Messages[3].Content), &correction))
	require.Len(t, correction.ValidationErrors, 1)
	assert.Equal(t, "request_id", correction.ValidationErrors[0].Field)
	assert.Contains(t, correction.ValidationErrors[0].Message, `expected "assess-1"`)
	assert.Empty(t, correction.SpanHints)
	assert.Empty(t, correction.EntitySelectionHints)
	assert.Contains(t, correction.Instruction, "complete replacement JSON object")
	assert.Contains(t, correction.Instruction, "predicate_key and predicate_version must both be null")
	assert.Equal(t, 1, metrics.AssessorValidationFailureCount("response_contract"))
	assert.Equal(t, 1, metrics.AssessorValidationFieldFailureCount("response_contract", "request_id"))
}

func TestOpenAIVerifierAssessSemanticReturnsSpanHintsInSameHistory(t *testing.T) {
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
			response.EntityResults[0].Start = 1
			response.EntityResults[0].End = 5
			response.EntityResults[0].Action = "create"
			response.EntityResults[0].CandidateEntityID = nil
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
	response, err := v.AssessSemantic(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, requests, 2)
	assert.Equal(t, 2, response.ProviderTurns)
	assert.Equal(t, invalidContent, requests[1].Messages[2].Content)

	var correction semanticAssessmentCorrection
	require.NoError(t, json.Unmarshal([]byte(requests[1].Messages[3].Content), &correction))
	require.Len(t, correction.SpanHints, 1)
	assert.Equal(t, "entity_results[0].surface", correction.SpanHints[0].Field)
	assert.Equal(t, "ev-1", correction.SpanHints[0].EvidenceID)
	assert.Equal(t, "Mark", correction.SpanHints[0].Surface)
	candidateID := "entity-mark"
	expectedSpan := semanticAssessmentCorrectionSpan{
		Start:             0,
		End:               4,
		Action:            "reuse",
		CandidateEntityID: &candidateID,
	}
	assert.Equal(t, []semanticAssessmentCorrectionSpan{expectedSpan}, correction.SpanHints[0].ValidSpans)
	assert.Equal(t, &expectedSpan, correction.SpanHints[0].RecommendedSpan)
	assert.False(t, correction.SpanHints[0].RemoveResult)
	assert.False(t, correction.SpanHints[0].Truncated)
	assert.Contains(t, requests[1].Messages[3].Content, `"truncated":false`)
	assert.Contains(t, requests[1].Messages[3].Content, `"candidate_entity_id":"entity-mark"`)
	assert.Contains(t, requests[1].Messages[3].Content, `"recommended_span"`)
	assert.Contains(t, requests[1].Messages[3].Content, `"surface":"Mark"`)
	assert.Contains(t, correction.Instruction, "span_hints")
	assert.Contains(t, correction.Instruction, "recommended_span")
	assert.Contains(t, correction.Instruction, "hint's surface")
}

func TestOpenAIVerifierAssessSemanticReturnsEntitySelectionHintsInSameHistory(t *testing.T) {
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
	req, _ := semanticAssessmentTestRequest(t)
	response, err := v.AssessSemantic(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, requests, 2)
	assert.Equal(t, 2, response.ProviderTurns)

	var correction semanticAssessmentCorrection
	require.NoError(t, json.Unmarshal([]byte(requests[1].Messages[3].Content), &correction))
	require.Len(t, correction.EntitySelectionHints, 1)
	candidateID := "entity-mark"
	assert.Equal(t, semanticAssessmentCorrectionEntitySelectionHint{
		Index:             0,
		Action:            "reuse",
		CandidateEntityID: &candidateID,
	}, correction.EntitySelectionHints[0])
	assert.Empty(t, correction.SpanHints)
	assert.Contains(t, correction.Instruction, "entity_selection_hints")
	assert.Contains(t, requests[1].Messages[3].Content, `"candidate_entity_id":"entity-mark"`)
}

func TestSemanticAssessmentCorrectionEntitySelectionHintsSkipUnsafeIndexes(t *testing.T) {
	req, _ := semanticAssessmentTestRequest(t)
	response := semanticAssessmentTestResponse()
	response.EntityResults[0].Action = "create"
	response.EntityResults[0].CandidateEntityID = nil
	selectionError := SemanticValidationError{
		Field:   "entity_results[0].action",
		Message: "cannot create when candidate context is truncated or a compatible candidate is available",
	}

	candidateID := "entity-mark"
	assert.Equal(t, []semanticAssessmentCorrectionEntitySelectionHint{{
		Index:             0,
		Action:            "reuse",
		CandidateEntityID: &candidateID,
	}}, semanticAssessmentCorrectionEntitySelectionHints(req, response, []SemanticValidationError{
		selectionError,
		{Field: "entity_results[0].confidence", Message: "must be between 0 and 1"},
	}))

	for _, unsafe := range []SemanticValidationError{
		{Field: "entity_results[0].kind", Message: "is unsupported"},
		{Field: "entity_results[0].evidence_id", Message: "is unknown"},
		{Field: "entity_results[0].surface", Message: "quote does not match the original evidence span"},
		{Field: "entity_results[0]", Message: "duplicates an entity evidence span"},
	} {
		t.Run(unsafe.Field, func(t *testing.T) {
			assert.Empty(t, semanticAssessmentCorrectionEntitySelectionHints(
				req,
				response,
				[]SemanticValidationError{selectionError, unsafe},
			))
		})
	}
}

func TestSemanticAssessmentCorrectionSpanHintsAreBoundedRuneOffsets(t *testing.T) {
	req := SemanticAssessmentRequest{Evidence: []SemanticReviewEvidence{{
		EvidenceID: "ev-1",
		Content:    "é Mark and Mark",
	}}}
	response := SemanticAssessmentResponse{EntityResults: []SemanticAssessmentEntityResult{{
		Surface:    "Mark",
		EvidenceID: "ev-1",
		Start:      3,
		End:        7,
	}}}
	hints := semanticAssessmentCorrectionSpanHints(req, response, []SemanticValidationError{
		{Field: "entity_results[0].surface", Message: "quote does not match the original evidence span"},
		{Field: "entity_results[0].surface", Message: "duplicate error for the same field"},
		{Field: "relationship_results[0].evidence[0]", Message: "span is invalid"},
	})
	require.Len(t, hints, 1)
	assert.Equal(t, []semanticAssessmentCorrectionSpan{
		{Start: 2, End: 6, Action: "create"},
		{Start: 11, End: 15, Action: "create"},
	}, hints[0].ValidSpans)
	assert.Equal(t, &semanticAssessmentCorrectionSpan{
		Start:  2,
		End:    6,
		Action: "create",
	}, hints[0].RecommendedSpan)
	assert.Equal(t, "Mark", hints[0].Surface)
	assert.False(t, hints[0].Truncated)
	assert.Contains(t, string(mustMarshalJSON(t, hints)), `"surface":"Mark"`)

	req.Evidence[0].Content = strings.Repeat("x", semanticAssessmentMaxCorrectionSpanOptions+1)
	response.EntityResults[0].Surface = "x"
	hints = semanticAssessmentCorrectionSpanHints(req, response, []SemanticValidationError{{
		Field:   "entity_results[0].surface",
		Message: "quote does not match the original evidence span",
	}})
	require.Len(t, hints, 1)
	assert.Len(t, hints[0].ValidSpans, semanticAssessmentMaxCorrectionSpanOptions)
	assert.Equal(t, "x", hints[0].Surface)
	assert.Nil(t, hints[0].RecommendedSpan)
	assert.True(t, hints[0].Truncated)
}

func TestSemanticAssessmentCorrectionSpanHintsHandleOccupiedAndDuplicateSpans(t *testing.T) {
	req := SemanticAssessmentRequest{Evidence: []SemanticReviewEvidence{{
		EvidenceID: "ev-1",
		Content:    "é Mark and Mark",
	}}}
	response := SemanticAssessmentResponse{EntityResults: []SemanticAssessmentEntityResult{
		{
			Surface:    "Mark",
			EvidenceID: "ev-1",
			Start:      2,
			End:        6,
		},
		{
			Surface:    "Mark",
			EvidenceID: "ev-1",
			Start:      10,
			End:        14,
		},
	}}

	hints := semanticAssessmentCorrectionSpanHints(req, response, []SemanticValidationError{{
		Field:   "entity_results[1].surface",
		Message: "quote does not match the original evidence span",
	}})
	require.Len(t, hints, 1)
	assert.False(t, hints[0].RemoveResult)
	assert.Equal(t, []semanticAssessmentCorrectionSpan{
		{Start: 2, End: 6, Action: "create", OccupiedByOtherResult: true},
		{Start: 11, End: 15, Action: "create"},
	}, hints[0].ValidSpans)
	assert.Equal(t, &semanticAssessmentCorrectionSpan{
		Start:  11,
		End:    15,
		Action: "create",
	}, hints[0].RecommendedSpan)

	response.EntityResults[1].Start = 3
	response.EntityResults[1].End = 7
	hints = semanticAssessmentCorrectionSpanHints(req, response, []SemanticValidationError{{
		Field:   "entity_results[1].surface",
		Message: "quote does not match the original evidence span",
	}})
	require.Len(t, hints, 1)
	assert.False(t, hints[0].RemoveResult)
	assert.Nil(t, hints[0].RecommendedSpan)

	response.EntityResults[1].Start = 2
	response.EntityResults[1].End = 6
	hints = semanticAssessmentCorrectionSpanHints(req, response, []SemanticValidationError{{
		Field:   "entity_results[1]",
		Message: "duplicates an entity evidence span",
	}})
	require.Len(t, hints, 1)
	assert.True(t, hints[0].RemoveResult)
	assert.Empty(t, hints[0].Surface)
	assert.Nil(t, hints[0].RecommendedSpan)
}

func TestSemanticAssessmentCorrectionSpanHintsRemoveWhenAllOccurrencesAreOccupied(t *testing.T) {
	req := SemanticAssessmentRequest{Evidence: []SemanticReviewEvidence{{
		EvidenceID: "ev-1",
		Content:    "Mark",
	}}}
	response := SemanticAssessmentResponse{EntityResults: []SemanticAssessmentEntityResult{
		{
			Surface:    "Mark",
			EvidenceID: "ev-1",
			Start:      0,
			End:        4,
		},
		{
			Surface:    "Mark",
			EvidenceID: "ev-1",
			Start:      1,
			End:        5,
		},
	}}

	hints := semanticAssessmentCorrectionSpanHints(req, response, []SemanticValidationError{{
		Field:   "entity_results[1].surface",
		Message: "quote does not match the original evidence span",
	}})
	require.Len(t, hints, 1)
	assert.True(t, hints[0].RemoveResult)
	assert.Nil(t, hints[0].RecommendedSpan)
	assert.Equal(t, []semanticAssessmentCorrectionSpan{{
		Start:                 0,
		End:                   4,
		Action:                "create",
		OccupiedByOtherResult: true,
	}}, hints[0].ValidSpans)
}

func TestSemanticAssessmentCorrectionSpanHintsRemoveWhenSurfaceDoesNotOccur(t *testing.T) {
	req := SemanticAssessmentRequest{Evidence: []SemanticReviewEvidence{{
		EvidenceID: "ev-1",
		Content:    "Mark",
	}}}
	response := SemanticAssessmentResponse{EntityResults: []SemanticAssessmentEntityResult{{
		Surface:    "invented",
		EvidenceID: "ev-1",
		Start:      0,
		End:        8,
	}}}

	hints := semanticAssessmentCorrectionSpanHints(req, response, []SemanticValidationError{{
		Field:   "entity_results[0].surface",
		Message: "quote does not match the original evidence span",
	}})
	require.Len(t, hints, 1)
	assert.True(t, hints[0].RemoveResult)
	assert.Empty(t, hints[0].Surface)
	assert.Empty(t, hints[0].ValidSpans)
	assert.Nil(t, hints[0].RecommendedSpan)
	assert.False(t, hints[0].Truncated)
}

func TestSemanticAssessmentCorrectionEntitySelectionMatchesCandidateRules(t *testing.T) {
	person := SemanticAssessmentEntityCandidate{EntityID: "entity-person", Kind: "person"}
	secondPerson := SemanticAssessmentEntityCandidate{EntityID: "entity-person-2", Kind: "person"}
	testCases := []struct {
		name       string
		group      SemanticAssessmentEntityCandidateGroup
		hasGroup   bool
		wantAction string
		wantID     *string
	}{
		{name: "no group", wantAction: "create"},
		{
			name:       "one compatible candidate",
			group:      SemanticAssessmentEntityCandidateGroup{Candidates: []SemanticAssessmentEntityCandidate{person}},
			hasGroup:   true,
			wantAction: "reuse",
			wantID:     stringPointer("entity-person"),
		},
		{
			name:       "multiple compatible candidates",
			group:      SemanticAssessmentEntityCandidateGroup{Candidates: []SemanticAssessmentEntityCandidate{person, secondPerson}},
			hasGroup:   true,
			wantAction: "ambiguous",
		},
		{
			name: "truncated candidate context",
			group: SemanticAssessmentEntityCandidateGroup{
				CandidateContextTruncated: true,
				Candidates:                []SemanticAssessmentEntityCandidate{person},
			},
			hasGroup:   true,
			wantAction: "ambiguous",
		},
		{
			name:       "no compatible candidate",
			group:      SemanticAssessmentEntityCandidateGroup{Candidates: []SemanticAssessmentEntityCandidate{{EntityID: "entity-product", Kind: "product"}}},
			hasGroup:   true,
			wantAction: "create",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			action, candidateID := semanticAssessmentCorrectionEntitySelection(
				testCase.group,
				testCase.hasGroup,
				"person",
			)
			assert.Equal(t, testCase.wantAction, action)
			assert.Equal(t, testCase.wantID, candidateID)
		})
	}
}

func TestSemanticAssessmentValidationFieldFamilyIsBounded(t *testing.T) {
	testCases := []struct {
		field string
		want  string
	}{
		{field: "request_id", want: "request_id"},
		{field: "security_signals[4].evidence_id", want: "security_signals"},
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
		"zero-based Unicode rune offsets",
		"end is exclusive",
		"at most one entity_result for each",
		"exactly one of object_ref and object_value",
		`temporal_verdict "absent"`,
		`temporal_verdict "entailed"`,
		"RFC3339",
	} {
		assert.Contains(t, semanticAssessmentSystemPrompt, expected)
	}
	for _, expected := range []string{
		"zero-based Unicode rune offsets",
		"end is exclusive",
		"Do not re-extract",
		"same array index",
		"start, end, action, and candidate_entity_id",
		"remove the invalid Entity result",
		"entity_selection_hints",
		"candidate context is not truncated",
		"submitted surface does not occur",
		"allowed_subject_kinds and allowed_object_kinds",
		`temporal_verdict "absent"`,
		`temporal_verdict "entailed"`,
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
	_, err := v.AssessSemantic(context.Background(), req)
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
			_, err := v.AssessSemantic(context.Background(), req)
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
	_, err := v.AssessSemantic(context.Background(), SemanticAssessmentRequest{})
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
