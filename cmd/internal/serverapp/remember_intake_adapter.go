package serverapp

import (
	"context"
	"errors"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

// rememberLedgerAdapter is the composition-only bridge from the legacy
// PostgreSQL ledger to the Remember-owned intake port. The application
// capability never imports repository or PostgreSQL types.
type rememberLedgerAdapter struct {
	ledger repository.LedgerRepository
}

var _ rememberapp.IntakePort = (*rememberLedgerAdapter)(nil)

func newRememberLedgerAdapter(ledger repository.LedgerRepository) rememberapp.IntakePort {
	return &rememberLedgerAdapter{ledger: ledger}
}

func (a *rememberLedgerAdapter) Stage(ctx context.Context, input rememberapp.StageRequest) (*rememberapp.StageResult, error) {
	if a == nil || a.ledger == nil {
		return nil, errors.New("remember intake adapter: ledger is required")
	}
	created, err := a.ledger.CreateIngest(ctx, repository.CreateIngestInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, SpaceID: input.SpaceID,
		SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
		RequestHash: input.RequestHash, CompatibleRequestHashes: input.CompatibleRequestHashes,
		SourceSummary: input.SourceSummary, Status: input.Status,
		TelemetryRemember: input.TelemetryRemember, Proposal: input.Proposal, Metadata: input.Metadata,
		Evidence: rememberEvidenceInputs(input.Evidence),
	})
	if err != nil {
		return nil, translateRememberAdapterError(err)
	}
	return rememberStageResult(created), nil
}

func (a *rememberLedgerAdapter) Status(ctx context.Context, input rememberapp.StatusRequest) (*rememberapp.StageResult, error) {
	if a == nil || a.ledger == nil {
		return nil, errors.New("remember intake adapter: ledger is required")
	}
	result, err := a.ledger.GetPlacementRun(ctx, repository.GetPlacementRunInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.SubmissionID,
	})
	if err != nil {
		return nil, translateRememberAdapterError(err)
	}
	if strings.TrimSpace(result.ContractVersion) != domain.ContractVersion {
		return nil, rememberapp.ErrPlacementNotFound
	}
	return rememberStageResult(result), nil
}

func rememberEvidenceInputs(items []rememberapp.EvidenceInput) []repository.EvidenceInput {
	if len(items) == 0 {
		return nil
	}
	result := make([]repository.EvidenceInput, 0, len(items))
	for _, item := range items {
		var event *repository.SecurityEventDraft
		if item.InitialEvent != nil {
			event = &repository.SecurityEventDraft{
				EventKind: item.InitialEvent.EventKind, Decision: item.InitialEvent.Decision,
				Reason: item.InitialEvent.Reason, Metadata: item.InitialEvent.Metadata,
			}
			for _, signal := range item.InitialEvent.Signals {
				event.Signals = append(event.Signals, repository.SecuritySignalInput{
					Kind: signal.Kind, Severity: signal.Severity, SpanStart: signal.SpanStart,
					SpanEnd: signal.SpanEnd, Metadata: signal.Metadata,
				})
			}
		}
		result = append(result, repository.EvidenceInput{
			Content: item.Content, ContentHash: item.ContentHash, SourceType: item.SourceType,
			Authority: item.Authority, SourceRef: item.SourceRef, SourceKey: item.SourceKey,
			SourceRevisionToken: item.SourceRevisionToken, ExpectedPreviousRevisionToken: item.ExpectedPreviousRevisionToken,
			SourceRevisionContentHash: item.SourceRevisionContentHash, SourceRevisionEnvelope: item.SourceRevisionEnvelope,
			SupersedesEvidenceIDs: append([]string(nil), item.SupersedesEvidenceIDs...),
			Labels:                append([]string(nil), item.Labels...), Metadata: item.Metadata, InitialEvent: event,
		})
	}
	return result
}

func rememberStageResult(result *repository.CreateIngestResult) *rememberapp.StageResult {
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

func translateRememberAdapterError(err error) error {
	var preflight *repository.RememberPreflightError
	if errors.As(err, &preflight) {
		translated := &rememberapp.RememberValidationError{IssuesTruncated: preflight.IssuesTruncated}
		for _, issue := range preflight.Issues {
			translated.Issues = append(translated.Issues, rememberapp.RememberValidationIssue{
				Path: issue.Path, Code: issue.Code, Message: issue.Message,
			})
		}
		return translated
	}
	switch {
	case errors.Is(err, repository.ErrIdempotencyConflict):
		return rememberapp.ErrIdempotencyConflict
	case errors.Is(err, repository.ErrSourceRevisionConflict):
		return rememberapp.ErrSourceRevisionConflict
	case errors.Is(err, repository.ErrTeamInactive):
		return rememberapp.ErrTeamInactive
	case errors.Is(err, repository.ErrPlacementNotFound):
		return rememberapp.ErrPlacementNotFound
	default:
		return err
	}
}
