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

func cloneSemanticAssessmentRequestSlices(req *SemanticAssessmentRequest) {
	if req == nil {
		return
	}
	req.Evidence = append([]SemanticReviewEvidence(nil), req.Evidence...)
	req.KnownEvidence = append([]SemanticReviewEvidence(nil), req.KnownEvidence...)
	req.EvidenceEquivalenceCandidates = cloneSemanticAssessmentEvidenceEquivalenceCandidateGroups(req.EvidenceEquivalenceCandidates)
}

func cloneSemanticAssessmentEvidenceEquivalenceCandidateGroups(groups []SemanticAssessmentEvidenceEquivalenceCandidateGroup) []SemanticAssessmentEvidenceEquivalenceCandidateGroup {
	if groups == nil {
		return nil
	}
	cloned := make([]SemanticAssessmentEvidenceEquivalenceCandidateGroup, len(groups))
	for index, group := range groups {
		cloned[index] = group
		cloned[index].Candidates = append([]SemanticAssessmentEvidenceEquivalenceCandidate(nil), group.Candidates...)
	}
	return cloned
}

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
		SemanticAssessmentResponseSchemaWithLimits(limits),
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

// CountSemanticAssessmentProviderFramingTokens returns the minimum serialized
// provider request cost for a valid assessment. It uses the same request
// builder as admission so startup cannot accept a budget that rejects every
// supported request.
func CountSemanticAssessmentProviderFramingTokens(limits SemanticAssessmentLimits) (int, error) {
	inputTokens, _, err := CountSemanticAssessmentRequestTokens(minimumSemanticAssessmentRequest(), limits)
	return inputTokens, err
}

// minimumSemanticAssessmentRequest mirrors the smallest production Remember
// envelope, including its generated ingest ID and always-present proposal map.
func minimumSemanticAssessmentRequest() SemanticAssessmentRequest {
	return SemanticAssessmentRequest{
		RequestID: "synchronous-remember:00000000-0000-0000-0000-000000000000",
		TeamID:    "t",
		Evidence: []SemanticReviewEvidence{
			PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{
				EvidenceID: "evidence:0",
				Content:    "x",
			}),
		},
		EvidenceEquivalenceCandidates: []SemanticAssessmentEvidenceEquivalenceCandidateGroup{},
	}
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
	preservedTruncation := req.CandidateContextTruncated
	req.CandidateContextTruncated = preservedTruncation

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

	maxRounds := 0
	for _, candidates := range allCandidates {
		if len(candidates) > maxRounds {
			maxRounds = len(candidates)
		}
	}
	for round := 0; round < maxRounds; round++ {
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
	req.CandidateContextTruncated = preservedTruncation || req.CandidateContextOmittedCandidates > 0
	req.CandidateContextTokens = contextTokens
	req.InputTokens = inputTokens
	return nil, nil
}

func semanticAssessmentConversationInputLimit(limits SemanticAssessmentLimits) int {
	limits = normalizeSemanticAssessmentLimits(limits)
	repairTurns := SemanticAssessmentMaxProviderTurns - 1
	headroom := repairTurns * (limits.MaxOutputTokens + semanticAssessmentRepairSafetyTokens)
	if headroom >= limits.MaxInputTokens {
		return 0
	}
	return limits.MaxInputTokens - headroom
}

// SemanticAssessmentLimitValidationError identifies the configured assessor
// limit responsible for a startup validation failure.
type SemanticAssessmentLimitValidationError struct {
	Field   string
	Message string
	Cause   error
}

func (e *SemanticAssessmentLimitValidationError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "invalid semantic assessment limits"
}

func (e *SemanticAssessmentLimitValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func semanticAssessmentLimitValidationError(field, message string, cause error) error {
	return &SemanticAssessmentLimitValidationError{Field: field, Message: message, Cause: cause}
}

func validateSemanticAssessmentLimits(limits SemanticAssessmentLimits) error {
	limits = normalizeSemanticAssessmentLimits(limits)
	framingTokens, candidateContextTokens, err := CountSemanticAssessmentRequestTokens(minimumSemanticAssessmentRequest(), limits)
	if err != nil {
		return semanticAssessmentLimitValidationError("tokenizer", "semantic assessment provider framing cannot be measured: "+err.Error(), err)
	}
	if candidateContextTokens > limits.MaxCandidateContextTokens {
		return semanticAssessmentLimitValidationError(
			"candidate_context_tokens",
			fmt.Sprintf("semantic assessment candidate context budget is below the minimum serialized context (limit %d, minimum %d)", limits.MaxCandidateContextTokens, candidateContextTokens),
			nil,
		)
	}
	conversationLimit := semanticAssessmentConversationInputLimit(limits)
	if conversationLimit < framingTokens {
		field := "output_tokens"
		if limits.MaxInputTokens <= framingTokens {
			field = "input_tokens"
		}
		return semanticAssessmentLimitValidationError(
			field,
			fmt.Sprintf("semantic assessment input budget leaves no usable room after repair headroom (input limit %d, framing %d)", limits.MaxInputTokens, framingTokens),
			nil,
		)
	}
	return nil
}

// SemanticAssessmentBudgetFailureStage identifies the remaining required
// context when optional assessor context has already been removed. The
// returned stage is used only for safe operator diagnostics; it does not alter
// the request or provider policy.
func SemanticAssessmentBudgetFailureStage(req SemanticAssessmentRequest, field string, limits SemanticAssessmentLimits) string {
	if field == "candidate_context_tokens" {
		return "catalog_context"
	}
	if tokens, err := CountSemanticAssessmentProviderFramingTokens(limits); err == nil && tokens > semanticAssessmentConversationInputLimit(limits) {
		return "provider_framing"
	}
	if len(req.KnownEvidence) > 0 {
		withoutKnown := req
		withoutKnown.KnownEvidence = nil
		if input, _, err := CountSemanticAssessmentRequestTokens(withoutKnown, limits); err == nil && input <= semanticAssessmentConversationInputLimit(limits) {
			return "known_evidence_context"
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
