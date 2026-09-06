package assessorprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/markhuangai/dense-mem/internal/observability"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

var openAISemanticAssessmentSessionSequence uint64

type openAISemanticAssessmentSession struct {
	id            string
	prepared      assessor.SemanticAssessmentRequest
	messages      []openAIVerifierMessage
	lastAssistant string
	turn          int
}

func (s *openAISemanticAssessmentSession) SessionID() string {
	if s == nil {
		return ""
	}
	return s.id
}

type semanticAssessmentSessionRepair struct {
	ValidationErrors []assessor.SemanticValidationError `json:"validation_errors"`
	Instruction      string                             `json:"instruction"`
	// Candidate context is immutable for a bounded repair conversation. The
	// initial request remains in the provider history, so repeating the same
	// server-selected context would spend the candidate budget twice without
	// adding information. Keep this field optional for wire compatibility with
	// providers that understand refreshed context, but omit it for ordinary
	// repairs.
	RefreshedCandidateContext *assessorCandidateContext `json:"refreshed_candidate_context,omitempty"`
}

const semanticAssessmentSessionRepairInstruction = `Return one complete replacement JSON object matching the required schema. Correct every validation error exactly. Return one evidence_security_results entry for every submitted evidence_id; use reject only when its signals array contains a matching cited security signal and pass only when it is empty. Return one evidence_equivalence_results entry for every evidence_equivalence_candidates entry, choosing only new or one of that entry's supplied candidates. Return evidence_conflict_results as complete cited opposing span sets with at least two positions including a submitted evidence item; copy only supplied evidence_id, start_ref, and end_ref values. Never search other memory, find support for evidence, or discover new Relationships. The submitted evidence, relationship refs, endpoints, typed values, polarity, and temporal bounds are immutable. The initial candidate context is server-owned and remains in the conversation; use it to repair identity or predicate selection and never invent a replacement allowlist. If an Entity without known_entity_id has multiple compatible candidates or truncated candidate context, set its action to ambiguous, set candidate_entity_id to null, and mark every dependent Relationship not_supported with reason not_supported_by_evidence and no splits. Copy only grounding_ref, start_ref, and end_ref values present in the current request. Never return a patch or explanation. Every stored split must use grounded Entities and a resolved predicate or a complete predicate_registration, with support ranges from that Relationship's submitted evidence allowlist. If a claim is unsupported, return not_supported with reason not_supported_by_evidence and no splits. Split indices must be contiguous from zero.`

const semanticAssessmentRepairHistoryPlaceholder = `The previous assessor response was omitted from repair history because its serialized form exceeded the configured input budget. Use the original request and validation_errors to return one complete replacement object.`

var _ assessor.RememberAssessor = (*OpenAIAssessor)(nil)

// Assess begins one Remember assessor session and performs exactly one provider
// turn. Response-contract errors are returned in the turn for application-owned
// repair; provider and transport failures are returned as errors.
func (v *OpenAIAssessor) Assess(ctx context.Context, req assessor.SemanticAssessmentRequest) (assessor.SemanticAssessmentSession, assessor.SemanticAssessmentTurn, error) {
	prepared, validationErrors := assessor.PrepareSemanticAssessmentRequest(req, v.assessmentLimits)
	if len(validationErrors) > 0 {
		return nil, assessor.SemanticAssessmentTurn{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "invalid semantic assessment request: " + openAIValidationSummary(validationErrors),
			FailureClass: ProviderFailureClassRequestInvalid,
		}
	}
	userJSON, err := json.Marshal(prepared)
	if err != nil {
		return nil, assessor.SemanticAssessmentTurn{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "failed to marshal semantic assessment request",
			Cause:        err,
			FailureClass: ProviderFailureClassProviderUnavailable,
		}
	}
	session := &openAISemanticAssessmentSession{
		id:       strconv.FormatUint(atomic.AddUint64(&openAISemanticAssessmentSessionSequence, 1), 10),
		prepared: prepared,
		messages: []openAIVerifierMessage{
			{Role: "system", Content: assessor.SemanticAssessmentSystemPrompt},
			{Role: "user", Content: string(userJSON)},
		},
	}
	turn, rawContent, err := v.runRememberAssessmentTurn(ctx, session, prepared, session.messages)
	if err != nil {
		return session, assessor.SemanticAssessmentTurn{}, err
	}
	session.lastAssistant = rawContent
	session.turn = 1
	turn.Turn = session.turn
	return session, turn, nil
}

