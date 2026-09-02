package assessor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSemanticAssessmentPrepareAndValidateCompleteResponse(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	if prepared.CandidateContextTokens <= 0 || prepared.InputTokens <= prepared.CandidateContextTokens {
		t.Fatalf("prepared token counts = input:%d candidate:%d", prepared.InputTokens, prepared.CandidateContextTokens)
	}

	raw, err := json.Marshal(semanticAssessmentTestResponse())
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	decoded, err := DecodeSemanticAssessmentResponseJSON(raw, limits)
	if err != nil {
		t.Fatalf("DecodeSemanticAssessmentResponseJSON() error = %v", err)
	}
	validated, errs := PrepareSemanticAssessmentResponse(prepared, decoded, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v", errs)
	}
	if validated.OutputTokens <= 0 {
		t.Fatalf("OutputTokens = %d, want positive", validated.OutputTokens)
	}
}

func TestSemanticAssessmentWireRejectsObsoleteAssessorFields(t *testing.T) {
	_, limits := semanticAssessmentTestRequest(t)
	base, err := json.Marshal(semanticAssessmentTestResponse())
	require.NoError(t, err)

	for _, field := range []string{"confidence", "rationale", "modality", "scope_status", "scope_key", "evidence_verdict", "temporal_verdict"} {
		t.Run(field, func(t *testing.T) {
			var payload map[string]any
			require.NoError(t, json.Unmarshal(base, &payload))
			relationships := payload["relationship_results"].([]any)
			relationships[0].(map[string]any)[field] = "obsolete"
			encoded, marshalErr := json.Marshal(payload)
			require.NoError(t, marshalErr)
			_, decodeErr := DecodeSemanticAssessmentResponseJSON(encoded, limits)
			require.Error(t, decodeErr)
			require.Contains(t, decodeErr.Error(), field+": is unknown")
		})
	}
}

func TestSemanticAssessmentWireRejectsDuplicateFields(t *testing.T) {
	_, limits := semanticAssessmentTestRequest(t)
	_, err := DecodeSemanticAssessmentResponseJSON([]byte(`{"request_id":"a","evidence_security_results":[],"entity_results":[],"relationship_results":[],"request_id":"b"}`), limits)
	require.ErrorContains(t, err, "duplicate JSON field")
}

