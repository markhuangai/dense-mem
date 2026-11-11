package assessor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrepareSemanticAssessmentRequestPreservesPriorTruncationOnReprepare(t *testing.T) {
	req := SemanticAssessmentRequest{
		RequestID: "request", TeamID: "team", OwnerProfileID: "owner",
		Evidence:                  []SemanticReviewEvidence{{EvidenceID: "evidence:0", Content: "submitted evidence"}},
		CandidateContextTruncated: true,
	}
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1_000_000
	limits.MaxCandidateContextTokens = 1_000_000
	first, errs := PrepareSemanticAssessmentRequest(req, limits)
	require.Empty(t, errs)
	require.True(t, first.CandidateContextTruncated)
	firstJSON, err := json.Marshal(first)
	require.NoError(t, err)

	second, errs := PrepareSemanticAssessmentRequest(first, limits)
	require.Empty(t, errs)
	require.True(t, second.CandidateContextTruncated)
	secondJSON, err := json.Marshal(second)
	require.NoError(t, err)
	require.JSONEq(t, string(firstJSON), string(secondJSON))
}

func TestPrepareSemanticAssessmentRequestDoesNotMutateCandidateContext(t *testing.T) {
	req := SemanticAssessmentRequest{
		RequestID: "request",
		TeamID:    "team",
		Evidence:  []SemanticReviewEvidence{{EvidenceID: "evidence:0", Content: "submitted evidence"}},
		EvidenceEquivalenceCandidates: []SemanticAssessmentEvidenceEquivalenceCandidateGroup{{
			EvidenceID: "evidence:0",
			Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{{
				EvidenceID: "candidate:0",
				Content:    "historical candidate evidence",
			}},
		}},
	}
	before, err := json.Marshal(req)
	require.NoError(t, err)
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1_000_000
	limits.MaxCandidateContextTokens = 1_000_000
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	require.Empty(t, errs)
	require.Len(t, prepared.EvidenceEquivalenceCandidates[0].Candidates, 1)
	require.Equal(t, "candidate:0", req.EvidenceEquivalenceCandidates[0].Candidates[0].EvidenceID)
	require.Empty(t, req.EvidenceEquivalenceCandidates[0].Candidates[0].BoundaryText)
	require.Nil(t, req.EvidenceEquivalenceCandidates[0].Candidates[0].BoundaryRefs)
	require.Empty(t, req.EvidenceEquivalenceCandidates[0].Candidates[0].BoundaryPrefix)
	after, err := json.Marshal(req)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
}

func TestSemanticAssessmentBudgetFailureStageIdentifiesProviderFramingOverflow(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	framingTokens, err := CountSemanticAssessmentProviderFramingTokens(limits)
	require.NoError(t, err)
	require.Greater(t, framingTokens, 0)
	limits.MaxInputTokens = framingTokens - 1
	require.Equal(t, "provider_framing", SemanticAssessmentBudgetFailureStage(SemanticAssessmentRequest{}, "input_tokens", limits))
	limits.MaxInputTokens = framingTokens + (SemanticAssessmentMaxProviderTurns-1)*(limits.MaxOutputTokens+semanticAssessmentRepairSafetyTokens) - 1
	require.Equal(t, "provider_framing", SemanticAssessmentBudgetFailureStage(SemanticAssessmentRequest{}, "input_tokens", limits))

	limits.LegacyProviderFraming = true
	legacyFramingTokens, err := CountSemanticAssessmentProviderFramingTokens(limits)
	require.NoError(t, err)
	require.Greater(t, legacyFramingTokens, 0)
	limits.MaxInputTokens = legacyFramingTokens - 1
	require.Equal(t, "provider_framing", SemanticAssessmentBudgetFailureStage(SemanticAssessmentRequest{}, "input_tokens", limits))
}

