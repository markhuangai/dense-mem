package verifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ConflictAssessmentSchemaName = "dense_mem_conflict_assessment_response"

	ConflictAssessmentDecisionSelect  = "select"
	ConflictAssessmentDecisionAbstain = "abstain"

	ConflictAssessmentMaxPositions = 50
	ConflictAssessmentMaxEvidence  = 500
	ConflictAssessmentMaxContent   = 4000
	ConflictAssessmentMaxRationale = 1000
)

const conflictAssessmentSystemPrompt = `You are Dense-Mem's overdue conflict assessor. Assess only the supplied, already accepted evidence and server-owned support metadata. Select one supplied position only when the dossier gives a clear, well-supported answer. Authority, independent source groups, distinct owner counts, accepted time, and explicit effective time can matter. Do not treat counts, deadline expiry, recency alone, or copied evidence as decisive. Do not invent evidence, IDs, sources, lifecycle actions, relationship updates, owners, or durable state.

If the dossier does not support a clear position, abstain. Return one complete JSON object matching the schema and no other text.`

const conflictAssessmentCorrectionInstruction = `Return one complete replacement JSON object matching the schema. Use decision "select" only with one supplied position_id and a confidence in [0,1]. Use decision "abstain" with position_id null and confidence 0. Do not return an explanation outside the JSON object.`

type ConflictAssessmentEvidence struct {
	EvidenceID     string     `json:"evidence_id"`
	PositionID     string     `json:"position_id"`
	SupportID      string     `json:"support_id"`
	SourceGroupKey string     `json:"source_group_key"`
	Authority      string     `json:"authority"`
	AcceptedAt     time.Time  `json:"accepted_at"`
	EffectiveAt    *time.Time `json:"effective_at,omitempty"`
	Content        string     `json:"content"`
}

type ConflictAssessmentPosition struct {
	PositionID              string `json:"position_id"`
	PositionKey             string `json:"position_key"`
	SupportGroupCount       int    `json:"support_group_count"`
	AuthoritativeGroupCount int    `json:"authoritative_group_count"`
	OwnerProfileCount       int    `json:"owner_profile_count"`
}

// ConflictAssessmentRequest is a bounded, immutable case dossier. Team and
// case version are server-side correlation fields and are not model authority.
type ConflictAssessmentRequest struct {
	RequestID string                       `json:"request_id"`
	CaseID    string                       `json:"case_id"`
	Version   int                          `json:"version"`
	Question  string                       `json:"question"`
	Positions []ConflictAssessmentPosition `json:"positions"`
	Evidence  []ConflictAssessmentEvidence `json:"evidence"`
}

type ConflictAssessmentResponse struct {
	Decision      string  `json:"decision"`
	PositionID    *string `json:"position_id"`
	Confidence    float64 `json:"confidence"`
	Rationale     string  `json:"rationale"`
	InputTokens   int     `json:"-"`
	OutputTokens  int     `json:"-"`
	ProviderTurns int     `json:"-"`
}

func ConflictAssessmentResponseSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"decision":    map[string]any{"type": "string", "enum": []string{ConflictAssessmentDecisionSelect, ConflictAssessmentDecisionAbstain}},
			"position_id": map[string]any{"type": []string{"string", "null"}},
			"confidence":  map[string]any{"type": "number", "minimum": 0, "maximum": 1},
			"rationale":   map[string]any{"type": "string", "minLength": 1, "maxLength": ConflictAssessmentMaxRationale},
		},
		"required":             []string{"decision", "position_id", "confidence", "rationale"},
		"additionalProperties": false,
	}
}