func TestSemanticAssessmentSubmissionContractPreservesTypedValue(t *testing.T) {
	display := " 42 ms "
	unit := " ms "
	predicate := "has_latency"
	version := 1
	evidence := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "ev-1", FragmentID: "fragment-1", Content: "Latency is 42."})
	latencyStart, _ := SemanticAssessmentBoundaryRef(evidence, 0)
	latencyEnd, _ := SemanticAssessmentBoundaryRef(evidence, 7)
	req := SemanticAssessmentRequest{
		RequestID:      "submission-value-1",
		TeamID:         "team-1",
		OwnerProfileID: "owner-1",
		Evidence:       []SemanticReviewEvidence{evidence},
		EntityCandidateGroups: []SemanticAssessmentEntityCandidateGroup{{
			Surface: "Latency", EvidenceID: "ev-1", GroundingRef: "grounding-latency", Start: 0, End: 7, Candidates: []SemanticAssessmentEntityCandidate{},
		}},
		PredicateOptions: []SemanticAssessmentPredicateOption{{
			PredicateKey:        predicate,
			Version:             version,
			Aliases:             []string{},
			AllowedSubjectKinds: []string{"concept"},
			AllowedObjectKinds:  []string{"number"},
			RelationshipKind:    "state",
			CurrentCardinality:  "many",
		}},
		SubmissionContract: &SemanticAssessmentSubmissionContract{
			Entities: []SemanticAssessmentRequiredEntityRef{{
				Ref: "entity:latency", Name: "Latency", Kind: "concept", EvidenceIDs: []string{"ev-1"},
				Groundings: []SemanticAssessmentEntityGrounding{{GroundingRef: "grounding-latency", EvidenceID: "ev-1", Surface: "Latency", StartRef: latencyStart, EndRef: latencyEnd}},
			}},
			Relationships: []SemanticAssessmentRequiredRelationshipRef{{
				ProposalID:    "relationship:latency",
				PredicateHint: "has_latency",
				EvidenceIDs:   []string{"ev-1"},
				SubjectRef:    "entity:latency",
				ObjectValue: &SemanticAssessmentValue{
					ValueType: " number ", CanonicalValue: " 42 ", Display: &display, Unit: &unit,
				},
				Polarity: "+",
			}},
		},
	}
	limits := DefaultSemanticAssessmentLimits()
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	requireValue := prepared.SubmittedRelationships[0].ObjectValue
	if requireValue == nil || requireValue.ValueType != "number" || requireValue.CanonicalValue != "42" || *requireValue.Display != "42 ms" || *requireValue.Unit != "ms" {
		t.Fatalf("submitted typed value = %#v", requireValue)
	}
	*requireValue.Display = "changed"
	if *prepared.SubmissionContract.Relationships[0].ObjectValue.Display != "42 ms" {
		t.Fatal("submitted typed value aliases the trusted contract")
	}
	*requireValue.Display = "42 ms"

	responseDisplay := "42 ms"
	responseUnit := "ms"
	groundingRef := "grounding-latency"
	predicateRange := semanticAssessmentTestRange(evidence, 8, 10)
	valueRange := semanticAssessmentTestRange(evidence, 11, 13)
	supportRange := semanticAssessmentTestRange(evidence, 0, 13)
	response := SemanticAssessmentResponse{
		RequestID:               prepared.RequestID,
		EvidenceSecurityResults: []SemanticAssessmentEvidenceSecurityResult{{EvidenceID: "ev-1", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{}}},
		EntityResults: []SemanticAssessmentEntityResult{{
			Ref: "entity:latency", GroundingRef: &groundingRef, Action: "create",
		}},
		RelationshipResults: []SemanticAssessmentRelationshipResult{{
			Ref: "relationship:latency", Disposition: "stored",
			Splits: []SemanticAssessmentRelationshipSplit{{
				SplitIndex:       0,
				SubjectRef:       "entity:latency",
				PredicateRange:   predicateRange,
				PredicateStatus:  "resolved",
				PredicateKey:     &predicate,
				PredicateVersion: &version,
				ObjectValue: &SemanticAssessmentValue{
					ValueType: "number", CanonicalValue: "42", Display: &responseDisplay, Unit: &responseUnit,
				},
				ValueRange:    &valueRange,
				Polarity:      "+",
				SupportRanges: []SemanticAssessmentGroundedRange{supportRange},
			}},
		}},
	}
	if _, errs := PrepareSemanticAssessmentResponse(prepared, response, limits); len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v", errs)
	}

	response.RelationshipResults[0].Splits[0].ObjectValue.CanonicalValue = "43"
	if _, errs := PrepareSemanticAssessmentResponse(prepared, response, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "does not preserve its submitted object") {
		t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want typed value preservation error", errs)
	}
}

func TestSemanticAssessmentSubmissionContractRepairsChangedExactConstraints(t *testing.T) {
	req, limits := semanticAssessmentSubmissionContractTestRequest(t)
	req.SubmissionContract.Entities[0].KnownEntityID = "entity-mark"
	req.SubmissionContract.Relationships[0].KnownPredicateKey = "works_on"
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	require.Empty(t, errs)
	require.Equal(t, "entity-mark", prepared.SubmittedEntities[0].KnownEntityID)
	require.Equal(t, "works_on", prepared.SubmittedRelationships[0].KnownPredicateKey)

	response := semanticAssessmentTestResponse()
	response.EntityResults[0].Action = "create"
	response.EntityResults[0].CandidateEntityID = nil
	otherPredicate := "other_predicate"
	response.RelationshipResults[0].Splits[0].PredicateKey = &otherPredicate
	_, errs = PrepareSemanticAssessmentResponse(prepared, response, limits)
	joined := semanticAssessmentJoinedErrors(errs)
	require.Contains(t, joined, "must reuse the exact known_entity_id")
	require.Contains(t, joined, "must equal the exact known_entity_id")
	require.Contains(t, joined, "must equal the exact known_predicate_key")
}

