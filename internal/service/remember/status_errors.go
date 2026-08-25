package remember

import (
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// SubmissionErrorCode is the closed public vocabulary for terminal submission
// failures. Internal/provider/database reasons must be translated into this
// set before they cross the status projection boundary.
type SubmissionErrorCode string

const (
	// Remember semantic rejection is intentionally limited to these two codes.
	SubmissionErrorNoSupportedMemory SubmissionErrorCode = "no_supported_memory"
	SubmissionErrorStaleInput        SubmissionErrorCode = "stale_input"

	SubmissionErrorProviderUnavailable      SubmissionErrorCode = "provider_unavailable"
	SubmissionErrorProviderResponseInvalid  SubmissionErrorCode = "provider_response_invalid"
	SubmissionErrorInputBudgetExceeded      SubmissionErrorCode = "input_budget_exceeded"
	SubmissionErrorConfigurationInvalid     SubmissionErrorCode = "configuration_invalid"
	SubmissionErrorIdempotencyConflict      SubmissionErrorCode = "idempotency_conflict"
	SubmissionErrorEmbeddingUnavailable     SubmissionErrorCode = "embedding_unavailable"
	SubmissionErrorEmbeddingResponseInvalid SubmissionErrorCode = "embedding_response_invalid"
	SubmissionErrorCommitConflict           SubmissionErrorCode = "commit_conflict"
	SubmissionErrorDatabaseFailure          SubmissionErrorCode = "database_failure"
	SubmissionErrorRequestTimeout           SubmissionErrorCode = "request_timeout"
	SubmissionErrorRequestCancelled         SubmissionErrorCode = "request_cancelled"
	SubmissionErrorInternalFailure          SubmissionErrorCode = "internal_failure"

	// These aliases are retained for correction and internal call sites; the
	// v2.6 Remember status projection emits the canonical codes above.
	SubmissionErrorPolicyRejected        SubmissionErrorCode = "submission_policy_rejected"
	SubmissionErrorAssessorInvalid       SubmissionErrorCode = SubmissionErrorProviderResponseInvalid
	SubmissionErrorAssessorUnavailable   SubmissionErrorCode = SubmissionErrorProviderUnavailable
	SubmissionErrorProcessingFailed      SubmissionErrorCode = SubmissionErrorInternalFailure
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
	SubmissionErrorNoSupportedMemory,
	SubmissionErrorStaleInput,
	SubmissionErrorProviderUnavailable,
	SubmissionErrorProviderResponseInvalid,
	SubmissionErrorInputBudgetExceeded,
	SubmissionErrorConfigurationInvalid,
	SubmissionErrorIdempotencyConflict,
	SubmissionErrorEmbeddingUnavailable,
	SubmissionErrorEmbeddingResponseInvalid,
	SubmissionErrorCommitConflict,
	SubmissionErrorDatabaseFailure,
	SubmissionErrorRequestTimeout,
	SubmissionErrorRequestCancelled,
	SubmissionErrorInternalFailure,
	SubmissionErrorSearchIndexingDelayed,
	SubmissionErrorQuarantined,
	SubmissionErrorPolicyRejected,
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
	SubmissionNextActionRetrySameRequest SubmissionNextAction = "retry_same_request"
	SubmissionNextActionResubmitRemember SubmissionNextAction = "resubmit_remember"
	SubmissionNextActionRetryCorrection  SubmissionNextAction = "retry_correction"
	SubmissionNextActionContactOperator  SubmissionNextAction = "contact_operator"
	SubmissionNextActionNone             SubmissionNextAction = "none"
)

var submissionNextActions = []SubmissionNextAction{
	SubmissionNextActionRetrySameRequest,
	SubmissionNextActionResubmitRemember,
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
	SubmissionErrorNoSupportedMemory:        "no supported memory could be stored from this submission",
	SubmissionErrorStaleInput:               "an exact client-owned input changed before commit",
	SubmissionErrorProviderUnavailable:      "the semantic assessor was unavailable",
	SubmissionErrorProviderResponseInvalid:  "the semantic assessor returned an invalid response",
	SubmissionErrorInputBudgetExceeded:      "the semantic assessor input exceeded the configured budget",
	SubmissionErrorConfigurationInvalid:     "Dense-Mem is missing valid semantic-assessor configuration",
	SubmissionErrorIdempotencyConflict:      "the idempotency key is already bound to a different request",
	SubmissionErrorEmbeddingUnavailable:     "the embedding provider was unavailable",
	SubmissionErrorEmbeddingResponseInvalid: "the embedding provider returned an invalid response",
	SubmissionErrorCommitConflict:           "server-owned state changed before commit",
	SubmissionErrorDatabaseFailure:          "Dense-Mem could not persist the submission",
	SubmissionErrorRequestTimeout:           "the bounded Remember request deadline was reached",
	SubmissionErrorRequestCancelled:         "the Remember request was cancelled before commit",
	SubmissionErrorInternalFailure:          "Dense-Mem could not complete the submission",
	SubmissionErrorPolicyRejected:           "submission was rejected by semantic placement policy",
	SubmissionErrorSearchIndexingDelayed:    "search indexing is delayed",
	SubmissionErrorQuarantined:              "submission was quarantined by security policy",

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
		code = SubmissionErrorInternalFailure
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

// StatusError creates a bounded public status error from the closed code set.
func StatusError(code SubmissionErrorCode) SubmissionStatusError {
	return submissionStatusError(code)
}

func submissionStatusErrorWithMessage(code SubmissionErrorCode, message string) SubmissionStatusError {
	result := submissionStatusError(code)
	result.Message = message
	return result
}

// StatusErrorWithMessage creates a bounded status error with a safe message
// override used for projection-specific guidance.
func StatusErrorWithMessage(code SubmissionErrorCode, message string) SubmissionStatusError {
	return submissionStatusErrorWithMessage(code, message)
}

func submissionErrorGuidance(code SubmissionErrorCode) (bool, SubmissionNextAction) {
	switch code {
	case SubmissionErrorProviderUnavailable, SubmissionErrorProviderResponseInvalid,
		SubmissionErrorEmbeddingUnavailable, SubmissionErrorEmbeddingResponseInvalid,
		SubmissionErrorCommitConflict, SubmissionErrorDatabaseFailure,
		SubmissionErrorRequestTimeout, SubmissionErrorRequestCancelled,
		SubmissionErrorInternalFailure:
		return true, SubmissionNextActionRetrySameRequest
	case SubmissionErrorNoSupportedMemory, SubmissionErrorStaleInput,
		SubmissionErrorIdempotencyConflict:
		return true, SubmissionNextActionResubmitRemember
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
	case SubmissionNextActionRetrySameRequest:
		return "Retry the same request with the same idempotency_key after the transient failure clears."
	case SubmissionNextActionResubmitRemember:
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
		return submissionStatusError(SubmissionErrorNoSupportedMemory)
	}
	return submissionStatusError(SubmissionErrorInternalFailure)
}

// StatusErrorForCode translates stored failure metadata into the closed public
// status error vocabulary.
func StatusErrorForCode(rawCode string, fallbackState string) SubmissionStatusError {
	return submissionStatusErrorForCode(rawCode, fallbackState)
}

func submissionFailureCode(stage, class string) SubmissionErrorCode {
	stage = strings.TrimSpace(stage)
	class = strings.TrimSpace(class)
	switch {
	case stage == "contract_superseded":
		return SubmissionErrorInternalFailure
	case stage == "input_budget" || stage == "input_budget_exceeded" || class == "input_budget":
		return SubmissionErrorInputBudgetExceeded
	case stage == "entity_catalog" || stage == "catalog_context" ||
		stage == "predicate_options_overflow" || stage == "assessment_input" || stage == "assessment_budget":
		return SubmissionErrorInputBudgetExceeded
	case stage == "configuration" || stage == "configuration_invalid":
		return SubmissionErrorConfigurationInvalid
	case stage == "database" || stage == "database_failure" || class == "database_failure":
		return SubmissionErrorDatabaseFailure
	case stage == "assessment" && class == "malformed_exhausted",
		class == "malformed_response", class == "validation_failed", class == "provider_protocol", class == "request_invalid":
		return SubmissionErrorProviderResponseInvalid
	case class == "timeout", class == "rate_limited", class == "http_4xx", class == "http_5xx",
		class == "http_unexpected", class == "transport", class == "provider_unavailable":
		return SubmissionErrorProviderUnavailable
	default:
		return SubmissionErrorInternalFailure
	}
}

// FailureCode translates bounded internal stage/class values into a public
// status code.
func FailureCode(stage, class string) SubmissionErrorCode {
	return submissionFailureCode(stage, class)
}

func submissionItemFailureError(item PlacementItem, processing string) *SubmissionStatusError {
	if item.Status != string(domain.PlacementRunFailed) && item.Status != "failed" && item.Status != "rejected" {
		return nil
	}
	stage, _ := item.Result["failure_stage"].(string)
	class, _ := item.Result["failure_class"].(string)
	code, _ := item.Result["failure_code"].(string)
	if item.Status == "rejected" && strings.TrimSpace(stage) == "" && strings.TrimSpace(class) == "" && strings.TrimSpace(code) == "" {
		return nil
	}
	if strings.TrimSpace(code) != "" {
		errorValue := submissionStatusErrorForCode(code, processing)
		return &errorValue
	}
	errorValue := submissionStatusError(submissionFailureCode(stage, class))
	return &errorValue
}

// ItemFailureError projects one stored placement item into a bounded public
// status error.
func ItemFailureError(item PlacementItem, processing string) *SubmissionStatusError {
	return submissionItemFailureError(item, processing)
}
