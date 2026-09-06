package assessor

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Reserve bounded correction-instruction room after the configured output
// allowance so optional context cannot consume the entire next-turn budget.
const semanticAssessmentRepairSafetyTokens = 4096

// CountSemanticAssessmentRequestTokens returns the token cost of the complete
// request envelope and the candidate-context sub-payload. The structured
// response schema is included because providers account for it as part of the
// request even though it is not a chat message.
func CountSemanticAssessmentRequestTokens(
	req SemanticAssessmentRequest,
	limits SemanticAssessmentLimits,
) (inputTokens int, candidateContextTokens int, err error) {
	limits = normalizeSemanticAssessmentLimits(limits)
	contextPayload, err := json.Marshal(semanticAssessmentCandidateContext{
		EntityCandidateGroups:         req.EntityCandidateGroups,
		PredicateOptions:              req.PredicateOptions,
		EvidenceEquivalenceCandidates: req.EvidenceEquivalenceCandidates,
	})
	if err != nil {
		return 0, 0, err
	}
	candidateContextTokens, err = CountTokens(string(contextPayload), limits.Tokenizer)
	if err != nil {
		return 0, 0, err
	}
	req.CandidateContextTokens = candidateContextTokens
	req.InputTokens = 0
	payload, err := json.Marshal(req)
	if err != nil {
		return 0, 0, err
	}
	if limits.LegacyProviderFraming {
		inputTokens, err = CountTokens(semanticAssessmentSystemPrompt+string(payload), limits.Tokenizer)
		if err != nil {
			return 0, 0, err
		}
		return inputTokens, candidateContextTokens, nil
	}
	inputTokens, err = CountSemanticAssessmentProviderRequestTokens(
		limits.ProviderModel,
		limits.ProviderSchemaName,
		SemanticAssessmentResponseSchema(),
		limits.ProviderTemperatureDisabled,
		[]SemanticAssessmentProviderMessage{
			{Role: "system", Content: SemanticAssessmentSystemPrompt},
			{Role: "user", Content: string(payload)},
		},
		limits.Tokenizer,
	)
	if err != nil {
		return 0, 0, err
	}
	return inputTokens, candidateContextTokens, nil
}

// CountSemanticAssessmentProviderFramingTokens returns the fixed request cost
// that remains when no request-specific content is supplied. The empty user
// message preserves the provider's message wrapper, so a budget below this
// value cannot admit any semantic assessment request.
func CountSemanticAssessmentProviderFramingTokens(limits SemanticAssessmentLimits) (int, error) {
	limits = normalizeSemanticAssessmentLimits(limits)
	if limits.LegacyProviderFraming {
		payload, err := json.Marshal(SemanticAssessmentRequest{})
		if err != nil {
			return 0, err
		}
		return CountTokens(semanticAssessmentSystemPrompt+string(payload), limits.Tokenizer)
	}
	return CountSemanticAssessmentProviderRequestTokens(
		limits.ProviderModel,
		limits.ProviderSchemaName,
		SemanticAssessmentResponseSchema(),
		limits.ProviderTemperatureDisabled,
		[]SemanticAssessmentProviderMessage{
			{Role: "system", Content: SemanticAssessmentSystemPrompt},
			{Role: "user", Content: "{}"},
		},
		limits.Tokenizer,
	)
}