func TestSemanticAssessmentKnownEntityIDDisambiguatesNameMatches(t *testing.T) {
	req, limits := semanticAssessmentSubmissionContractTestRequest(t)
	req.SubmissionContract.Entities[0].KnownEntityID = "entity-mark"
	req.EntityCandidateGroups[0].Candidates = append(req.EntityCandidateGroups[0].Candidates,
		SemanticAssessmentEntityCandidate{
			EntityID: "entity-mark-duplicate", CanonicalName: "Mark Huang", Kind: "person",
		},
	)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	require.Empty(t, errs)

	_, errs = PrepareSemanticAssessmentResponse(prepared, semanticAssessmentTestResponse(), limits)
	require.Empty(t, errs)
}

func TestSemanticAssessmentSubmissionContractRejectsUntrustedTargets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SemanticAssessmentRequest)
		want   string
	}{
		{
			name: "submitted fields without contract",
			mutate: func(request *SemanticAssessmentRequest) {
				request.SubmissionContract = nil
				request.SubmittedEntities = []SemanticAssessmentSubmittedEntity{{Ref: "untrusted"}}
			},
			want: "submission_contract",
		},
		{
			name: "mixed empty contract",
			mutate: func(request *SemanticAssessmentRequest) {
				request.SubmissionContract.Relationships = nil
			},
			want: "must both be empty or both be non-empty",
		},
		{
			name: "duplicate entity ref",
			mutate: func(request *SemanticAssessmentRequest) {
				request.SubmissionContract.Entities = append(request.SubmissionContract.Entities, request.SubmissionContract.Entities[0])
			},
			want: "is duplicated",
		},
		{
			name: "entity evidence outside request",
			mutate: func(request *SemanticAssessmentRequest) {
				request.SubmissionContract.Entities[0].EvidenceIDs[0] = "unknown"
			},
			want: "contains unknown evidence",
		},
		{
			name: "relationship subject outside entities",
			mutate: func(request *SemanticAssessmentRequest) {
				request.SubmissionContract.Relationships[0].SubjectRef = "entity:unknown"
			},
			want: "must reference a submitted entity",
		},
		{
			name: "relationship object has both endpoint forms",
			mutate: func(request *SemanticAssessmentRequest) {
				request.SubmissionContract.Relationships[0].ObjectValue = &SemanticAssessmentValue{ValueType: "string", CanonicalValue: "Dense-Mem"}
			},
			want: "requires exactly one object_ref or object_value",
		},
		{
			name: "relationship typed value is invalid",
			mutate: func(request *SemanticAssessmentRequest) {
				request.SubmissionContract.Relationships[0].ObjectRef = nil
				request.SubmissionContract.Relationships[0].ObjectValue = &SemanticAssessmentValue{ValueType: "unsupported", CanonicalValue: "value"}
			},
			want: "object_value",
		},
		{
			name: "relationship evidence is required",
			mutate: func(request *SemanticAssessmentRequest) {
				request.SubmissionContract.Relationships[0].EvidenceIDs = nil
			},
			want: "must be present and bounded",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, limits := semanticAssessmentSubmissionContractTestRequest(t)
			test.mutate(&request)

			_, errs := PrepareSemanticAssessmentRequest(request, limits)

			if len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), test.want) {
				t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v, want %q", errs, test.want)
			}
		})
	}
}

