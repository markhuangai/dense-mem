package memoryservice

import (
	"strings"

	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

// Status projection policy lives in the Remember application boundary. These
// aliases keep the broader memory-service package wired to the same contract.
type SubmissionErrorCode = rememberapp.SubmissionErrorCode
type SubmissionStatusError = rememberapp.SubmissionStatusError
type SubmissionNextAction = rememberapp.SubmissionNextAction

const submissionStatusMaxIssueMessageLength = 512

const (
	SubmissionErrorStaleInput = rememberapp.SubmissionErrorStaleInput

	SubmissionErrorProviderUnavailable      = rememberapp.SubmissionErrorProviderUnavailable
	SubmissionErrorProviderResponseInvalid  = rememberapp.SubmissionErrorProviderResponseInvalid
	SubmissionErrorInputBudgetExceeded      = rememberapp.SubmissionErrorInputBudgetExceeded
	SubmissionErrorConfigurationInvalid     = rememberapp.SubmissionErrorConfigurationInvalid
	SubmissionErrorIdempotencyConflict      = rememberapp.SubmissionErrorIdempotencyConflict
	SubmissionErrorEmbeddingUnavailable     = rememberapp.SubmissionErrorEmbeddingUnavailable
	SubmissionErrorEmbeddingResponseInvalid = rememberapp.SubmissionErrorEmbeddingResponseInvalid
	SubmissionErrorCommitConflict           = rememberapp.SubmissionErrorCommitConflict
	SubmissionErrorDatabaseFailure          = rememberapp.SubmissionErrorDatabaseFailure
	SubmissionErrorRequestTimeout           = rememberapp.SubmissionErrorRequestTimeout
	SubmissionErrorRequestCancelled         = rememberapp.SubmissionErrorRequestCancelled
	SubmissionErrorInternalFailure          = rememberapp.SubmissionErrorInternalFailure

	SubmissionErrorPolicyRejected      = rememberapp.SubmissionErrorPolicyRejected
	SubmissionErrorAssessorInvalid     = rememberapp.SubmissionErrorAssessorInvalid
	SubmissionErrorAssessorUnavailable = rememberapp.SubmissionErrorAssessorUnavailable
	SubmissionErrorProcessingFailed    = rememberapp.SubmissionErrorProcessingFailed

	SubmissionErrorRelationshipVersionStale      = rememberapp.SubmissionErrorRelationshipVersionStale
	SubmissionErrorRelationshipNotActive         = rememberapp.SubmissionErrorRelationshipNotActive
	SubmissionErrorObjectKindChangeForbidden     = rememberapp.SubmissionErrorObjectKindChangeForbidden
	SubmissionErrorSupportSetMismatch            = rememberapp.SubmissionErrorSupportSetMismatch
	SubmissionErrorEntityNotFound                = rememberapp.SubmissionErrorEntityNotFound
	SubmissionErrorTooManyEntityCandidates       = rememberapp.SubmissionErrorTooManyEntityCandidates
	SubmissionErrorPredicateNotFound             = rememberapp.SubmissionErrorPredicateNotFound
	SubmissionErrorPredicateSubjectKindMismatch  = rememberapp.SubmissionErrorPredicateSubjectKindMismatch
	SubmissionErrorPredicateObjectKindMismatch   = rememberapp.SubmissionErrorPredicateObjectKindMismatch
	SubmissionErrorNoChange                      = rememberapp.SubmissionErrorNoChange
	SubmissionErrorConfirmationExpired           = rememberapp.SubmissionErrorConfirmationExpired
	SubmissionErrorRelationshipChanged           = rememberapp.SubmissionErrorRelationshipChanged
	SubmissionErrorSupportSetChanged             = rememberapp.SubmissionErrorSupportSetChanged
	SubmissionErrorPersistentAmbiguity           = rememberapp.SubmissionErrorPersistentAmbiguity
	SubmissionErrorInactiveRelationshipCollision = rememberapp.SubmissionErrorInactiveRelationshipCollision

	SubmissionNextActionRetrySameRequest = rememberapp.SubmissionNextActionRetrySameRequest
	SubmissionNextActionResubmitRemember = rememberapp.SubmissionNextActionResubmitRemember
	SubmissionNextActionRetryCorrection  = rememberapp.SubmissionNextActionRetryCorrection
	SubmissionNextActionContactOperator  = rememberapp.SubmissionNextActionContactOperator
	SubmissionNextActionNone             = rememberapp.SubmissionNextActionNone
)

func SubmissionErrorCodes() []string {
	return rememberapp.SubmissionErrorCodes()
}
func SubmissionNextActions() []string {
	return rememberapp.SubmissionNextActions()
}

func submissionStatusError(code SubmissionErrorCode) SubmissionStatusError {
	return rememberapp.StatusError(code)
}

func submissionStatusErrorForCode(rawCode string, fallbackState string) SubmissionStatusError {
	return rememberapp.StatusErrorForCode(rawCode, fallbackState)
}

func correctionStatusErrorForCode(rawCode string, fallbackState string) SubmissionStatusError {
	if strings.TrimSpace(rawCode) == string(SubmissionErrorPolicyRejected) ||
		(strings.TrimSpace(rawCode) == "" && strings.TrimSpace(fallbackState) == "rejected") {
		return submissionStatusError(SubmissionErrorPolicyRejected)
	}
	return submissionStatusErrorForCode(rawCode, fallbackState)
}

func submissionFailureCode(stage, class string) SubmissionErrorCode {
	return rememberapp.FailureCode(stage, class)
}
