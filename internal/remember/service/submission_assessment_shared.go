package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

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
	"request_id": "request_id", "input_tokens": "input_tokens", "output_tokens": "output_tokens",
	"response": "response", "tokenizer": "tokenizer", "request": "request", "team_id": "team_id",
	"evidence": "evidence", "known_evidence": "known_evidence", "candidate_context_tokens": "candidate_context_tokens",
	"evidence_security_results": "evidence_security_results", "evidence_equivalence_results": "evidence_equivalence_results",
	"evidence_conflict_results": "evidence_conflict_results", "submission_contract": "submission_contract",
	"submission_contract.entities": "submission_contract.entities", "submission_contract.relationships": "submission_contract.relationships",
	"entity_results": "entity_results", "relationship_results": "relationship_results",
	"entity_candidate_groups": "entity_candidate_groups", "predicate_options": "predicate_options",
	"evidence[]": "evidence.evidence", "evidence[].evidence_id": "evidence.evidence",
	"evidence[].content": "evidence.evidence", "evidence[].source_revision_id": "evidence.evidence",
	"known_evidence[]": "known_evidence.evidence", "known_evidence[].evidence_id": "known_evidence.evidence",
	"known_evidence[].content":  "known_evidence.evidence",
	"entity_candidate_groups[]": "entity_candidate_groups", "entity_candidate_groups[].grounding_ref": "entity_candidate_groups.ref",
	"entity_candidate_groups[].evidence_id": "entity_candidate_groups.evidence", "entity_candidate_groups[].surface": "entity_candidate_groups.evidence",
	"entity_candidate_groups[].candidates":                  "entity_candidate_groups.candidates",
	"entity_candidate_groups[].candidates[]":                "entity_candidate_groups.candidates",
	"entity_candidate_groups[].candidates[].entity_id":      "entity_candidate_groups.candidates",
	"entity_candidate_groups[].candidates[].canonical_name": "entity_candidate_groups.candidates",
	"entity_candidate_groups[].candidates[].kind":           "entity_candidate_groups.candidates",
	"predicate_options[]":                                   "predicate_options", "predicate_options[].predicate_key": "predicate_options.predicate",
	"predicate_options[].version": "predicate_options.predicate", "predicate_options[].relationship_kind": "predicate_options.kind",
	"predicate_options[].current_cardinality": "predicate_options.kind", "predicate_options[].allowed_subject_kinds": "predicate_options.kind",
	"predicate_options[].allowed_object_kinds": "predicate_options.kind",
	"entity_results[]":                         "entity_results", "entity_results[].ref": "entity_results.ref",
	"entity_results[].action": "entity_results.semantics", "entity_results[].candidate_entity_id": "entity_results.ref",
	"entity_results[].grounding_ref": "entity_results.ref", "entity_results[].anchor_ref": "entity_results.ref",
	"entity_results[].kind": "entity_results.kind", "entity_results[].surface": "entity_results.evidence",
	"entity_results[].evidence_id": "entity_results.evidence",
	"relationship_results[]":       "relationship_results", "relationship_results[].ref": "relationship_results.ref",
	"relationship_results[].disposition": "relationship_results.semantics", "relationship_results[].reason": "relationship_results.semantics",
	"relationship_results[].object_ref": "relationship_results.object", "relationship_results[].object_value": "relationship_results.object",
	"relationship_results[].splits":               "relationship_results.semantics",
	"relationship_results[].splits[]":             "relationship_results.semantics",
	"relationship_results[].splits[].split_index": "relationship_results.semantics",
	"relationship_results[].splits[].subject_ref": "relationship_results.ref", "relationship_results[].splits[].object_ref": "relationship_results.object",
	"relationship_results[].splits[].object": "relationship_results.object", "relationship_results[].splits[].object_value": "relationship_results.object",
	"relationship_results[].splits[].value_range": "relationship_results.object", "relationship_results[].splits[].value_range.evidence_id": "relationship_results.object", "relationship_results[].splits[].value_range.start_ref": "relationship_results.object", "relationship_results[].splits[].value_range.end_ref": "relationship_results.object", "relationship_results[].splits[].original_predicate": "relationship_results.predicate",
	"relationship_results[].splits[].predicate_key": "relationship_results.predicate", "relationship_results[].splits[].predicate_version": "relationship_results.predicate",
	"relationship_results[].splits[].predicate_status": "relationship_results.predicate", "relationship_results[].splits[].predicate_range": "relationship_results.predicate", "relationship_results[].splits[].predicate_range.evidence_id": "relationship_results.predicate", "relationship_results[].splits[].predicate_range.start_ref": "relationship_results.predicate", "relationship_results[].splits[].predicate_range.end_ref": "relationship_results.predicate",
	"relationship_results[].splits[].predicate_registration": "relationship_results.predicate", "relationship_results[].splits[].predicate_registration.predicate_key": "relationship_results.predicate", "relationship_results[].splits[].predicate_registration.relationship_kind": "relationship_results.predicate", "relationship_results[].splits[].predicate_registration.current_cardinality": "relationship_results.predicate", "relationship_results[].splits[].support_ranges": "relationship_results.evidence", "relationship_results[].splits[].support_ranges[].start_ref": "relationship_results.evidence", "relationship_results[].splits[].support_ranges[].end_ref": "relationship_results.evidence",
	"relationship_results[].splits[].valid_from": "relationship_results.temporal", "relationship_results[].splits[].valid_to": "relationship_results.temporal",
	"relationship_results[].splits[].object_value.value_type": "relationship_results.object", "relationship_results[].splits[].object_value.canonical_value": "relationship_results.object", "relationship_results[].splits[].object_value.display": "relationship_results.object", "relationship_results[].splits[].object_value.unit": "relationship_results.object",
	"relationship_results[].splits[].polarity": "relationship_results.semantics", "relationship_results[].splits[].validity": "relationship_results.temporal",
	"relationship_results[].splits[].support_ranges[]":             "relationship_results.evidence",
	"relationship_results[].splits[].support_ranges[].evidence_id": "relationship_results.evidence",
	"relationship_results.splits[]":                                "relationship_results.semantics",
	"relationship_results[].splits[].evidence":                     "relationship_results.evidence",
	"relationship_results[].splits[].evidence[]":                   "relationship_results.evidence",
	"relationship_results[].splits[].evidence[].evidence_id":       "relationship_results.evidence",
	"evidence_security_results[]":                                  "evidence_security_results", "evidence_security_results[].evidence_id": "evidence_security_results.evidence",
	"evidence_security_results[].decision": "evidence_security_results.semantics", "evidence_security_results[].signals": "evidence_security_results.semantics",
	"evidence_security_results[].signals[]": "evidence_security_results.semantics", "evidence_security_results[].signals[].kind": "evidence_security_results.semantics",
	"evidence_security_results[].signals[].start_ref": "evidence_security_results.evidence", "evidence_security_results[].signals[].end_ref": "evidence_security_results.evidence",
	"evidence_security_results[].signals[].span": "evidence_security_results.evidence",
	"evidence_equivalence_results[]":             "evidence_equivalence_results", "evidence_equivalence_results[].evidence_id": "evidence_equivalence_results.evidence",
	"evidence_equivalence_results[].action": "evidence_equivalence_results.semantics", "evidence_equivalence_results[].candidate_evidence_id": "evidence_equivalence_results.evidence",
	"evidence_conflict_results[]": "evidence_conflict_results", "evidence_conflict_results[].positions": "evidence_conflict_results.semantics",
	"evidence_conflict_results[].positions[]": "evidence_conflict_results.semantics", "evidence_conflict_results[].positions[].evidence_id": "evidence_conflict_results.evidence",
	"evidence_conflict_results[].positions[].start_ref": "evidence_conflict_results.evidence", "evidence_conflict_results[].positions[].end_ref": "evidence_conflict_results.evidence",
	"submission_contract.entities[]": "submission_contract.entities", "submission_contract.entities[].ref": "submission_contract.entities.ref",
	"submission_contract.entities[].name": "submission_contract.entities", "submission_contract.entities[].kind": "submission_contract.entities.kind",
	"submission_contract.entities[].groundings": "submission_contract.entities.evidence", "submission_contract.entities[].groundings[]": "submission_contract.entities.evidence",
	"submission_contract.entities[].groundings[].evidence_id": "submission_contract.entities.evidence", "submission_contract.entities[].groundings[].surface": "submission_contract.entities.evidence",
	"submission_contract.entities[].groundings[].grounding_ref": "submission_contract.entities.ref", "submission_contract.entities[].groundings[].anchor_ref": "submission_contract.entities.ref",
	"submission_contract.entities[].anchors": "submission_contract.entities.evidence", "submission_contract.entities[].anchors[]": "submission_contract.entities.evidence",
	"submission_contract.entities[].anchors[].evidence_id": "submission_contract.entities.evidence", "submission_contract.entities[].anchors[].anchor_ref": "submission_contract.entities.ref",
	"submission_contract.entities[].anchors[].surface": "submission_contract.entities.evidence", "submission_contract.entities[].candidate_entity_ids": "submission_contract.entities.ref",
	"submission_contract.relationships[]": "submission_contract.relationships", "submission_contract.relationships[].ref": "submission_contract.relationships.ref",
	"submission_contract.relationships[].subject_ref": "submission_contract.relationships.ref", "submission_contract.relationships[].object": "submission_contract.relationships.object",
	"submission_contract.relationships[].predicate": "submission_contract.relationships.predicate", "submission_contract.relationships[].polarity": "submission_contract.relationships.semantics",
	"submission_contract.relationships[].evidence": "submission_contract.relationships.evidence", "submission_contract.relationships[].evidence_ids": "submission_contract.relationships.evidence",
	"submission_contract.relationships[].known_evidence_ids": "submission_contract.relationships.evidence", "submission_contract.relationships[].evidence[]": "submission_contract.relationships.evidence",
	"submission_contract.relationships[].evidence[].evidence_id": "submission_contract.relationships.evidence",
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
	case "entity_catalog":
		return "assessor.required_entity_catalog"
	case "known_evidence_context":
		return "assessor.required_known_evidence"
	case "catalog_context", "catalog_context_validation", "predicate_options_overflow":
		return "assessor.optional_context"
	case "predicate_context":
		return "assessor.required_predicate_context"
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