// allocateSemanticAssessmentOptionalContext removes only optional provider
// context until the complete serialized request fits both configured budgets.
// Required entity catalogs, known evidence, exact predicates, and equivalence
// groups remain present even when a group has no selected candidates.
func allocateSemanticAssessmentOptionalContext(
	req *SemanticAssessmentRequest,
	limits SemanticAssessmentLimits,
) ([]SemanticValidationError, error) {
	if req == nil {
		return nil, errors.New("semantic assessment request is required")
	}
	limits = normalizeSemanticAssessmentLimits(limits)
	req.CandidateContextOmittedCandidates = 0
	req.CandidateContextOmittedPredicateOptions = 0
	preservedTruncation := req.CandidateContextTruncated
	req.CandidateContextTruncated = preservedTruncation

	mandatoryPredicate := make(map[string]struct{})
	for _, relationship := range req.RequiredRelationshipRefs {
		if key := strings.TrimSpace(relationship.KnownPredicateKey); key != "" {
			mandatoryPredicate[key] = struct{}{}
		}
	}
	if req.SubmissionContract != nil {
		for _, relationship := range req.SubmissionContract.Relationships {
			if key := strings.TrimSpace(relationship.KnownPredicateKey); key != "" {
				mandatoryPredicate[key] = struct{}{}
			}
		}
	}

	allPredicates := append([]SemanticAssessmentPredicateOption(nil), req.PredicateOptions...)
	req.PredicateOptions = req.PredicateOptions[:0]
	optionalPredicates := make([]SemanticAssessmentPredicateOption, 0, len(allPredicates))
	for _, option := range allPredicates {
		key := strings.TrimSpace(option.PredicateKey)
		if _, required := mandatoryPredicate[key]; required {
			req.PredicateOptions = append(req.PredicateOptions, option)
			continue
		}
		optionalPredicates = append(optionalPredicates, option)
	}

	// Candidate groups are emitted in submitted evidence-index order. The
	// numeric order is part of the round-robin contract; lexical sorting would
	// place evidence:10 before evidence:2.
	sort.SliceStable(req.EvidenceEquivalenceCandidates, func(i, j int) bool {
		left, leftOK := semanticAssessmentEvidenceIndex(req.EvidenceEquivalenceCandidates[i].EvidenceID)
		right, rightOK := semanticAssessmentEvidenceIndex(req.EvidenceEquivalenceCandidates[j].EvidenceID)
		if leftOK && rightOK && left != right {
			return left < right
		}
		if leftOK != rightOK {
			return leftOK
		}
		return req.EvidenceEquivalenceCandidates[i].EvidenceID < req.EvidenceEquivalenceCandidates[j].EvidenceID
	})
	allCandidates := make([][]SemanticAssessmentEvidenceEquivalenceCandidate, len(req.EvidenceEquivalenceCandidates))
	for index := range req.EvidenceEquivalenceCandidates {
		allCandidates[index] = append([]SemanticAssessmentEvidenceEquivalenceCandidate(nil), req.EvidenceEquivalenceCandidates[index].Candidates...)
		req.EvidenceEquivalenceCandidates[index].Candidates = []SemanticAssessmentEvidenceEquivalenceCandidate{}
	}

	inputTokens, contextTokens, err := CountSemanticAssessmentRequestTokens(*req, limits)
	if err != nil {
		return nil, err
	}
	// Preserve the measured values before returning any required-context
	// failure. Callers use these fields to report the actual server-owned
	// boundary that prevented admission.
	req.InputTokens = inputTokens
	req.CandidateContextTokens = contextTokens
	if contextTokens > limits.MaxCandidateContextTokens {
		return []SemanticValidationError{semanticErr("candidate_context_tokens", fmt.Sprintf("required assessor context exceeds the configured budget (observed %d, limit %d)", contextTokens, limits.MaxCandidateContextTokens))}, nil
	}
	conversationInputLimit := semanticAssessmentConversationInputLimit(limits)
	if inputTokens > conversationInputLimit {
		return []SemanticValidationError{semanticErr("input_tokens", fmt.Sprintf("required assessor input leaves insufficient repair headroom (observed %d, limit %d)", inputTokens, conversationInputLimit))}, nil
	}
	optionalInputLimit := conversationInputLimit

	maxRounds := len(optionalPredicates)
	for _, candidates := range allCandidates {
		if len(candidates) > maxRounds {
			maxRounds = len(candidates)
		}
	}
	for round := 0; round < maxRounds; round++ {
		if round < len(optionalPredicates) {
			option := optionalPredicates[round]
			req.PredicateOptions = append(req.PredicateOptions, option)
			candidateInput, candidateContext, countErr := CountSemanticAssessmentRequestTokens(*req, limits)
			if countErr != nil {
				return nil, countErr
			}
			if candidateContext <= limits.MaxCandidateContextTokens && candidateInput <= optionalInputLimit {
				inputTokens, contextTokens = candidateInput, candidateContext
			} else {
				req.PredicateOptions = req.PredicateOptions[:len(req.PredicateOptions)-1]
				req.CandidateContextOmittedPredicateOptions++
			}
		}
		for groupIndex := range req.EvidenceEquivalenceCandidates {
			if round >= len(allCandidates[groupIndex]) {
				continue
			}
			candidate := allCandidates[groupIndex][round]
			req.EvidenceEquivalenceCandidates[groupIndex].Candidates = append(req.EvidenceEquivalenceCandidates[groupIndex].Candidates, candidate)
			candidateInput, candidateContext, countErr := CountSemanticAssessmentRequestTokens(*req, limits)
			if countErr != nil {
				return nil, countErr
			}
			if candidateContext <= limits.MaxCandidateContextTokens && candidateInput <= optionalInputLimit {
				inputTokens, contextTokens = candidateInput, candidateContext
				continue
			}
			candidates := req.EvidenceEquivalenceCandidates[groupIndex].Candidates
			req.EvidenceEquivalenceCandidates[groupIndex].Candidates = candidates[:len(candidates)-1]
			req.CandidateContextOmittedCandidates++
		}
	}
	req.CandidateContextTruncated = preservedTruncation || req.CandidateContextOmittedCandidates > 0 || req.CandidateContextOmittedPredicateOptions > 0
	req.CandidateContextTokens = contextTokens
	req.InputTokens = inputTokens
	return nil, nil
}

