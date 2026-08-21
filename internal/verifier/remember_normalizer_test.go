package verifier

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func validRememberNormalizerRequest(t *testing.T) (RememberNormalizerRequest, SemanticAssessmentLimits) {
	t.Helper()
	request, limits := semanticAssessmentSubmissionContractTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(request, limits)
	require.Empty(t, errs)
	return prepared, limits
}

func validRememberNormalizerResponse(t *testing.T, request RememberNormalizerRequest) RememberNormalizerResponse {
	t.Helper()
	contract := request.SubmissionContract
	require.NotNil(t, contract)
	markGrounding := contract.Entities[0].Groundings[0]
	denseGrounding := contract.Entities[1].Groundings[0]
	predicateStart, predicateStartOK := SemanticAssessmentBoundaryRef(request.Evidence[0], 5)
	predicateEnd, predicateEndOK := SemanticAssessmentBoundaryRef(request.Evidence[0], 13)
	supportStart, supportStartOK := SemanticAssessmentBoundaryRef(request.Evidence[0], 0)
	supportEnd, supportEndOK := SemanticAssessmentBoundaryRef(request.Evidence[0], 24)
	require.True(t, predicateStartOK && predicateEndOK && supportStartOK && supportEndOK)
	markID := "entity-mark"
	denseID := "entity-dense-mem"
	predicateKey := "works_on"
	predicateVersion := 1
	return RememberNormalizerResponse{
		RequestID:       request.RequestID,
		SecuritySignals: []RememberNormalizerSecuritySignal{},
		EntityResults: []RememberNormalizerEntityResult{
			{Ref: contract.Entities[0].Ref, GroundingRef: stringPointer(markGrounding.GroundingRef), Action: "reuse", CandidateEntityID: &markID},
			{Ref: contract.Entities[1].Ref, GroundingRef: stringPointer(denseGrounding.GroundingRef), Action: "reuse", CandidateEntityID: &denseID},
		},
		RelationshipResults: []RememberNormalizerRelationshipResult{{
			Ref:              contract.Relationships[0].ProposalID,
			SubjectRef:       contract.Relationships[0].SubjectRef,
			PredicateRange:   RememberNormalizerRange{EvidenceID: "ev-1", StartRef: predicateStart, EndRef: predicateEnd},
			PredicateStatus:  "resolved",
			PredicateKey:     &predicateKey,
			PredicateVersion: &predicateVersion,
			ObjectRef:        stringPointer("product-1"),
			Polarity:         "+",
			Modality:         "statement",
			SupportRanges:    []RememberNormalizerRange{{EvidenceID: "ev-1", StartRef: supportStart, EndRef: supportEnd}},
			ScopeStatus:      "absent",
		}},
	}
}

func TestRememberNormalizerSchemaExcludesPolicyFields(t *testing.T) {
	raw, err := json.Marshal(RememberNormalizerResponseSchema())
	require.NoError(t, err)
	text := string(raw)
	for _, forbidden := range []string{"confidence", "rationale", "evidence_verdict", "temporal_verdict", "ownership", "lifecycle", "review"} {
		require.NotContains(t, text, forbidden)
	}
}

func TestPrepareRememberNormalizerResponseRequiresCompleteGroundedReplacement(t *testing.T) {
	request, limits := validRememberNormalizerRequest(t)
	response := validRememberNormalizerResponse(t, request)
	prepared, errs := PrepareRememberNormalizerResponse(request, response, limits)
	require.Empty(t, errs)
	require.Equal(t, 5, prepared.RelationshipResults[0].PredicateRange.Start)
	require.Equal(t, 13, prepared.RelationshipResults[0].PredicateRange.End)

	response.RelationshipResults[0].SupportRanges = nil
	_, errs = PrepareRememberNormalizerResponse(request, response, limits)
	require.NotEmpty(t, errs)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "support_ranges")

	response = validRememberNormalizerResponse(t, request)
	response.RelationshipResults = append(response.RelationshipResults, response.RelationshipResults[0])
	_, errs = PrepareRememberNormalizerResponse(request, response, limits)
	require.NotEmpty(t, errs)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "duplicated")
}

