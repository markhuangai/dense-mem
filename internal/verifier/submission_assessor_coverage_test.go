package verifier

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubmissionAssessmentDecodeAndPredicateValidationBoundaries(t *testing.T) {
	response := submissionAssessmentTestResponse()
	raw, err := json.Marshal(response)
	require.NoError(t, err)

	limits := DefaultSemanticAssessmentLimits()
	limits.MaxOutputTokens = 1
	_, err = DecodeSubmissionAssessmentResponseJSON(raw, limits)
	require.ErrorContains(t, err, "exceeds")

	_, err = DecodeSubmissionAssessmentResponseJSON([]byte(`not-json`), DefaultSemanticAssessmentLimits())
	require.ErrorContains(t, err, "must be an object")
	_, err = DecodeSubmissionAssessmentResponseJSON([]byte(`{"request_id":"only"}`), DefaultSemanticAssessmentLimits())
	require.Error(t, err)
	invalidTokenizer := DefaultSemanticAssessmentLimits()
	invalidTokenizer.Tokenizer = "unknown-tokenizer"
	_, err = DecodeSubmissionAssessmentResponseJSON(raw, invalidTokenizer)
	require.ErrorContains(t, err, "tokenizer")

	resolved := response.RelationshipResults[0]
	resolved.PredicateCandidate = &SubmissionPredicateCandidate{PredicateKey: "novel_predicate", RelationshipKind: "state"}
	needsReview := response.RelationshipResults[0]
	needsReview.PredicateStatus = "needs_review"
	needsReview.PredicateKey = nil
	needsReview.PredicateVersion = nil
	needsReview.PredicateCandidate = nil
	invalidCandidate := needsReview
	invalidCandidate.PredicateCandidate = &SubmissionPredicateCandidate{PredicateKey: "Invalid Key", RelationshipKind: "unsupported"}

	errs := validateSubmissionPredicateCandidates(nil, []SubmissionAssessmentRelationshipResult{resolved, needsReview, invalidCandidate})
	joined := semanticAssessmentJoinedErrors(errs)
	require.Contains(t, joined, "must be null for resolved")
	require.Contains(t, joined, "is required for a novel predicate")
	require.Contains(t, joined, "lowercase snake_case")
	require.Contains(t, joined, "relationship_kind")

	for _, key := range []string{"works_on", "v2", "a"} {
		require.True(t, submissionPredicateKeyAllowed(key), key)
	}
	for _, key := range []string{"", "_leading", "trailing_", "two-dashes", "Upper", strings.Repeat("a", 65)} {
		require.False(t, submissionPredicateKeyAllowed(key), key)
	}

	knownCandidate := needsReview
	knownCandidate.PredicateCandidate = &SubmissionPredicateCandidate{PredicateKey: "works_on", RelationshipKind: "state"}
	knownErrs := validateSubmissionPredicateCandidates([]SemanticAssessmentPredicateOption{{
		PredicateKey: "contributes_to",
		Aliases:      []string{" Works-On "},
	}}, []SubmissionAssessmentRelationshipResult{knownCandidate})
	require.Contains(t, semanticAssessmentJoinedErrors(knownErrs), "must identify a new predicate")
}

