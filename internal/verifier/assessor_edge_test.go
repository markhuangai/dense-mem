package verifier

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSemanticAssessmentRequestRejectsInvalidCandidateContext(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*SemanticAssessmentRequest, SemanticAssessmentLimits)
		want   string
	}{
		{
			name: "stale evidence revision",
			mutate: func(req *SemanticAssessmentRequest, _ SemanticAssessmentLimits) {
				req.Evidence[0].SourceRevisionID = "old"
				req.Evidence[0].CurrentSourceRevisionID = "current"
			},
			want: "is not current",
		},
		{
			name: "unknown candidate evidence",
			mutate: func(req *SemanticAssessmentRequest, _ SemanticAssessmentLimits) {
				req.EntityCandidateGroups[0].EvidenceID = "missing"
			},
			want: "entity_candidate_groups[0].evidence_id: is unknown",
		},
		{
			name: "too many candidates",
			mutate: func(req *SemanticAssessmentRequest, limits SemanticAssessmentLimits) {
				candidate := req.EntityCandidateGroups[0].Candidates[0]
				for range limits.MaxCandidatesPerSurface {
					req.EntityCandidateGroups[0].Candidates = append(req.EntityCandidateGroups[0].Candidates, candidate)
				}
			},
			want: "must contain at most",
		},
		{
			name: "too many evidence items",
			mutate: func(req *SemanticAssessmentRequest, _ SemanticAssessmentLimits) {
				for index := len(req.Evidence); index <= SemanticAssessmentMaxEvidenceSpans; index++ {
					req.Evidence = append(req.Evidence, SemanticReviewEvidence{
						EvidenceID: fmt.Sprintf("ev-%d", index+1), FragmentID: fmt.Sprintf("fragment-%d", index+1), Content: "additional evidence",
					})
				}
			},
			want: "evidence: must contain at most",
		},
		{
			name: "duplicate candidate span with different context",
			mutate: func(req *SemanticAssessmentRequest, _ SemanticAssessmentLimits) {
				duplicate := req.EntityCandidateGroups[0]
				duplicate.GroundingRef = "grounding-duplicate"
				duplicate.Candidates = append([]SemanticAssessmentEntityCandidate(nil), duplicate.Candidates...)
				duplicate.Candidates[0].EntityID = "entity-different"
				req.EntityCandidateGroups = append(req.EntityCandidateGroups, duplicate)
			},
			want: "duplicates an entity evidence span with different candidate context",
		},
		{
			name: "invalid candidate kind",
			mutate: func(req *SemanticAssessmentRequest, _ SemanticAssessmentLimits) {
				req.EntityCandidateGroups[0].Candidates[0].Kind = "unsupported"
			},
			want: "kind: is unsupported",
		},
		{
			name: "duplicate predicate option",
			mutate: func(req *SemanticAssessmentRequest, _ SemanticAssessmentLimits) {
				req.PredicateOptions = append(req.PredicateOptions, req.PredicateOptions[0])
			},
			want: "predicate_options[1]: is duplicated",
		},
		{
			name: "invalid predicate endpoint kind",
			mutate: func(req *SemanticAssessmentRequest, _ SemanticAssessmentLimits) {
				req.PredicateOptions[0].AllowedObjectKinds = []string{"unsupported"}
			},
			want: "allowed_object_kinds: must contain supported entity or value kinds",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			req, limits := semanticAssessmentTestRequest(t)
			testCase.mutate(&req, limits)
			if _, errs := PrepareSemanticAssessmentRequest(req, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), testCase.want) {
				t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v, want %q", errs, testCase.want)
			}
		})
	}
}

