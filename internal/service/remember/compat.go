// Package remember preserves the historical Remember application import path
// while the live pipeline is owned by internal/remember/service.
package remember

import rememberservice "github.com/markhuangai/dense-mem/internal/remember/service"

type (
	RememberValidationIssue      = rememberservice.RememberValidationIssue
	RememberValidationError      = rememberservice.RememberValidationError
	EvidenceInput                = rememberservice.EvidenceInput
	SecurityEventDraft           = rememberservice.SecurityEventDraft
	SecuritySignalInput          = rememberservice.SecuritySignalInput
	EvidenceFragment             = rememberservice.EvidenceFragment
	SubmissionRelationshipSplit  = rememberservice.SubmissionRelationshipSplit
	SubmissionRelationshipResult = rememberservice.SubmissionRelationshipResult
	SecurityRejectionAuditor     = rememberservice.SecurityRejectionAuditor
	SecurityRejectionAuditInput  = rememberservice.SecurityRejectionAuditInput
	SecurityRejectionAuditSignal = rememberservice.SecurityRejectionAuditSignal
	SubmissionAssessmentCatalog  = rememberservice.SubmissionAssessmentCatalog

	RememberPhase                 = rememberservice.RememberPhase
	RememberDeadlines             = rememberservice.RememberDeadlines
	SubmissionSecurityError       = rememberservice.SubmissionSecurityError
	SubmissionSecuritySignal      = rememberservice.SubmissionSecuritySignal
	SubmissionSecurityScan        = rememberservice.SubmissionSecurityScan
	SubmissionSecurityBatchSignal = rememberservice.SubmissionSecurityBatchSignal
	SubmissionSecurityBatchScan   = rememberservice.SubmissionSecurityBatchScan

	Service                         = rememberservice.Service
	RememberService                 = rememberservice.RememberService
	Dependencies                    = rememberservice.Dependencies
	RememberRequest                 = rememberservice.RememberRequest
	RememberEvidenceInput           = rememberservice.RememberEvidenceInput
	RememberResult                  = rememberservice.RememberResult
	SubmissionStatusResult          = rememberservice.SubmissionStatusResult
	SubmissionAwaitingConfirmation  = rememberservice.SubmissionAwaitingConfirmation
	RelationshipCorrectionCandidate = rememberservice.RelationshipCorrectionCandidate
	RelationshipCorrectionResult    = rememberservice.RelationshipCorrectionResult
	SubmissionEvidenceStatus        = rememberservice.SubmissionEvidenceStatus

	SubmissionErrorCode   = rememberservice.SubmissionErrorCode
	SubmissionStatusError = rememberservice.SubmissionStatusError
	SubmissionNextAction  = rememberservice.SubmissionNextAction

	DiagnosticCapture       = rememberservice.DiagnosticCapture
	ResultKind              = rememberservice.ResultKind
	SynchronousProcessor    = rememberservice.SynchronousProcessor
	RememberProcessError    = rememberservice.RememberProcessError
	RememberProcessRequest  = rememberservice.RememberProcessRequest
	TerminalRememberResult  = rememberservice.TerminalRememberResult
	TerminalEvidenceResult  = rememberservice.TerminalEvidenceResult
	TerminalProcessingState = rememberservice.TerminalProcessingState
	TerminalSearchState     = rememberservice.TerminalSearchState
	TerminalErrorCode       = rememberservice.TerminalErrorCode
	TerminalNextAction      = rememberservice.TerminalNextAction

	SynchronousAssessmentDependencies = rememberservice.SynchronousAssessmentDependencies
	RememberAssessmentScope           = rememberservice.RememberAssessmentScope
	RememberAssessmentItem            = rememberservice.RememberAssessmentItem
	RememberAssessmentSnapshot        = rememberservice.RememberAssessmentSnapshot
	SynchronousAssessmentInput        = rememberservice.SynchronousAssessmentInput
	SynchronousAssessmentResult       = rememberservice.SynchronousAssessmentResult
	SynchronousRememberCommitRequest  = rememberservice.SynchronousRememberCommitRequest
)