func TestSemanticAssessmentSubmissionContractRejectsResponseTargetDrift(t *testing.T) {
	request, limits := semanticAssessmentSubmissionContractTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(request, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}

	tests := []struct {
		name   string
		mutate func(*SemanticAssessmentResponse)
		want   string
	}{
		{
			name: "entity grounding target drift",
			mutate: func(response *SemanticAssessmentResponse) {
				value := "grounding-other"
				response.EntityResults[0].GroundingRef = &value
			},
			want: "is outside the submitted grounding allowlist",
		},
		{
			name: "relationship target drift",
			mutate: func(response *SemanticAssessmentResponse) {
				response.RelationshipResults[0].Splits[0].Polarity = "-"
			},
			want: "does not preserve its submitted relationship target",
		},
		{
			name: "relationship evidence outside contract",
			mutate: func(response *SemanticAssessmentResponse) {
				response.RelationshipResults[0].Splits[0].SupportRanges[0].EvidenceID = "ev-other"
			},
			want: "is outside the submitted evidence allowlist",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := semanticAssessmentTestResponse()
			test.mutate(&response)

			_, errs := PrepareSemanticAssessmentResponse(prepared, response, limits)

			if len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), test.want) {
				t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want %q", errs, test.want)
			}
		})
	}
}

func TestSemanticAssessmentCandidateGroupsAreOptionalReuseAllowlists(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	response := semanticAssessmentTestResponse()
	response.EntityResults = response.EntityResults[:1]
	response.RelationshipResults = []SemanticAssessmentRelationshipResult{}

	if _, errs := PrepareSemanticAssessmentResponse(prepared, response, limits); len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentResponse() optional candidate errors = %#v", errs)
	}
}

func TestSemanticAssessmentRequiresTrustedProposalCorrespondence(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	req.RequiredRelationshipRefs = []SemanticAssessmentRequiredRelationshipRef{{
		ProposalID: "proposal-works-on",
		Evidence:   []SemanticAssessmentEvidenceSpan{{EvidenceID: "ev-1", Start: 0, End: 24}},
	}}
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}

	response := semanticAssessmentTestResponse()
	if _, errs := PrepareSemanticAssessmentResponse(prepared, response, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "missing result for trusted proposal") {
		t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want missing trusted proposal result", errs)
	}

	response.RelationshipResults[0].Ref = "proposal-works-on"
	if _, errs := PrepareSemanticAssessmentResponse(prepared, response, limits); len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v", errs)
	}

	response.RelationshipResults[0].Splits[0].SupportRanges[0] = semanticAssessmentTestRange(prepared.Evidence[0], 0, 4)
	if _, errs := PrepareSemanticAssessmentResponse(prepared, response, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "does not use trusted proposal evidence") {
		t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want trusted span rejection", errs)
	}
}

func TestSemanticAssessmentRejectsWholeResponseWhenRequiredFieldIsMissing(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	response := semanticAssessmentTestResponse()
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	relationships := payload["relationship_results"].([]any)
	splits := relationships[0].(map[string]any)["splits"].([]any)
	delete(splits[0].(map[string]any), "predicate_version")
	encoded, err = json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal malformed response: %v", err)
	}
	if _, err := DecodeSemanticAssessmentResponseJSON(encoded, limits); err == nil || !strings.Contains(err.Error(), "predicate_version") {
		t.Fatalf("DecodeSemanticAssessmentResponseJSON() error = %v, want missing predicate_version", err)
	}
	_ = prepared
}