func TestSemanticAssessmentSecurityResultErrorsUseMatchingResultIndex(t *testing.T) {
	request, limits := semanticAssessmentTestRequest(t)
	request.Evidence = append(request.Evidence, SemanticReviewEvidence{
		EvidenceID: "ev-2", FragmentID: "fragment-2", Content: "A second evidence item.",
	})
	prepared, errs := PrepareSemanticAssessmentRequest(request, limits)
	require.Empty(t, errs)

	response := semanticAssessmentTestResponse()
	response.SecurityResults = []SemanticAssessmentSecurityResult{
		{EvidenceID: "ev-2", Decision: "pass"},
		{EvidenceID: "ev-1", Decision: "pass"},
	}
	startRef, _ := SemanticAssessmentBoundaryRef(prepared.Evidence[0], 0)
	endRef, _ := SemanticAssessmentBoundaryRef(prepared.Evidence[0], 4)
	response.SecuritySignals = []SemanticAssessmentSecuritySignal{{
		EvidenceID: "ev-1", Kind: "instruction_override", StartRef: startRef, EndRef: endRef,
	}}

	_, validationErrors := PrepareSemanticAssessmentResponse(prepared, response, limits)
	joined := semanticAssessmentJoinedErrors(validationErrors)
	require.Contains(t, joined, "security_results[1].decision: must be quarantine when security_signals cite the evidence")
	require.NotContains(t, joined, "security_results[0].decision: must be quarantine when security_signals cite the evidence")
}

func TestSemanticAssessmentRequestRejectsMalformedTrustedRelationshipRefs(t *testing.T) {
	testCases := []struct {
		name string
		refs []SemanticAssessmentRequiredRelationshipRef
		want []string
	}{
		{
			name: "missing proposal and evidence",
			refs: []SemanticAssessmentRequiredRelationshipRef{{}},
			want: []string{
				"required_relationship_refs[0].proposal_id: is required",
				"required_relationship_refs[0].evidence_ids: must contain between 1 and",
			},
		},
		{
			name: "duplicate proposal",
			refs: []SemanticAssessmentRequiredRelationshipRef{
				{ProposalID: "proposal-1", Evidence: []SemanticAssessmentEvidenceSpan{{EvidenceID: "ev-1", Start: 0, End: 4}}},
				{ProposalID: "proposal-1", Evidence: []SemanticAssessmentEvidenceSpan{{EvidenceID: "ev-1", Start: 0, End: 4}}},
			},
			want: []string{"required_relationship_refs[1].proposal_id: is duplicated"},
		},
		{
			name: "unknown invalid and duplicate spans",
			refs: []SemanticAssessmentRequiredRelationshipRef{{
				ProposalID: "proposal-1",
				Evidence: []SemanticAssessmentEvidenceSpan{
					{EvidenceID: "missing", Start: 0, End: 4},
					{EvidenceID: "ev-1", Start: -1, End: 0},
					{EvidenceID: "ev-1", Start: 0, End: 4},
					{EvidenceID: "ev-1", Start: 0, End: 4},
				},
			}},
			want: []string{
				"required_relationship_refs[0].evidence[0].evidence_id: is unknown",
				"required_relationship_refs[0].evidence[1]: span is invalid",
				"required_relationship_refs[0].evidence[3]: is duplicated",
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			req, limits := semanticAssessmentTestRequest(t)
			req.RequiredRelationshipRefs = testCase.refs
			_, errs := PrepareSemanticAssessmentRequest(req, limits)
			if len(errs) == 0 {
				t.Fatal("PrepareSemanticAssessmentRequest() errors = nil, want malformed trusted relationship refs rejection")
			}
			joined := semanticAssessmentJoinedErrors(errs)
			for _, want := range testCase.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("PrepareSemanticAssessmentRequest() errors = %q, want %q", joined, want)
				}
			}
		})
	}
}