func semanticAssessmentConversationInputLimit(limits SemanticAssessmentLimits) int {
	repairTurns := SemanticAssessmentMaxProviderTurns - 1
	headroom := repairTurns * (limits.MaxOutputTokens + semanticAssessmentRepairSafetyTokens)
	if headroom >= limits.MaxInputTokens {
		return 0
	}
	return limits.MaxInputTokens - headroom
}

// SemanticAssessmentBudgetFailureStage identifies the remaining required
// context when optional assessor context has already been removed. The
// returned stage is used only for safe operator diagnostics; it does not alter
// the request or provider policy.
func SemanticAssessmentBudgetFailureStage(req SemanticAssessmentRequest, field string, limits SemanticAssessmentLimits) string {
	limits = normalizeSemanticAssessmentLimits(limits)
	field = strings.TrimSpace(field)
	if field == "candidate_context_tokens" {
		if tokens := semanticAssessmentContextComponentTokens(req.EntityCandidateGroups, limits.Tokenizer); tokens > limits.MaxCandidateContextTokens {
			return "entity_catalog"
		}
		if tokens := semanticAssessmentContextComponentTokens(req.PredicateOptions, limits.Tokenizer); tokens > limits.MaxCandidateContextTokens {
			return "predicate_context"
		}
		// The entity catalog and exact predicate context are both required. If
		// their combined serialized payload exceeds the cap while neither one
		// does independently, attribute the failure to the first required
		// component in the deterministic request order instead of calling it
		// optional context.
		if len(req.EntityCandidateGroups) > 0 {
			return "entity_catalog"
		}
		if len(req.PredicateOptions) > 0 {
			return "predicate_context"
		}
		return "catalog_context"
	}
	if field != "input_tokens" {
		return "assessment_input"
	}
	if framingTokens, err := CountSemanticAssessmentProviderFramingTokens(limits); err == nil && framingTokens > limits.MaxInputTokens {
		return "provider_framing"
	}
	// A required known-evidence catalog is the narrowest server-owned cause
	// when removing it makes the complete serialized request fit.
	if len(req.KnownEvidence) > 0 {
		withoutKnown := req
		withoutKnown.KnownEvidence = nil
		if input, _, err := CountSemanticAssessmentRequestTokens(withoutKnown, limits); err == nil && input <= limits.MaxInputTokens {
			return "known_evidence_context"
		}
	}
	if len(req.EntityCandidateGroups) > 0 {
		withoutCatalog := req
		withoutCatalog.EntityCandidateGroups = nil
		if input, _, err := CountSemanticAssessmentRequestTokens(withoutCatalog, limits); err == nil && input <= limits.MaxInputTokens {
			return "entity_catalog"
		}
	}
	if len(req.PredicateOptions) > 0 {
		withoutPredicates := req
		withoutPredicates.PredicateOptions = nil
		if input, _, err := CountSemanticAssessmentRequestTokens(withoutPredicates, limits); err == nil && input <= limits.MaxInputTokens {
			return "predicate_context"
		}
	}
	// If no single required component accounts for the overflow, distinguish
	// combined server-owned context from an independently oversized client
	// payload. Only the former may be attributed to the server.
	withoutRequired := req
	withoutRequired.EntityCandidateGroups = nil
	withoutRequired.KnownEvidence = nil
	withoutRequired.PredicateOptions = nil
	if input, _, err := CountSemanticAssessmentRequestTokens(withoutRequired, limits); err == nil && input <= limits.MaxInputTokens {
		if len(req.EntityCandidateGroups) > 0 {
			return "entity_catalog"
		}
		if len(req.KnownEvidence) > 0 {
			return "known_evidence_context"
		}
		if len(req.PredicateOptions) > 0 {
			return "predicate_context"
		}
	}
	return "assessment_input"
}

func semanticAssessmentContextComponentTokens(value any, tokenizerName string) int {
	payload, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	tokens, err := CountTokens(string(payload), tokenizerName)
	if err != nil {
		return 0
	}
	return tokens
}

func semanticAssessmentEvidenceIndex(evidenceID string) (int, bool) {
	prefix := "evidence:"
	if !strings.HasPrefix(evidenceID, prefix) {
		return 0, false
	}
	index, err := strconv.Atoi(strings.TrimPrefix(evidenceID, prefix))
	return index, err == nil && index >= 0
}