const (
	RememberPhaseAssessment          = rememberservice.RememberPhaseAssessment
	RememberPhaseEmbedding           = rememberservice.RememberPhaseEmbedding
	RememberPhaseCommit              = rememberservice.RememberPhaseCommit
	RememberTotalBudget              = rememberservice.RememberTotalBudget
	RememberAssessmentBudget         = rememberservice.RememberAssessmentBudget
	RememberEmbeddingBudget          = rememberservice.RememberEmbeddingBudget
	RememberCommitBudget             = rememberservice.RememberCommitBudget
	RememberFailurePersistenceBudget = rememberservice.RememberFailurePersistenceBudget

	SubmissionSecurityErrorEncodedEvidence = rememberservice.SubmissionSecurityErrorEncodedEvidence
	SubmissionSecurityErrorRejected        = rememberservice.SubmissionSecurityErrorRejected
	SecuritySourceEvidence                 = rememberservice.SecuritySourceEvidence
	SecuritySourceProposal                 = rememberservice.SecuritySourceProposal

	SubmissionErrorStaleInput                    = rememberservice.SubmissionErrorStaleInput
	SubmissionErrorProviderUnavailable           = rememberservice.SubmissionErrorProviderUnavailable
	SubmissionErrorProviderResponseInvalid       = rememberservice.SubmissionErrorProviderResponseInvalid
	SubmissionErrorInputBudgetExceeded           = rememberservice.SubmissionErrorInputBudgetExceeded
	SubmissionErrorConfigurationInvalid          = rememberservice.SubmissionErrorConfigurationInvalid
	SubmissionErrorIdempotencyConflict           = rememberservice.SubmissionErrorIdempotencyConflict
	SubmissionErrorEmbeddingUnavailable          = rememberservice.SubmissionErrorEmbeddingUnavailable
	SubmissionErrorEmbeddingResponseInvalid      = rememberservice.SubmissionErrorEmbeddingResponseInvalid
	SubmissionErrorCommitConflict                = rememberservice.SubmissionErrorCommitConflict
	SubmissionErrorDatabaseFailure               = rememberservice.SubmissionErrorDatabaseFailure
	SubmissionErrorRequestTimeout                = rememberservice.SubmissionErrorRequestTimeout
	SubmissionErrorRequestCancelled              = rememberservice.SubmissionErrorRequestCancelled
	SubmissionErrorInternalFailure               = rememberservice.SubmissionErrorInternalFailure
	SubmissionErrorPolicyRejected                = rememberservice.SubmissionErrorPolicyRejected
	SubmissionErrorAssessorInvalid               = rememberservice.SubmissionErrorAssessorInvalid
	SubmissionErrorAssessorUnavailable           = rememberservice.SubmissionErrorAssessorUnavailable
	SubmissionErrorProcessingFailed              = rememberservice.SubmissionErrorProcessingFailed
	SubmissionErrorRelationshipVersionStale      = rememberservice.SubmissionErrorRelationshipVersionStale
	SubmissionErrorRelationshipNotActive         = rememberservice.SubmissionErrorRelationshipNotActive
	SubmissionErrorObjectKindChangeForbidden     = rememberservice.SubmissionErrorObjectKindChangeForbidden
	SubmissionErrorSupportSetMismatch            = rememberservice.SubmissionErrorSupportSetMismatch
	SubmissionErrorEntityNotFound                = rememberservice.SubmissionErrorEntityNotFound
	SubmissionErrorTooManyEntityCandidates       = rememberservice.SubmissionErrorTooManyEntityCandidates
	SubmissionErrorPredicateNotFound             = rememberservice.SubmissionErrorPredicateNotFound
	SubmissionErrorPredicateSubjectKindMismatch  = rememberservice.SubmissionErrorPredicateSubjectKindMismatch
	SubmissionErrorPredicateObjectKindMismatch   = rememberservice.SubmissionErrorPredicateObjectKindMismatch
	SubmissionErrorNoChange                      = rememberservice.SubmissionErrorNoChange
	SubmissionErrorConfirmationExpired           = rememberservice.SubmissionErrorConfirmationExpired
	SubmissionErrorRelationshipChanged           = rememberservice.SubmissionErrorRelationshipChanged
	SubmissionErrorSupportSetChanged             = rememberservice.SubmissionErrorSupportSetChanged
	SubmissionErrorPersistentAmbiguity           = rememberservice.SubmissionErrorPersistentAmbiguity
	SubmissionErrorInactiveRelationshipCollision = rememberservice.SubmissionErrorInactiveRelationshipCollision

	SubmissionNextActionRetrySameRequest = rememberservice.SubmissionNextActionRetrySameRequest
	SubmissionNextActionResubmitRemember = rememberservice.SubmissionNextActionResubmitRemember
	SubmissionNextActionRetryCorrection  = rememberservice.SubmissionNextActionRetryCorrection
	SubmissionNextActionContactOperator  = rememberservice.SubmissionNextActionContactOperator
	SubmissionNextActionNone             = rememberservice.SubmissionNextActionNone

	ResultKindTerminal                    = rememberservice.ResultKindTerminal
	TerminalProcessingCompleted           = rememberservice.TerminalProcessingCompleted
	TerminalProcessingFailed              = rememberservice.TerminalProcessingFailed
	TerminalSearchCurrent                 = rememberservice.TerminalSearchCurrent
	TerminalSearchNotRequired             = rememberservice.TerminalSearchNotRequired
	TerminalErrorPolicyRejected           = rememberservice.TerminalErrorPolicyRejected
	TerminalErrorStaleInput               = rememberservice.TerminalErrorStaleInput
	TerminalErrorProviderUnavailable      = rememberservice.TerminalErrorProviderUnavailable
	TerminalErrorProviderResponseInvalid  = rememberservice.TerminalErrorProviderResponseInvalid
	TerminalErrorInputBudgetExceeded      = rememberservice.TerminalErrorInputBudgetExceeded
	TerminalErrorConfigurationInvalid     = rememberservice.TerminalErrorConfigurationInvalid
	TerminalErrorIdempotencyConflict      = rememberservice.TerminalErrorIdempotencyConflict
	TerminalErrorEmbeddingUnavailable     = rememberservice.TerminalErrorEmbeddingUnavailable
	TerminalErrorEmbeddingResponseInvalid = rememberservice.TerminalErrorEmbeddingResponseInvalid
	TerminalErrorCommitConflict           = rememberservice.TerminalErrorCommitConflict
	TerminalErrorDatabaseFailure          = rememberservice.TerminalErrorDatabaseFailure
	TerminalErrorRequestTimeout           = rememberservice.TerminalErrorRequestTimeout
	TerminalErrorRequestCancelled         = rememberservice.TerminalErrorRequestCancelled
	TerminalErrorInternalFailure          = rememberservice.TerminalErrorInternalFailure
	TerminalNextActionRetrySameRequest    = rememberservice.TerminalNextActionRetrySameRequest
	TerminalNextActionResubmitRemember    = rememberservice.TerminalNextActionResubmitRemember
	TerminalNextActionRetryDreamFeedback  = rememberservice.TerminalNextActionRetryDreamFeedback
	TerminalNextActionRetryCorrection     = rememberservice.TerminalNextActionRetryCorrection
	TerminalNextActionContactOperator     = rememberservice.TerminalNextActionContactOperator
	TerminalNextActionNone                = rememberservice.TerminalNextActionNone
)

