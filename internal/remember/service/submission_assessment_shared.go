package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"errors"

	"sort"
	"strconv"
	"strings"

	"github.com/markhuangai/dense-mem/internal/assessor"
	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

// SemanticMaxAssessorTurns covers the initial Remember assessor response and
// at most two complete-response corrections in the same provider conversation.
// Other assessor workflows retain their own broader historical limits.
const SemanticMaxAssessorTurns = 3

const maxAssessorValidationFields = 20

type semanticAssessmentPreflightError struct {
	stage        string
	reasonCode   string
	failureClass string
	measurement  *assessor.FailureMeasurement
	err          error
	cause        error
}

func (err *semanticAssessmentPreflightError) Error() string {
	if err == nil || err.err == nil {
		return "semantic assessment preflight failed"
	}
	return err.err.Error()
}

func (err *semanticAssessmentPreflightError) Unwrap() error {
	if err == nil {
		return nil
	}
	if err.cause != nil {
		return err.cause
	}
	return err.err
}

func deterministicSemanticAssessmentPreflightError(stage, message string) error {
	return &semanticAssessmentPreflightError{
		stage:        strings.TrimSpace(stage),
		reasonCode:   strings.TrimSpace(stage),
		failureClass: "validation_failed",
		err:          errors.New(message),
	}
}

func deterministicSemanticAssessmentPreflightErrorWithMeasurement(
	stage string,
	message string,
	measurement assessor.FailureMeasurement,
) error {
	result := deterministicSemanticAssessmentPreflightError(stage, message).(*semanticAssessmentPreflightError)
	result.measurement = &measurement
	return result
}

func deterministicSemanticAssessmentPreflightErrorWithCause(stage, message string, cause error) error {
	result := deterministicSemanticAssessmentPreflightError(stage, message).(*semanticAssessmentPreflightError)
	result.cause = cause
	return result
}

func semanticAssessmentPreflightFailure(err error) (string, bool) {
	var preflight *semanticAssessmentPreflightError
	if errors.As(err, &preflight) && preflight.stage != "" {
		return preflight.stage, true
	}
	return "candidate_prefetch", false
}

// SynchronousAssessmentFailureDetails exposes only bounded, server-owned
// measurements for an assessor failure. Provider text and response content
// remain private to logs and diagnostics.
func SynchronousAssessmentFailureDetails(err error) (string, map[string]any) {
	if err == nil {
		return "", nil
	}
	var diagnostic *submissionDiagnosticFailure
	if errors.As(err, &diagnostic) {
		return diagnostic.reason, boundedStatusErrorDetails(diagnostic.details)
	}
	var preflight *semanticAssessmentPreflightError
	if errors.As(err, &preflight) && preflight != nil {
		reasonCode := strings.TrimSpace(preflight.reasonCode)
		if reasonCode == "" {
			reasonCode = strings.TrimSpace(preflight.stage)
		}
		if reasonCode == "" {
			reasonCode = "assessor_preflight_failed"
		}
		details := map[string]any{
			"component": assessorFailureComponent(preflight.stage),
		}
		if assessmentFailureIsClientControlled(preflight.stage) {
			details["client_controlled"] = true
		} else {
			details["server_owned"] = true
		}
		if preflight.measurement != nil {
			details["unit"] = preflight.measurement.Unit
			details["observed"] = preflight.measurement.Observed
			details["limit"] = preflight.measurement.Limit
			if preflight.measurement.ObservedAtLeast {
				details["observed_at_least"] = true
			}
		}
		return reasonCode, details
	}
	var malformed *assessor.MalformedResponseError
	if errors.As(err, &malformed) && malformed != nil && strings.TrimSpace(malformed.FailureClass) == "input_budget" {
		details := map[string]any{
			"component":    assessorFailureComponent(malformed.ValidationStage),
			"server_owned": true,
		}
		if malformed.Measurement != nil {
			details["unit"] = malformed.Measurement.Unit
			details["observed"] = malformed.Measurement.Observed
			details["limit"] = malformed.Measurement.Limit
			if malformed.Measurement.ObservedAtLeast {
				details["observed_at_least"] = true
			}
		}
		reasonCode := "assessor_conversation_input_exceeded"
		if strings.TrimSpace(malformed.ValidationStage) == "conversation_candidate_context_tokens" {
			reasonCode = "assessor_conversation_candidate_context_exceeded"
		}
		return reasonCode, details
	}
	return "", nil
}

