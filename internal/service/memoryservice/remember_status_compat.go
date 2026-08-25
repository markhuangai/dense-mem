package memoryservice

import (
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func submissionStatusResultFromLedger(placement *repository.CreateIngestResult) *SubmissionStatusResult {
	return rememberStatusCompat(rememberapp.ProjectSubmissionStatus(rememberStageResultCompat(placement)))
}

// ProjectSubmissionStatus exposes the bounded Remember projection to legacy
// diagnostics without retaining a second projection implementation here.
func ProjectSubmissionStatus(placement *repository.CreateIngestResult) *SubmissionStatusResult {
	return submissionStatusResultFromLedger(placement)
}

func rememberStageResultCompat(result *repository.CreateIngestResult) *rememberapp.StageResult {
	if result == nil {
		return nil
	}
	converted := &rememberapp.StageResult{
		TeamID: result.TeamID, OwnerProfileID: result.OwnerProfileID, SubmissionID: result.IngestID,
		PlacementRunID: result.PlacementRunID, Status: result.Status, CorrelationID: result.CorrelationID,
		Attempts: result.Attempts, MaxAttempts: result.MaxAttempts, Existing: result.Existing,
		Proposal: result.Proposal, SubmittedAt: result.SubmittedAt, NextAttemptAt: result.NextAttemptAt,
		StartedAt: result.StartedAt, UpdatedAt: result.UpdatedAt, CompletedAt: result.CompletedAt,
		QuarantineExpiresAt: result.QuarantineExpiresAt,
	}
	for _, item := range result.RelationshipResults {
		convertedResult := rememberapp.SubmissionRelationshipResult{
			RelationshipRef: item.RelationshipRef,
			Disposition:     item.Disposition,
			Reason:          item.Reason,
			Splits:          make([]rememberapp.SubmissionRelationshipSplit, 0, len(item.Splits)),
		}
		for _, split := range item.Splits {
			convertedResult.Splits = append(convertedResult.Splits, rememberapp.SubmissionRelationshipSplit{
				SplitIndex: split.SplitIndex, RelationshipID: split.RelationshipID,
				RelationshipVersion: split.RelationshipVersion, Status: split.Status,
			})
		}
		converted.RelationshipResults = append(converted.RelationshipResults, convertedResult)
	}
	for _, item := range result.Evidence {
		converted.Evidence = append(converted.Evidence, rememberapp.EvidenceFragment{
			FragmentID: item.FragmentID, EvidenceIndex: item.EvidenceIndex, Content: item.Content,
			ContentHash: item.ContentHash, Authority: item.Authority, SourceID: item.SourceID,
			SourceRevisionID: item.SourceRevisionID, SupersededEvidenceIDs: append([]string(nil), item.SupersededEvidenceIDs...),
		})
	}
	for _, item := range result.Items {
		converted.Items = append(converted.Items, rememberapp.PlacementItem{
			PlacementItemID: item.PlacementItemID, FragmentID: item.FragmentID, ClaimKey: item.ClaimKey,
			EvidenceIndex: item.EvidenceIndex, Status: item.Status, Category: item.Category,
			Version: item.Version, Result: item.Result,
		})
	}
	if result.FirstDisposition != nil {
		converted.FirstDisposition = &rememberapp.FirstDisposition{
			Status: result.FirstDisposition.Status, CreatedAt: result.FirstDisposition.CreatedAt,
			CompletedAt: result.FirstDisposition.CompletedAt, IsRemember: result.FirstDisposition.IsRemember,
		}
	}
	return converted
}

func rememberStatusCompat(result *rememberapp.SubmissionStatusResult) *SubmissionStatusResult {
	if result == nil {
		return nil
	}
	converted := &SubmissionStatusResult{
		ContractVersion: result.ContractVersion,
		SubmissionID:    result.SubmissionID, SubmissionKind: result.SubmissionKind,
		ProcessingState: result.ProcessingState, SearchState: result.SearchState,
		CheckAfterSeconds: result.CheckAfterSeconds, CorrelationID: result.CorrelationID,
		Attempts: result.Attempts, MaxAttempts: result.MaxAttempts, SubmittedAt: result.SubmittedAt,
		NextAttemptAt: result.NextAttemptAt, StartedAt: result.StartedAt, UpdatedAt: result.UpdatedAt,
		CompletedAt: result.CompletedAt, QuarantineExpiresAt: result.QuarantineExpiresAt,
		Evidence:     make([]SubmissionEvidenceStatus, 0, len(result.Evidence)),
		Errors:       make([]SubmissionStatusError, 0, len(result.Errors)),
		Degradations: make([]SubmissionStatusDegradation, 0, len(result.Degradations)),
	}
	for _, item := range result.RelationshipResults {
		convertedResult := SubmissionRelationshipResult{
			RelationshipRef: item.RelationshipRef,
			Disposition:     item.Disposition,
			Reason:          item.Reason,
			Splits:          make([]SubmissionRelationshipSplit, 0, len(item.Splits)),
		}
		for _, split := range item.Splits {
			convertedResult.Splits = append(convertedResult.Splits, SubmissionRelationshipSplit{
				SplitIndex: split.SplitIndex, RelationshipID: split.RelationshipID,
				RelationshipVersion: split.RelationshipVersion, Status: split.Status,
			})
		}
		converted.RelationshipResults = append(converted.RelationshipResults, convertedResult)
	}
	for _, item := range result.Evidence {
		superseded := append([]string(nil), item.SupersededEvidenceIDs...)
		if superseded == nil {
			superseded = []string{}
		}
		converted.Evidence = append(converted.Evidence, SubmissionEvidenceStatus{
			Disposition: item.Disposition, EvidenceID: item.EvidenceID, EvidenceIndex: item.EvidenceIndex,
			SupersededEvidenceIDs: superseded, SearchState: item.SearchState,
			Reason: item.Reason, Error: item.Error,
		})
	}
	converted.Errors = append(converted.Errors, result.Errors...)
	converted.Degradations = append(converted.Degradations, result.Degradations...)
	if result.AwaitingConfirmation != nil {
		converted.AwaitingConfirmation = &SubmissionAwaitingConfirmation{
			ConfirmationToken: result.AwaitingConfirmation.ConfirmationToken,
			ExpiresAt:         result.AwaitingConfirmation.ExpiresAt,
			Candidates:        make([]repository.RelationshipCorrectionCandidate, 0, len(result.AwaitingConfirmation.Candidates)),
		}
		for _, candidate := range result.AwaitingConfirmation.Candidates {
			converted.AwaitingConfirmation.Candidates = append(converted.AwaitingConfirmation.Candidates, repository.RelationshipCorrectionCandidate{
				Endpoint: candidate.Endpoint, EntityID: candidate.EntityID, EntityKind: candidate.EntityKind,
				CanonicalName: candidate.CanonicalName,
			})
		}
	}
	if result.CorrectionResult != nil {
		converted.CorrectionResult = &repository.RelationshipCorrectionResult{
			OriginalRelationshipID:  result.CorrectionResult.OriginalRelationshipID,
			OriginalVersion:         result.CorrectionResult.OriginalVersion,
			SuccessorRelationshipID: result.CorrectionResult.SuccessorRelationshipID,
			SuccessorVersion:        result.CorrectionResult.SuccessorVersion,
			ReusedSuccessor:         result.CorrectionResult.ReusedSuccessor,
		}
	}
	return converted
}