func TestSubmissionRequiredProposalAndCorrespondenceValidationBoundaries(t *testing.T) {
	req, _ := semanticAssessmentTestRequest(t)
	evidence := semanticEvidenceByID(req.Evidence)

	empty := &SubmissionAssessmentRequiredProposal{}
	errs := normalizeSubmissionAssessmentRequiredProposal(empty, evidence)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "entities")
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "relationships")

	unknown := &SubmissionAssessmentRequiredProposal{
		Entities: []SubmissionAssessmentRequiredEntity{{Ref: "subject", Surface: "Mark", EvidenceID: "unknown", Start: 0, End: 4}},
		Relationships: []SubmissionAssessmentRequiredRelationship{{
			ProposalID: "relationship", SubjectRef: "missing", OriginalPredicate: "wrong", PredicateEvidenceID: "unknown", PredicateStart: 0, PredicateEnd: 1,
			ObjectValueType: "unsupported", Polarity: "?", Modality: "command", Evidence: []SemanticAssessmentEvidenceSpan{{EvidenceID: "unknown", Start: 0, End: 1}},
		}},
	}
	errs = normalizeSubmissionAssessmentRequiredProposal(unknown, evidence)
	joined := semanticAssessmentJoinedErrors(errs)
	for _, want := range []string{"evidence_id", "subject_ref", "object_value_type", "predicate.evidence_id", "polarity", "modality", "evidence[0]"} {
		require.Contains(t, joined, want)
	}

	required := submissionAssessmentTestRequiredProposal()
	semantic := semanticAssessmentTestResponse()
	entities := semantic.EntityResults
	relationships := make([]SubmissionAssessmentRelationshipResult, len(semantic.RelationshipResults))
	for index, result := range semantic.RelationshipResults {
		relationships[index] = SubmissionAssessmentRelationshipResult{SemanticAssessmentRelationshipResult: result}
	}
	errs = validateSubmissionAssessmentRequiredProposal(required, entities, relationships)
	require.Contains(t, semanticAssessmentJoinedErrors(errs), "must retain a submitted entity ref")

	required = submissionAssessmentTestRequiredProposal()
	entities[0].Ref = "subject"
	entities[1].Ref = "object"
	relationships[0].Ref = "relationship-1"
	relationships[0].SubjectRef = "subject"
	relationships[0].ObjectRef = stringPointer("object")
	errs = validateSubmissionAssessmentRequiredProposal(required, entities, relationships)
	require.Empty(t, errs)

	require.False(t, submissionAssessmentRequiredEvidenceMatches([]SemanticAssessmentEvidenceSpan{{EvidenceID: "ev-1", Start: 0, End: 1}}, []SemanticAssessmentEvidenceSpan{{EvidenceID: "ev-1", Start: 1, End: 2}}))
	require.False(t, submissionAssessmentRequiredObjectMatches(SubmissionAssessmentRequiredRelationship{ObjectRef: "entity"}, SemanticAssessmentRelationshipResult{}))
}

func TestSubmissionSecurityAssessmentValidationBoundaries(t *testing.T) {
	req, _ := semanticAssessmentTestRequest(t)
	require.Contains(t, semanticAssessmentJoinedErrors(validateSubmissionSecurityAssessments(req, nil)), "is required")

	base := submissionAssessmentTestResponse().SecurityAssessments[0]
	noConcernSignals := base
	noConcernSignals.Signals = []SemanticSecuritySignal{{EvidenceID: "ev-1", Kind: "instruction_override", Start: 0, End: 4}}
	concernWithoutSignals := base
	concernWithoutSignals.Verdict = "concern"
	unknown := base
	unknown.EvidenceID = "unknown"
	unknown.Justification = ""

	require.Contains(t, semanticAssessmentJoinedErrors(validateSubmissionSecurityAssessments(req, []SubmissionSecurityAssessment{noConcernSignals})), "must be empty")
	require.Contains(t, semanticAssessmentJoinedErrors(validateSubmissionSecurityAssessments(req, []SubmissionSecurityAssessment{concernWithoutSignals})), "must identify")
	unknownErrors := semanticAssessmentJoinedErrors(validateSubmissionSecurityAssessments(req, []SubmissionSecurityAssessment{unknown}))
	require.Contains(t, unknownErrors, "is unknown")
	require.Contains(t, unknownErrors, "justification")
	require.Contains(t, semanticAssessmentJoinedErrors(validateSubmissionSecurityAssessments(req, []SubmissionSecurityAssessment{base, base})), "exactly one assessment")

	prefixed := prefixSubmissionSecurityErrors("security", []SemanticValidationError{{Field: "signals[0]", Message: "invalid"}})
	require.Equal(t, "security.signals[0]", prefixed[0].Field)
	require.False(t, SubmissionAssessmentResponse{}.HasSecurityConcern())
	require.True(t, SubmissionAssessmentResponse{SecurityAssessments: []SubmissionSecurityAssessment{{Verdict: "concern"}}}.HasSecurityConcern())
}