// SynchronousAssessmentValidationDiagnostics returns the bounded, server-owned
// validation history for operator logs and Remember attempt events. It never
// includes provider messages or response content.
func SynchronousAssessmentValidationDiagnostics(err error) map[string]any {
	if err == nil {
		return nil
	}
	var historyErr *submissionAssessmentValidationHistoryError
	var malformed *assessor.MalformedResponseError
	hasHistory := errors.As(err, &historyErr)
	hasMalformed := errors.As(err, &malformed)
	if !hasHistory && !hasMalformed {
		return nil
	}
	turns := make([]submissionAssessmentValidationTurn, 0, SemanticMaxAssessorTurns)
	if historyErr != nil {
		turns = append(turns, historyErr.turns...)
	}
	if len(turns) == 0 && malformed != nil {
		turns = append(turns, submissionAssessmentValidationTurn{
			Attempt:    malformed.Attempts,
			Stage:      assessmentValidationStage(malformed.ValidationStage),
			Fields:     append([]string(nil), malformed.ValidationFieldFamilies...),
			ErrorCount: len(malformed.ValidationFieldFamilies),
		})
	}
	if len(turns) == 0 {
		return nil
	}
	turnsTruncated := len(turns) > SemanticMaxAssessorTurns
	if turnsTruncated {
		turns = turns[:SemanticMaxAssessorTurns]
	}
	projectedTurns := make([]any, 0, len(turns))
	validationTruncated := false
	for _, turn := range turns {
		fields := make([]string, 0, len(turn.Fields))
		families := make([]string, 0, len(turn.Fields))
		fieldSeen := make(map[string]struct{}, len(turn.Fields))
		familySeen := make(map[string]struct{}, len(turn.Fields))
		truncated := turn.ErrorCount > maxAssessorValidationFields
		for _, raw := range turn.Fields {
			field, family := normalizeAssessmentValidationField(raw)
			if field == "" {
				field, family = "other", "other"
			}
			if _, ok := fieldSeen[field]; !ok {
				if len(fields) >= maxAssessorValidationFields {
					truncated = true
				} else {
					fields = append(fields, field)
					fieldSeen[field] = struct{}{}
				}
			}
			if _, ok := familySeen[family]; !ok {
				if len(families) >= maxAssessorValidationFields {
					truncated = true
				} else {
					families = append(families, family)
					familySeen[family] = struct{}{}
				}
			}
		}
		sort.Strings(fields)
		sort.Strings(families)
		projectedTurns = append(projectedTurns, map[string]any{
			"attempt":        clampAssessorValidationAttempt(turn.Attempt),
			"stage":          boundedAssessorValidationStage(turn.Stage),
			"fields":         fields,
			"field_families": families,
			"error_count":    validationTurnErrorCount(turn),
			"truncated":      truncated,
		})
		validationTruncated = validationTruncated || truncated
	}
	if turnsTruncated && len(projectedTurns) > 0 {
		projectedTurns[len(projectedTurns)-1].(map[string]any)["truncated"] = true
	}
	failureClass := assessorValidationFailureClass(err)
	if malformed != nil && strings.TrimSpace(malformed.FailureClass) != "" {
		failureClass = boundedAssessorFailureClass(malformed.FailureClass)
	}
	return map[string]any{
		"failure_class": failureClass,
		"turns":         projectedTurns,
		"truncated":     turnsTruncated || validationTruncated,
	}
}

