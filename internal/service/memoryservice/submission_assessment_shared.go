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

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

// SemanticPlacementMaxAssessorTurns covers the initial assessor response and
// bounded complete-response corrections in the same provider conversation.
const SemanticPlacementMaxAssessorTurns = verifier.SemanticAssessmentMaxProviderTurns

type semanticAssessmentPreflightError struct {
	stage        string
	reasonCode   string
	failureClass string
	measurement  *verifier.FailureMeasurement
	err          error
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
	return err.err
}

func deterministicSemanticAssessmentPreflightError(stage, message string) error {
	return &semanticAssessmentPreflightError{
		stage:        strings.TrimSpace(stage),
		reasonCode:   placementFailureReasonCode(strings.TrimSpace(stage), "validation_failed"),
		failureClass: "validation_failed",
		err:          errors.New(message),
	}
}

func deterministicSemanticAssessmentPreflightErrorWithMeasurement(
	stage string,
	message string,
	measurement verifier.FailureMeasurement,
) error {
	result := deterministicSemanticAssessmentPreflightError(stage, message).(*semanticAssessmentPreflightError)
	result.measurement = &measurement
	return result
}

func semanticAssessmentPreflightFailure(err error) (string, bool) {
	var preflight *semanticAssessmentPreflightError
	if errors.As(err, &preflight) && preflight.stage != "" {
		return preflight.stage, true
	}
	return "candidate_prefetch", false
}

func terminalizeAfterError(original error, complete func() error) error {
	if completionErr := complete(); completionErr != nil {
		return errors.Join(original, completionErr)
	}
	return nil
}

func assessmentTokenizer(limits verifier.SemanticAssessmentLimits) string {
	if tokenizer := strings.TrimSpace(limits.Tokenizer); tokenizer != "" {
		return tokenizer
	}
	return verifier.DefaultSemanticAssessmentLimits().Tokenizer
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
	for _, relationship := range placementProposalObjectArray(cloned, "relationship_hints", "relationships") {
		delete(relationship, "correction_target")
		delete(relationship, "conflict_context")
	}
	return cloned
}

func semanticAssessmentMalformedFailure(err error) (string, int) {
	var malformed *verifier.MalformedResponseError
	if !errors.As(err, &malformed) {
		return "malformed_response", 0
	}
	failureClass := strings.TrimSpace(malformed.FailureClass)
	if failureClass == "" {
		failureClass = "malformed_response"
	}
	return failureClass, malformed.Attempts
}

func semanticAssessmentRetryPayload(
	stage string,
	providerAttempted bool,
	failure ...verifier.ProviderFailureMetadata,
) map[string]any {
	diagnostic := placementFailureDiagnosticFor(stage, nil)
	if len(failure) > 0 {
		diagnostic = placementFailureDiagnosticForProvider(stage, failure[0])
	}
	payload := diagnostic.payload(providerAttempted)
	payload["assessor_contract"] = domain.ContractVersion
	return payload
}

func semanticAssessmentFailurePayload(
	stage string,
	providerAttempted bool,
	cause error,
	failure ...verifier.ProviderFailureMetadata,
) map[string]any {
	diagnostic := placementFailureDiagnosticFor(stage, cause)
	if len(failure) > 0 {
		diagnostic = placementFailureDiagnosticForProvider(stage, failure[0])
		if cause != nil {
			fromCause := placementFailureDiagnosticFor(stage, cause)
			diagnostic.ValidationStage = fromCause.ValidationStage
			diagnostic.ValidationFieldFamilies = fromCause.ValidationFieldFamilies
			diagnostic.Measurement = fromCause.Measurement
			diagnostic.AssessorTurns = fromCause.AssessorTurns
		}
	}
	payload := diagnostic.payload(providerAttempted)
	payload["assessor_contract"] = domain.ContractVersion
	return payload
}

func assessmentCandidateGroupKey(evidenceID string, start, end int) string {
	return evidenceID + ":" + strconv.Itoa(start) + ":" + strconv.Itoa(end)
}

func assessmentGroupsBySpan(groups []verifier.SemanticAssessmentEntityCandidateGroup) map[string]*verifier.SemanticAssessmentEntityCandidateGroup {
	result := make(map[string]*verifier.SemanticAssessmentEntityCandidateGroup, len(groups))
	for index := range groups {
		group := &groups[index]
		result[assessmentCandidateGroupKey(group.EvidenceID, group.Start, group.End)] = group
	}
	return result
}

func semanticAssessmentEvidence(fragment repository.EvidenceFragment, evidenceID string) verifier.SemanticReviewEvidence {
	return verifier.SemanticReviewEvidence{
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

func placementProposalObjectArray(raw map[string]any, keys ...string) []map[string]any {
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

func placementProposalCorrectionTarget(raw map[string]any) (verifier.RelationshipCorrectionTarget, bool) {
	target, ok := proposalMap(raw["correction_target"])
	if !ok {
		return verifier.RelationshipCorrectionTarget{}, false
	}
	relationshipID := proposalString(target, "relationship_id")
	expectedVersion, ok := proposalInt(target, "expected_version")
	if relationshipID == "" || !ok {
		return verifier.RelationshipCorrectionTarget{}, false
	}
	return verifier.RelationshipCorrectionTarget{
		RelationshipID:  relationshipID,
		ExpectedVersion: expectedVersion,
	}, true
}

func placementProposalConflictContext(raw map[string]any) (verifier.RelationshipConflictContext, bool) {
	conflictContext, ok := proposalMap(raw["conflict_context"])
	if !ok {
		return verifier.RelationshipConflictContext{}, false
	}
	conflictID := proposalString(conflictContext, "conflict_id")
	expectedVersion, ok := proposalInt(conflictContext, "expected_version")
	if conflictID == "" || !ok {
		return verifier.RelationshipConflictContext{}, false
	}
	return verifier.RelationshipConflictContext{
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
