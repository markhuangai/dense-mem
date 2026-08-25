package memoryservice

import (
	"context"
	"errors"
	"strings"
)

func (s *submissionAssessmentPlacementWorkerService) ProcessSubmissionAssessmentPlacement(ctx context.Context, submissionID string) (bool, error) {
	scoped := *s
	scoped.targetID = strings.TrimSpace(submissionID)
	if scoped.targetID == "" {
		return false, errors.New("submission assessment worker: submission_id is required")
	}
	return scoped.ProcessNextSubmissionAssessmentPlacement(ctx)
}