func TestSemanticAssessmentBudgetFailureStageUsesRepairAwareInputLimit(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	request := SemanticAssessmentRequest{
		Evidence: []SemanticReviewEvidence{
			PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "submitted", Content: strings.Repeat("x", SemanticAssessmentMaxEvidenceConflictQuoteRunes+1)}),
		},
		KnownEvidence: []SemanticReviewEvidence{
			PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "known", Content: "known"}),
		},
	}
	countLimits := limits
	countLimits.MaxInputTokens = 1_000_000
	inputTokens, _, err := CountSemanticAssessmentRequestTokens(request, countLimits)
	require.NoError(t, err)
	withoutKnown := request
	withoutKnown.KnownEvidence = nil
	requiredTokens, _, err := CountSemanticAssessmentRequestTokens(withoutKnown, countLimits)
	require.NoError(t, err)
	for _, test := range []struct {
		inputLimit int
		wantStage  string
	}{
		{requiredTokens - 1, "assessment_input"},
		{inputTokens - 1, "known_evidence_context"},
	} {
		limits.MaxInputTokens = test.inputLimit + (SemanticAssessmentMaxProviderTurns-1)*(limits.MaxOutputTokens+semanticAssessmentRepairSafetyTokens)
		require.Greater(t, inputTokens, semanticAssessmentConversationInputLimit(limits))
		require.Less(t, inputTokens, limits.MaxInputTokens)
		require.Equal(t, test.wantStage, SemanticAssessmentBudgetFailureStage(request, "input_tokens", limits))
	}
}

func TestSemanticAssessmentConversationInputLimitNormalizesDefaults(t *testing.T) {
	defaults := DefaultSemanticAssessmentLimits()
	expected := semanticAssessmentConversationInputLimit(defaults)

	require.Equal(t, expected, SemanticAssessmentConversationInputLimit(SemanticAssessmentLimits{}))

	partial := defaults
	partial.MaxOutputTokens = 0
	require.Equal(t, expected, SemanticAssessmentConversationInputLimit(partial))
}

func TestValidateSemanticAssessmentLimitsRequiresUsableInputBudget(t *testing.T) {
	require.NoError(t, ValidateSemanticAssessmentLimits(DefaultSemanticAssessmentLimits()))
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 50_000
	require.ErrorContains(t, ValidateSemanticAssessmentLimits(limits), "no usable room")
	limits.Tokenizer = "not-a-tokenizer"
	require.ErrorContains(t, ValidateSemanticAssessmentLimits(limits), "cannot be measured")
}

func TestValidateSemanticAssessmentLimitsRequiresMinimumValidRequestBudget(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	_, requestErrors := PrepareSemanticAssessmentRequest(minimumSemanticAssessmentRequest(), limits)
	require.Empty(t, requestErrors)
	emptyTokens, _, err := CountSemanticAssessmentRequestTokens(SemanticAssessmentRequest{}, limits)
	require.NoError(t, err)
	minimumTokens, _, err := CountSemanticAssessmentRequestTokens(minimumSemanticAssessmentRequest(), limits)
	require.NoError(t, err)
	require.Greater(t, minimumTokens, emptyTokens)

	repairHeadroom := (SemanticAssessmentMaxProviderTurns - 1) * (limits.MaxOutputTokens + semanticAssessmentRepairSafetyTokens)
	limits.MaxInputTokens = emptyTokens + repairHeadroom + 1
	var validationErr *SemanticAssessmentLimitValidationError
	require.ErrorAs(t, ValidateSemanticAssessmentLimits(limits), &validationErr)
	require.Contains(t, validationErr.Error(), "no usable room")
}

func TestValidateSemanticAssessmentLimitsAcceptsMinimumEvidenceOnlyContextBudget(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	_, minimumCandidateContextTokens, err := CountSemanticAssessmentRequestTokens(minimumSemanticAssessmentRequest(), limits)
	require.NoError(t, err)
	require.Equal(t, 1, minimumCandidateContextTokens)
	limits.MaxCandidateContextTokens = minimumCandidateContextTokens
	require.NoError(t, ValidateSemanticAssessmentLimits(limits))
}