var (
	ErrIdempotencyConflict             = rememberservice.ErrIdempotencyConflict
	ErrSourceRevisionConflict          = rememberservice.ErrSourceRevisionConflict
	ErrTeamInactive                    = rememberservice.ErrTeamInactive
	ErrRememberAuthContext             = rememberservice.ErrRememberAuthContext
	ErrRememberConflict                = rememberservice.ErrRememberConflict
	ErrRememberPolicyRejected          = rememberservice.ErrRememberPolicyRejected
	ErrRememberStaleInput              = rememberservice.ErrRememberStaleInput
	ErrRememberPersistence             = rememberservice.ErrRememberPersistence
	ErrRememberProcessor               = rememberservice.ErrRememberProcessor
	ErrRememberProviderUnavailable     = rememberservice.ErrRememberProviderUnavailable
	ErrRememberProviderResponseInvalid = rememberservice.ErrRememberProviderResponseInvalid
	ErrRememberInputBudgetExceeded     = rememberservice.ErrRememberInputBudgetExceeded
	ErrRememberEmbeddingUnavailable    = rememberservice.ErrRememberEmbeddingUnavailable
	ErrRememberEmbeddingInvalid        = rememberservice.ErrRememberEmbeddingInvalid
	ErrRememberCommitConflict          = rememberservice.ErrRememberCommitConflict
	ErrRememberDatabaseFailure         = rememberservice.ErrRememberDatabaseFailure
	ErrRememberRequestTimeout          = rememberservice.ErrRememberRequestTimeout
	ErrRememberRequestCancelled        = rememberservice.ErrRememberRequestCancelled
	ErrEncodedEvidenceNotAllowed       = rememberservice.ErrEncodedEvidenceNotAllowed
	ErrEvidenceSecurityRejected        = rememberservice.ErrEvidenceSecurityRejected
)