func TestPrepareRememberNormalizerResponseRejectsOutOfBoundaryAndUncontainedSpans(t *testing.T) {
	request, limits := validRememberNormalizerRequest(t)
	response := validRememberNormalizerResponse(t, request)
	response.RelationshipResults[0].PredicateRange.StartRef = "not-a-boundary"
	_, errs := PrepareRememberNormalizerResponse(request, response, limits)
	require.NotEmpty(t, errs)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "exact submitted boundary range")

	response = validRememberNormalizerResponse(t, request)
	start, startOK := SemanticAssessmentBoundaryRef(request.Evidence[0], 0)
	end, endOK := SemanticAssessmentBoundaryRef(request.Evidence[0], 4)
	require.True(t, startOK && endOK)
	response.RelationshipResults[0].SupportRanges[0] = RememberNormalizerRange{EvidenceID: "ev-1", StartRef: start, EndRef: end}
	_, errs = PrepareRememberNormalizerResponse(request, response, limits)
	require.NotEmpty(t, errs)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "contained in a support range")
}

func TestPrepareRememberNormalizerResponseEnforcesCandidateAndPredicateAllowlists(t *testing.T) {
	t.Run("reuse must select the one compatible candidate", func(t *testing.T) {
		request, limits := validRememberNormalizerRequest(t)
		response := validRememberNormalizerResponse(t, request)
		request.EntityCandidateGroups[0].Candidates = append(request.EntityCandidateGroups[0].Candidates, SemanticAssessmentEntityCandidate{
			EntityID: "entity-mark-2", Kind: "person", CanonicalName: "Mark Two",
		})
		_, errs := PrepareRememberNormalizerResponse(request, response, limits)
		require.Contains(t, semanticAssessmentJoinedErrors(errs), "reuse requires one compatible submitted candidate")

		request, limits = validRememberNormalizerRequest(t)
		response = validRememberNormalizerResponse(t, request)
		response.EntityResults[0].Action = "create"
		response.EntityResults[0].CandidateEntityID = nil
		_, errs = PrepareRememberNormalizerResponse(request, response, limits)
		require.Contains(t, semanticAssessmentJoinedErrors(errs), "create is not allowed")

		request, limits = validRememberNormalizerRequest(t)
		response = validRememberNormalizerResponse(t, request)
		request.EntityCandidateGroups[0].CandidateContextTruncated = true
		_, errs = PrepareRememberNormalizerResponse(request, response, limits)
		require.Contains(t, semanticAssessmentJoinedErrors(errs), "reuse requires one compatible submitted candidate")
	})

	t.Run("resolved predicate must match endpoint kinds", func(t *testing.T) {
		request, limits := validRememberNormalizerRequest(t)
		response := validRememberNormalizerResponse(t, request)
		request.PredicateOptions[0].AllowedObjectKinds = []string{"person"}
		_, errs := PrepareRememberNormalizerResponse(request, response, limits)
		require.Contains(t, semanticAssessmentJoinedErrors(errs), "resolved must select one compatible supplied predicate")
	})
}

func TestPrepareRememberNormalizerResponseRejectsForeignAndDuplicateSupportRanges(t *testing.T) {
	request, limits := validRememberNormalizerRequest(t)
	response := validRememberNormalizerResponse(t, request)
	response.RelationshipResults[0].SupportRanges = append(response.RelationshipResults[0].SupportRanges, response.RelationshipResults[0].SupportRanges[0])
	_, errs := PrepareRememberNormalizerResponse(request, response, limits)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "duplicates a support range")

	extraEvidence := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "ev-2", Content: "Unrelated evidence."})
	request.Evidence = append(request.Evidence, extraEvidence)
	foreignStart, foreignStartOK := SemanticAssessmentBoundaryRef(extraEvidence, 0)
	foreignEnd, foreignEndOK := SemanticAssessmentBoundaryRef(extraEvidence, 5)
	require.True(t, foreignStartOK && foreignEndOK)
	response = validRememberNormalizerResponse(t, request)
	response.RelationshipResults[0].PredicateRange = RememberNormalizerRange{EvidenceID: "ev-2", StartRef: foreignStart, EndRef: foreignEnd}
	response.RelationshipResults[0].SupportRanges = []RememberNormalizerRange{{EvidenceID: "ev-2", StartRef: foreignStart, EndRef: foreignEnd}}
	_, errs = PrepareRememberNormalizerResponse(request, response, limits)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "must use an exact submitted boundary range")
}