// Repair performs one complete correction turn in the same provider
// conversation. The application decides when to call it and supplies refreshed
// server-owned candidate context in the request.
func (v *OpenAIAssessor) Repair(ctx context.Context, sessionRef assessor.SemanticAssessmentSession, repair assessor.SemanticAssessmentRepairRequest) (assessor.SemanticAssessmentTurn, error) {
	session, ok := sessionRef.(*openAISemanticAssessmentSession)
	if !ok || session == nil || session.SessionID() == "" {
		return assessor.SemanticAssessmentTurn{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "invalid semantic assessment session",
			FailureClass: ProviderFailureClassRequestInvalid,
		}
	}
	if session.turn >= assessor.SemanticAssessmentMaxProviderTurns {
		return assessor.SemanticAssessmentTurn{}, &MalformedResponseError{
			Provider:     openAIVerifierProvider,
			Message:      "semantic assessment session exceeded its turn bound",
			FailureClass: "malformed_exhausted",
			Attempts:     session.turn,
		}
	}
	prepared, validationErrors := assessor.PrepareSemanticAssessmentRequest(repair.Request, v.assessmentLimits)
	if len(validationErrors) > 0 {
		return assessor.SemanticAssessmentTurn{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "invalid semantic assessment repair request: " + openAIValidationSummary(validationErrors),
			FailureClass: ProviderFailureClassRequestInvalid,
		}
	}
	if semanticAssessmentRequestEnvelope(session.prepared) != semanticAssessmentRequestEnvelope(prepared) {
		return assessor.SemanticAssessmentTurn{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "semantic assessment repair changed the submitted envelope",
			FailureClass: ProviderFailureClassRequestInvalid,
		}
	}
	if !semanticAssessmentCandidateContextCompatible(session.prepared, prepared) {
		return assessor.SemanticAssessmentTurn{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "semantic assessment repair changed the selected candidate context",
			FailureClass: ProviderFailureClassRequestInvalid,
		}
	}
	correctionJSON, err := json.Marshal(semanticAssessmentSessionRepair{
		ValidationErrors: boundedSemanticAssessmentCorrectionErrors(repair.ValidationErrors),
		Instruction:      semanticAssessmentSessionRepairInstruction,
	})
	if err != nil {
		return assessor.SemanticAssessmentTurn{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "failed to marshal semantic assessment repair feedback",
			Cause:        err,
			FailureClass: ProviderFailureClassProviderUnavailable,
		}
	}
	messages := append([]openAIVerifierMessage(nil), session.messages...)
	messages = append(messages,
		openAIVerifierMessage{Role: "assistant", Content: session.lastAssistant},
		openAIVerifierMessage{Role: "user", Content: string(correctionJSON)},
	)
	messages, err = v.boundRepairHistory(messages)
	if err != nil {
		return assessor.SemanticAssessmentTurn{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "failed to count semantic assessment repair history",
			Cause:        err,
			FailureClass: ProviderFailureClassProviderUnavailable,
		}
	}
	turn, rawContent, err := v.runRememberAssessmentTurn(ctx, session, prepared, messages)
	if err != nil {
		return assessor.SemanticAssessmentTurn{}, err
	}
	session.messages = messages
	// The selected allowlist is frozen for the whole bounded conversation. The
	// immutable envelope was checked above; retaining the initial prepared value
	// keeps validation and commit allowlists aligned while refreshed descriptive
	// context may be supplied for the same allowlisted IDs.
	session.lastAssistant = rawContent
	session.turn++
	turn.Turn = session.turn
	return turn, nil
}