func TestCountSemanticAssessmentProviderFramingTokensUsesTheRequestEnvelope(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	requestTokens, _, err := CountSemanticAssessmentRequestTokens(minimumSemanticAssessmentRequest(), limits)
	require.NoError(t, err)
	framingTokens, err := CountSemanticAssessmentProviderFramingTokens(limits)
	require.NoError(t, err)
	require.Equal(t, requestTokens, framingTokens)

	repairHeadroom := (SemanticAssessmentMaxProviderTurns - 1) * (limits.MaxOutputTokens + semanticAssessmentRepairSafetyTokens)
	limits.MaxInputTokens = framingTokens + repairHeadroom
	var validationErr *SemanticAssessmentLimitValidationError
	require.NoError(t, ValidateSemanticAssessmentLimits(limits))
	limits.MaxInputTokens--
	require.ErrorAs(t, ValidateSemanticAssessmentLimits(limits), &validationErr)
	require.Equal(t, "output_tokens", validationErr.Field)
}

func TestCountSemanticAssessmentRequestTokensMatchesOmittedCandidateFields(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	populated := minimumSemanticAssessmentRequest()
	populated.EvidenceEquivalenceCandidates = []SemanticAssessmentEvidenceEquivalenceCandidateGroup{{
		EvidenceID: "evidence:0",
		Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{{EvidenceID: "known", Content: "known evidence"}},
	}}
	for _, req := range []SemanticAssessmentRequest{minimumSemanticAssessmentRequest(), populated} {
		_, candidateContextTokens, err := CountSemanticAssessmentRequestTokens(req, limits)
		require.NoError(t, err)

		payload, err := json.Marshal(req)
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(payload, &fields))
		candidateFields := map[string]json.RawMessage{}
		for _, key := range []string{"evidence_equivalence_candidates"} {
			if value, ok := fields[key]; ok {
				candidateFields[key] = value
			}
		}
		expectedPayload, err := json.Marshal(candidateFields)
		require.NoError(t, err)
		expectedTokens, err := CountTokens(string(expectedPayload), limits.Tokenizer)
		require.NoError(t, err)
		require.Equal(t, expectedTokens, candidateContextTokens)
	}
	minimumPayload, err := json.Marshal(minimumSemanticAssessmentRequest())
	require.NoError(t, err)
	var minimumFields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(minimumPayload, &minimumFields))
	_, hasOmittedEquivalence := minimumFields["evidence_equivalence_candidates"]
	require.False(t, hasOmittedEquivalence)
}

func TestValidateSemanticAssessmentLimitsIdentifiesInputLimit(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	framingTokens, err := CountSemanticAssessmentProviderFramingTokens(limits)
	require.NoError(t, err)
	limits.MaxInputTokens = framingTokens
	var validationErr *SemanticAssessmentLimitValidationError
	require.ErrorAs(t, ValidateSemanticAssessmentLimits(limits), &validationErr)
	require.Equal(t, "input_tokens", validationErr.Field)
}

func TestSemanticAssessmentContextHelpersHandleInvalidValues(t *testing.T) {
	require.Equal(t, 0, semanticAssessmentContextComponentTokens(map[string]any{"unsupported": func() {}}, "o200k_base"))
	require.Equal(t, 0, semanticAssessmentContextComponentTokens("value", "not-a-tokenizer"))
	for _, test := range []struct {
		evidenceID string
		index      int
		ok         bool
	}{
		{evidenceID: "evidence:0", index: 0, ok: true},
		{evidenceID: "evidence:10", index: 10, ok: true},
		{evidenceID: "evidence:-1", index: -1, ok: false},
		{evidenceID: "evidence:bad", ok: false},
		{evidenceID: "other", ok: false},
	} {
		index, ok := semanticAssessmentEvidenceIndex(test.evidenceID)
		require.Equal(t, test.index, index)
		require.Equal(t, test.ok, ok)
	}
}

