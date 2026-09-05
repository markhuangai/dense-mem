package assessor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCountSemanticAssessmentRequestTokensHandlesFramingAndSerializationErrors(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1_000_000
	limits.MaxCandidateContextTokens = 1_000_000

	contextError := SemanticAssessmentRequest{
		EntityCandidateGroups: []SemanticAssessmentEntityCandidateGroup{{Candidates: []SemanticAssessmentEntityCandidate{{IdentityContext: map[string]any{"unsupported": func() {}}}}}},
	}
	_, _, err := CountSemanticAssessmentRequestTokens(contextError, limits)
	require.Error(t, err)

	payloadError := SemanticAssessmentRequest{ClientProposal: map[string]any{"unsupported": func() {}}}
	_, _, err = CountSemanticAssessmentRequestTokens(payloadError, limits)
	require.Error(t, err)

	limits.Tokenizer = "not-a-tokenizer"
	_, _, err = CountSemanticAssessmentRequestTokens(SemanticAssessmentRequest{}, limits)
	require.Error(t, err)

	legacyLimits := DefaultSemanticAssessmentLimits()
	legacyLimits.LegacyProviderFraming = true
	inputTokens, candidateContextTokens, err := CountSemanticAssessmentRequestTokens(SemanticAssessmentRequest{RequestID: "legacy"}, legacyLimits)
	require.NoError(t, err)
	require.Greater(t, inputTokens, candidateContextTokens)
}

func TestAllocateSemanticAssessmentOptionalContextPreservesRequiredAndOmitsOptional(t *testing.T) {
	base := SemanticAssessmentRequest{
		RequestID: "request",
		Evidence:  []SemanticReviewEvidence{{EvidenceID: "evidence:0", Content: "submitted evidence"}},
	}
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1_000_000
	limits.MaxCandidateContextTokens = 1_000_000

	baseInput, _, err := CountSemanticAssessmentRequestTokens(base, limits)
	require.NoError(t, err)
	optional := base
	optional.PredicateOptions = []SemanticAssessmentPredicateOption{{
		PredicateKey:        strings.Repeat("optional_predicate_", 80),
		Version:             1,
		AllowedSubjectKinds: []string{"concept"},
		AllowedObjectKinds:  []string{"concept"},
		RelationshipKind:    "state",
		CurrentCardinality:  "many",
	}}
	optionalLimits := limits
	optionalLimits.MaxInputTokens = baseInput
	errs, err := allocateSemanticAssessmentOptionalContext(&optional, optionalLimits)
	require.NoError(t, err)
	require.Empty(t, errs)
	require.Len(t, optional.PredicateOptions, 0)
	require.Equal(t, 1, optional.CandidateContextOmittedPredicateOptions)
	require.True(t, optional.CandidateContextTruncated)

	required := base
	required.PredicateOptions = []SemanticAssessmentPredicateOption{{PredicateKey: "required", Version: 1}}
	required.RequiredRelationshipRefs = []SemanticAssessmentRequiredRelationshipRef{{KnownPredicateKey: " required "}}
	required.SubmissionContract = &SemanticAssessmentSubmissionContract{Relationships: []SemanticAssessmentRequiredRelationshipRef{{KnownPredicateKey: "required"}}}
	errs, err = allocateSemanticAssessmentOptionalContext(&required, limits)
	require.NoError(t, err)
	require.Empty(t, errs)
	require.Len(t, required.PredicateOptions, 1)
	require.Equal(t, 0, required.CandidateContextOmittedPredicateOptions)

	groups := base
	groups.EvidenceEquivalenceCandidates = []SemanticAssessmentEvidenceEquivalenceCandidateGroup{
		{EvidenceID: "nonnumeric"},
		{EvidenceID: "evidence:bad", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{{EvidenceID: "bad", Content: "bad"}}},
		{EvidenceID: "evidence:-1", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{{EvidenceID: "negative", Content: "negative"}}},
		{EvidenceID: "evidence:2", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{{EvidenceID: "numeric", Content: "numeric"}}},
	}
	errs, err = allocateSemanticAssessmentOptionalContext(&groups, limits)
	require.NoError(t, err)
	require.Empty(t, errs)
	require.Equal(t, "evidence:2", groups.EvidenceEquivalenceCandidates[0].EvidenceID)
	require.Len(t, groups.EvidenceEquivalenceCandidates[0].Candidates, 1)
	// Empty groups are retained, and the round-robin allocator skips them.
	require.Empty(t, groups.EvidenceEquivalenceCandidates[3].Candidates)
	require.Len(t, groups.EvidenceEquivalenceCandidates[1].Candidates, 1)
}

