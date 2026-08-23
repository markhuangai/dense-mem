package serverapp

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

// rememberServiceCompat keeps the pre-boundary transport and scheduled
// dreaming interfaces source-compatible while the composition root adopts the
// Remember-owned application service.
type rememberServiceCompat struct {
	service rememberapp.Service
}

var _ memoryservice.RememberService = (*rememberServiceCompat)(nil)
var _ memoryservice.SubmissionStatusService = (*rememberServiceCompat)(nil)

func newRememberServiceCompat(service rememberapp.Service) *rememberServiceCompat {
	return &rememberServiceCompat{service: service}
}

func (a *rememberServiceCompat) Remember(ctx context.Context, req memoryservice.RememberRequest) (*memoryservice.RememberResult, error) {
	result, err := a.service.Remember(ctx, rememberapp.RememberRequest{
		Evidence: rememberEvidenceRequestCompat(req.Evidence), EntityHints: req.EntityHints, RelationshipHints: req.RelationshipHints, IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil || result == nil {
		return nil, err
	}
	return &memoryservice.RememberResult{
		IngestID: result.IngestID, SubmissionID: result.SubmissionID, SubmissionKind: result.SubmissionKind,
		ProcessingState: result.ProcessingState, CheckAfterSeconds: result.CheckAfterSeconds,
		StatusTool: result.StatusTool, CorrelationID: result.CorrelationID,
	}, nil
}

func rememberEvidenceRequestCompat(values []memoryservice.RememberEvidenceInput) []rememberapp.RememberEvidenceInput {
	if len(values) == 0 {
		return nil
	}
	converted := make([]rememberapp.RememberEvidenceInput, 0, len(values))
	for _, value := range values {
		converted = append(converted, rememberapp.RememberEvidenceInput{
			Content: value.Content, SourceType: value.SourceType, Source: value.Source, SourceGroup: value.SourceGroup,
			Authority: value.Authority, SourceKey: value.SourceKey, SourceRevision: value.SourceRevision,
			PreviousSourceRevision: value.PreviousSourceRevision, SupersedesEvidenceIDs: append([]string(nil), value.SupersedesEvidenceIDs...),
			IdempotencyKey: value.IdempotencyKey, Labels: append([]string(nil), value.Labels...), Metadata: value.Metadata,
		})
	}
	return converted
}

func (a *rememberServiceCompat) GetSubmissionStatus(ctx context.Context, req memoryservice.GetSubmissionStatusRequest) (*memoryservice.SubmissionStatusResult, error) {
	result, err := a.service.GetSubmissionStatus(ctx, rememberapp.GetSubmissionStatusRequest{SubmissionID: req.SubmissionID})
	if err != nil || result == nil {
		return nil, err
	}
	converted := &memoryservice.SubmissionStatusResult{
		SubmissionID: result.SubmissionID, SubmissionKind: result.SubmissionKind, ProcessingState: result.ProcessingState,
		SearchState: result.SearchState, CheckAfterSeconds: result.CheckAfterSeconds, CorrelationID: result.CorrelationID,
		Attempts: result.Attempts, MaxAttempts: result.MaxAttempts, SubmittedAt: result.SubmittedAt, NextAttemptAt: result.NextAttemptAt,
		StartedAt: result.StartedAt, UpdatedAt: result.UpdatedAt, CompletedAt: result.CompletedAt,
		QuarantineExpiresAt: result.QuarantineExpiresAt,
		Evidence:            make([]memoryservice.SubmissionEvidenceStatus, 0, len(result.Evidence)),
		Errors:              make([]memoryservice.SubmissionStatusError, 0, len(result.Errors)),
	}
	for _, item := range result.Evidence {
		superseded := append([]string(nil), item.SupersededEvidenceIDs...)
		if superseded == nil {
			superseded = []string{}
		}
		converted.Evidence = append(converted.Evidence, memoryservice.SubmissionEvidenceStatus{
			EvidenceID: item.EvidenceID, EvidenceIndex: item.EvidenceIndex,
			SupersededEvidenceIDs: superseded, SearchState: item.SearchState,
			Error: rememberStatusErrorCompat(item.Error),
		})
	}
	for _, item := range result.Errors {
		converted.Errors = append(converted.Errors, memoryservice.SubmissionStatusError{
			Code: item.Code, Message: item.Message, Retryable: item.Retryable, NextAction: item.NextAction,
			Remediation: item.Remediation, ResubmissionIssues: rememberResubmissionIssuesCompat(item.ResubmissionIssues),
			ResubmissionIssuesTruncated: item.ResubmissionIssuesTruncated,
		})
	}
	if result.AwaitingConfirmation != nil {
		converted.AwaitingConfirmation = &memoryservice.SubmissionAwaitingConfirmation{
			ConfirmationToken: result.AwaitingConfirmation.ConfirmationToken,
			ExpiresAt:         result.AwaitingConfirmation.ExpiresAt,
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
	return converted, nil
}

func rememberStatusErrorCompat(value *rememberapp.SubmissionStatusError) *memoryservice.SubmissionStatusError {
	if value == nil {
		return nil
	}
	converted := memoryservice.SubmissionStatusError{
		Code: value.Code, Message: value.Message, Retryable: value.Retryable, NextAction: value.NextAction,
		Remediation: value.Remediation, ResubmissionIssues: rememberResubmissionIssuesCompat(value.ResubmissionIssues),
		ResubmissionIssuesTruncated: value.ResubmissionIssuesTruncated,
	}
	return &converted
}

func rememberResubmissionIssuesCompat(values []rememberapp.SubmissionResubmissionIssue) []memoryservice.SubmissionResubmissionIssue {
	return values
}
