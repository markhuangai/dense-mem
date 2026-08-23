package memoryservice

import (
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

// Status projection policy lives in the Remember application boundary. These
// aliases and adapters preserve the legacy package's public and worker-facing
// names during the capability migration.
type SubmissionErrorCode = rememberapp.SubmissionErrorCode
type SubmissionStatusError = rememberapp.SubmissionStatusError
type SubmissionResubmissionIssue = rememberapp.SubmissionResubmissionIssue
type SubmissionNextAction = rememberapp.SubmissionNextAction

const submissionStatusMaxIssueMessageLength = 512

const (
	SubmissionErrorRequiresResubmission  = rememberapp.SubmissionErrorRequiresResubmission
	SubmissionErrorNormalizationFailed   = rememberapp.SubmissionErrorNormalizationFailed
	SubmissionErrorNormalizerUnavailable = rememberapp.SubmissionErrorNormalizerUnavailable
	SubmissionErrorPolicyRejected        = rememberapp.SubmissionErrorPolicyRejected
	SubmissionErrorAssessorInvalid       = rememberapp.SubmissionErrorAssessorInvalid
	SubmissionErrorAssessorUnavailable   = rememberapp.SubmissionErrorAssessorUnavailable
	SubmissionErrorProcessingFailed      = rememberapp.SubmissionErrorProcessingFailed
	SubmissionErrorContractSuperseded    = rememberapp.SubmissionErrorContractSuperseded
	SubmissionErrorSearchIndexingDelayed = rememberapp.SubmissionErrorSearchIndexingDelayed
	SubmissionErrorQuarantined           = rememberapp.SubmissionErrorQuarantined

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

	SubmissionNextActionPollStatus         = rememberapp.SubmissionNextActionPollStatus
	SubmissionNextActionResubmitSubmission = rememberapp.SubmissionNextActionResubmitSubmission
	SubmissionNextActionRetryCorrection    = rememberapp.SubmissionNextActionRetryCorrection
	SubmissionNextActionContactOperator    = rememberapp.SubmissionNextActionContactOperator
	SubmissionNextActionNone               = rememberapp.SubmissionNextActionNone
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

func submissionFailureCode(stage, class string) SubmissionErrorCode {
	return rememberapp.FailureCode(stage, class)
}

func submissionItemFailureError(item repository.PlacementItem, processing string) *SubmissionStatusError {
	return rememberapp.ItemFailureError(rememberapp.PlacementItem{
		FragmentID: item.FragmentID, EvidenceIndex: item.EvidenceIndex, Status: item.Status, Result: item.Result,
	}, processing)
}