func (v *OpenAIAssessor) boundRepairHistory(messages []openAIVerifierMessage) ([]openAIVerifierMessage, error) {
	inputTokens, err := semanticAssessmentTurnTokens(
		v.model,
		assessor.SemanticAssessmentSchemaName,
		messages,
		assessor.SemanticAssessmentResponseSchema(),
		v.disableTemperature,
		v.assessmentLimits.Tokenizer,
	)
	if err != nil || inputTokens <= v.assessmentLimits.MaxInputTokens {
		return messages, err
	}
	if len(messages) < 2 || messages[len(messages)-2].Role != "assistant" {
		return messages, nil
	}
	bounded := append([]openAIVerifierMessage(nil), messages...)
	for index := len(bounded) - 2; index >= 0 && inputTokens > v.assessmentLimits.MaxInputTokens; index-- {
		if strings.ToLower(strings.TrimSpace(bounded[index].Role)) != "assistant" || bounded[index].Content == semanticAssessmentRepairHistoryPlaceholder {
			continue
		}
		bounded[index].Content = semanticAssessmentRepairHistoryPlaceholder
		inputTokens, err = semanticAssessmentTurnTokens(
			v.model,
			assessor.SemanticAssessmentSchemaName,
			bounded,
			assessor.SemanticAssessmentResponseSchema(),
			v.disableTemperature,
			v.assessmentLimits.Tokenizer,
		)
		if err != nil {
			return messages, err
		}
	}
	if inputTokens > v.assessmentLimits.MaxInputTokens {
		return messages, nil
	}
	return bounded, nil
}

func (v *OpenAIAssessor) runRememberAssessmentTurn(
	ctx context.Context,
	session *openAISemanticAssessmentSession,
	prepared assessor.SemanticAssessmentRequest,
	messages []openAIVerifierMessage,
) (assessor.SemanticAssessmentTurn, string, error) {
	inputTokens, err := semanticAssessmentTurnTokens(
		v.model,
		assessor.SemanticAssessmentSchemaName,
		messages,
		assessor.SemanticAssessmentResponseSchema(),
		v.disableTemperature,
		v.assessmentLimits.Tokenizer,
	)
	if err != nil {
		return assessor.SemanticAssessmentTurn{}, "", &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "failed to count semantic assessment conversation tokens",
			Cause:        err,
			FailureClass: ProviderFailureClassProviderUnavailable,
		}
	}
	if inputTokens > v.assessmentLimits.MaxInputTokens {
		observability.RecordAssessorValidationFailure(v.metrics, "input_budget")
		return assessor.SemanticAssessmentTurn{}, "", &MalformedResponseError{
			Provider:                openAIVerifierProvider,
			Message:                 "semantic assessment conversation exceeds input token limit",
			FailureClass:            "input_budget",
			Attempts:                session.turn,
			ValidationStage:         "conversation_input_tokens",
			ValidationFieldFamilies: []string{"input_tokens"},
			Measurement:             &FailureMeasurement{Unit: "tokens", Observed: inputTokens, Limit: v.assessmentLimits.MaxInputTokens},
		}
	}
	candidateContextTokens, err := semanticAssessmentConversationCandidateContextTokens(messages, v.assessmentLimits.Tokenizer)
	if err != nil {
		return assessor.SemanticAssessmentTurn{}, "", &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "failed to count semantic assessment candidate context tokens",
			Cause:        err,
			FailureClass: ProviderFailureClassProviderUnavailable,
		}
	}
	if candidateContextTokens > v.assessmentLimits.MaxCandidateContextTokens {
		observability.RecordAssessorValidationFailure(v.metrics, "input_budget")
		return assessor.SemanticAssessmentTurn{}, "", &MalformedResponseError{
			Provider:                openAIVerifierProvider,
			Message:                 "semantic assessment conversation exceeds candidate context token limit",
			FailureClass:            "input_budget",
			Attempts:                session.turn,
			ValidationStage:         "conversation_candidate_context_tokens",
			ValidationFieldFamilies: []string{"candidate_context_tokens"},
			Measurement:             &FailureMeasurement{Unit: "tokens", Observed: candidateContextTokens, Limit: v.assessmentLimits.MaxCandidateContextTokens},
		}
	}
	providerResult, err := v.openAIStructuredChatMessagesJSONWithUsage(
		ctx,
		v.model,
		assessor.SemanticAssessmentSchemaName,
		assessor.SemanticAssessmentResponseSchema(),
		messages,
	)
	if err != nil {
		return assessor.SemanticAssessmentTurn{}, "", err
	}
	if providerResult.ReportedUsage != nil && providerResult.ReportedUsage.PromptTokens > int64(v.assessmentLimits.MaxInputTokens) {
		observability.RecordAssessorValidationFailure(v.metrics, "input_budget")
		return assessor.SemanticAssessmentTurn{}, "", &MalformedResponseError{
			Provider:                openAIVerifierProvider,
			Message:                 "provider reported input tokens beyond semantic assessment limit",
			FailureClass:            "input_budget",
			Attempts:                session.turn + 1,
			ValidationStage:         "conversation_input_tokens",
			ValidationFieldFamilies: []string{"input_tokens"},
			Measurement:             &FailureMeasurement{Unit: "tokens", Observed: int(providerResult.ReportedUsage.PromptTokens), Limit: v.assessmentLimits.MaxInputTokens},
		}
	}
	response, responseErrors, failureStage := semanticAssessmentResponseForCorrection(prepared, providerResult, v.assessmentLimits)
	turn := assessor.SemanticAssessmentTurn{
		Response:         response,
		ValidationErrors: responseErrors,
		ValidationStage:  failureStage,
		InputTokens:      inputTokens,
		OutputTokens:     response.OutputTokens,
	}
	if providerResult.ReportedUsage != nil && providerResult.ReportedUsage.PromptTokens > 0 {
		turn.InputTokens = int(providerResult.ReportedUsage.PromptTokens)
	}
	if len(responseErrors) == 0 {
		response.InputTokens = turn.InputTokens
		response.ProviderTurns = session.turn + 1
		turn.Response = response
	}
	return turn, providerResult.Content, nil
}