func TestSubmissionAssessmentRawResponseAndCorrectionBoundaries(t *testing.T) {
	response := submissionAssessmentTestResponse()
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	require.Empty(t, validateSubmissionAssessmentResponseRaw(raw))

	payload := func() map[string]any {
		var value map[string]any
		require.NoError(t, json.Unmarshal(raw, &value))
		return value
	}
	for _, testCase := range []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"security assessments must be an array", func(value map[string]any) { value["security_assessments"] = "invalid" }, "security_assessments"},
		{"security signals must be an array", func(value map[string]any) {
			value["security_assessments"].([]any)[0].(map[string]any)["signals"] = "invalid"
		}, "signals"},
		{"relationships must be an array", func(value map[string]any) { value["relationship_results"] = "invalid" }, "relationship_results"},
		{"relationship evidence must be an array", func(value map[string]any) {
			value["relationship_results"].([]any)[0].(map[string]any)["evidence"] = "invalid"
		}, "evidence"},
		{"object value must be closed", func(value map[string]any) {
			value["relationship_results"].([]any)[0].(map[string]any)["object_value"] = map[string]any{"unexpected": true}
		}, "object_value"},
		{"predicate candidate must be closed", func(value map[string]any) {
			value["relationship_results"].([]any)[0].(map[string]any)["predicate_candidate"] = map[string]any{"unexpected": true}
		}, "predicate_candidate"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			value := payload()
			testCase.edit(value)
			encoded, err := json.Marshal(value)
			require.NoError(t, err)
			require.Contains(t, semanticAssessmentJoinedErrors(validateSubmissionAssessmentResponseRaw(encoded)), testCase.want)
		})
	}

	req, limits := semanticAssessmentTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	require.Empty(t, errs)
	validated, validationErrors, stage := submissionAssessmentResponseForCorrection(prepared, openAIStructuredChatResult{Content: string(raw)}, limits)
	require.Empty(t, validationErrors)
	require.Empty(t, stage)
	require.Positive(t, validated.OutputTokens)

	limited := limits
	limited.MaxOutputTokens = 1
	_, validationErrors, stage = submissionAssessmentResponseForCorrection(prepared, openAIStructuredChatResult{
		Content: string(raw), ReportedUsage: &openAIVerifierUsage{CompletionTokens: 2},
	}, limited)
	require.Equal(t, "response_output_tokens", stage)
	require.Contains(t, semanticAssessmentJoinedErrors(validationErrors), "output_tokens")

	wrongType := payload()
	wrongType["request_id"] = 1
	wrongTypeRaw, err := json.Marshal(wrongType)
	require.NoError(t, err)
	_, validationErrors, stage = submissionAssessmentResponseForCorrection(prepared, openAIStructuredChatResult{Content: string(wrongTypeRaw)}, limits)
	require.Equal(t, "response_json", stage)
	require.Contains(t, semanticAssessmentJoinedErrors(validationErrors), "request_id")

	prepared.RequiredSubmissionProposal = submissionAssessmentTestRequiredProposal()
	_, validationErrors, stage = submissionAssessmentResponseForCorrection(prepared, openAIStructuredChatResult{Content: string(raw)}, limits)
	require.Equal(t, "response_contract", stage)
	require.Contains(t, semanticAssessmentJoinedErrors(validationErrors), "submitted entity ref")

	knownPredicate := submissionAssessmentTestResponse()
	knownPredicate.RelationshipResults[0].PredicateStatus = "needs_review"
	knownPredicate.RelationshipResults[0].PredicateKey = nil
	knownPredicate.RelationshipResults[0].PredicateVersion = nil
	knownPredicate.RelationshipResults[0].PredicateCandidate = &SubmissionPredicateCandidate{PredicateKey: "works_on", RelationshipKind: "state"}
	knownRaw, err := json.Marshal(knownPredicate)
	require.NoError(t, err)
	prepared.RequiredSubmissionProposal = nil
	_, validationErrors, stage = submissionAssessmentResponseForCorrection(prepared, openAIStructuredChatResult{Content: string(knownRaw)}, limits)
	require.Equal(t, "response_contract", stage)
	require.Contains(t, semanticAssessmentJoinedErrors(validationErrors), "must identify a new predicate")

	semanticRelationships := submissionSemanticRelationships(response.RelationshipResults)
	require.Equal(t, response.RelationshipResults[0].Ref, semanticRelationships[0].Ref)
}