func TestSemanticAssessmentRejectsUnknownDuplicateAndUnauthorizedCompleteResponses(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}

	t.Run("unknown field", func(t *testing.T) {
		encoded, err := json.Marshal(semanticAssessmentTestResponse())
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(encoded, &payload); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		payload["unexpected"] = true
		encoded, err = json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		if _, err := DecodeSemanticAssessmentResponseJSON(encoded, limits); err == nil || !strings.Contains(err.Error(), "unexpected") {
			t.Fatalf("DecodeSemanticAssessmentResponseJSON() error = %v, want unknown field rejection", err)
		}
	})

	t.Run("duplicate entity ref", func(t *testing.T) {
		response := semanticAssessmentTestResponse()
		response.EntityResults = append(response.EntityResults, response.EntityResults[0])
		if _, errs := PrepareSemanticAssessmentResponse(prepared, response, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "is duplicated") {
			t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want duplicate ref rejection", errs)
		}
	})

	t.Run("duplicate entity evidence span", func(t *testing.T) {
		response := semanticAssessmentTestResponse()
		duplicate := response.EntityResults[0]
		duplicate.Ref = "person-duplicate"
		response.EntityResults = append(response.EntityResults, duplicate)
		if _, errs := PrepareSemanticAssessmentResponse(prepared, response, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "duplicates an entity evidence span") {
			t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want duplicate evidence span rejection", errs)
		}
	})

	t.Run("candidate outside allowlist", func(t *testing.T) {
		response := semanticAssessmentTestResponse()
		candidateID := "entity-not-allowed"
		response.EntityResults[0].CandidateEntityID = &candidateID
		if _, errs := PrepareSemanticAssessmentResponse(prepared, response, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "single reusable exact candidate") {
			t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want candidate allowlist rejection", errs)
		}
	})

	t.Run("invalid evidence span", func(t *testing.T) {
		response := semanticAssessmentTestResponse()
		invalidGrounding := "grounding-invalid"
		response.EntityResults[0].GroundingRef = &invalidGrounding
		if _, errs := PrepareSemanticAssessmentResponse(prepared, response, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "is outside the grounding allowlist") {
			t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want span rejection", errs)
		}
	})

	t.Run("non-ambiguous entity requires grounding", func(t *testing.T) {
		response := semanticAssessmentTestResponse()
		response.EntityResults[0].GroundingRef = nil
		if _, errs := PrepareSemanticAssessmentResponse(prepared, response, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "grounding_ref: is required unless action is ambiguous") {
			t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want repairable grounding rejection", errs)
		}
	})
}

func TestSemanticAssessmentRejectsCreateWhenCandidateContextCannotProveAbsence(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*SemanticAssessmentRequest)
	}{
		{
			name:   "compatible candidate exists",
			mutate: func(*SemanticAssessmentRequest) {},
		},
		{
			name: "candidate context is truncated",
			mutate: func(req *SemanticAssessmentRequest) {
				req.EntityCandidateGroups[0].CandidateContextTruncated = true
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			req, limits := semanticAssessmentTestRequest(t)
			testCase.mutate(&req)
			prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
			if len(errs) != 0 {
				t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
			}
			response := semanticAssessmentTestResponse()
			response.EntityResults[0].Action = "create"
			response.EntityResults[0].CandidateEntityID = nil
			if _, errs := PrepareSemanticAssessmentResponse(prepared, response, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "cannot create when candidate context is truncated or a compatible candidate is available") {
				t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want create rejection", errs)
			}
		})
	}
}

func TestSemanticAssessmentRejectsReuseFromTruncatedCandidateGroup(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	req.EntityCandidateGroups[0].CandidateContextTruncated = true
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	response := semanticAssessmentTestResponse()
	_, errs = PrepareSemanticAssessmentResponse(prepared, response, limits)
	if len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "single reusable exact candidate") {
		t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want truncated candidate rejection", errs)
	}
}

func TestSemanticAssessmentRejectsReuseFromAmbiguousCandidateGroup(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	req.EntityCandidateGroups[0].Candidates = append(req.EntityCandidateGroups[0].Candidates, SemanticAssessmentEntityCandidate{
		EntityID:      "entity-other-mark",
		CanonicalName: "Mark Other",
		Kind:          "person",
	})
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	response := semanticAssessmentTestResponse()
	_, errs = PrepareSemanticAssessmentResponse(prepared, response, limits)
	if len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "single reusable exact candidate") {
		t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want ambiguous candidate rejection", errs)
	}
}

func TestSemanticAssessmentRejectsResolvedPredicateWithIncompatibleEndpointKind(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	response := semanticAssessmentTestResponse()
	response.RelationshipResults[0].Splits[0].ObjectRef = nil
	response.RelationshipResults[0].Splits[0].ObjectValue = &SemanticAssessmentValue{
		ValueType:      "string",
		CanonicalValue: "Dense-Mem",
	}
	_, errs = PrepareSemanticAssessmentResponse(prepared, response, limits)
	if len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "does not accept the object kind") {
		t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want incompatible predicate rejection", errs)
	}
}