func TestAllocateSemanticAssessmentOptionalContextUsesNumericEvidenceOrder(t *testing.T) {
	req := SemanticAssessmentRequest{
		EvidenceEquivalenceCandidates: []SemanticAssessmentEvidenceEquivalenceCandidateGroup{
			{EvidenceID: "evidence:10", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{{EvidenceID: "candidate-10", Content: "ten"}}},
			{EvidenceID: "evidence:2", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{{EvidenceID: "candidate-2", Content: "two"}}},
		},
	}
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1_000_000
	limits.MaxCandidateContextTokens = 1_000_000
	if errs, err := allocateSemanticAssessmentOptionalContext(&req, limits); err != nil || len(errs) != 0 {
		t.Fatalf("allocate optional context: errs=%v err=%v", errs, err)
	}
	if len(req.EvidenceEquivalenceCandidates) != 2 {
		t.Fatalf("groups = %#v", req.EvidenceEquivalenceCandidates)
	}
	if got := req.EvidenceEquivalenceCandidates[0].EvidenceID; got != "evidence:2" {
		t.Fatalf("first group = %q, want evidence:2", got)
	}
	if got := req.EvidenceEquivalenceCandidates[1].EvidenceID; got != "evidence:10" {
		t.Fatalf("second group = %q, want evidence:10", got)
	}
}

func TestAllocateSemanticAssessmentOptionalContextSkipsOversizedCandidate(t *testing.T) {
	small := SemanticAssessmentEvidenceEquivalenceCandidate{EvidenceID: "small", Content: "small candidate"}
	base := SemanticAssessmentRequest{EvidenceEquivalenceCandidates: []SemanticAssessmentEvidenceEquivalenceCandidateGroup{{EvidenceID: "evidence:0", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{small}}}}
	_, baseContext, err := CountSemanticAssessmentRequestTokens(base, DefaultSemanticAssessmentLimits())
	if err != nil {
		t.Fatalf("count base context: %v", err)
	}
	req := SemanticAssessmentRequest{EvidenceEquivalenceCandidates: []SemanticAssessmentEvidenceEquivalenceCandidateGroup{{EvidenceID: "evidence:0", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{
		{EvidenceID: "large", Content: strings.Repeat("large ", 2_000)}, small,
	}}}}
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1_000_000
	limits.MaxCandidateContextTokens = baseContext
	errs, err := allocateSemanticAssessmentOptionalContext(&req, limits)
	if err != nil {
		t.Fatalf("allocate optional context: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("allocation errors = %#v", errs)
	}
	if req.CandidateContextOmittedCandidates != 1 || !req.CandidateContextTruncated {
		t.Fatalf("omission diagnostics = candidates:%d truncated:%v", req.CandidateContextOmittedCandidates, req.CandidateContextTruncated)
	}
	if len(req.EvidenceEquivalenceCandidates[0].Candidates) != 1 || req.EvidenceEquivalenceCandidates[0].Candidates[0].EvidenceID != "small" {
		t.Fatalf("selected candidates = %#v", req.EvidenceEquivalenceCandidates[0].Candidates)
	}
}

func TestAllocateSemanticAssessmentOptionalContextReservesRepairHeadroomForRequiredContext(t *testing.T) {
	req := SemanticAssessmentRequest{
		RequestID: "request",
		Evidence:  []SemanticReviewEvidence{{EvidenceID: "evidence:0", Content: "required evidence"}},
	}
	countLimits := DefaultSemanticAssessmentLimits()
	countLimits.MaxInputTokens = 1_000_000
	countLimits.MaxCandidateContextTokens = 1_000_000
	baseInput, _, err := CountSemanticAssessmentRequestTokens(req, countLimits)
	require.NoError(t, err)
	repairHeadroom := (SemanticAssessmentMaxProviderTurns - 1) * (countLimits.MaxOutputTokens + semanticAssessmentRepairSafetyTokens)

	limits := countLimits
	limits.MaxInputTokens = baseInput + repairHeadroom - 1
	prepared := req
	errs, err := allocateSemanticAssessmentOptionalContext(&prepared, limits)
	require.NoError(t, err)
	require.Len(t, errs, 1)
	require.Equal(t, "input_tokens", errs[0].Field)
	require.Equal(t, baseInput, prepared.InputTokens)
}