func TestSubmissionRequiredProposalNormalizationRejectsUnsafeBindings(t *testing.T) {
	req, _ := semanticAssessmentTestRequest(t)
	evidence := semanticEvidenceByID(req.Evidence)
	required := &SubmissionAssessmentRequiredProposal{
		Entities: []SubmissionAssessmentRequiredEntity{
			{Ref: "duplicate", Surface: "Mark", EvidenceID: "ev-1", Start: 0, End: 4},
			{Ref: "duplicate", Surface: "wrong", EvidenceID: "ev-1", Start: 0, End: 4},
			{Ref: "unknown", Surface: "Mark", EvidenceID: "missing", Start: 0, End: 4},
		},
		Relationships: []SubmissionAssessmentRequiredRelationship{
			{ProposalID: "duplicate", SubjectRef: "missing", OriginalPredicate: "works on", PredicateEvidenceID: "missing", PredicateStart: 0, PredicateEnd: 1, Polarity: "?", Modality: "command"},
			{ProposalID: "duplicate", SubjectRef: "duplicate", OriginalPredicate: "wrong", PredicateEvidenceID: "ev-1", PredicateStart: 0, PredicateEnd: 1, ObjectValueType: "unsupported", Polarity: "+", Modality: "statement", Evidence: []SemanticAssessmentEvidenceSpan{{EvidenceID: "missing", Start: 0, End: 1}, {EvidenceID: "ev-1", Start: 0, End: 1}, {EvidenceID: "ev-1", Start: 0, End: 1}}},
		},
	}
	errs := normalizeSubmissionAssessmentRequiredProposal(required, evidence)
	joined := semanticAssessmentJoinedErrors(errs)
	for _, want := range []string{"entities[1].ref", "entities[2].evidence_id", "relationships[0].subject_ref", "relationships[0].object", "relationships[0].predicate.evidence_id", "relationships[0].polarity", "relationships[0].modality", "relationships[1].proposal_id", "relationships[1].object_value_type", "relationships[1].predicate.span", "relationships[1].evidence[0].evidence_id", "relationships[1].evidence[2]"} {
		require.Contains(t, joined, want)
	}

	display := "PostgreSQL"
	unit := "database"
	valueType := SubmissionAssessmentRequiredRelationship{
		ObjectValueType: "string", ObjectValueCanonical: "PostgreSQL", ObjectValueDisplay: &display, ObjectValueUnit: &unit,
	}
	require.True(t, submissionAssessmentRequiredObjectMatches(valueType, SemanticAssessmentRelationshipResult{ObjectValue: &SemanticAssessmentValue{
		ValueType: "string", CanonicalValue: "PostgreSQL", Display: &display, Unit: &unit,
	}}))
	require.False(t, submissionAssessmentRequiredObjectMatches(valueType, SemanticAssessmentRelationshipResult{ObjectValue: &SemanticAssessmentValue{
		ValueType: "string", CanonicalValue: "MySQL", Display: &display, Unit: &unit,
	}}))
	require.True(t, submissionAssessmentNullableValueMatches(nil, nil))
	require.False(t, submissionAssessmentNullableValueMatches(nil, &display))
	require.False(t, submissionAssessmentNullableValueMatches(&display, nil))
	otherDisplay := "MySQL"
	require.False(t, submissionAssessmentNullableValueMatches(&display, &otherDisplay))
	require.False(t, submissionAssessmentRequiredEvidenceMatches([]SemanticAssessmentEvidenceSpan{{EvidenceID: "ev-1", Start: 0, End: 1}}, nil))
	require.Nil(t, prefixSubmissionSecurityErrors("security", nil))
}