func PrepareConflictAssessmentRequest(req ConflictAssessmentRequest, limits SemanticAssessmentLimits) (ConflictAssessmentRequest, []SemanticValidationError) {
	limits = normalizeSemanticAssessmentLimits(limits)
	req.RequestID = strings.TrimSpace(req.RequestID)
	req.CaseID = strings.TrimSpace(req.CaseID)
	req.Question = strings.TrimSpace(req.Question)
	if req.Positions == nil {
		req.Positions = []ConflictAssessmentPosition{}
	}
	if req.Evidence == nil {
		req.Evidence = []ConflictAssessmentEvidence{}
	}
	errs := make([]SemanticValidationError, 0)
	if req.RequestID == "" {
		errs = append(errs, SemanticValidationError{Field: "request_id", Message: "is required"})
	}
	if req.CaseID == "" {
		errs = append(errs, SemanticValidationError{Field: "case_id", Message: "is required"})
	}
	if req.Version < 1 {
		errs = append(errs, SemanticValidationError{Field: "version", Message: "must be at least 1"})
	}
	if req.Question == "" {
		errs = append(errs, SemanticValidationError{Field: "question", Message: "is required"})
	}
	if len(req.Positions) < 2 || len(req.Positions) > ConflictAssessmentMaxPositions {
		errs = append(errs, SemanticValidationError{Field: "positions", Message: "must contain between 2 and 50 positions"})
	}
	if len(req.Evidence) == 0 || len(req.Evidence) > ConflictAssessmentMaxEvidence {
		errs = append(errs, SemanticValidationError{Field: "evidence", Message: "must contain between 1 and 500 items"})
	}
	positions := make(map[string]struct{}, len(req.Positions))
	for index := range req.Positions {
		position := &req.Positions[index]
		position.PositionID = strings.TrimSpace(position.PositionID)
		position.PositionKey = strings.TrimSpace(position.PositionKey)
		if position.PositionID == "" {
			errs = append(errs, SemanticValidationError{Field: fmt.Sprintf("positions[%d].position_id", index), Message: "is required"})
			continue
		}
		if _, exists := positions[position.PositionID]; exists {
			errs = append(errs, SemanticValidationError{Field: fmt.Sprintf("positions[%d].position_id", index), Message: "must be unique"})
		}
		positions[position.PositionID] = struct{}{}
		if position.PositionKey == "" {
			errs = append(errs, SemanticValidationError{Field: fmt.Sprintf("positions[%d].position_key", index), Message: "is required"})
		}
		if position.SupportGroupCount < 0 || position.AuthoritativeGroupCount < 0 || position.OwnerProfileCount < 0 {
			errs = append(errs, SemanticValidationError{Field: fmt.Sprintf("positions[%d]", index), Message: "support counts must not be negative"})
		}
	}
	for index := range req.Evidence {
		evidence := &req.Evidence[index]
		evidence.EvidenceID = strings.TrimSpace(evidence.EvidenceID)
		evidence.PositionID = strings.TrimSpace(evidence.PositionID)
		evidence.SupportID = strings.TrimSpace(evidence.SupportID)
		evidence.SourceGroupKey = strings.TrimSpace(evidence.SourceGroupKey)
		evidence.Authority = strings.TrimSpace(evidence.Authority)
		evidence.Content = strings.TrimSpace(evidence.Content)
		if evidence.EvidenceID == "" || evidence.PositionID == "" || evidence.SupportID == "" || evidence.SourceGroupKey == "" || evidence.Content == "" {
			errs = append(errs, SemanticValidationError{Field: fmt.Sprintf("evidence[%d]", index), Message: "evidence_id, position_id, support_id, source_group_key, and content are required"})
		}
		if _, exists := positions[evidence.PositionID]; !exists {
			errs = append(errs, SemanticValidationError{Field: fmt.Sprintf("evidence[%d].position_id", index), Message: "must reference a supplied position"})
		}
		if len(evidence.Content) > ConflictAssessmentMaxContent {
			errs = append(errs, SemanticValidationError{Field: fmt.Sprintf("evidence[%d].content", index), Message: "exceeds maximum length"})
		}
		if evidence.AcceptedAt.IsZero() {
			errs = append(errs, SemanticValidationError{Field: fmt.Sprintf("evidence[%d].accepted_at", index), Message: "is required"})
		} else {
			evidence.AcceptedAt = evidence.AcceptedAt.UTC()
		}
		if evidence.EffectiveAt != nil {
			value := evidence.EffectiveAt.UTC()
			evidence.EffectiveAt = &value
		}
	}
	if len(errs) > 0 {
		return req, errs
	}
	sort.Slice(req.Positions, func(i, j int) bool { return req.Positions[i].PositionID < req.Positions[j].PositionID })
	sort.Slice(req.Evidence, func(i, j int) bool {
		if req.Evidence[i].PositionID != req.Evidence[j].PositionID {
			return req.Evidence[i].PositionID < req.Evidence[j].PositionID
		}
		if !req.Evidence[i].AcceptedAt.Equal(req.Evidence[j].AcceptedAt) {
			return req.Evidence[i].AcceptedAt.Before(req.Evidence[j].AcceptedAt)
		}
		return req.Evidence[i].EvidenceID < req.Evidence[j].EvidenceID
	})
	encoded, err := json.Marshal(req)
	if err != nil {
		return req, []SemanticValidationError{{Field: "request", Message: "cannot be encoded"}}
	}
	count, err := CountTokens(string(encoded), limits.Tokenizer)
	if err != nil {
		return req, []SemanticValidationError{{Field: "request", Message: "cannot count tokens"}}
	}
	if count > limits.MaxInputTokens {
		return req, []SemanticValidationError{{Field: "request", Message: "exceeds input token budget"}}
	}
	return req, nil
}