func TestSemanticAssessmentTokenBudgetsAreEnforced(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	limits.MaxCandidateContextTokens = 1
	if _, errs := PrepareSemanticAssessmentRequest(req, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "candidate_context_tokens") {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v, want candidate token budget error", errs)
	}

	limits = DefaultSemanticAssessmentLimits()
	limits.MaxOutputTokens = 1
	raw, err := json.Marshal(semanticAssessmentTestResponse())
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if _, err := DecodeSemanticAssessmentResponseJSON(raw, limits); err == nil || !strings.Contains(err.Error(), "token limit") {
		t.Fatalf("DecodeSemanticAssessmentResponseJSON() error = %v, want output token budget error", err)
	}
}

func TestSemanticAssessmentInputBudgetIncludesFixedSystemPrompt(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	payload, err := json.Marshal(prepared)
	if err != nil {
		t.Fatalf("marshal prepared request: %v", err)
	}
	payloadTokens, err := CountTokens(string(payload), limits.Tokenizer)
	if err != nil {
		t.Fatalf("CountTokens(payload) error = %v", err)
	}
	if prepared.InputTokens <= payloadTokens {
		t.Fatalf("InputTokens = %d, want prompt-inclusive count greater than payload %d", prepared.InputTokens, payloadTokens)
	}
	limits.MaxInputTokens = payloadTokens + 1
	if _, errs := PrepareSemanticAssessmentRequest(req, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "input_tokens") {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v, want prompt-inclusive input token budget error", errs)
	}
}

func TestSemanticAssessmentResponseSchemaIsClosed(t *testing.T) {
	schema := SemanticAssessmentResponseSchema()
	assertClosedProviderObjects(t, schema, "assessment response")
	assertOpenAIStrictSchemaSubset(t, schema, "assessment response")
}

func semanticAssessmentTestRequest(t *testing.T) (SemanticAssessmentRequest, SemanticAssessmentLimits) {
	t.Helper()
	return SemanticAssessmentRequest{
		RequestID:      "assess-1",
		TeamID:         "team-1",
		OwnerProfileID: "owner-1",
		Evidence: []SemanticReviewEvidence{{
			EvidenceID: "ev-1",
			FragmentID: "fragment-1",
			Content:    "Mark works on Dense-Mem.",
		}},
		EntityCandidateGroups: []SemanticAssessmentEntityCandidateGroup{
			{
				Surface:      "Mark",
				EvidenceID:   "ev-1",
				GroundingRef: "grounding-mark",
				Start:        0,
				End:          4,
				Candidates: []SemanticAssessmentEntityCandidate{{
					EntityID:      "entity-mark",
					CanonicalName: "Mark Huang",
					Kind:          "person",
				}},
			},
			{
				Surface:      "Dense-Mem",
				EvidenceID:   "ev-1",
				GroundingRef: "grounding-dense-mem",
				Start:        14,
				End:          23,
				Candidates: []SemanticAssessmentEntityCandidate{{
					EntityID:      "entity-dense-mem",
					CanonicalName: "Dense-Mem",
					Kind:          "product",
				}},
			},
		},
		PredicateOptions: []SemanticAssessmentPredicateOption{{
			PredicateKey:        "works_on",
			Version:             1,
			Aliases:             []string{"works on"},
			AllowedSubjectKinds: []string{"person"},
			AllowedObjectKinds:  []string{"product"},
			RelationshipKind:    "state",
			CurrentCardinality:  "many",
		}},
	}, DefaultSemanticAssessmentLimits()
}

