package service

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// SubmissionErrorCode is the closed public vocabulary for terminal submission
// failures. Internal/provider/database reasons must be translated into this
// set before they cross the status projection boundary.
type SubmissionErrorCode string

const (
	SubmissionErrorStaleInput SubmissionErrorCode = "stale_input"

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

	// Policy rejection is a canonical v2.6.2 Remember failure. The remaining
	// names are compatibility aliases used by correction/internal callers.
	SubmissionErrorPolicyRejected      SubmissionErrorCode = "submission_policy_rejected"
	SubmissionErrorAssessorInvalid     SubmissionErrorCode = SubmissionErrorProviderResponseInvalid
	SubmissionErrorAssessorUnavailable SubmissionErrorCode = SubmissionErrorProviderUnavailable
	SubmissionErrorProcessingFailed    SubmissionErrorCode = SubmissionErrorInternalFailure

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
	Code        string         `json:"code"`
	Message     string         `json:"message"`
	Retryable   bool           `json:"retryable"`
	NextAction  string         `json:"next_action"`
	Remediation string         `json:"remediation"`
	ReasonCode  string         `json:"reason_code,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
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
	SubmissionErrorStaleInput:               "A source revision or exact reference changed while Remember was processing this request.",
	SubmissionErrorProviderUnavailable:      "Remember could not reach the required assessment service or obtain a usable response. This does not mean the submitted evidence is false.",
	SubmissionErrorProviderResponseInvalid:  "The assessment service did not return a complete response that satisfied the required schema and validation checks.",
	SubmissionErrorInputBudgetExceeded:      "The evidence, proposals, or assessment context exceeded a configured processing limit.",
	SubmissionErrorConfigurationInvalid:     "A required assessment or search provider is not configured correctly, so Dense-Mem cannot complete this operation.",
	SubmissionErrorIdempotencyConflict:      "This idempotency_key was already used for a different request. The new request was not applied.",
	SubmissionErrorEmbeddingUnavailable:     "The required embedding service could not produce the vectors needed to complete this operation.",
	SubmissionErrorEmbeddingResponseInvalid: "The embedding service returned missing, incorrectly ordered, or invalid vectors that could not be accepted.",
	SubmissionErrorCommitConflict:           "Server state changed between assessment and the attempted write, so this operation could not finish against the assessed state.",
	SubmissionErrorDatabaseFailure:          "Dense-Mem could not confirm that the operation was saved. The stored outcome must be checked by retrying the original request.",
	SubmissionErrorRequestTimeout:           "The operation did not finish within its allowed time. Its final stored outcome may require an idempotent retry to confirm.",
	SubmissionErrorRequestCancelled:         "The caller cancelled this operation before a terminal response could be confirmed.",
	SubmissionErrorInternalFailure:          "Dense-Mem could not complete this operation, and the public result does not establish a more specific cause.",
	SubmissionErrorPolicyRejected:           "A security check rejected text in the submitted batch. The batch was not accepted as memory.",

	SubmissionErrorRelationshipVersionStale:      "The relationship's current version does not match expected_version in your request, so the correction was not applied.",
	SubmissionErrorRelationshipNotActive:         "The selected relationship is not currently active, supported, and canonical, so it cannot be corrected.",
	SubmissionErrorObjectKindChangeForbidden:     "The correction would replace a typed Value with an Entity, which this correction operation does not permit.",
	SubmissionErrorSupportSetMismatch:            "The supplied supports do not exactly match the relationship's current effective evidence spans, so the correction was not applied.",
	SubmissionErrorEntityNotFound:                "A referenced corrected Entity is not available in your authorized context. Its existence elsewhere is not disclosed.",
	SubmissionErrorTooManyEntityCandidates:       "The corrected Entity name matches more candidates than can be offered for selection.",
	SubmissionErrorPredicateNotFound:             "The requested predicate is not available as an active registered predicate in your team.",
	SubmissionErrorPredicateSubjectKindMismatch:  "The registered predicate does not permit the corrected subject Entity kind.",
	SubmissionErrorPredicateObjectKindMismatch:   "The registered predicate does not permit the corrected object Entity or Value kind.",
	SubmissionErrorNoChange:                      "The proposed correction has the same effective relationship content as the current relationship, so no correction is needed.",
	SubmissionErrorConfirmationExpired:           "The correction confirmation expired before it was submitted, so that confirmation cannot apply the correction.",
	SubmissionErrorRelationshipChanged:           "The relationship changed after correction confirmation was requested, so that confirmation no longer describes current state.",
	SubmissionErrorSupportSetChanged:             "The effective evidence supports changed after correction confirmation was requested, so those supports must be refreshed.",
	SubmissionErrorPersistentAmbiguity:           "The selected Entity candidate is no longer available for this correction; a new candidate selection is required.",
	SubmissionErrorInactiveRelationshipCollision: "The proposed correction matches an inactive or unsupported historical relationship and cannot create an active duplicate.",
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
		Remediation: submissionErrorRemediationForCode(code, nextAction),
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

// StatusErrorWithDetails adds bounded, server-safe context while retaining the
// canonical recovery policy, with operator guidance for server-owned input
// budget failures.
func StatusErrorWithDetails(code SubmissionErrorCode, reasonCode string, details map[string]any) SubmissionStatusError {
	result := submissionStatusError(code)
	result.ReasonCode = boundedStatusErrorText(reasonCode, 128)
	result.Details = boundedStatusErrorDetails(details)
	applyServerOwnedInputBudgetGuidance(result.Code, &result)
	applySubmissionDiagnosticGuidance(&result)
	return result
}

const serverOwnedInputBudgetRemediation = "Ask an operator to review the configured assessor budget and server-owned context before retrying."

func applyServerOwnedInputBudgetGuidance(code string, result *SubmissionStatusError) {
	if result == nil || code != string(SubmissionErrorInputBudgetExceeded) || !statusErrorServerOwned(result.Details) {
		return
	}
	result.Retryable = false
	result.NextAction = string(SubmissionNextActionContactOperator)
	result.Remediation = serverOwnedInputBudgetRemediation
}

func statusErrorServerOwned(details map[string]any) bool {
	owned, _ := details["server_owned"].(bool)
	return owned
}

func boundedStatusErrorText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	return string([]rune(value)[:maxRunes])
}

func boundedStatusErrorDetails(details map[string]any) map[string]any {
	if len(details) == 0 {
		return nil
	}
	keys := make([]string, 0, len(details))
	for key := range details {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make(map[string]any, min(len(keys), 20))
	selected := 0
	for _, originalKey := range keys {
		key := boundedStatusErrorText(originalKey, 128)
		if key == "" {
			continue
		}
		if selected >= 20 {
			break
		}
		selected++
		if key == "issues" {
			issues, truncated := boundedSubmissionDiagnosticIssues(details[originalKey])
			if len(issues) > 0 {
				result[key] = issues
			}
			if truncated {
				result["issues_truncated"] = true
			}
			continue
		}
		if key == "issues_truncated" {
			value, _ := details[originalKey].(bool)
			previous, _ := result[key].(bool)
			result[key] = value || previous
			continue
		}
		switch value := details[originalKey].(type) {
		case string:
			result[key] = boundedStatusErrorText(value, 512)
		case int:
			result[key] = value
		case int64:
			result[key] = value
		case bool:
			result[key] = value
		case float64:
			result[key] = value
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func submissionErrorGuidance(code SubmissionErrorCode) (bool, SubmissionNextAction) {
	switch code {
	case SubmissionErrorProviderUnavailable, SubmissionErrorProviderResponseInvalid,
		SubmissionErrorEmbeddingUnavailable, SubmissionErrorEmbeddingResponseInvalid,
		SubmissionErrorCommitConflict, SubmissionErrorDatabaseFailure,
		SubmissionErrorRequestTimeout, SubmissionErrorRequestCancelled,
		SubmissionErrorInternalFailure:
		return true, SubmissionNextActionRetrySameRequest
	case SubmissionErrorPolicyRejected, SubmissionErrorStaleInput,
		SubmissionErrorIdempotencyConflict, SubmissionErrorInputBudgetExceeded:
		return false, SubmissionNextActionResubmitRemember
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
		return submissionStatusError(SubmissionErrorPolicyRejected)
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
	case stage == "entity_catalog" || stage == "known_evidence_context" || stage == "catalog_context" ||
		stage == "catalog_context_validation" || stage == "predicate_context" ||
		stage == "predicate_options_overflow" || stage == "assessment_input" || stage == "assessment_budget" ||
		stage == "provider_framing":
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