// DecodeConflictAssessmentResponseJSON rejects unknown, missing, nullable,
// trailing, and over-budget output before dossier-dependent validation.
func DecodeConflictAssessmentResponseJSON(
	data []byte,
	limits SemanticAssessmentLimits,
) (ConflictAssessmentResponse, error) {
	limits = normalizeSemanticAssessmentLimits(limits)
	outputTokens, err := CountTokens(string(data), limits.Tokenizer)
	if err != nil {
		return ConflictAssessmentResponse{}, err
	}
	if outputTokens > limits.MaxOutputTokens {
		return ConflictAssessmentResponse{}, fmt.Errorf("conflict assessment response exceeds %d token limit", limits.MaxOutputTokens)
	}
	if validationErrors := validateConflictAssessmentResponseRaw(data); len(validationErrors) > 0 {
		return ConflictAssessmentResponse{}, errors.New(semanticAssessmentJoinedErrors(validationErrors))
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var response ConflictAssessmentResponse
	if err := decoder.Decode(&response); err != nil {
		return ConflictAssessmentResponse{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ConflictAssessmentResponse{}, errors.New("conflict assessment response contains trailing JSON")
	}
	response = normalizeConflictAssessmentResponse(response)
	response.OutputTokens = outputTokens
	return response, nil
}

func validateConflictAssessmentResponseRaw(raw []byte) []SemanticValidationError {
	errs := conflictAssessmentDuplicateFields(raw)
	_, objectErrs := assessmentRawObject(
		raw,
		"",
		[]string{"decision", "position_id", "confidence", "rationale"},
		map[string]bool{"position_id": true},
	)
	errs = append(errs, objectErrs...)
	return errs
}

func conflictAssessmentDuplicateFields(raw []byte) []SemanticValidationError {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil
	}
	opening, ok := token.(json.Delim)
	if !ok || opening != '{' {
		return nil
	}
	seen := make(map[string]struct{})
	var errs []SemanticValidationError
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return nil
		}
		field, ok := fieldToken.(string)
		if !ok {
			return nil
		}
		if _, exists := seen[field]; exists {
			errs = append(errs, SemanticValidationError{Field: field, Message: "must not be duplicated"})
		}
		seen[field] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil
		}
	}
	return errs
}

func normalizeConflictAssessmentResponse(response ConflictAssessmentResponse) ConflictAssessmentResponse {
	response.Decision = strings.TrimSpace(response.Decision)
	response.Rationale = strings.TrimSpace(response.Rationale)
	if response.PositionID != nil {
		positionID := strings.TrimSpace(*response.PositionID)
		response.PositionID = &positionID
	}
	return response
}

func validateConflictAssessmentResponse(req ConflictAssessmentRequest, response ConflictAssessmentResponse) []SemanticValidationError {
	errs := make([]SemanticValidationError, 0)
	response = normalizeConflictAssessmentResponse(response)
	if response.Rationale == "" || utf8.RuneCountInString(response.Rationale) > ConflictAssessmentMaxRationale {
		errs = append(errs, SemanticValidationError{Field: "rationale", Message: "must be a bounded non-empty string"})
	}
	switch response.Decision {
	case ConflictAssessmentDecisionSelect:
		if response.PositionID == nil || strings.TrimSpace(*response.PositionID) == "" {
			errs = append(errs, SemanticValidationError{Field: "position_id", Message: "is required for select"})
			break
		}
		positionID := strings.TrimSpace(*response.PositionID)
		known := false
		for _, position := range req.Positions {
			if position.PositionID == positionID {
				known = true
				break
			}
		}
		if !known {
			errs = append(errs, SemanticValidationError{Field: "position_id", Message: "must be a supplied position"})
		}
		if response.Confidence < 0 || response.Confidence > 1 {
			errs = append(errs, SemanticValidationError{Field: "confidence", Message: "must be between 0 and 1"})
		}
	case ConflictAssessmentDecisionAbstain:
		if response.PositionID != nil {
			errs = append(errs, SemanticValidationError{Field: "position_id", Message: "must be null for abstain"})
		}
		if response.Confidence != 0 {
			errs = append(errs, SemanticValidationError{Field: "confidence", Message: "must be 0 for abstain"})
		}
	default:
		errs = append(errs, SemanticValidationError{Field: "decision", Message: "must be select or abstain"})
	}
	return errs
}