func semanticAssessmentSubmissionContractTestRequest(t *testing.T) (SemanticAssessmentRequest, SemanticAssessmentLimits) {
	t.Helper()
	request, limits := semanticAssessmentTestRequest(t)
	request.Evidence[0] = PrepareSemanticAssessmentEvidence(request.Evidence[0])
	markStart, _ := SemanticAssessmentBoundaryRef(request.Evidence[0], 0)
	markEnd, _ := SemanticAssessmentBoundaryRef(request.Evidence[0], 4)
	denseMemStart, _ := SemanticAssessmentBoundaryRef(request.Evidence[0], 14)
	denseMemEnd, _ := SemanticAssessmentBoundaryRef(request.Evidence[0], 23)
	productRef := "product-1"
	request.SubmissionContract = &SemanticAssessmentSubmissionContract{
		Entities: []SemanticAssessmentRequiredEntityRef{
			{
				Ref: "person-1", Name: "Mark", Kind: "person", EvidenceIDs: []string{"ev-1"},
				Groundings: []SemanticAssessmentEntityGrounding{{GroundingRef: "grounding-mark", EvidenceID: "ev-1", Surface: "Mark", StartRef: markStart, EndRef: markEnd}},
			},
			{
				Ref: "product-1", Name: "Dense-Mem", Kind: "product", EvidenceIDs: []string{"ev-1"},
				Groundings: []SemanticAssessmentEntityGrounding{{GroundingRef: "grounding-dense-mem", EvidenceID: "ev-1", Surface: "Dense-Mem", StartRef: denseMemStart, EndRef: denseMemEnd}},
			},
		},
		Relationships: []SemanticAssessmentRequiredRelationshipRef{{
			ProposalID:    "relationship-1",
			PredicateHint: "works_on",
			EvidenceIDs:   []string{"ev-1"},
			SubjectRef:    "person-1",
			ObjectRef:     &productRef,
			Polarity:      "+",
		}},
	}
	return request, limits
}

func semanticAssessmentTestResponse() SemanticAssessmentResponse {
	mark := "entity-mark"
	denseMem := "entity-dense-mem"
	predicate := "works_on"
	version := 1
	markGrounding := "grounding-mark"
	denseMemGrounding := "grounding-dense-mem"
	evidence := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "ev-1", Content: "Mark works on Dense-Mem."})
	return SemanticAssessmentResponse{
		RequestID:               "assess-1",
		EvidenceSecurityResults: []SemanticAssessmentEvidenceSecurityResult{{EvidenceID: "ev-1", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{}}},
		EntityResults: []SemanticAssessmentEntityResult{
			{
				Ref:               "person-1",
				GroundingRef:      &markGrounding,
				Surface:           "Mark",
				Kind:              "person",
				EvidenceID:        "ev-1",
				Start:             0,
				End:               4,
				Action:            "reuse",
				CandidateEntityID: &mark,
			},
			{
				Ref:               "product-1",
				GroundingRef:      &denseMemGrounding,
				Surface:           "Dense-Mem",
				Kind:              "product",
				EvidenceID:        "ev-1",
				Start:             14,
				End:               23,
				Action:            "reuse",
				CandidateEntityID: &denseMem,
			},
		},
		RelationshipResults: []SemanticAssessmentRelationshipResult{{
			Ref: "relationship-1", Disposition: "stored",
			Splits: []SemanticAssessmentRelationshipSplit{{
				SplitIndex:        0,
				SubjectRef:        "person-1",
				OriginalPredicate: "works on",
				PredicateRange:    semanticAssessmentTestRange(evidence, 5, 13),
				PredicateStatus:   "resolved",
				PredicateKey:      &predicate,
				PredicateVersion:  &version,
				ObjectRef:         stringPointer("product-1"),
				ObjectValue:       nil,
				Polarity:          "+",
				Evidence: []SemanticAssessmentEvidenceSpan{{
					EvidenceID: "ev-1",
					Start:      0,
					End:        24,
				}},
				SupportRanges: []SemanticAssessmentGroundedRange{semanticAssessmentTestRange(evidence, 0, 24)},
				ValidFrom:     nil,
				ValidTo:       nil,
			}},
		}},
	}
}

func semanticAssessmentTestRange(evidence SemanticReviewEvidence, start, end int) SemanticAssessmentGroundedRange {
	startRef, startOK := SemanticAssessmentBoundaryRef(evidence, start)
	endRef, endOK := SemanticAssessmentBoundaryRef(evidence, end)
	if !startOK || !endOK {
		panic("semantic assessment test boundary is missing")
	}
	return SemanticAssessmentGroundedRange{
		EvidenceID: evidence.EvidenceID,
		StartRef:   startRef,
		EndRef:     endRef,
		Start:      start,
		End:        end,
	}
}

func stringPointer(value string) *string {
	return &value
}