func TestSemanticAssessmentSubmissionContractRequiresExactCompleteResponse(t *testing.T) {
	req, limits := semanticAssessmentSubmissionContractTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	if len(prepared.SubmittedEntities) != 2 || len(prepared.SubmittedRelationships) != 1 {
		t.Fatalf("prepared submission contract = %#v / %#v", prepared.SubmittedEntities, prepared.SubmittedRelationships)
	}

	t.Run("accepts exact response and registration request", func(t *testing.T) {
		response := semanticAssessmentTestResponse()
		response.RelationshipResults[0].Splits[0].PredicateStatus = "registration_required"
		response.RelationshipResults[0].Splits[0].PredicateKey = nil
		response.RelationshipResults[0].Splits[0].PredicateVersion = nil
		response.RelationshipResults[0].Splits[0].PredicateRegistration = &SemanticAssessmentPredicateRegistration{
			PredicateKey: "works_on", RelationshipKind: "state", CurrentCardinality: "many",
		}
		if _, errs := PrepareSemanticAssessmentResponse(prepared, response, limits); len(errs) != 0 {
			t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v", errs)
		}
	})

	for _, testCase := range []struct {
		name   string
		mutate func(*SemanticAssessmentResponse)
		want   string
	}{
		{
			name:   "missing entity target",
			mutate: func(response *SemanticAssessmentResponse) { response.EntityResults = response.EntityResults[:1] },
			want:   "is missing a submitted entity target",
		},
		{
			name:   "extra relationship target",
			mutate: func(response *SemanticAssessmentResponse) { response.RelationshipResults[0].Ref = "invented" },
			want:   "is outside the submitted relationship contract",
		},
		{
			name:   "changes preserved relationship polarity",
			mutate: func(response *SemanticAssessmentResponse) { response.RelationshipResults[0].Splits[0].Polarity = "-" },
			want:   "does not preserve its submitted relationship target",
		},
		{
			name: "adds support outside evidence allowlist",
			mutate: func(response *SemanticAssessmentResponse) {
				response.RelationshipResults[0].Splits[0].SupportRanges[0].EvidenceID = "ev-other"
			},
			want: "is outside the submitted evidence allowlist",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := semanticAssessmentTestResponse()
			testCase.mutate(&response)
			if _, errs := PrepareSemanticAssessmentResponse(prepared, response, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), testCase.want) {
				t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want %q", errs, testCase.want)
			}
		})
	}
}

func TestSemanticAssessmentRelationshipDispositionsAndSplits(t *testing.T) {
	request, limits := semanticAssessmentSubmissionContractTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(request, limits)
	require.Empty(t, errs)

	t.Run("not supported permits intentionally ungrounded entities", func(t *testing.T) {
		response := semanticAssessmentTestResponse()
		reason := "not_supported_by_evidence"
		response.RelationshipResults[0] = SemanticAssessmentRelationshipResult{
			Ref: response.RelationshipResults[0].Ref, Disposition: "not_supported", Reason: &reason,
			Splits: []SemanticAssessmentRelationshipSplit{},
		}
		for index := range response.EntityResults {
			response.EntityResults[index].GroundingRef = nil
			response.EntityResults[index].Action = "ambiguous"
			response.EntityResults[index].CandidateEntityID = nil
		}

		_, validationErrors := PrepareSemanticAssessmentResponse(prepared, response, limits)
		require.Empty(t, validationErrors)
	})

	t.Run("not supported permits an ungrounded exact known entity", func(t *testing.T) {
		exactRequest, exactLimits := semanticAssessmentSubmissionContractTestRequest(t)
		knownEntityID := "entity-mark"
		exactRequest.SubmissionContract.Entities[0].Name = "Mark Huang"
		exactRequest.SubmissionContract.Entities[0].KnownEntityID = knownEntityID
		exactRequest.SubmissionContract.Entities[0].Groundings = nil
		exactRequest.EntityCandidateGroups = exactRequest.EntityCandidateGroups[1:]
		exactPrepared, requestErrors := PrepareSemanticAssessmentRequest(exactRequest, exactLimits)
		require.Empty(t, requestErrors)

		response := semanticAssessmentTestResponse()
		response.EntityResults[0].GroundingRef = nil
		response.EntityResults[0].Action = "reuse"
		response.EntityResults[0].CandidateEntityID = &knownEntityID
		reason := "not_supported_by_evidence"
		response.RelationshipResults[0] = SemanticAssessmentRelationshipResult{
			Ref: response.RelationshipResults[0].Ref, Disposition: "not_supported", Reason: &reason,
			Splits: []SemanticAssessmentRelationshipSplit{},
		}

		_, validationErrors := PrepareSemanticAssessmentResponse(exactPrepared, response, exactLimits)
		require.Empty(t, validationErrors)

		response.RelationshipResults = semanticAssessmentTestResponse().RelationshipResults
		_, validationErrors = PrepareSemanticAssessmentResponse(exactPrepared, response, exactLimits)
		require.Contains(t, semanticAssessmentJoinedErrors(validationErrors), "grounding_ref: is required unless action is ambiguous")
	})

	t.Run("stored split requires grounded entities", func(t *testing.T) {
		response := semanticAssessmentTestResponse()
		response.EntityResults[0].GroundingRef = nil
		response.EntityResults[0].Action = "ambiguous"
		response.EntityResults[0].CandidateEntityID = nil

		_, validationErrors := PrepareSemanticAssessmentResponse(prepared, response, limits)
		require.Contains(t, semanticAssessmentJoinedErrors(validationErrors), "references an ungrounded Entity")
	})

	t.Run("stored splits are contiguous", func(t *testing.T) {
		response := semanticAssessmentTestResponse()
		second := response.RelationshipResults[0].Splits[0]
		second.SplitIndex = 1
		response.RelationshipResults[0].Splits = append(response.RelationshipResults[0].Splits, second)

		_, validationErrors := PrepareSemanticAssessmentResponse(prepared, response, limits)
		require.Empty(t, validationErrors)

		response.RelationshipResults[0].Splits[1].SplitIndex = 2
		_, validationErrors = PrepareSemanticAssessmentResponse(prepared, response, limits)
		require.Contains(t, semanticAssessmentJoinedErrors(validationErrors), "split_index: must equal 1")
	})
}