func cloneAssessmentProposal(proposal map[string]any) map[string]any {
	if proposal == nil {
		return map[string]any{}
	}
	encoded, err := json.Marshal(proposal)
	if err != nil {
		return map[string]any{}
	}
	var cloned map[string]any
	if json.Unmarshal(encoded, &cloned) != nil {
		return map[string]any{}
	}
	return cloned
}

func assessmentClientProposalWithoutTrustedContext(proposal map[string]any) map[string]any {
	cloned := cloneAssessmentProposal(proposal)
	for _, relationship := range semanticProposalObjectArray(cloned, "relationship_hints", "relationships") {
		delete(relationship, "correction_target")
		delete(relationship, "conflict_context")
	}
	return cloned
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

func assessmentGroupsBySpan(groups []assessor.SemanticAssessmentEntityCandidateGroup) map[string]*assessor.SemanticAssessmentEntityCandidateGroup {
	result := make(map[string]*assessor.SemanticAssessmentEntityCandidateGroup, len(groups))
	for index := range groups {
		group := &groups[index]
		result[assessmentCandidateGroupKey(group.EvidenceID, group.Start, group.End)] = group
	}
	return result
}

func semanticAssessmentEvidence(fragment repository.EvidenceFragment, evidenceID string) assessor.SemanticReviewEvidence {
	return assessor.SemanticReviewEvidence{
		EvidenceID:              evidenceID,
		FragmentID:              fragment.FragmentID,
		EvidenceIndex:           fragment.EvidenceIndex,
		Content:                 fragment.Content,
		Authority:               fragment.Authority,
		SourceID:                fragment.SourceID,
		SourceRevisionID:        fragment.SourceRevisionID,
		CurrentSourceRevisionID: fragment.SourceRevisionID,
	}
}

func proposalMap(raw any) (map[string]any, bool) {
	fields, ok := raw.(map[string]any)
	return fields, ok
}

func semanticProposalObjectArray(raw map[string]any, keys ...string) []map[string]any {
	for _, key := range keys {
		values, ok := raw[key]
		if !ok {
			continue
		}
		switch typed := values.(type) {
		case []map[string]any:
			return typed
		case []any:
			out := make([]map[string]any, 0, len(typed))
			for _, item := range typed {
				if fields, ok := proposalMap(item); ok {
					out = append(out, fields)
				}
			}
			return out
		}
	}
	return nil
}

func proposalString(fields map[string]any, key string) string {
	if fields == nil {
		return ""
	}
	value, _ := fields[key].(string)
	return strings.TrimSpace(value)
}

func proposalInt(fields map[string]any, key string) (int, bool) {
	if fields == nil {
		return 0, false
	}
	switch value := fields[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		if value == float64(int(value)) {
			return int(value), true
		}
	}
	return 0, false
}

func proposalOptionalTime(fields map[string]any, key string) (*time.Time, error) {
	if fields == nil {
		return nil, nil
	}
	raw, exists := fields[key]
	if !exists || raw == nil {
		return nil, nil
	}
	switch value := raw.(type) {
	case time.Time:
		parsed := value.UTC()
		return &parsed, nil
	case *time.Time:
		if value == nil {
			return nil, nil
		}
		parsed := value.UTC()
		return &parsed, nil
	case string:
		text := strings.TrimSpace(value)
		if text == "" {
			return nil, nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return nil, fmt.Errorf("must be RFC3339 timestamp")
		}
		parsed = parsed.UTC()
		return &parsed, nil
	default:
		return nil, fmt.Errorf("must be RFC3339 timestamp")
	}
}

func semanticProposalCorrectionTarget(raw map[string]any) (assessor.RelationshipCorrectionTarget, bool) {
	target, ok := proposalMap(raw["correction_target"])
	if !ok {
		return assessor.RelationshipCorrectionTarget{}, false
	}
	relationshipID := proposalString(target, "relationship_id")
	expectedVersion, ok := proposalInt(target, "expected_version")
	if relationshipID == "" || !ok {
		return assessor.RelationshipCorrectionTarget{}, false
	}
	return assessor.RelationshipCorrectionTarget{
		RelationshipID:  relationshipID,
		ExpectedVersion: expectedVersion,
	}, true
}

func semanticProposalConflictContext(raw map[string]any) (assessor.RelationshipConflictContext, bool) {
	conflictContext, ok := proposalMap(raw["conflict_context"])
	if !ok {
		return assessor.RelationshipConflictContext{}, false
	}
	conflictID := proposalString(conflictContext, "conflict_id")
	expectedVersion, ok := proposalInt(conflictContext, "expected_version")
	if conflictID == "" || !ok {
		return assessor.RelationshipConflictContext{}, false
	}
	return assessor.RelationshipConflictContext{
		ConflictID:      conflictID,
		ExpectedVersion: expectedVersion,
	}, true
}

func stringPointer(value string) *string {
	value = strings.TrimSpace(value)
	return &value
}

func intPointer(value int) *int {
	return &value
}
