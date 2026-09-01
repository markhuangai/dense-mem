package assessorprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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
	ValidationErrors          []assessor.SemanticValidationError `json:"validation_errors"`
	Instruction               string                             `json:"instruction"`
	RefreshedCandidateContext assessorCandidateContext           `json:"refreshed_candidate_context"`
}

const semanticAssessmentSessionRepairInstruction = `Return one complete replacement JSON object matching the required schema. Correct every validation error exactly. Return one evidence_security_results entry for every submitted evidence_id; use reject only when its signals array contains a matching cited security signal and pass only when it is empty. Never search other memory, find support for evidence, or discover new Relationships. The submitted evidence, relationship refs, endpoints, typed values, polarity, and temporal bounds are immutable. The refreshed candidate context is server-owned and may be used to repair identity or predicate selection. If an Entity without known_entity_id has multiple compatible candidates or truncated candidate context, set its action to ambiguous, set candidate_entity_id to null, and mark every dependent Relationship not_supported with reason not_supported_by_evidence and no splits. Copy only grounding_ref, start_ref, and end_ref values present in the current request. Never return a patch or explanation. Every stored split must use grounded Entities and a resolved predicate or a complete predicate_registration, with support ranges from that Relationship's submitted evidence allowlist. If a claim is unsupported, return not_supported with reason not_supported_by_evidence and no splits. Split indices must be contiguous from zero.`

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
	correctionJSON, err := json.Marshal(semanticAssessmentSessionRepair{
		ValidationErrors:          boundedSemanticAssessmentCorrectionErrors(repair.ValidationErrors),
		Instruction:               semanticAssessmentSessionRepairInstruction,
		RefreshedCandidateContext: assessorCandidateContext{EntityCandidateGroups: prepared.EntityCandidateGroups, PredicateOptions: prepared.PredicateOptions},
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
	turn, rawContent, err := v.runRememberAssessmentTurn(ctx, session, prepared, messages)
	if err != nil {
		return assessor.SemanticAssessmentTurn{}, err
	}
	session.messages = messages
	session.prepared = prepared
	session.lastAssistant = rawContent
	session.turn++
	turn.Turn = session.turn
	return turn, nil
}

func (v *OpenAIAssessor) runRememberAssessmentTurn(
	ctx context.Context,
	session *openAISemanticAssessmentSession,
	prepared assessor.SemanticAssessmentRequest,
	messages []openAIVerifierMessage,
) (assessor.SemanticAssessmentTurn, string, error) {
	inputTokens, err := semanticAssessmentMessageTokens(messages, v.assessmentLimits.Tokenizer)
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
	reduced.CandidateContextTokens = 0
	reduced.CandidateContextTruncated = false
	reduced.InputTokens = 0
	encoded, err := json.Marshal(reduced)
	if err != nil {
		return fmt.Sprintf("invalid:%v", err)
	}
	return string(encoded)
}

type assessorCandidateContext struct {
	EntityCandidateGroups []assessor.SemanticAssessmentEntityCandidateGroup `json:"entity_candidate_groups"`
	PredicateOptions      []assessor.SemanticAssessmentPredicateOption      `json:"predicate_options"`
}