func TestPrepareRememberNormalizerResponsePreservesTypedValueOptionalFields(t *testing.T) {
	request, limits := validRememberNormalizerRequest(t)
	target := &request.SubmissionContract.Relationships[0]
	target.ObjectRef = nil
	target.ObjectValue = &SemanticAssessmentValue{ValueType: "string", CanonicalValue: "typed"}
	request.PredicateOptions[0].AllowedObjectKinds = []string{"string"}
	response := validRememberNormalizerResponse(t, request)
	valueRange := response.RelationshipResults[0].SupportRanges[0]
	response.RelationshipResults[0].ObjectRef = nil
	response.RelationshipResults[0].ObjectValue = &SemanticAssessmentValue{ValueType: "string", CanonicalValue: "typed"}
	response.RelationshipResults[0].ValueRange = &valueRange
	_, errs := PrepareRememberNormalizerResponse(request, response, limits)
	require.Empty(t, errs)

	emptyDisplay := ""
	response.RelationshipResults[0].ObjectValue.Display = &emptyDisplay
	_, errs = PrepareRememberNormalizerResponse(request, response, limits)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "must preserve the submitted typed value")
}

func TestNormalizeRememberRetriesTransportFailuresWithinOneClaim(t *testing.T) {
	request, _ := validRememberNormalizerRequest(t)
	valid, err := json.Marshal(validRememberNormalizerResponse(t, request))
	require.NoError(t, err)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := calls.Add(1)
		if call < 3 {
			http.Error(w, "temporary provider failure", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": string(valid)}}},
			"usage":   map[string]any{"prompt_tokens": 20, "completion_tokens": 30, "total_tokens": 50},
		}))
	}))
	defer server.Close()

	verifier := NewOpenAIVerifier(newTestVerifierConfig(server.URL, "key", "normalizer-model"), server.Client())
	response, err := verifier.NormalizeRemember(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, int32(3), calls.Load())
	require.Equal(t, request.RequestID, response.RequestID)
}

func TestNormalizeRememberRegeneratesCompleteResponseAfterValidationError(t *testing.T) {
	request, _ := validRememberNormalizerRequest(t)
	valid, err := json.Marshal(validRememberNormalizerResponse(t, request))
	require.NoError(t, err)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var body openAIVerifierRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		if call == 2 {
			require.Contains(t, body.Messages[len(body.Messages)-1].Content, "validation_errors")
		}
		content := string(valid)
		if call == 1 {
			var malformed map[string]any
			require.NoError(t, json.Unmarshal(valid, &malformed))
			malformed["relationship_results"] = []any{}
			encoded, marshalErr := json.Marshal(malformed)
			require.NoError(t, marshalErr)
			content = string(encoded)
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		}))
	}))
	defer server.Close()

	verifier := NewOpenAIVerifier(newTestVerifierConfig(server.URL, "key", "normalizer-model"), server.Client())
	response, err := verifier.NormalizeRemember(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, int32(2), calls.Load())
	require.Len(t, response.RelationshipResults, 1)
}

