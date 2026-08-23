package verifier

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync/atomic"

	"github.com/markhuangai/dense-mem/internal/observability"
)

var openAISemanticAssessmentSessionSequence uint64

type openAISemanticAssessmentSession struct {
	id            string
	prepared      SemanticAssessmentRequest
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
	ValidationErrors          []SemanticValidationError          `json:"validation_errors"`
	Instruction               string                             `json:"instruction"`
	RefreshedCandidateContext semanticAssessmentCandidateContext `json:"refreshed_candidate_context"`
}

const semanticAssessmentSessionRepairInstruction = `Return one complete replacement JSON object matching the required schema. Correct every validation error exactly. The submitted evidence, relationship refs, endpoints, typed values, polarity, and temporal bounds are immutable. The refreshed candidate context is server-owned and may be used to repair identity or predicate selection. Copy only grounding_ref, start_ref, and end_ref values present in the current request. Never return a patch or explanation. Every stored split must use grounded Entities and a resolved predicate or a complete predicate_registration. If a claim is unsupported, return not_supported with reason not_supported_by_evidence and no splits. Split indices must be contiguous from zero.`

var _ RememberAssessor = (*OpenAIVerifier)(nil)

// Assess begins one Remember assessor session and performs exactly one provider
// turn. Response-contract errors are returned in the turn for application-owned
// repair; provider and transport failures are returned as errors.
func (v *OpenAIVerifier) Assess(ctx context.Context, req SemanticAssessmentRequest) (SemanticAssessmentSession, SemanticAssessmentTurn, error) {
	prepared, validationErrors := PrepareSemanticAssessmentRequest(req, v.assessmentLimits)
	if len(validationErrors) > 0 {
		return nil, SemanticAssessmentTurn{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "invalid semantic assessment request: " + openAIValidationSummary(validationErrors),
			FailureClass: ProviderFailureClassRequestInvalid,
		}
	}
	userJSON, err := json.Marshal(prepared)
	if err != nil {
		return nil, SemanticAssessmentTurn{}, &ProviderError{
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
			{Role: "system", Content: semanticAssessmentSystemPrompt},
			{Role: "user", Content: string(userJSON)},
		},
	}
	turn, rawContent, err := v.runRememberAssessmentTurn(ctx, session, prepared, session.messages)
	if err != nil {
		return session, SemanticAssessmentTurn{}, err
	}
	session.lastAssistant = rawContent
	session.turn = 1
	turn.Turn = session.turn
	return session, turn, nil
}

// Repair performs one complete correction turn in the same provider
// conversation. The application decides when to call it and supplies refreshed
// server-owned candidate context in the request.
func (v *OpenAIVerifier) Repair(ctx context.Context, sessionRef SemanticAssessmentSession, repair SemanticAssessmentRepairRequest) (SemanticAssessmentTurn, error) {
	session, ok := sessionRef.(*openAISemanticAssessmentSession)
	if !ok || session == nil || session.SessionID() == "" {
		return SemanticAssessmentTurn{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "invalid semantic assessment session",
			FailureClass: ProviderFailureClassRequestInvalid,
		}
	}
	if session.turn >= SemanticAssessmentMaxProviderTurns {
		return SemanticAssessmentTurn{}, &MalformedResponseError{
			Provider:     openAIVerifierProvider,
			Message:      "semantic assessment session exceeded its turn bound",
			FailureClass: "malformed_exhausted",
			Attempts:     session.turn,
		}
	}
	prepared, validationErrors := PrepareSemanticAssessmentRequest(repair.Request, v.assessmentLimits)
	if len(validationErrors) > 0 {
		return SemanticAssessmentTurn{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "invalid semantic assessment repair request: " + openAIValidationSummary(validationErrors),
			FailureClass: ProviderFailureClassRequestInvalid,
		}
	}
	if semanticAssessmentRequestEnvelope(session.prepared) != semanticAssessmentRequestEnvelope(prepared) {
		return SemanticAssessmentTurn{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "semantic assessment repair changed the submitted envelope",
			FailureClass: ProviderFailureClassRequestInvalid,
		}
	}
	correctionJSON, err := json.Marshal(semanticAssessmentSessionRepair{
		ValidationErrors:          boundedSemanticAssessmentCorrectionErrors(repair.ValidationErrors),
		Instruction:               semanticAssessmentSessionRepairInstruction,
		RefreshedCandidateContext: semanticAssessmentCandidateContext{EntityCandidateGroups: prepared.EntityCandidateGroups, PredicateOptions: prepared.PredicateOptions},
	})
	if err != nil {
		return SemanticAssessmentTurn{}, &ProviderError{
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
		return SemanticAssessmentTurn{}, err
	}
	session.messages = messages
	session.prepared = prepared
	session.lastAssistant = rawContent
	session.turn++
	turn.Turn = session.turn
	return turn, nil
}

func (v *OpenAIVerifier) runRememberAssessmentTurn(
	ctx context.Context,
	session *openAISemanticAssessmentSession,
	prepared SemanticAssessmentRequest,
	messages []openAIVerifierMessage,
) (SemanticAssessmentTurn, string, error) {
	inputTokens, err := semanticAssessmentMessageTokens(messages, v.assessmentLimits.Tokenizer)
	if err != nil {
		return SemanticAssessmentTurn{}, "", &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "failed to count semantic assessment conversation tokens",
			Cause:        err,
			FailureClass: ProviderFailureClassProviderUnavailable,
		}
	}
	if inputTokens > v.assessmentLimits.MaxInputTokens {
		observability.RecordAssessorValidationFailure(v.metrics, "input_budget")
		return SemanticAssessmentTurn{}, "", &MalformedResponseError{
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
		SemanticAssessmentSchemaName,
		SemanticAssessmentResponseSchema(),
		messages,
	)
	if err != nil {
		return SemanticAssessmentTurn{}, "", err
	}
	if providerResult.ReportedUsage != nil && providerResult.ReportedUsage.PromptTokens > int64(v.assessmentLimits.MaxInputTokens) {
		observability.RecordAssessorValidationFailure(v.metrics, "input_budget")
		return SemanticAssessmentTurn{}, "", &MalformedResponseError{
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
	turn := SemanticAssessmentTurn{
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

func semanticAssessmentRequestEnvelope(req SemanticAssessmentRequest) string {
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