func TestSemanticAssessmentLegacyEvidenceIsBounded(t *testing.T) {
	request, limits := semanticAssessmentTestRequest(t)
	legacyEvidence := make([]SemanticAssessmentEvidenceSpan, SemanticAssessmentMaxEvidenceSpans+1)
	for index := range legacyEvidence {
		legacyEvidence[index] = SemanticAssessmentEvidenceSpan{EvidenceID: "ev-1", Start: 0, End: 1}
	}
	request.RequiredRelationshipRefs = []SemanticAssessmentRequiredRelationshipRef{{
		ProposalID: "relationship-1",
		Evidence:   legacyEvidence,
	}}
	_, errs := PrepareSemanticAssessmentRequest(request, limits)
	if len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "must contain at most") {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v, want legacy evidence bound", errs)
	}
}

func TestSemanticAssessmentEntityGroundingsAreBounded(t *testing.T) {
	request, limits := semanticAssessmentSubmissionContractTestRequest(t)
	groundings := request.SubmissionContract.Entities[0].Groundings
	for index := 1; index < SemanticAssessmentMaxEntityGroundings+1; index++ {
		grounding := groundings[0]
		grounding.GroundingRef = "grounding-mark-" + fmt.Sprint(index)
		groundings = append(groundings, grounding)
	}
	request.SubmissionContract.Entities[0].Groundings = groundings
	_, errs := PrepareSemanticAssessmentRequest(request, limits)
	if len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "groundings: must contain at most") {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v, want grounding bound", errs)
	}
}