// AssessRelationshipConflict runs the bounded complete-response correction
// loop used by semantic assessment, while keeping this schema independent.
func (v *OpenAIVerifier) AssessRelationshipConflict(ctx context.Context, req ConflictAssessmentRequest) (ConflictAssessmentResponse, error) {
	prepared, validationErrors := PrepareConflictAssessmentRequest(req, v.assessmentLimits)
	if len(validationErrors) > 0 {
		return ConflictAssessmentResponse{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "invalid conflict assessment request: " + openAIValidationSummary(validationErrors),
			FailureClass: ProviderFailureClassProviderUnavailable,
		}
	}
	payload, err := json.Marshal(prepared)
	if err != nil {
		return ConflictAssessmentResponse{}, &ProviderError{Provider: openAIVerifierProvider, Message: "failed to marshal conflict assessment request", Cause: err, FailureClass: ProviderFailureClassProviderUnavailable}
	}
	messages := []openAIVerifierMessage{{Role: "system", Content: conflictAssessmentSystemPrompt}, {Role: "user", Content: string(payload)}}
	for turn := 1; turn <= SemanticAssessmentMaxProviderTurns; turn++ {
		inputTokens, err := semanticAssessmentMessageTokens(messages, v.assessmentLimits.Tokenizer)
		if err != nil {
			return ConflictAssessmentResponse{}, &ProviderError{Provider: openAIVerifierProvider, Message: "failed to count conflict assessment tokens", Cause: err, FailureClass: ProviderFailureClassProviderUnavailable}
		}
		if inputTokens > v.assessmentLimits.MaxInputTokens {
			return ConflictAssessmentResponse{}, &MalformedResponseError{Provider: openAIVerifierProvider, Message: "conflict assessment exceeds input token budget", FailureClass: "input_budget", Attempts: turn - 1}
		}
		result, err := v.openAIStructuredChatMessagesJSONWithUsage(ctx, v.model, ConflictAssessmentSchemaName, ConflictAssessmentResponseSchema(), messages)
		if err != nil {
			return ConflictAssessmentResponse{}, err
		}
		if result.ReportedUsage != nil && result.ReportedUsage.PromptTokens > int64(v.assessmentLimits.MaxInputTokens) {
			return ConflictAssessmentResponse{}, &MalformedResponseError{
				Provider:     openAIVerifierProvider,
				Message:      "provider reported input tokens beyond conflict assessment limit",
				FailureClass: "input_budget",
				Attempts:     turn,
			}
		}
		responseErrors := []SemanticValidationError{}
		response := ConflictAssessmentResponse{}
		if result.ReportedUsage != nil && result.ReportedUsage.CompletionTokens > int64(v.assessmentLimits.MaxOutputTokens) {
			responseErrors = append(responseErrors, SemanticValidationError{
				Field:   "output_tokens",
				Message: fmt.Sprintf("provider reported more than the allowed %d tokens", v.assessmentLimits.MaxOutputTokens),
			})
		} else {
			decoded, decodeErr := DecodeConflictAssessmentResponseJSON([]byte(result.Content), v.assessmentLimits)
			if decodeErr != nil {
				responseErrors = append(responseErrors, SemanticValidationError{Field: "response", Message: "must be one complete JSON object matching the required field types"})
			} else {
				response = decoded
				responseErrors = append(responseErrors, validateConflictAssessmentResponse(prepared, response)...)
			}
		}
		if len(responseErrors) == 0 {
			response.InputTokens = inputTokens
			if result.ReportedUsage != nil && result.ReportedUsage.PromptTokens > 0 {
				response.InputTokens = int(result.ReportedUsage.PromptTokens)
			}
			response.ProviderTurns = turn
			return response, nil
		}
		if turn == SemanticAssessmentMaxProviderTurns {
			return ConflictAssessmentResponse{}, &MalformedResponseError{Provider: openAIVerifierProvider, Message: "conflict assessment response remained invalid after bounded correction", FailureClass: "malformed_exhausted", Attempts: turn}
		}
		correction, err := json.Marshal(map[string]any{
			"validation_errors": boundedSemanticAssessmentCorrectionErrors(responseErrors),
			"instruction":       conflictAssessmentCorrectionInstruction,
		})
		if err != nil {
			return ConflictAssessmentResponse{}, &ProviderError{Provider: openAIVerifierProvider, Message: "failed to marshal conflict assessment correction", Cause: err, FailureClass: ProviderFailureClassProviderUnavailable}
		}
		messages = append(messages, openAIVerifierMessage{Role: "assistant", Content: result.Content}, openAIVerifierMessage{Role: "user", Content: string(correction)})
	}
	return ConflictAssessmentResponse{}, &MalformedResponseError{Provider: openAIVerifierProvider, Message: "conflict assessment response remained invalid after bounded correction", FailureClass: "malformed_exhausted", Attempts: SemanticAssessmentMaxProviderTurns}
}