func TestSubmissionAssessmentValidationRejectsIncompleteBindings(t *testing.T) {
	req, _ := semanticAssessmentTestRequest(t)
	invalidSecurity := SubmissionSecurityAssessment{EvidenceID: "", Verdict: "unsupported", Signals: []SemanticSecuritySignal{}, Justification: "bounded"}
	securityErrors := semanticAssessmentJoinedErrors(validateSubmissionSecurityAssessments(req, []SubmissionSecurityAssessment{invalidSecurity}))
	require.Contains(t, securityErrors, "evidence_id")
	require.Contains(t, securityErrors, "verdict")

	twoEvidence := req
	twoEvidence.Evidence = append(twoEvidence.Evidence, SemanticReviewEvidence{EvidenceID: "ev-2", FragmentID: "fragment-2", Content: "Other evidence."})
	base := submissionAssessmentTestResponse().SecurityAssessments[0]
	duplicateErrors := semanticAssessmentJoinedErrors(validateSubmissionSecurityAssessments(twoEvidence, []SubmissionSecurityAssessment{base, base}))
	require.Contains(t, duplicateErrors, "duplicated")
	require.Contains(t, duplicateErrors, "missing an evidence assessment")

	evidenceByID := semanticEvidenceByID(req.Evidence)
	required := &SubmissionAssessmentRequiredProposal{
		Entities: []SubmissionAssessmentRequiredEntity{
			{Ref: "subject", Surface: "Mark", EvidenceID: "ev-1", Start: 0, End: 4},
			{Ref: "", Surface: "Mark", EvidenceID: "ev-1", Start: 0, End: 4},
		},
		Relationships: []SubmissionAssessmentRequiredRelationship{{
			ProposalID: "", SubjectRef: "subject", ObjectRef: "missing", OriginalPredicate: "works on", PredicateEvidenceID: "ev-1", PredicateStart: 5, PredicateEnd: 13,
			Polarity: "+", Modality: "statement", Evidence: []SemanticAssessmentEvidenceSpan{{EvidenceID: "ev-1", Start: 0, End: 99}},
		}},
	}
	normalizedErrors := semanticAssessmentJoinedErrors(normalizeSubmissionAssessmentRequiredProposal(required, evidenceByID))
	for _, want := range []string{"entities[1].ref", "relationships[0].proposal_id", "relationships[0].object_ref", "relationships[0].evidence[0]"} {
		require.Contains(t, normalizedErrors, want)
	}

	matching := submissionAssessmentTestRequiredProposal()
	correspondenceErrors := semanticAssessmentJoinedErrors(validateSubmissionAssessmentRequiredProposal(matching, nil, nil))
	require.Contains(t, correspondenceErrors, "entity_results")
	require.Contains(t, correspondenceErrors, "relationship_results")
	wrongEntities := []SemanticAssessmentEntityResult{{Ref: "subject", Surface: "wrong", EvidenceID: "ev-1", Start: 0, End: 4}}
	wrongRelationships := []SubmissionAssessmentRelationshipResult{{SemanticAssessmentRelationshipResult: SemanticAssessmentRelationshipResult{Ref: "unknown"}}}
	correspondenceErrors = semanticAssessmentJoinedErrors(validateSubmissionAssessmentRequiredProposal(matching, wrongEntities, wrongRelationships))
	require.Contains(t, correspondenceErrors, "exact entity span")
	require.Contains(t, correspondenceErrors, "submitted proposal_id")
}