var (
	NewService                                      = rememberservice.NewService
	CanonicalRequestBodyHash                        = rememberservice.CanonicalRequestBodyHash
	CanonicalLegacyRequestBodyHash                  = rememberservice.CanonicalLegacyRequestBodyHash
	WithRememberDeadlines                           = rememberservice.WithRememberDeadlines
	ContextForPhase                                 = rememberservice.ContextForPhase
	RecordSecurityRejectionAudit                    = rememberservice.RecordSecurityRejectionAudit
	ScanSubmissionEvidence                          = rememberservice.ScanSubmissionEvidence
	ScanSubmissionBatch                             = rememberservice.ScanSubmissionBatch
	ScanSubmissionWithProviderProposal              = rememberservice.ScanSubmissionWithProviderProposal
	SubmissionSecurityPassEvent                     = rememberservice.SubmissionSecurityPassEvent
	SubmissionSecurityBatchQuarantineEvent          = rememberservice.SubmissionSecurityBatchQuarantineEvent
	SubmissionSecurityQuarantineEventForSignals     = rememberservice.SubmissionSecurityQuarantineEventForSignals
	SubmissionErrorCodes                            = rememberservice.SubmissionErrorCodes
	SubmissionNextActions                           = rememberservice.SubmissionNextActions
	StatusError                                     = rememberservice.StatusError
	StatusErrorWithMessage                          = rememberservice.StatusErrorWithMessage
	StatusErrorWithDetails                          = rememberservice.StatusErrorWithDetails
	StatusErrorForCode                              = rememberservice.StatusErrorForCode
	FailureCode                                     = rememberservice.FailureCode
	NewDiagnosticCapture                            = rememberservice.NewDiagnosticCapture
	WithCallerResponseRequestContext                = rememberservice.WithCallerResponseRequestContext
	CallerResponseRequestContextFromContext         = rememberservice.CallerResponseRequestContextFromContext
	WithDiagnosticCapture                           = rememberservice.WithDiagnosticCapture
	DiagnosticCaptureFromContext                    = rememberservice.DiagnosticCaptureFromContext
	NormalizeTerminalCorrelationID                  = rememberservice.NormalizeTerminalCorrelationID
	TerminalErrorCodes                              = rememberservice.TerminalErrorCodes
	TerminalNextActions                             = rememberservice.TerminalNextActions
	TerminalStatusError                             = rememberservice.TerminalStatusError
	TerminalStatusErrorWithDetails                  = rememberservice.TerminalStatusErrorWithDetails
	IsTerminalErrorCode                             = rememberservice.IsTerminalErrorCode
	IsTerminalNextAction                            = rememberservice.IsTerminalNextAction
	ValidateTerminalStatusError                     = rememberservice.ValidateTerminalStatusError
	DreamFeedbackRetryRemediation                   = rememberservice.DreamFeedbackRetryRemediation
	ValidateTerminalRememberResult                  = rememberservice.ValidateTerminalRememberResult
	TerminalResultWithError                         = rememberservice.TerminalResultWithError
	SynchronousAssessmentProviderTurns              = rememberservice.SynchronousAssessmentProviderTurns
	BuildSynchronousRememberCommitInput             = rememberservice.BuildSynchronousRememberCommitInput
	BuildSynchronousRememberEvidenceSecurityResults = rememberservice.BuildSynchronousRememberEvidenceSecurityResults
	AssessSynchronousRemember                       = rememberservice.AssessSynchronousRemember
	SynchronousAssessmentFailureDetails             = rememberservice.SynchronousAssessmentFailureDetails
	IsRememberStaleInputError                       = rememberservice.IsRememberStaleInputError
)