func semanticAssessmentRequestEnvelope(req assessor.SemanticAssessmentRequest) string {
	reduced := req
	reduced.EntityCandidateGroups = nil
	reduced.PredicateOptions = nil
	reduced.EvidenceEquivalenceCandidates = nil
	reduced.CandidateContextTokens = 0
	reduced.CandidateContextTruncated = false
	reduced.InputTokens = 0
	encoded, err := json.Marshal(reduced)
	if err != nil {
		return fmt.Sprintf("invalid:%v", err)
	}
	return string(encoded)
}

func semanticAssessmentCandidateContextCompatible(left, right assessor.SemanticAssessmentRequest) bool {
	if len(left.EntityCandidateGroups) != len(right.EntityCandidateGroups) || len(left.PredicateOptions) != len(right.PredicateOptions) || len(left.EvidenceEquivalenceCandidates) != len(right.EvidenceEquivalenceCandidates) {
		return false
	}
	for index := range left.EntityCandidateGroups {
		leftGroup, rightGroup := left.EntityCandidateGroups[index], right.EntityCandidateGroups[index]
		if leftGroup.Surface != rightGroup.Surface || leftGroup.EvidenceID != rightGroup.EvidenceID || leftGroup.GroundingRef != rightGroup.GroundingRef || leftGroup.Start != rightGroup.Start || leftGroup.End != rightGroup.End || leftGroup.CandidateContextTruncated != rightGroup.CandidateContextTruncated || len(leftGroup.Candidates) != len(rightGroup.Candidates) {
			return false
		}
		for candidateIndex := range leftGroup.Candidates {
			leftCandidate, rightCandidate := leftGroup.Candidates[candidateIndex], rightGroup.Candidates[candidateIndex]
			if leftCandidate.EntityID != rightCandidate.EntityID || leftCandidate.CanonicalName != rightCandidate.CanonicalName || leftCandidate.Kind != rightCandidate.Kind {
				return false
			}
		}
	}
	for index := range left.PredicateOptions {
		leftOption, rightOption := left.PredicateOptions[index], right.PredicateOptions[index]
		leftJSON, leftErr := json.Marshal(leftOption)
		rightJSON, rightErr := json.Marshal(rightOption)
		if leftErr != nil || rightErr != nil || string(leftJSON) != string(rightJSON) {
			return false
		}
	}
	for index := range left.EvidenceEquivalenceCandidates {
		leftGroup, rightGroup := left.EvidenceEquivalenceCandidates[index], right.EvidenceEquivalenceCandidates[index]
		if leftGroup.EvidenceID != rightGroup.EvidenceID || len(leftGroup.Candidates) != len(rightGroup.Candidates) {
			return false
		}
		for candidateIndex := range leftGroup.Candidates {
			if leftGroup.Candidates[candidateIndex].EvidenceID != rightGroup.Candidates[candidateIndex].EvidenceID {
				return false
			}
		}
	}
	return true
}

type assessorCandidateContext struct {
	EntityCandidateGroups         []assessor.SemanticAssessmentEntityCandidateGroup              `json:"entity_candidate_groups"`
	PredicateOptions              []assessor.SemanticAssessmentPredicateOption                   `json:"predicate_options"`
	EvidenceEquivalenceCandidates []assessor.SemanticAssessmentEvidenceEquivalenceCandidateGroup `json:"evidence_equivalence_candidates"`
}