func TestNormalizeRememberExhaustsCompleteCorrectionsWithoutLeakingInvalidResponse(t *testing.T) {
	request, _ := validRememberNormalizerRequest(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"request_id":"assess-1","security_signals":[],"entity_results":[],"relationship_results":[]}`,
			}}},
		}))
	}))
	defer server.Close()

	verifier := NewOpenAIVerifier(newTestVerifierConfig(server.URL, "key", "normalizer-model"), server.Client())
	_, err := verifier.NormalizeRemember(context.Background(), request)
	var malformed *MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	require.Equal(t, RememberNormalizerMaxProviderTurns, malformed.Attempts)
	require.Equal(t, int32(RememberNormalizerMaxProviderTurns), calls.Load())
}

func TestRememberNormalizerCorrectionInstructionRemainsBounded(t *testing.T) {
	require.LessOrEqual(t, len([]rune(rememberNormalizerCorrectionInstruction)), 1000)
	require.True(t, strings.Contains(rememberNormalizerCorrectionInstruction, "complete replacement"))
}

func TestDecodeRememberNormalizerResponseRejectsRawShapeVariants(t *testing.T) {
	request, limits := validRememberNormalizerRequest(t)
	valid, err := json.Marshal(validRememberNormalizerResponse(t, request))
	require.NoError(t, err)

	var root map[string]any
	require.NoError(t, json.Unmarshal(valid, &root))
	root["relationship_results"] = map[string]any{}
	wrongRelationshipArray, err := json.Marshal(root)
	require.NoError(t, err)
	_, err = DecodeRememberNormalizerResponseJSON(wrongRelationshipArray, limits)
	require.Error(t, err)
	require.Contains(t, err.Error(), "relationship_results: must be an array")

	root, err = mapFromJSON(valid)
	require.NoError(t, err)
	root["relationship_results"] = []any{map[string]any{"ref": "incomplete"}}
	incompleteRelationship, err := json.Marshal(root)
	require.NoError(t, err)
	_, err = DecodeRememberNormalizerResponseJSON(incompleteRelationship, limits)
	require.Error(t, err)
	require.Contains(t, err.Error(), "predicate_range")

	root, err = mapFromJSON(valid)
	require.NoError(t, err)
	root["relationship_results"] = []any{map[string]any{
		"ref": "r", "subject_ref": "s", "predicate_range": map[string]any{"evidence_id": "e", "start_ref": "a", "end_ref": "b"},
		"predicate_status": "resolved", "predicate_key": nil, "predicate_version": nil, "object_ref": nil,
		"object_value": map[string]any{"value_type": "string", "canonical_value": "value", "display": nil, "unit": nil},
		"value_range":  map[string]any{"evidence_id": "e", "start_ref": "a", "end_ref": "b"},
		"polarity":     "+", "modality": "statement", "support_ranges": []any{map[string]any{"evidence_id": "e", "start_ref": "a", "end_ref": "b"}},
		"valid_from": nil, "valid_to": nil, "scope_status": "absent", "scope_key": nil,
	}}
	nested, err := json.Marshal(root)
	require.NoError(t, err)
	require.Empty(t, validateRememberNormalizerResponseRaw(nested))

	_, err = DecodeRememberNormalizerResponseJSON(append(valid, []byte("{}")...), limits)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be an object")

	root, err = mapFromJSON(valid)
	require.NoError(t, err)
	root["unknown"] = true
	unknown, err := json.Marshal(root)
	require.NoError(t, err)
	_, err = DecodeRememberNormalizerResponseJSON(unknown, limits)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown: is unknown")

	_, err = DecodeRememberNormalizerResponseJSON([]byte("[]"), limits)
	require.Error(t, err)

	tight := limits
	tight.MaxOutputTokens = 1
	_, err = DecodeRememberNormalizerResponseJSON(valid, tight)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds 1 token limit")
}

func TestPrepareRememberNormalizerResponseCollectsPolicyErrors(t *testing.T) {
	request, limits := validRememberNormalizerRequest(t)
	response := validRememberNormalizerResponse(t, request)
	response.RequestID = "different"
	response.SecuritySignals = []RememberNormalizerSecuritySignal{
		{EvidenceID: "unknown", Kind: "unsupported", StartRef: "bad", EndRef: "bad"},
		{EvidenceID: "ev-1", Kind: "prompt_injection", StartRef: "bad", EndRef: "bad"},
	}
	response.EntityResults[0].Action = "unsupported"
	response.EntityResults[0].GroundingRef = stringPointer("unknown-grounding")
	response.EntityResults[0].CandidateEntityID = stringPointer("unexpected")
	response.EntityResults = append(response.EntityResults,
		RememberNormalizerEntityResult{Ref: response.EntityResults[0].Ref, Action: "ambiguous"},
		RememberNormalizerEntityResult{Ref: "unknown-ref", Action: "ambiguous"},
	)
	relationship := &response.RelationshipResults[0]
	relationship.SubjectRef = "other"
	relationship.Polarity = "-"
	relationship.Modality = "question"
	relationship.PredicateRange = RememberNormalizerRange{EvidenceID: "unknown", StartRef: "bad", EndRef: "bad"}
	relationship.SupportRanges = nil
	relationship.ObjectRef = nil
	relationship.ObjectValue = nil
	relationship.PredicateStatus = "unsupported"
	relationship.ScopeStatus = "unsupported"
	relationship.ScopeKey = stringPointer("scope")
	badFrom, badTo := "not-a-time", "2020-01-01T00:00:00Z"
	relationship.ValidFrom, relationship.ValidTo = &badFrom, &badTo

	_, errs := PrepareRememberNormalizerResponse(request, response, limits)
	require.NotEmpty(t, errs)
	joined := semanticAssessmentJoinedErrors(errs)
	for _, expected := range []string{
		"request_id", "security_signals", "outside the submitted grounding allowlist", "is duplicated",
		"outside the submitted entity contract", "does not preserve", "requires exactly one object",
		"predicate_status", "scope_status", "must contain RFC3339",
	} {
		require.Contains(t, joined, expected)
	}
}

func TestPrepareRememberNormalizerResponseEnforcesBoundsAndValueContainment(t *testing.T) {
	request, limits := validRememberNormalizerRequest(t)
	limits.MaxEntityResults = 1
	limits.MaxRelationshipResults = 1
	response := validRememberNormalizerResponse(t, request)
	response.SecuritySignals = make([]RememberNormalizerSecuritySignal, rememberNormalizerMaxSecuritySignals+1)
	response.EntityResults = append(response.EntityResults, response.EntityResults[0])
	response.RelationshipResults = append(response.RelationshipResults, response.RelationshipResults[0])
	_, errs := PrepareRememberNormalizerResponse(request, response, limits)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "at most")

	request, limits = validRememberNormalizerRequest(t)
	target := &request.SubmissionContract.Relationships[0]
	target.ObjectRef = nil
	target.ObjectValue = &SemanticAssessmentValue{ValueType: "string", CanonicalValue: "typed"}
	request.PredicateOptions[0].AllowedObjectKinds = []string{"string"}
	response = validRememberNormalizerResponse(t, request)
	response.RelationshipResults[0].ObjectRef = nil
	response.RelationshipResults[0].ObjectValue = &SemanticAssessmentValue{ValueType: "string", CanonicalValue: "different"}
	response.RelationshipResults[0].ValueRange = nil
	_, errs = PrepareRememberNormalizerResponse(request, response, limits)
	joined := semanticAssessmentJoinedErrors(errs)
	require.Contains(t, joined, "must preserve the submitted typed value")
	require.Contains(t, joined, "is required for a Value object")

	response = validRememberNormalizerResponse(t, request)
	response.RelationshipResults[0].ObjectRef = nil
	response.RelationshipResults[0].ObjectValue = target.ObjectValue
	valueRange := response.RelationshipResults[0].SupportRanges[0]
	valueRange.StartRef, valueRange.EndRef = response.RelationshipResults[0].PredicateRange.StartRef, response.RelationshipResults[0].PredicateRange.EndRef
	response.RelationshipResults[0].ValueRange = &valueRange
	response.RelationshipResults[0].SupportRanges = []RememberNormalizerRange{{EvidenceID: "ev-1", StartRef: response.RelationshipResults[0].SupportRanges[0].StartRef, EndRef: response.RelationshipResults[0].SupportRanges[0].EndRef}}
	_, errs = PrepareRememberNormalizerResponse(request, response, limits)
	require.Empty(t, errs)
}

func TestRememberNormalizerHelperBranches(t *testing.T) {
	request, _ := validRememberNormalizerRequest(t)
	target := request.SubmissionContract.Entities[0]
	if candidates, truncated := normalizerCompatibleCandidates(request, target, nil); truncated || len(candidates) != 1 {
		t.Fatalf("normalizerCompatibleCandidates() = %d, %v; want one candidate and no truncation", len(candidates), truncated)
	}
	request.EntityCandidateGroups[0].CandidateContextTruncated = true
	if _, truncated := normalizerCompatibleCandidates(request, target, nil); !truncated {
		t.Fatal("normalizerCompatibleCandidates did not report truncation")
	}

	entities := map[string]SemanticAssessmentRequiredEntityRef{"subject": {Kind: "person"}, "object": {Kind: "product"}}
	key, version := "works_on", 1
	result := RememberNormalizerRelationshipResult{SubjectRef: "subject", ObjectRef: stringPointer("object"), PredicateKey: &key, PredicateVersion: &version}
	require.True(t, normalizerPredicateAllowed(request, result, entities))
	result.PredicateKey = stringPointer("missing")
	require.False(t, normalizerPredicateAllowed(request, result, entities))
	result.PredicateKey = nil
	require.False(t, normalizerPredicateAllowed(request, result, entities))

	require.True(t, semanticValuesEqual(nil, nil))
	require.False(t, semanticValuesEqual(&SemanticAssessmentValue{}, nil))
	empty := ""
	require.True(t, optionalStringEqual(&empty, &empty))
	require.False(t, optionalStringEqual(&empty, stringPointer("different")))
	require.False(t, normalizerRangeValid(nil, semanticEvidenceByID(request.Evidence), nil, "range"))
	unknownRange := RememberNormalizerRange{EvidenceID: "missing", StartRef: "a", EndRef: "b"}
	require.False(t, normalizerRangeValid(&unknownRange, semanticEvidenceByID(request.Evidence), nil, "range"))
	require.True(t, normalizerRangeContained(RememberNormalizerRange{EvidenceID: "e", Start: 0, End: 10}, RememberNormalizerRange{EvidenceID: "e", Start: 2, End: 8}))
	require.False(t, normalizerRangeContained(RememberNormalizerRange{EvidenceID: "other", Start: 0, End: 10}, RememberNormalizerRange{EvidenceID: "e", Start: 2, End: 8}))
}

func TestPrepareRememberNormalizerResponseCoversRemainingRelationshipBranches(t *testing.T) {
	request, limits := validRememberNormalizerRequest(t)
	response := validRememberNormalizerResponse(t, request)
	response.EntityResults[0].GroundingRef = nil
	response.EntityResults[0].Action = "reuse"
	response.RelationshipResults = append(response.RelationshipResults, RememberNormalizerRelationshipResult{Ref: "unknown", SubjectRef: "unknown"})
	relationship := &response.RelationshipResults[0]
	fullStart, fullStartOK := SemanticAssessmentBoundaryRef(request.Evidence[0], 0)
	fullEnd, fullEndOK := SemanticAssessmentBoundaryRef(request.Evidence[0], 24)
	require.True(t, fullStartOK && fullEndOK)
	relationship.ValueRange = &RememberNormalizerRange{EvidenceID: "ev-1", StartRef: fullStart, EndRef: fullEnd}
	relationship.SupportRanges[0].StartRef = relationship.PredicateRange.StartRef
	relationship.SupportRanges[0].EndRef = relationship.PredicateRange.EndRef
	relationship.PredicateStatus = "registration_required"
	relationship.PredicateKey = stringPointer("unexpected")
	relationship.PredicateVersion = intPointer(1)
	relationship.ScopeStatus = "resolved"
	relationship.ScopeKey = nil
	from, to := "2026-01-02T00:00:00Z", "2026-01-01T00:00:00Z"
	relationship.ValidFrom, relationship.ValidTo = &from, &to
	_, errs := PrepareRememberNormalizerResponse(request, response, limits)
	joined := semanticAssessmentJoinedErrors(errs)
	require.Contains(t, joined, "may be null only for an ambiguous result")
	require.Contains(t, joined, "registration_required cannot choose")
	require.Contains(t, joined, "scope_key")
	require.Contains(t, joined, "must not be before valid_from")
	require.Contains(t, joined, "must be contained in a support range")

	response = validRememberNormalizerResponse(t, request)
	relationship = &response.RelationshipResults[0]
	relationship.ValueRange = &RememberNormalizerRange{EvidenceID: "ev-1", StartRef: relationship.PredicateRange.StartRef, EndRef: relationship.PredicateRange.EndRef}
	relationship.SupportRanges = append(make([]RememberNormalizerRange, 0, SemanticAssessmentMaxEvidenceSpans+1), relationship.SupportRanges...)
	for len(relationship.SupportRanges) <= SemanticAssessmentMaxEvidenceSpans {
		relationship.SupportRanges = append(relationship.SupportRanges, relationship.SupportRanges[0])
	}
	_, errs = PrepareRememberNormalizerResponse(request, response, limits)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "at most")
}

func TestRememberNormalizerTransportRetryableBranches(t *testing.T) {
	require.False(t, rememberNormalizerTransportRetryable(context.Background(), nil))
	require.False(t, rememberNormalizerTransportRetryable(context.Background(), errors.New("plain")))
	require.True(t, rememberNormalizerTransportRetryable(context.Background(), ErrVerifierTimeout))
	require.False(t, rememberNormalizerTransportRetryable(context.Background(), &ProviderError{FailureClass: ProviderFailureClassRateLimited}))
	require.True(t, rememberNormalizerTransportRetryable(context.Background(), &ProviderError{FailureClass: ProviderFailureClassTransport}))
	require.True(t, rememberNormalizerTransportRetryable(context.Background(), &ProviderError{FailureClass: ProviderFailureClassHTTPServer}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, rememberNormalizerTransportRetryable(ctx, ErrVerifierTimeout))
}

func TestNormalizeRememberRejectsInvalidRequestAndInputBudget(t *testing.T) {
	provider := NewOpenAIVerifier(newTestVerifierConfig("", "", "normalizer-model"), nil)
	_, err := provider.NormalizeRemember(context.Background(), RememberNormalizerRequest{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid remember normalizer request")

	request, _ := validRememberNormalizerRequest(t)
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = request.InputTokens + 1
	provider = NewOpenAIVerifierWithAssessmentLimits(newTestVerifierConfig("", "", "normalizer-model"), nil, limits)
	_, err = provider.NormalizeRemember(context.Background(), request)
	require.Error(t, err)
	require.Contains(t, err.Error(), "HTTP request failed")
}

func TestNormalizeRememberTerminalizesOverBudgetMalformedProviderOutput(t *testing.T) {
	request, _ := validRememberNormalizerRequest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}],"usage":{"completion_tokens":999999}}`))
	}))
	defer server.Close()
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxOutputTokens = 10
	provider := NewOpenAIVerifierWithAssessmentLimits(newTestVerifierConfig(server.URL, "key", "normalizer-model"), server.Client(), limits)
	_, err := provider.NormalizeRemember(context.Background(), request)
	require.Error(t, err)
	var malformed *MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	require.Equal(t, "malformed_exhausted", malformed.FailureClass)
}

func intPointer(value int) *int { return &value }

func mapFromJSON(raw []byte) (map[string]any, error) {
	var value map[string]any
	err := json.Unmarshal(raw, &value)
	return value, err
}
