package memoryservice

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func (s *semanticAssessmentPlacementWorkerService) recordDeterministicSecuritySignals(
	ctx context.Context,
	run repository.PlacementRun,
	fragment repository.EvidenceFragment,
	scan SubmissionSecurityScan,
) error {
	_, err := s.ledger.AppendSecurityEvent(ctx, repository.SecurityEventInput{
		TeamID:             run.TeamID,
		OwnerProfileID:     run.OwnerProfileID,
		IngestID:           run.IngestID,
		FragmentID:         fragment.FragmentID,
		SecurityEventDraft: submissionSecurityQuarantineEvent(scan),
	})
	return err
}
