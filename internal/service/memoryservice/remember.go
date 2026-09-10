package memoryservice

import rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"

var ErrRememberConflict = rememberapp.ErrRememberConflict

// RememberService is the single synchronous Remember application contract.
// This package aliases it for callers that compose the broader memory tool
// set; there is no second compatibility implementation.
type RememberService = rememberapp.Service
type RememberRequest = rememberapp.RememberRequest
type RememberEvidenceInput = rememberapp.RememberEvidenceInput
type RememberResult = rememberapp.RememberResult
type SubmissionStatusResult = rememberapp.SubmissionStatusResult
type SubmissionRelationshipSplit = rememberapp.SubmissionRelationshipSplit
type SubmissionRelationshipResult = rememberapp.SubmissionRelationshipResult
type SubmissionAwaitingConfirmation = rememberapp.SubmissionAwaitingConfirmation
type RelationshipCorrectionCandidate = rememberapp.RelationshipCorrectionCandidate
type SubmissionEvidenceStatus = rememberapp.SubmissionEvidenceStatus

// SynchronousAssessmentFailureDetails forwards the bounded Remember
// assessment projection while registry callers migrate to the native owner.
// The compatibility entry point is retained for removal by issue #382.
func SynchronousAssessmentFailureDetails(err error) (string, map[string]any) {
	return rememberapp.SynchronousAssessmentFailureDetails(err)
}
