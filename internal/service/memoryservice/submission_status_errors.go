package memoryservice

import (
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

// SubmissionErrorCode is the closed public vocabulary for terminal submission
// failures. Internal/provider/database reasons must be translated into this
// set before they cross the status projection boundary.
type SubmissionErrorCode string

const (
	SubmissionErrorRequiresResubmission  SubmissionErrorCode = "submission_requires_resubmission"
	SubmissionErrorNormalizationFailed   SubmissionErrorCode = "normalization_failed"
	SubmissionErrorNormalizerUnavailable SubmissionErrorCode = "normalizer_unavailable"
	SubmissionErrorPolicyRejected        SubmissionErrorCode = "submission_policy_rejected"
	SubmissionErrorAssessorInvalid       SubmissionErrorCode = "assessor_response_invalid"
	SubmissionErrorAssessorUnavailable   SubmissionErrorCode = "assessor_unavailable"
	SubmissionErrorProcessingFailed      SubmissionErrorCode = "submission_processing_failed"
	SubmissionErrorContractSuperseded    SubmissionErrorCode = "contract_superseded"
	SubmissionErrorSearchIndexingDelayed SubmissionErrorCode = "search_indexing_delayed"
	SubmissionErrorQuarantined           SubmissionErrorCode = "submission_quarantined"

	SubmissionErrorRelationshipVersionStale      SubmissionErrorCode = "relationship_version_stale"
	SubmissionErrorRelationshipNotActive         SubmissionErrorCode = "relationship_not_active"
	SubmissionErrorObjectKindChangeForbidden     SubmissionErrorCode = "object_kind_change_forbidden"
	SubmissionErrorSupportSetMismatch            SubmissionErrorCode = "support_set_mismatch"
	SubmissionErrorEntityNotFound                SubmissionErrorCode = "entity_not_found"
	SubmissionErrorTooManyEntityCandidates       SubmissionErrorCode = "too_many_entity_candidates"
	SubmissionErrorPredicateNotFound             SubmissionErrorCode = "predicate_not_found"
	SubmissionErrorPredicateSubjectKindMismatch  SubmissionErrorCode = "predicate_subject_kind_mismatch"
	SubmissionErrorPredicateObjectKindMismatch   SubmissionErrorCode = "predicate_object_kind_mismatch"
	SubmissionErrorNoChange                      SubmissionErrorCode = "no_change"
	SubmissionErrorConfirmationExpired           SubmissionErrorCode = "confirmation_expired"
	SubmissionErrorRelationshipChanged           SubmissionErrorCode = "relationship_changed"
	SubmissionErrorSupportSetChanged             SubmissionErrorCode = "support_set_changed"
	SubmissionErrorPersistentAmbiguity           SubmissionErrorCode = "persistent_ambiguity"
	SubmissionErrorInactiveRelationshipCollision SubmissionErrorCode = "inactive_relationship_collision"
)

var submissionErrorCodes = []SubmissionErrorCode{
	SubmissionErrorRequiresResubmission,
	SubmissionErrorNormalizationFailed,
	SubmissionErrorNormalizerUnavailable,
	SubmissionErrorPolicyRejected,
	SubmissionErrorAssessorInvalid,
	SubmissionErrorAssessorUnavailable,
	SubmissionErrorProcessingFailed,
	SubmissionErrorContractSuperseded,
	SubmissionErrorSearchIndexingDelayed,
	SubmissionErrorQuarantined,
	SubmissionErrorRelationshipVersionStale,
	SubmissionErrorRelationshipNotActive,
	SubmissionErrorObjectKindChangeForbidden,
	SubmissionErrorSupportSetMismatch,
	SubmissionErrorEntityNotFound,
	SubmissionErrorTooManyEntityCandidates,
	SubmissionErrorPredicateNotFound,
	SubmissionErrorPredicateSubjectKindMismatch,
	SubmissionErrorPredicateObjectKindMismatch,
	SubmissionErrorNoChange,
	SubmissionErrorConfirmationExpired,
	SubmissionErrorRelationshipChanged,
	SubmissionErrorSupportSetChanged,
	SubmissionErrorPersistentAmbiguity,
	SubmissionErrorInactiveRelationshipCollision,
}

// SubmissionErrorCodes returns the public enum in deterministic schema order.
func SubmissionErrorCodes() []string {
	result := make([]string, 0, len(submissionErrorCodes))
	for _, code := range submissionErrorCodes {
		result = append(result, string(code))
	}
	return result
}

type SubmissionStatusError struct {
	Code                        string                        `json:"code"`
	Message                     string                        `json:"message"`
	Retryable                   bool                          `json:"retryable"`
	NextAction                  string                        `json:"next_action"`
	Remediation                 string                        `json:"remediation"`
	ResubmissionIssues          []SubmissionResubmissionIssue `json:"resubmission_issues,omitempty"`
	ResubmissionIssuesTruncated bool                          `json:"resubmission_issues_truncated,omitempty"`
}

type SubmissionResubmissionIssue struct {
	Code            string `json:"code"`
	RelationshipRef string `json:"relationship_ref,omitempty"`
	Component       string `json:"component,omitempty"`
	Message         string `json:"message"`
}

type SubmissionNextAction string

const (
	SubmissionNextActionPollStatus         SubmissionNextAction = "poll_status"
	SubmissionNextActionResubmitSubmission SubmissionNextAction = "resubmit_submission"
	SubmissionNextActionRetryCorrection    SubmissionNextAction = "retry_correction"
	SubmissionNextActionContactOperator    SubmissionNextAction = "contact_operator"
	SubmissionNextActionNone               SubmissionNextAction = "none"
)

var submissionNextActions = []SubmissionNextAction{
	SubmissionNextActionPollStatus,
	SubmissionNextActionResubmitSubmission,
	SubmissionNextActionRetryCorrection,
	SubmissionNextActionContactOperator,
	SubmissionNextActionNone,
}

func SubmissionNextActions() []string {
	result := make([]string, 0, len(submissionNextActions))
	for _, action := range submissionNextActions {
		result = append(result, string(action))
	}
	return result
}

var submissionErrorMessages = map[SubmissionErrorCode]string{
	SubmissionErrorRequiresResubmission:  "the complete submission must be sent again",
	SubmissionErrorNormalizationFailed:   "submission normalization failed after bounded corrections",
	SubmissionErrorNormalizerUnavailable: "the submission normalizer was unavailable after bounded retries",
	SubmissionErrorPolicyRejected:        "submission was rejected by semantic placement policy",
	SubmissionErrorAssessorInvalid:       "submission assessment returned an invalid response",
	SubmissionErrorAssessorUnavailable:   "submission assessment was unavailable after bounded retries",
	SubmissionErrorProcessingFailed:      "submission processing failed",
	SubmissionErrorContractSuperseded:    "submission uses a superseded remember contract; resubmit the complete batch using the current contract",
	SubmissionErrorSearchIndexingDelayed: "search indexing is delayed",
	SubmissionErrorQuarantined:           "submission was quarantined by security policy",

	SubmissionErrorRelationshipVersionStale:      "relationship version is stale",
	SubmissionErrorRelationshipNotActive:         "relationship must be active, supported, and canonical",
	SubmissionErrorObjectKindChangeForbidden:     "a Value object cannot be replaced with an Entity",
	SubmissionErrorSupportSetMismatch:            "supports must exactly match the relationship's effective evidence spans",
	SubmissionErrorEntityNotFound:                "corrected Entity is not active and available to the team",
	SubmissionErrorTooManyEntityCandidates:       "corrected Entity name has too many exact candidates",
	SubmissionErrorPredicateNotFound:             "predicate is not registered and active for the team",
	SubmissionErrorPredicateSubjectKindMismatch:  "predicate does not allow the corrected subject kind",
	SubmissionErrorPredicateObjectKindMismatch:   "predicate does not allow the corrected object kind",
	SubmissionErrorNoChange:                      "correction does not change the Relationship",
	SubmissionErrorConfirmationExpired:           "relationship correction confirmation expired",
	SubmissionErrorRelationshipChanged:           "relationship changed while confirmation was pending",
	SubmissionErrorSupportSetChanged:             "relationship supports changed while confirmation was pending",
	SubmissionErrorPersistentAmbiguity:           "selected Entity candidate is no longer available",
	SubmissionErrorInactiveRelationshipCollision: "corrected Relationship collides with inactive or unsupported history",
}

func submissionStatusError(code SubmissionErrorCode) SubmissionStatusError {
	message := submissionErrorMessages[code]
	if message == "" {
		code = SubmissionErrorProcessingFailed
		message = submissionErrorMessages[code]
	}
	retryable, nextAction := submissionErrorGuidance(code)
	return SubmissionStatusError{
		Code:        string(code),
		Message:     message,
		Retryable:   retryable,
		NextAction:  string(nextAction),
		Remediation: submissionErrorRemediation(nextAction),
	}
}

func submissionStatusErrorWithMessage(code SubmissionErrorCode, message string) SubmissionStatusError {
	result := submissionStatusError(code)
	result.Message = message
	return result
}

func submissionErrorGuidance(code SubmissionErrorCode) (bool, SubmissionNextAction) {
	switch code {
	case SubmissionErrorSearchIndexingDelayed:
		return true, SubmissionNextActionPollStatus
	case SubmissionErrorPolicyRejected,
		SubmissionErrorContractSuperseded,
		SubmissionErrorRequiresResubmission, SubmissionErrorNormalizationFailed,
		SubmissionErrorNormalizerUnavailable:
		return true, SubmissionNextActionResubmitSubmission
	case SubmissionErrorNoChange:
		return false, SubmissionNextActionNone
	case SubmissionErrorRelationshipVersionStale, SubmissionErrorRelationshipNotActive,
		SubmissionErrorObjectKindChangeForbidden, SubmissionErrorSupportSetMismatch,
		SubmissionErrorEntityNotFound, SubmissionErrorTooManyEntityCandidates,
		SubmissionErrorPredicateNotFound, SubmissionErrorPredicateSubjectKindMismatch,
		SubmissionErrorPredicateObjectKindMismatch, SubmissionErrorConfirmationExpired,
		SubmissionErrorRelationshipChanged, SubmissionErrorSupportSetChanged,
		SubmissionErrorPersistentAmbiguity, SubmissionErrorInactiveRelationshipCollision:
		return true, SubmissionNextActionRetryCorrection
	default:
		return false, SubmissionNextActionContactOperator
	}
}

func submissionErrorRemediation(action SubmissionNextAction) string {
	switch action {
	case SubmissionNextActionPollStatus:
		return "Poll get_submission_status after check_after_seconds."
	case SubmissionNextActionResubmitSubmission:
		return "Submit the complete batch again with remember and a new idempotency_key after correcting the input."
	case SubmissionNextActionRetryCorrection:
		return "Retry correct_relationship with current relationship state and a new idempotency_key."
	case SubmissionNextActionNone:
		return "No action is required."
	default:
		return "Contact an operator with submission_id and correlation_id."
	}
}

func submissionStatusErrorForCode(rawCode string, fallbackState string) SubmissionStatusError {
	code := SubmissionErrorCode(strings.TrimSpace(rawCode))
	for _, known := range submissionErrorCodes {
		if code == known {
			return submissionStatusError(code)
		}
	}
	if fallbackState == "rejected" {
		return submissionStatusError(SubmissionErrorPolicyRejected)
	}
	return submissionStatusError(SubmissionErrorProcessingFailed)
}

func submissionFailureCode(stage, class string) SubmissionErrorCode {
	stage = strings.TrimSpace(stage)
	class = strings.TrimSpace(class)
	switch {
	case stage == "contract_superseded":
		return SubmissionErrorContractSuperseded
	case stage == "normalization_failed" || stage == "assessment" && class == "malformed_exhausted":
		return SubmissionErrorNormalizationFailed
	case stage == "normalizer_unavailable":
		return SubmissionErrorNormalizerUnavailable
	case class == "malformed_response", class == "validation_failed", class == "provider_protocol", class == "request_invalid":
		return SubmissionErrorAssessorInvalid
	case class == "timeout", class == "rate_limited", class == "http_4xx", class == "http_5xx",
		class == "http_unexpected", class == "transport", class == "provider_unavailable":
		return SubmissionErrorAssessorUnavailable
	case stage == "policy_review", stage == "confidence_policy", stage == "security_signal",
		stage == "commit_review", stage == "conflict_context_stale":
		return SubmissionErrorPolicyRejected
	default:
		return SubmissionErrorProcessingFailed
	}
}

func submissionItemFailureError(item repository.PlacementItem, processing string) *SubmissionStatusError {
	if item.Status != string(domain.PlacementRunFailed) && item.Status != "failed" && item.Status != "rejected" {
		return nil
	}
	stage, _ := item.Result["failure_stage"].(string)
	class, _ := item.Result["failure_class"].(string)
	code, _ := item.Result["failure_code"].(string)
	if item.Status == "rejected" && strings.TrimSpace(stage) == "" && strings.TrimSpace(class) == "" {
		return nil
	}
	if strings.TrimSpace(code) != "" {
		errorValue := submissionStatusErrorForCode(code, processing)
		attachSubmissionResubmissionIssues(&errorValue, item.Result)
		return &errorValue
	}
	errorValue := submissionStatusError(submissionFailureCode(stage, class))
	attachSubmissionResubmissionIssues(&errorValue, item.Result)
	return &errorValue
}

const (
	submissionStatusMaxResubmissionIssues = 50
	submissionStatusMaxIssueCodeLength    = 128
	submissionStatusMaxIssueRefLength     = 128
	submissionStatusMaxIssueComponentLen  = 128
	submissionStatusMaxIssueMessageLength = 512
)

func attachSubmissionResubmissionIssues(errorValue *SubmissionStatusError, result map[string]any) {
	if errorValue == nil || errorValue.Code != string(SubmissionErrorRequiresResubmission) {
		return
	}
	issues, truncated := submissionResubmissionIssues(result)
	errorValue.ResubmissionIssues = issues
	errorValue.ResubmissionIssuesTruncated = truncated
}

func submissionResubmissionIssues(result map[string]any) ([]SubmissionResubmissionIssue, bool) {
	rawIssues := resultArray(result, "resubmission_issues")
	truncated, _ := result["resubmission_issues_truncated"].(bool)
	if len(rawIssues) > submissionStatusMaxResubmissionIssues {
		truncated = true
	}
	issues := make([]SubmissionResubmissionIssue, 0, min(len(rawIssues), submissionStatusMaxResubmissionIssues))
	for index, rawIssue := range rawIssues {
		if index >= submissionStatusMaxResubmissionIssues {
			break
		}
		issueMap, ok := rawIssue.(map[string]any)
		if !ok {
			truncated = true
			continue
		}
		code, codeTruncated := boundedSubmissionIssueString(issueMap["code"], submissionStatusMaxIssueCodeLength)
		relationshipRef, refTruncated := boundedSubmissionIssueString(issueMap["relationship_ref"], submissionStatusMaxIssueRefLength)
		component, componentTruncated := boundedSubmissionIssueString(issueMap["component"], submissionStatusMaxIssueComponentLen)
		message, messageTruncated := boundedSubmissionIssueString(issueMap["message"], submissionStatusMaxIssueMessageLength)
		truncated = truncated || codeTruncated || refTruncated || componentTruncated || messageTruncated
		if code == "" || message == "" {
			truncated = true
			continue
		}
		issues = append(issues, SubmissionResubmissionIssue{
			Code:            code,
			RelationshipRef: relationshipRef,
			Component:       component,
			Message:         message,
		})
	}
	return issues, truncated
}

func boundedSubmissionIssueString(raw any, maxLength int) (string, bool) {
	value, ok := raw.(string)
	if !ok {
		return "", raw != nil
	}
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxLength {
		return value, false
	}
	return string(runes[:maxLength]), true
}