func assessorValidationFailureClass(err error) string {
	switch {
	case errors.Is(err, ErrRememberInputBudgetExceeded):
		return "input_budget"
	case errors.Is(err, ErrRememberProviderResponseInvalid):
		return "provider_response_invalid"
	case errors.Is(err, ErrRememberProviderUnavailable):
		return "provider_unavailable"
	case errors.Is(err, ErrRememberRequestTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "provider_error"
	}
}

func normalizeAssessmentValidationField(field string) (string, string) {
	field, ok := normalizeAssessmentValidationIndexes(field)
	if !ok {
		return "other", "other"
	}
	field = strings.ToLower(strings.TrimSpace(field))
	if field == "" || len([]byte(field)) > 256 {
		return "other", "other"
	}
	if family, ok := assessmentValidationFieldPathFamilies[field]; ok {
		return field, family
	}
	return "other", "other"
}

func normalizeAssessmentValidationIndexes(field string) (string, bool) {
	var out strings.Builder
	for index := 0; index < len(field); index++ {
		if field[index] != '[' {
			out.WriteByte(field[index])
			continue
		}
		close := strings.IndexByte(field[index:], ']')
		if close < 0 {
			return "", false
		}
		close += index
		if close == index+1 {
			return "", false
		}
		for _, value := range field[index+1 : close] {
			if value < '0' || value > '9' {
				return "", false
			}
		}
		out.WriteString("[]")
		index = close
	}
	return out.String(), true
}

func validationTurnErrorCount(turn submissionAssessmentValidationTurn) int {
	count := turn.ErrorCount
	if count <= 0 {
		count = len(turn.Fields)
	}
	if count > maxAssessorValidationFields {
		return maxAssessorValidationFields
	}
	return count
}

var assessmentValidationFieldPathFamilies = map[string]string{
	"evidence_conflict_results":                     "evidence_conflict_results",
	"evidence_equivalence_candidates":               "evidence_equivalence_candidates",
	"evidence_equivalence_candidates[]":             "evidence_equivalence_candidates",
	"evidence_equivalence_candidates[].evidence_id": "evidence_equivalence_candidates.evidence",
	"evidence_equivalence_candidates[].candidates":  "evidence_equivalence_candidates.evidence",
	"request_id": "request_id", "input_tokens": "input_tokens", "output_tokens": "output_tokens",
	"response": "response", "tokenizer": "tokenizer", "request": "request", "team_id": "team_id",
	"evidence": "evidence", "known_evidence": "known_evidence", "candidate_context_tokens": "candidate_context_tokens",
	"evidence_security_results": "evidence_security_results", "evidence_equivalence_results": "evidence_equivalence_results",
	"evidence[]": "evidence.evidence", "evidence[].evidence_id": "evidence.evidence",
	"evidence[].content": "evidence.evidence", "evidence[].source_revision_id": "evidence.evidence",
	"known_evidence[]": "known_evidence.evidence", "known_evidence[].evidence_id": "known_evidence.evidence",
	"known_evidence[].content":    "known_evidence.evidence",
	"evidence_security_results[]": "evidence_security_results", "evidence_security_results[].evidence_id": "evidence_security_results.evidence",
	"evidence_security_results[].decision": "evidence_security_results.semantics", "evidence_security_results[].signals": "evidence_security_results.semantics",
	"evidence_security_results[].signals[]": "evidence_security_results.semantics", "evidence_security_results[].signals[].kind": "evidence_security_results.semantics",
	"evidence_security_results[].signals[].start_ref": "evidence_security_results.evidence", "evidence_security_results[].signals[].end_ref": "evidence_security_results.evidence",
	"evidence_security_results[].signals[].span": "evidence_security_results.evidence",
	"evidence_equivalence_results[]":             "evidence_equivalence_results", "evidence_equivalence_results[].evidence_id": "evidence_equivalence_results.evidence",
	"evidence_equivalence_results[].action": "evidence_equivalence_results.semantics", "evidence_equivalence_results[].candidate_evidence_id": "evidence_equivalence_results.evidence",
	"evidence_conflict_results[]": "evidence_conflict_results", "evidence_conflict_results[].positions": "evidence_conflict_results.semantics",
	"evidence_conflict_results[].positions[]": "evidence_conflict_results.semantics", "evidence_conflict_results[].positions[].evidence_id": "evidence_conflict_results.evidence",
	"evidence_conflict_results[].positions[].start_ref": "evidence_conflict_results.evidence", "evidence_conflict_results[].positions[].end_ref": "evidence_conflict_results.evidence",
}

func boundedAssessorValidationStage(stage string) string {
	switch strings.TrimSpace(stage) {
	case "response_output_tokens", "response_json", "response_contract", "conversation_input_tokens", "conversation_candidate_context_tokens", "input_budget", "assessment":
		return strings.TrimSpace(stage)
	default:
		return "other"
	}
}

func boundedAssessorFailureClass(value string) string {
	switch strings.TrimSpace(value) {
	case "malformed_exhausted", "validation_failed", "input_budget", "provider", "provider_error", "provider_response_invalid", "provider_unavailable", "timeout", "canceled":
		return strings.TrimSpace(value)
	default:
		return "other"
	}
}

func clampAssessorValidationAttempt(value int) int {
	if value < 1 {
		return 1
	}
	if value > SemanticMaxAssessorTurns {
		return SemanticMaxAssessorTurns
	}
	return value
}

func assessorFailureComponent(stage string) string {
	switch strings.TrimSpace(stage) {
	case "known_evidence_context":
		return "assessor.required_known_evidence"
	case "catalog_context", "catalog_context_validation":
		return "assessor.optional_context"
	case "required_context":
		return "assessor.required_context"
	case "assessment_input", "input_tokens":
		return "assessor.required_input"
	case "provider_framing":
		return "assessor.provider_framing"
	case "conversation_input_tokens":
		return "assessor.conversation"
	case "conversation_candidate_context_tokens":
		return "assessor.conversation_candidate_context"
	default:
		return "assessor"
	}
}

func assessmentFailureIsClientControlled(stage string) bool {
	switch strings.TrimSpace(stage) {
	case "assessment_input", "input_tokens":
		return true
	default:
		return false
	}
}

func terminalizeAfterError(original error, complete func() error) error {
	if completionErr := complete(); completionErr != nil {
		return errors.Join(original, completionErr)
	}
	return nil
}

func assessmentTokenizer(limits assessor.SemanticAssessmentLimits) string {
	if tokenizer := strings.TrimSpace(limits.Tokenizer); tokenizer != "" {
		return tokenizer
	}
	return assessor.DefaultSemanticAssessmentLimits().Tokenizer
}

func semanticAssessmentHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func semanticAssessmentMalformedFailure(err error) (string, int) {
	var malformed *assessor.MalformedResponseError
	if !errors.As(err, &malformed) {
		return "malformed_response", 0
	}
	failureClass := strings.TrimSpace(malformed.FailureClass)
	if failureClass == "" {
		failureClass = "malformed_response"
	}
	return failureClass, malformed.Attempts
}

func assessmentCandidateGroupKey(evidenceID string, start, end int) string {
	return evidenceID + ":" + strconv.Itoa(start) + ":" + strconv.Itoa(end)
}

func semanticAssessmentEvidence(fragment repository.EvidenceFragment, evidenceID string) assessor.SemanticReviewEvidence {
	result := assessor.SemanticReviewEvidence{
		EvidenceID:              evidenceID,
		FragmentID:              fragment.FragmentID,
		EvidenceIndex:           fragment.EvidenceIndex,
		Content:                 fragment.Content,
		Authority:               fragment.Authority,
		SourceID:                fragment.SourceID,
		SourceRevisionID:        fragment.SourceRevisionID,
		CurrentSourceRevisionID: fragment.SourceRevisionID,
	}
	if fragment.Partition != nil {
		result.SourceItemID = fragment.Partition.SourceItemID
		result.PartitionIndex = fragment.Partition.Index
	}
	return result
}

func assessmentValidationStage(stage string) string {
	if strings.TrimSpace(stage) == "" {
		return "response_contract"
	}
	return stage
}

func semanticAssessmentValidationFieldFamiliesForService(errs []assessor.SemanticValidationError) []string {
	seen := make(map[string]struct{}, len(errs))
	result := make([]string, 0, len(errs))
	for _, err := range errs {
		field := strings.TrimSpace(err.Field)
		if field == "" {
			field = "other"
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		result = append(result, field)
	}
	return result
}