func TestAllocateSemanticAssessmentOptionalContextRejectsNilAndRequiredBudgets(t *testing.T) {
	_, err := allocateSemanticAssessmentOptionalContext(nil, DefaultSemanticAssessmentLimits())
	require.EqualError(t, err, "semantic assessment request is required")

	req := SemanticAssessmentRequest{RequestID: "request", Evidence: []SemanticReviewEvidence{{EvidenceID: "evidence:0", Content: "evidence"}}}
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1
	limits.MaxCandidateContextTokens = 1_000_000
	errs, err := allocateSemanticAssessmentOptionalContext(&req, limits)
	require.NoError(t, err)
	require.Len(t, errs, 1)
	require.Equal(t, "input_tokens", errs[0].Field)
	require.Greater(t, req.InputTokens, limits.MaxInputTokens)

	contextReq := SemanticAssessmentRequest{EntityCandidateGroups: []SemanticAssessmentEntityCandidateGroup{{EvidenceID: "evidence:0", Candidates: []SemanticAssessmentEntityCandidate{{EntityID: "entity", CanonicalName: "entity"}}}}}
	limits = DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1_000_000
	limits.MaxCandidateContextTokens = 1
	errs, err = allocateSemanticAssessmentOptionalContext(&contextReq, limits)
	require.NoError(t, err)
	require.Len(t, errs, 1)
	require.Equal(t, "candidate_context_tokens", errs[0].Field)
}

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

func TestSemanticAssessmentBudgetFailureStageIdentifiesRequiredContext(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxCandidateContextTokens = 1
	entityReq := SemanticAssessmentRequest{EntityCandidateGroups: []SemanticAssessmentEntityCandidateGroup{{EvidenceID: "evidence:0", Candidates: []SemanticAssessmentEntityCandidate{{CanonicalName: strings.Repeat("entity ", 20)}}}}}
	require.Equal(t, "entity_catalog", SemanticAssessmentBudgetFailureStage(entityReq, "candidate_context_tokens", limits))
	predicateReq := SemanticAssessmentRequest{PredicateOptions: []SemanticAssessmentPredicateOption{{PredicateKey: strings.Repeat("predicate_", 20)}}}
	require.Equal(t, "predicate_context", SemanticAssessmentBudgetFailureStage(predicateReq, "candidate_context_tokens", limits))
	require.Equal(t, "catalog_context", SemanticAssessmentBudgetFailureStage(SemanticAssessmentRequest{}, "candidate_context_tokens", limits))
	require.Equal(t, "assessment_input", SemanticAssessmentBudgetFailureStage(SemanticAssessmentRequest{}, "other", limits))

	base := SemanticAssessmentRequest{RequestID: "request", Evidence: []SemanticReviewEvidence{{EvidenceID: "submitted", Content: "submitted"}}}
	withKnown := base
	withKnown.KnownEvidence = []SemanticReviewEvidence{{EvidenceID: "known", Content: strings.Repeat("known ", 100)}}
	require.Equal(t, "known_evidence_context", stageForInputOverflow(t, withKnown, limits))
	withEntity := base
	withEntity.EntityCandidateGroups = []SemanticAssessmentEntityCandidateGroup{{EvidenceID: "submitted", Candidates: []SemanticAssessmentEntityCandidate{{CanonicalName: strings.Repeat("entity ", 100)}}}}
	require.Equal(t, "entity_catalog", stageForInputOverflow(t, withEntity, limits))
	withPredicate := base
	withPredicate.PredicateOptions = []SemanticAssessmentPredicateOption{{PredicateKey: strings.Repeat("predicate_", 100)}}
	require.Equal(t, "predicate_context", stageForInputOverflow(t, withPredicate, limits))

	client := SemanticAssessmentRequest{Evidence: []SemanticReviewEvidence{{EvidenceID: "submitted", Content: strings.Repeat("client ", 1000)}}}
	clientLimits := limits
	clientLimits.MaxInputTokens = 1
	require.Equal(t, "assessment_input", SemanticAssessmentBudgetFailureStage(client, "input_tokens", clientLimits))
}