func TestSemanticAssessmentSubmissionRangeValidationIsDeterministic(t *testing.T) {
	request, limits := semanticAssessmentSubmissionContractTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(request, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	response := semanticAssessmentTestResponse()
	response.RelationshipResults[0].Splits[0].PredicateRange.EvidenceID = "ev-other"
	valueRange := response.RelationshipResults[0].Splits[0].PredicateRange
	response.RelationshipResults[0].Splits[0].ValueRange = &valueRange

	validationErrors := validateSemanticAssessmentSubmissionResponse(prepared.SubmissionContract, response)
	fields := make([]string, 0, 2)
	for _, validationError := range validationErrors {
		if strings.Contains(validationError.Message, "outside the submitted evidence allowlist") {
			fields = append(fields, validationError.Field)
		}
	}
	if want := []string{
		"relationship_results[0].splits[0].predicate_range.evidence_id",
		"relationship_results[0].splits[0].value_range.evidence_id",
	}; len(fields) != len(want) || fields[0] != want[0] || fields[1] != want[1] {
		t.Fatalf("range validation fields = %#v, want %#v", fields, want)
	}
}

func TestSemanticAssessmentSubmissionPreservesTemporalBounds(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*SemanticAssessmentRelationshipSplit)
	}{
		{
			name: "changed valid_from",
			mutate: func(result *SemanticAssessmentRelationshipSplit) {
				result.ValidFrom = stringPointer("2026-07-01T00:00:00Z")
			},
		},
		{
			name: "cleared valid_to",
			mutate: func(result *SemanticAssessmentRelationshipSplit) {
				result.ValidTo = nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, limits := semanticAssessmentSubmissionContractTestRequest(t)
			from, to := "2026-06-20T00:00:00Z", "2026-06-27T00:00:00Z"
			request.SubmissionContract.Relationships[0].ValidFrom = &from
			request.SubmissionContract.Relationships[0].ValidTo = &to
			prepared, errs := PrepareSemanticAssessmentRequest(request, limits)
			require.Empty(t, errs)
			response := semanticAssessmentTestResponse()
			response.RelationshipResults[0].Splits[0].ValidFrom = stringPointer(from)
			response.RelationshipResults[0].Splits[0].ValidTo = stringPointer(to)
			test.mutate(&response.RelationshipResults[0].Splits[0])
			validationErrors := validateSemanticAssessmentSubmissionResponse(prepared.SubmissionContract, response)
			require.Contains(t, semanticAssessmentJoinedErrors(validationErrors), "does not preserve the submitted temporal bounds")
		})
	}
}

func TestSemanticAssessmentResponseNormalizesSecurityTimeAndValue(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}

	response := semanticAssessmentTestResponse()
	securityStartRef, _ := SemanticAssessmentBoundaryRef(prepared.Evidence[0], 0)
	securityEndRef, _ := SemanticAssessmentBoundaryRef(prepared.Evidence[0], 4)
	response.SecuritySignals = []SemanticAssessmentSecuritySignal{{
		EvidenceID: "ev-1", Kind: "instruction_override", StartRef: securityStartRef, EndRef: securityEndRef,
	}}
	response.SecurityResults[0].Decision = "quarantine"
	relationship := &response.RelationshipResults[0].Splits[0]
	relationship.PredicateStatus = "registration_required"
	relationship.PredicateKey = nil
	relationship.PredicateVersion = nil
	relationship.PredicateRegistration = &SemanticAssessmentPredicateRegistration{
		PredicateKey: "describes", RelationshipKind: "state", CurrentCardinality: "many",
	}
	relationship.ObjectRef = nil
	relationship.ObjectValue = &SemanticAssessmentValue{
		ValueType:      "string",
		CanonicalValue: " Dense-Mem ",
		Display:        stringPointer(" Dense-Mem "),
		Unit:           stringPointer(" product "),
	}
	relationship.ValidFrom = stringPointer("2026-07-28T04:00:00+04:00")
	relationship.ValidTo = stringPointer("2026-07-30T00:00:00Z")

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	decoded, err := DecodeSemanticAssessmentResponseJSON(encoded, limits)
	if err != nil {
		t.Fatalf("DecodeSemanticAssessmentResponseJSON() error = %v", err)
	}
	validated, errs := PrepareSemanticAssessmentResponse(prepared, decoded, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v", errs)
	}
	if got := *validated.RelationshipResults[0].Splits[0].ValidFrom; got != "2026-07-28T00:00:00Z" {
		t.Fatalf("normalized valid_from = %q, want UTC", got)
	}
	if got := validated.RelationshipResults[0].Splits[0].ObjectValue.CanonicalValue; got != "Dense-Mem" {
		t.Fatalf("normalized canonical_value = %q, want trimmed value", got)
	}
}

