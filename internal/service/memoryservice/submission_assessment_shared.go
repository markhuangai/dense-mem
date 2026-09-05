package memoryservice

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/repository"
)

// SemanticMaxAssessorTurns covers the initial Remember assessor response and
// at most two complete-response corrections in the same provider conversation.
// Other assessor workflows retain their own broader historical limits.
const SemanticMaxAssessorTurns = 3

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
