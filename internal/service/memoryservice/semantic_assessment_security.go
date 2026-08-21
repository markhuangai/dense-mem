package memoryservice

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

// Pre-staging rejections have no ingest to record, so they use the safe audit
// log; staged fragments retain their canonical security event with the terminal placement transaction.
func deterministicSecurityQuarantine(
	fragmentID string,
	scan SubmissionSecurityBatchScan,
) *repository.PlacementSecurityQuarantineInput {
	return &repository.PlacementSecurityQuarantineInput{
		FragmentID:         fragmentID,
		SecurityEventDraft: submissionSecurityBatchQuarantineEvent(scan),
	}
}

func (s *semanticAssessmentPlacementWorkerService) completeTerminalWithSecurityQuarantine(
	ctx context.Context,
	run repository.PlacementRun,
	item repository.PlacementItem,
	fragmentID string,
	scan SubmissionSecurityBatchScan,
	stage string,
) error {
	return s.completeTerminalWithSecurityEvent(ctx, run, item, string(domain.SemanticReviewQuarantined), "quarantined", stage, deterministicSecurityQuarantine(fragmentID, scan))
}

func (s *semanticAssessmentPlacementWorkerService) completeTerminalWithSecurityEvent(
	ctx context.Context,
	run repository.PlacementRun,
	item repository.PlacementItem,
	status, category, stage string,
	securityQuarantine *repository.PlacementSecurityQuarantineInput,
) error {
	completed, err := s.commit.CompletePlacementReviewResult(ctx, repository.CompletePlacementReviewInput{
		TeamID:           run.TeamID,
		OwnerProfileID:   run.OwnerProfileID,
		IngestID:         run.IngestID,
		PlacementRunID:   run.PlacementRunID,
		PlacementItemID:  item.PlacementItemID,
		WorkerID:         s.workerID,
		ExpectedAttempts: run.Attempts,
		Status:           status,
		Category:         category,
		Payload: func() map[string]any {
			payload := placementFailureDiagnosticFor(stage, nil).payload(false)
			payload["assessor_contract"] = domain.ContractVersion
			payload["failure_reason_code"] = "security_quarantine"
			return payload
		}(),
		SecurityQuarantine: securityQuarantine,
	})
	if err == nil && completed != nil {
		s.recordFirstDisposition(ctx, run, completed.FirstDisposition)
		if status == string(domain.SemanticReviewTerminalFailure) {
			observability.RecordAssessorTerminalFailure(s.metrics, stage)
		}
	}
	if err != nil {
		return newPlacementWorkerError(run.TeamID, run.IngestID, stage, err)
	}
	if completed == nil {
		return newPlacementWorkerError(run.TeamID, run.IngestID, stage, errors.New("semantic assessment worker: nil terminal result"))
	}
	return err
}
