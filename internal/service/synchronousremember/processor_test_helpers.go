package synchronousremember

import (
	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func synchronousPipelineRunScope(created *repository.CreateIngestResult) repository.SubmissionAssessmentRunScope {
	return repository.SubmissionAssessmentRunScope{
		TeamID: created.TeamID, OwnerProfileID: created.OwnerProfileID, IngestID: created.IngestID, PlacementRunID: created.PlacementRunID,
		WorkerID: "synchronous-pipeline", ExpectedAttempts: 1, MaxAttempts: 1,
	}
}

func synchronousPipelineCreated(input repository.CreateIngestInput) *repository.CreateIngestResult {
	created := &repository.CreateIngestResult{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: uuid.NewString(), PlacementRunID: uuid.NewString(), Proposal: input.Proposal}
	for index, evidence := range input.Evidence {
		fragmentID := uuid.NewString()
		created.Evidence = append(created.Evidence, repository.EvidenceFragment{FragmentID: fragmentID, EvidenceIndex: index, Content: evidence.Content, ContentHash: evidence.ContentHash, Authority: evidence.Authority, SupersededEvidenceIDs: []string{}})
		created.Items = append(created.Items, repository.PlacementItem{PlacementItemID: uuid.NewString(), FragmentID: fragmentID, EvidenceIndex: index, Status: "queued"})
	}
	return created
}
