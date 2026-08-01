package memoryservice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

const submissionStatusTool = "get_submission_status"

func (s *rememberService) rememberSubmission(ctx context.Context, req RememberRequest) (*RememberResult, error) {
	if s.submissions == nil {
		return nil, errors.New("remember: submission repository is required")
	}
	if strings.TrimSpace(req.ContractVersion) != domain.ContractVersion {
		return nil, fmt.Errorf("remember: invalid contract_version %q", req.ContractVersion)
	}
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.ProfileID == uuid.Nil {
		return nil, ErrRememberAuthContext
	}
	credential, ok := requestctx.ActorCredentialFromContext(ctx)
	if !ok || credential.KeyID == uuid.Nil {
		return nil, ErrRememberCredential
	}
	if len(req.Evidence) == 0 {
		return nil, errors.New("remember: evidence is required")
	}
	if err := ValidateSubmissionProposal(req); err != nil {
		return nil, err
	}
	for _, evidence := range req.Evidence {
		if _, err := ScanSubmissionEvidence(evidence.Content); err != nil {
			return nil, err
		}
	}
	requestHash, err := canonicalSubmissionRequestHash(req)
	if err != nil {
		return nil, err
	}
	proposal := map[string]any{
		"entities":      cloneSubmissionProposalMaps(req.EntityHints),
		"relationships": cloneSubmissionProposalMaps(req.RelationshipHints),
	}
	evidence := make([]repository.SubmissionEvidenceInput, 0, len(req.Evidence))
	for _, item := range req.Evidence {
		evidence = append(evidence, submissionRepositoryEvidence(item))
	}
	created, err := s.submissions.CreateSubmission(ctx, repository.CreateSubmissionInput{
		TeamID:                          actor.TeamID.String(),
		OwnerProfileID:                  actor.ProfileID.String(),
		ActorCredentialID:               credential.KeyID.String(),
		ActorAuthMethod:                 credential.AuthMethod,
		ActorRole:                       credential.Role,
		ActorScopes:                     append([]string(nil), credential.Scopes...),
		CorrelationID:                   correlation.FromContext(ctx),
		IdempotencyKey:                  strings.TrimSpace(req.IdempotencyKey),
		RequestHash:                     requestHash,
		SourceSummary:                   sourceSummary(req.Evidence),
		Proposal:                        proposal,
		Evidence:                        evidence,
		ReplacesQuarantinedSubmissionID: strings.TrimSpace(req.ReplacesQuarantinedSubmissionID),
	})
	if err != nil {
		return nil, translateSubmissionError(err)
	}
	return &RememberResult{
		SubmissionID:      created.SubmissionID,
		ProcessingState:   created.Status,
		CheckAfterSeconds: rememberCheckAfterSeconds,
		StatusTool:        submissionStatusTool,
		CorrelationID:     correlation.FromContext(ctx),
	}, nil
}

func (s *rememberService) GetSubmissionStatus(ctx context.Context, req GetSubmissionStatusRequest) (*SubmissionStatusResult, error) {
	if s.submissions == nil {
		return nil, errors.New("submission status: submission repository is required")
	}
	if strings.TrimSpace(req.ContractVersion) != domain.ContractVersion {
		return nil, fmt.Errorf("submission status: invalid contract_version %q", req.ContractVersion)
	}
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.ProfileID == uuid.Nil {
		return nil, ErrRememberAuthContext
	}
	result, err := s.submissions.GetSubmissionStatus(ctx, repository.GetSubmissionStatusInput{
		TeamID:         actor.TeamID.String(),
		OwnerProfileID: actor.ProfileID.String(),
		SubmissionID:   strings.TrimSpace(req.SubmissionID),
	})
	if err != nil {
		return nil, translateSubmissionError(err)
	}
	return &SubmissionStatusResult{
		SubmissionID:         result.SubmissionID,
		ProcessingState:      result.ProcessingState,
		SearchState:          result.SearchState,
		Evidence:             append([]repository.SubmissionEvidenceStatus(nil), result.Evidence...),
		RelationshipOutcomes: append([]repository.SubmissionRelationshipOutcome(nil), result.RelationshipOutcomes...),
		Errors:               append([]repository.SubmissionStatusError(nil), result.Errors...),
		QuarantineExpiresAt:  result.QuarantineExpiresAt,
	}, nil
}

func canonicalSubmissionRequestHash(req RememberRequest) (string, error) {
	return canonicalRequestHash(RememberRequest{
		ContractVersion:                 req.ContractVersion,
		Evidence:                        req.Evidence,
		EntityHints:                     req.EntityHints,
		RelationshipHints:               req.RelationshipHints,
		ReplacesQuarantinedSubmissionID: req.ReplacesQuarantinedSubmissionID,
		IdempotencyKey:                  req.IdempotencyKey,
	})
}

func cloneSubmissionProposalMaps(items []map[string]any) []map[string]any {
	if items == nil {
		return []map[string]any{}
	}
	cloned := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out := make(map[string]any, len(item))
		for key, value := range item {
			out[key] = value
		}
		cloned = append(cloned, out)
	}
	return cloned
}

func translateSubmissionError(err error) error {
	switch {
	case errors.Is(err, ErrEncodedEvidenceNotAllowed), errors.Is(err, ErrEvidenceSecurityRejected):
		return err
	case errors.Is(err, repository.ErrSubmissionNotFound):
		return httperr.New(httperr.NOT_FOUND, "submission not found")
	case errors.Is(err, repository.ErrSubmissionConflict), errors.Is(err, repository.ErrIdempotencyConflict):
		return fmt.Errorf("%w: duplicate or stale submission", ErrRememberConflict)
	case errors.Is(err, repository.ErrTeamInactive):
		return httperr.New(httperr.NOT_FOUND, "team not found")
	default:
		return ErrRememberPersistence
	}
}
