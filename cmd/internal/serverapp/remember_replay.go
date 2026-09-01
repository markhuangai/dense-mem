package serverapp

import (
	"strings"

	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func rememberAttemptStatusForRequest(
	attempt *repository.RememberAttempt,
	input rememberapp.RememberProcessRequest,
) (*rememberapp.SubmissionStatusResult, error) {
	status, err := rememberAttemptStatus(attempt)
	if err != nil {
		return nil, err
	}
	reorderRememberRelationshipResults(status, rememberFailureRelationshipRefs(input.Proposal))
	return status, nil
}

func reorderRememberRelationshipResults(status *rememberapp.SubmissionStatusResult, refs []string) {
	if status == nil || len(refs) == 0 || len(status.RelationshipResults) != len(refs) {
		return
	}
	byRef := make(map[string]rememberapp.SubmissionRelationshipResult, len(status.RelationshipResults))
	for _, item := range status.RelationshipResults {
		ref := strings.TrimSpace(item.RelationshipRef)
		if ref == "" {
			return
		}
		byRef[ref] = item
	}
	if len(byRef) != len(refs) {
		return
	}
	reordered := make([]rememberapp.SubmissionRelationshipResult, len(refs))
	for index, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return
		}
		item, exists := byRef[ref]
		if !exists {
			return
		}
		reordered[index] = item
	}
	status.RelationshipResults = reordered
}

func rememberAttemptReplay(attempt *repository.RememberAttempt, input rememberapp.RememberProcessRequest) (*rememberapp.SubmissionStatusResult, error) {
	status, err := rememberAttemptStatusForRequest(attempt, input)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(attempt.Outcome) != "failed" && strings.TrimSpace(status.ProcessingState) != "failed" {
		return status, nil
	}
	return nil, &rememberapp.RememberProcessError{Status: status, Err: rememberapp.ErrRememberPersistence}
}