func stageForInputOverflow(t *testing.T, req SemanticAssessmentRequest, limits SemanticAssessmentLimits) string {
	t.Helper()
	countLimits := limits
	countLimits.MaxInputTokens = 1_000_000
	countLimits.MaxCandidateContextTokens = 1_000_000
	input, _, err := CountSemanticAssessmentRequestTokens(req, countLimits)
	require.NoError(t, err)
	withoutRequired := req
	withoutRequired.KnownEvidence = nil
	withoutRequired.EntityCandidateGroups = nil
	withoutRequired.PredicateOptions = nil
	baseInput, _, err := CountSemanticAssessmentRequestTokens(withoutRequired, countLimits)
	require.NoError(t, err)
	require.Greater(t, input, baseInput)
	limits.MaxInputTokens = baseInput
	limits.MaxCandidateContextTokens = 1_000_000
	return SemanticAssessmentBudgetFailureStage(req, "input_tokens", limits)
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

func TestSemanticAssessmentBudgetFailureStageKeepsOversizedClientInputClientControlled(t *testing.T) {
	req := SemanticAssessmentRequest{
		RequestID: "request", TeamID: "team",
		Evidence: []SemanticReviewEvidence{{EvidenceID: "evidence:0", Content: strings.Repeat("client evidence ", 5_000)}},
		EntityCandidateGroups: []SemanticAssessmentEntityCandidateGroup{{
			EvidenceID: "evidence:0", GroundingRef: "grounding", Surface: "client", Start: 0, End: 6,
		}},
	}
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1
	require.Equal(t, "assessment_input", SemanticAssessmentBudgetFailureStage(req, "input_tokens", limits))
}

func TestSemanticAssessmentBudgetFailureStageAttributesCombinedRequiredContext(t *testing.T) {
	entityGroups := []SemanticAssessmentEntityCandidateGroup{{
		EvidenceID: "evidence:0", GroundingRef: "grounding", Surface: "entity",
		Candidates: []SemanticAssessmentEntityCandidate{{
			EntityID: "entity-1", CanonicalName: strings.Repeat("entity ", 30), Kind: "concept",
		}},
	}}
	predicateOptions := []SemanticAssessmentPredicateOption{{
		PredicateKey: strings.Repeat("predicate_", 10), Version: 1,
		AllowedSubjectKinds: []string{"concept"}, AllowedObjectKinds: []string{"concept"},
		RelationshipKind: "state", CurrentCardinality: "many",
	}}
	req := SemanticAssessmentRequest{
		RequestID: "request", TeamID: "team",
		Evidence:                 []SemanticReviewEvidence{{EvidenceID: "evidence:0", Content: "entity predicate"}},
		EntityCandidateGroups:    entityGroups,
		PredicateOptions:         predicateOptions,
		RequiredRelationshipRefs: []SemanticAssessmentRequiredRelationshipRef{{KnownPredicateKey: predicateOptions[0].PredicateKey}},
	}
	limits := DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1_000_000
	entityTokens := semanticAssessmentContextComponentTokens(entityGroups, limits.Tokenizer)
	predicateTokens := semanticAssessmentContextComponentTokens(predicateOptions, limits.Tokenizer)
	inputTokens, contextTokens, err := CountSemanticAssessmentRequestTokens(req, limits)
	require.NoError(t, err)
	require.Greater(t, contextTokens, entityTokens)
	require.Greater(t, contextTokens, predicateTokens)
	limits.MaxCandidateContextTokens = contextTokens - 1
	_, allocationErr := allocateSemanticAssessmentOptionalContext(&req, limits)
	require.NoError(t, allocationErr)
	// The exact predicate is mandatory, so the allocator cannot remove either
	// required component; the resulting failure must be server-owned.
	require.Equal(t, "entity_catalog", SemanticAssessmentBudgetFailureStage(req, "candidate_context_tokens", limits))
	limits.MaxInputTokens = inputTokens - 1
	require.Equal(t, "entity_catalog", SemanticAssessmentBudgetFailureStage(req, "input_tokens", limits))
}