func TestSemanticAssessmentResponseRejectsEachTypedObjectValueBound(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}

	testCases := []struct {
		name  string
		value SemanticAssessmentValue
		want  string
	}{
		{
			name:  "canonical value is required",
			value: SemanticAssessmentValue{ValueType: "string"},
			want:  "object: object_value.canonical_value is required and must be bounded",
		},
		{
			name: "unit is bounded independently of display",
			value: SemanticAssessmentValue{
				ValueType: "string", CanonicalValue: "Dense-Mem", Unit: stringPointer(strings.Repeat("u", 129)),
			},
			want: "object: object_value.unit must be bounded",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			response := semanticAssessmentTestResponse()
			response.RelationshipResults[0].Splits[0].ObjectRef = nil
			response.RelationshipResults[0].Splits[0].ObjectValue = &testCase.value
			_, errs := PrepareSemanticAssessmentResponse(prepared, response, limits)
			if len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), testCase.want) {
				t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want %q", errs, testCase.want)
			}
		})
	}
}

func TestDecodeSemanticAssessmentResponseRejectsRawShapeBoundaries(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{
			name: "null required array",
			mutate: func(payload map[string]any) {
				payload["security_signals"] = nil
			},
			want: "security_signals: must not be null",
		},
		{
			name: "non-object entity result",
			mutate: func(payload map[string]any) {
				payload["entity_results"] = []any{nil}
			},
			want: "entity_results[0]: must be an object",
		},
		{
			name: "unknown nested object value field",
			mutate: func(payload map[string]any) {
				relationships := payload["relationship_results"].([]any)
				relationship := relationships[0].(map[string]any)
				split := relationship["splits"].([]any)[0].(map[string]any)
				split["object_ref"] = nil
				split["object_value"] = map[string]any{
					"value_type": "string", "canonical_value": "Dense-Mem", "display": nil, "unit": nil, "unknown": true,
				}
			},
			want: "relationship_results[0].splits[0].object_value.unknown: is unknown",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			encoded, err := json.Marshal(semanticAssessmentTestResponse())
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			payload := map[string]any{}
			if err := json.Unmarshal(encoded, &payload); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			testCase.mutate(payload)
			encoded, err = json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			if _, err := DecodeSemanticAssessmentResponseJSON(encoded, DefaultSemanticAssessmentLimits()); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("DecodeSemanticAssessmentResponseJSON() error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestSemanticAssessmentLimitNormalizationAndTokenizerFailure(t *testing.T) {
	defaults := DefaultSemanticAssessmentLimits()
	if got := normalizeSemanticAssessmentLimits(SemanticAssessmentLimits{}); got != defaults {
		t.Fatalf("normalized defaults = %#v, want %#v", got, defaults)
	}
	if _, err := CountTokens("Dense-Mem", "not-a-tokenizer"); err == nil || !strings.Contains(err.Error(), "not-a-tokenizer") {
		t.Fatalf("CountTokens() error = %v, want tokenizer error", err)
	}
	if got := normalizeAssessmentStrings([]string{" beta ", "", "alpha", "alpha", strings.Repeat("x", 129)}, 2, 128); strings.Join(got, ",") != "alpha,beta" {
		t.Fatalf("normalizeAssessmentStrings() = %#v, want sorted unique values", got)
	}
	if !assessmentKindsAllowed([]string{"person", "string"}, true) {
		t.Fatal("assessmentKindsAllowed() rejected supported entity and value kinds")
	}
	if assessmentKindsAllowed([]string{"string"}, false) {
		t.Fatal("assessmentKindsAllowed() accepted a value kind where only entities are allowed")
	}
}

func TestSemanticAssessmentPreservesPredicateCatalogRank(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	second := req.PredicateOptions[0]
	second.PredicateKey = "second_ranked"
	second.Aliases = []string{"second ranked"}
	first := req.PredicateOptions[0]
	first.PredicateKey = "first_ranked"
	first.Aliases = []string{"first ranked"}
	req.PredicateOptions = []SemanticAssessmentPredicateOption{first, second}
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	if got := []string{prepared.PredicateOptions[0].PredicateKey, prepared.PredicateOptions[1].PredicateKey}; strings.Join(got, ",") != "first_ranked,second_ranked" {
		t.Fatalf("predicate order = %#v, want catalog rank order", got)
	}

}

func TestSemanticAssessmentResponseRejectsSemanticBoundaryViolations(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	validStartRef, _ := SemanticAssessmentBoundaryRef(prepared.Evidence[0], 0)
	validEndRef, _ := SemanticAssessmentBoundaryRef(prepared.Evidence[0], 4)

	testCases := []struct {
		name   string
		mutate func(*SemanticAssessmentResponse)
		want   string
	}{
		{
			name: "required result arrays",
			mutate: func(response *SemanticAssessmentResponse) {
				response.SecuritySignals = nil
				response.EntityResults = nil
				response.RelationshipResults = nil
			},
			want: "security_signals: is required",
		},
		{
			name: "result arrays are bounded",
			mutate: func(response *SemanticAssessmentResponse) {
				response.RequestID = ""
				response.SecuritySignals = make([]SemanticAssessmentSecuritySignal, 65)
				response.EntityResults = make([]SemanticAssessmentEntityResult, SemanticAssessmentMaxEntityResults+1)
				response.RelationshipResults = make([]SemanticAssessmentRelationshipResult, SemanticAssessmentMaxRelationshipResults+1)
			},
			want: "security_signals: must contain at most 64 entries",
		},
		{
			name: "security signals must use authorized boundary references",
			mutate: func(response *SemanticAssessmentResponse) {
				response.SecuritySignals = []SemanticAssessmentSecuritySignal{
					{EvidenceID: "missing", Kind: "instruction_override", StartRef: validStartRef, EndRef: validEndRef},
					{EvidenceID: "ev-1", Kind: "unsupported", StartRef: "invalid", EndRef: "invalid"},
					{EvidenceID: "ev-1", Kind: "instruction_override", StartRef: validStartRef, EndRef: validEndRef},
					{EvidenceID: "ev-1", Kind: "instruction_override", StartRef: validStartRef, EndRef: validEndRef},
				}
			},
			want: "security_signals[1].kind: is unsupported",
		},
		{
			name: "hidden control markup signal requires a matching span",
			mutate: func(response *SemanticAssessmentResponse) {
				response.SecuritySignals = []SemanticAssessmentSecuritySignal{{EvidenceID: "ev-1", Kind: "hidden_control_markup", StartRef: validStartRef, EndRef: validEndRef}}
			},
			want: "hidden_control_markup requires a hidden control or active markup",
		},
		{
			name: "entity result fields and selection",
			mutate: func(response *SemanticAssessmentResponse) {
				result := &response.EntityResults[0]
				result.Ref = ""
				result.Surface = "not Mark"
				result.Kind = "unsupported"
				result.Action = "unsupported"
			},
			want: "entity_results[0].action: is unsupported",
		},
		{
			name: "entity action candidate constraints",
			mutate: func(response *SemanticAssessmentResponse) {
				response.EntityResults[0].CandidateEntityID = nil
				response.EntityResults[1].Action = "create"
				response.EntityResults[1].CandidateEntityID = stringPointer("entity-dense-mem")
				response.EntityResults = append(response.EntityResults, SemanticAssessmentEntityResult{
					Ref:               "ambiguous-1",
					Surface:           "Mark",
					Kind:              "person",
					EvidenceID:        "ev-1",
					Start:             0,
					End:               4,
					Action:            "ambiguous",
					CandidateEntityID: stringPointer("entity-mark"),
				})
			},
			want: "candidate_entity_id: is required for reuse",
		},
		{
			name: "relationship required fields and enums",
			mutate: func(response *SemanticAssessmentResponse) {
				response.RelationshipResults[0].Ref = ""
				result := &response.RelationshipResults[0].Splits[0]
				result.SubjectRef = "missing"
				result.OriginalPredicate = ""
				result.ObjectRef = stringPointer("missing")
				result.Polarity = "?"
				result.PredicateStatus = "unsupported"
				result.Evidence = nil
			},
			want: "relationship_results[0].splits[0].subject_ref: is unknown",
		},
		{
			name: "resolved predicate requires selected allowlist member",
			mutate: func(response *SemanticAssessmentResponse) {
				response.RelationshipResults[0].Splits[0].PredicateKey = nil
				response.RelationshipResults[0].Splits[0].PredicateVersion = nil
			},
			want: "predicate_key and predicate_version are required for resolved",
		},
		{
			name: "resolved predicate cannot escape allowlist",
			mutate: func(response *SemanticAssessmentResponse) {
				response.RelationshipResults[0].Splits[0].PredicateKey = stringPointer("not_allowed")
			},
			want: "predicate_key: is outside predicate allowlist",
		},
		{
			name: "unresolved predicate is not a stored outcome",
			mutate: func(response *SemanticAssessmentResponse) {
				response.RelationshipResults[0].Splits[0].PredicateStatus = "unresolved"
			},
			want: "predicate_status: is unsupported",
		},
		{
			name: "relationship object must be exactly one typed value or entity",
			mutate: func(response *SemanticAssessmentResponse) {
				response.RelationshipResults[0].Splits[0].ObjectValue = &SemanticAssessmentValue{ValueType: "string", CanonicalValue: "Dense-Mem"}
			},
			want: "object: requires exactly one object_ref or object_value",
		},
		{
			name: "typed object value is bounded and typed",
			mutate: func(response *SemanticAssessmentResponse) {
				response.RelationshipResults[0].Splits[0].ObjectRef = nil
				response.RelationshipResults[0].Splits[0].ObjectValue = &SemanticAssessmentValue{
					ValueType:      "unsupported",
					CanonicalValue: "",
					Display:        stringPointer(strings.Repeat("d", 4097)),
					Unit:           stringPointer(strings.Repeat("u", 129)),
				}
			},
			want: "object: object_value.value_type is unsupported",
		},
		{
			name: "typed object display and unit are bounded",
			mutate: func(response *SemanticAssessmentResponse) {
				response.RelationshipResults[0].Splits[0].ObjectRef = nil
				response.RelationshipResults[0].Splits[0].ObjectValue = &SemanticAssessmentValue{
					ValueType:      "string",
					CanonicalValue: "Dense-Mem",
					Display:        stringPointer(strings.Repeat("d", 4097)),
					Unit:           stringPointer(strings.Repeat("u", 129)),
				}
			},
			want: "object: object_value.display must be bounded",
		},
		{
			name: "relationship support ranges require valid references",
			mutate: func(response *SemanticAssessmentResponse) {
				response.RelationshipResults[0].Splits[0].SupportRanges[0].StartRef = "invalid"
			},
			want: "relationship_results[0].splits[0].support_ranges[0]: boundary references are invalid",
		},
		{
			name: "temporal bounds are ordered",
			mutate: func(response *SemanticAssessmentResponse) {
				response.RelationshipResults[0].Splits[0].ValidFrom = stringPointer("2026-07-30T00:00:00Z")
				response.RelationshipResults[0].Splits[0].ValidTo = stringPointer("2026-07-28T00:00:00Z")
			},
			want: "valid_to: must not be before valid_from",
		},
		{
			name: "time requires an RFC3339 timestamp",
			mutate: func(response *SemanticAssessmentResponse) {
				response.RelationshipResults[0].Splits[0].ValidFrom = stringPointer("not-a-time")
			},
			want: "valid_from: must be an RFC3339 timestamp or null",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			response := semanticAssessmentTestResponse()
			testCase.mutate(&response)
			if _, validationErrors := PrepareSemanticAssessmentResponse(prepared, response, limits); len(validationErrors) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(validationErrors), testCase.want) {
				t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want %q", validationErrors, testCase.want)
			}
		})
	}
}
