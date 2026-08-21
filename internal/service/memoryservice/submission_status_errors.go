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
	SubmissionErrorSemanticHold          SubmissionErrorCode = "submission_semantic_hold"
	SubmissionErrorPolicyRejected        SubmissionErrorCode = "submission_policy_rejected"
	SubmissionErrorAssessorInvalid       SubmissionErrorCode = "assessor_response_invalid"
	SubmissionErrorAssessorUnavailable   SubmissionErrorCode = "assessor_unavailable"
	SubmissionErrorReplacementConflict   SubmissionErrorCode = "submission_replacement_conflict"
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
	SubmissionErrorSemanticHold,
	SubmissionErrorPolicyRejected,
	SubmissionErrorAssessorInvalid,
	SubmissionErrorAssessorUnavailable,
	SubmissionErrorReplacementConflict,
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
	Code        string `json:"code"`
	Message     string `json:"message"`
	Retryable   bool   `json:"retryable"`
	NextAction  string `json:"next_action"`
	Remediation string `json:"remediation"`
}

type SubmissionNextAction string

const (
	SubmissionNextActionPollStatus         SubmissionNextAction = "poll_status"
	SubmissionNextActionResubmitSubmission SubmissionNextAction = "resubmit_submission"
	SubmissionNextActionSubmitReplacement  SubmissionNextAction = "submit_replacement"
	SubmissionNextActionRetryCorrection    SubmissionNextAction = "retry_correction"
	SubmissionNextActionContactOperator    SubmissionNextAction = "contact_operator"
	SubmissionNextActionNone               SubmissionNextAction = "none"
)

var submissionNextActions = []SubmissionNextAction{
	SubmissionNextActionPollStatus,
	SubmissionNextActionResubmitSubmission,
	SubmissionNextActionSubmitReplacement,
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
	SubmissionErrorSemanticHold:          "submission was rejected by semantic hold policy",
	SubmissionErrorPolicyRejected:        "submission was rejected by semantic placement policy",
	SubmissionErrorAssessorInvalid:       "submission assessment returned an invalid response",
	SubmissionErrorAssessorUnavailable:   "submission assessment was unavailable after bounded retries",
	SubmissionErrorReplacementConflict:   "submission replacement conflicted with current state",
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
	case SubmissionErrorSemanticHold:
		return true, SubmissionNextActionSubmitReplacement
	case SubmissionErrorContractSuperseded, SubmissionErrorReplacementConflict:
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
	case SubmissionNextActionSubmitReplacement:
		return "Call remember with a complete corrected batch and replaces_submission_id set to this submission_id."
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
	case stage == "replacement_conflict":
		return SubmissionErrorReplacementConflict
	case class == "malformed_response", class == "malformed_exhausted", class == "input_budget", class == "validation_failed", class == "provider_protocol":
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
	if processing == "awaiting_review" {
		return nil
	}
	if item.Status != string(domain.PlacementRunFailed) && item.Status != "failed" && item.Status != "rejected" && item.Status != "awaiting_review" {
		return nil
	}
	stage, _ := item.Result["failure_stage"].(string)
	class, _ := item.Result["failure_class"].(string)
	if (item.Status == "rejected" || item.Status == "awaiting_review") && strings.TrimSpace(stage) == "" && strings.TrimSpace(class) == "" {
		return nil
	}
	errorValue := submissionStatusError(submissionFailureCode(stage, class))
	return &errorValue
}
