package memoryservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

var (
	ErrLifecycleAuthContext = errors.New("memory lifecycle: authenticated actor context is required")
	ErrLifecyclePersistence = errors.New("memory lifecycle: persistence failed")
)

type LifecycleService interface {
	CorrectRelationship(ctx context.Context, req CorrectRelationshipRequest) (*CorrectRelationshipReceipt, error)
	GetRelationshipCorrectionStatus(ctx context.Context, req GetSubmissionStatusRequest) (*SubmissionStatusResult, error)
	RetractEvidence(ctx context.Context, req RetractEvidenceRequest) (*RetractEvidenceResult, error)
}

type LifecycleDependencies struct {
	Semantic LifecycleSemanticRepository
	Evidence LifecycleEvidenceRepository
}

type LifecycleSemanticRepository interface {
	CorrectRelationship(ctx context.Context, input repository.CorrectRelationshipInput) (*repository.CorrectRelationshipResult, error)
	GetRelationshipCorrection(ctx context.Context, input repository.GetRelationshipCorrectionInput) (*repository.RelationshipCorrectionStatus, error)
}

type LifecycleEvidenceRepository interface {
	RetractEvidence(ctx context.Context, input repository.RetractEvidenceInput) (*repository.EvidenceLifecycleResult, error)
}

type lifecycleService struct {
	semantic LifecycleSemanticRepository
	evidence LifecycleEvidenceRepository
}

func NewLifecycleService(deps LifecycleDependencies) LifecycleService {
	return &lifecycleService{semantic: deps.Semantic, evidence: deps.Evidence}
}

type CorrectRelationshipRequest struct {
	Action            string                                     `json:"action"`
	RelationshipID    string                                     `json:"relationship_id,omitempty"`
	ExpectedVersion   int                                        `json:"expected_version,omitempty"`
	Patch             repository.RelationshipCorrectionPatch     `json:"patch,omitempty"`
	Supports          []repository.RelationshipCorrectionSupport `json:"supports,omitempty"`
	Reason            string                                     `json:"reason,omitempty"`
	SubmissionID      string                                     `json:"submission_id,omitempty"`
	ConfirmationToken string                                     `json:"confirmation_token,omitempty"`
	Selection         repository.RelationshipCorrectionSelection `json:"selection,omitempty"`
	IdempotencyKey    string                                     `json:"idempotency_key"`
}

type CorrectRelationshipReceipt struct {
	SubmissionID      string `json:"submission_id"`
	SubmissionKind    string `json:"submission_kind"`
	ProcessingState   string `json:"processing_state"`
	CheckAfterSeconds int    `json:"check_after_seconds"`
	StatusTool        string `json:"status_tool"`
	CorrelationID     string `json:"correlation_id"`
}

type RetractEvidenceRequest struct {
	EvidenceIDs    []string `json:"evidence_ids"`
	Reason         string   `json:"reason"`
	IdempotencyKey string   `json:"idempotency_key"`
}

type RetractEvidenceResult struct {
	DecisionID                      string   `json:"decision_id"`
	ProcessingState                 string   `json:"processing_state"`
	RetractedEvidenceIDs            []string `json:"retracted_evidence_ids"`
	AffectedRelationshipCount       int      `json:"affected_relationship_count"`
	PendingRelationshipCount        int      `json:"pending_relationship_count"`
	RetainedActiveRelationshipCount int      `json:"retained_active_relationship_count"`
}

func (s *lifecycleService) CorrectRelationship(
	ctx context.Context,
	req CorrectRelationshipRequest,
) (*CorrectRelationshipReceipt, error) {
	if s.semantic == nil {
		return nil, errors.New("memory lifecycle: semantic repository is required")
	}
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.OwnerID == uuid.Nil {
		return nil, ErrLifecycleAuthContext
	}
	if req.Action != "submit" && req.Action != "confirm" {
		return nil, errors.New("memory lifecycle: action must be submit or confirm")
	}
	result, err := s.semantic.CorrectRelationship(ctx, repository.CorrectRelationshipInput{
		TeamID:            actor.TeamID.String(),
		OwnerProfileID:    actor.OwnerID.String(),
		Action:            req.Action,
		RelationshipID:    req.RelationshipID,
		ExpectedVersion:   req.ExpectedVersion,
		Patch:             req.Patch,
		Supports:          req.Supports,
		Reason:            req.Reason,
		SubmissionID:      req.SubmissionID,
		ConfirmationToken: req.ConfirmationToken,
		Selection:         req.Selection,
		IdempotencyKey:    req.IdempotencyKey,
	})
	if err != nil {
		return nil, translateRelationshipCorrectionError(err)
	}
	return &CorrectRelationshipReceipt{
		SubmissionID:      result.SubmissionID,
		SubmissionKind:    "relationship_correction",
		ProcessingState:   result.ProcessingState,
		CheckAfterSeconds: 0,
		StatusTool:        rememberStatusTool,
		CorrelationID:     correlation.FromContext(ctx),
	}, nil
}

func (s *lifecycleService) GetRelationshipCorrectionStatus(
	ctx context.Context,
	req GetSubmissionStatusRequest,
) (*SubmissionStatusResult, error) {
	if s.semantic == nil {
		return nil, errors.New("submission status: semantic repository is required")
	}
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.OwnerID == uuid.Nil {
		return nil, ErrLifecycleAuthContext
	}
	submissionID := strings.TrimSpace(req.SubmissionID)
	if _, err := uuid.Parse(submissionID); err != nil {
		return nil, httperr.New(httperr.NOT_FOUND, "submission not found")
	}
	result, err := s.semantic.GetRelationshipCorrection(ctx, repository.GetRelationshipCorrectionInput{
		TeamID: actor.TeamID.String(), OwnerProfileID: actor.OwnerID.String(), SubmissionID: submissionID,
	})
	if err != nil {
		return nil, translateRelationshipCorrectionError(err)
	}
	return relationshipCorrectionSubmissionStatus(result), nil
}

func translateRelationshipCorrectionError(err error) error {
	if errors.Is(err, repository.ErrSemanticOwnerMismatch) || errors.Is(err, repository.ErrRelationshipCorrectionNotFound) {
		return httperr.New(httperr.NOT_FOUND, "submission not found")
	}
	if errors.Is(err, repository.ErrSemanticIdempotencyConflict) ||
		errors.Is(err, repository.ErrRelationshipCorrectionConfirmation) ||
		errors.Is(err, repository.ErrRelationshipCorrectionConfirmationExpired) ||
		errors.Is(err, repository.ErrRelationshipCorrectionStateConflict) {
		return httperr.New(httperr.CONFLICT, "relationship correction conflict")
	}
	return ErrLifecyclePersistence
}

func relationshipCorrectionSubmissionStatus(result *repository.RelationshipCorrectionStatus) *SubmissionStatusResult {
	status := &SubmissionStatusResult{
		SubmissionKind: "relationship_correction", SearchState: string(domain.SearchProjectionNotRequired),
		CheckAfterSeconds: 0, Evidence: []SubmissionEvidenceStatus{}, Errors: []SubmissionStatusError{},
	}
	if result == nil {
		return status
	}
	status.SubmissionID = result.SubmissionID
	status.ProcessingState = result.ProcessingState
	if result.SearchState != "" {
		status.SearchState = result.SearchState
	}
	if result.Confirmation != nil {
		status.AwaitingConfirmation = &SubmissionAwaitingConfirmation{
			ConfirmationToken: result.Confirmation.Token,
			ExpiresAt:         result.Confirmation.ExpiresAt,
			Candidates:        append([]repository.RelationshipCorrectionCandidate(nil), result.Confirmation.Candidates...),
		}
	}
	status.CorrectionResult = result.Correction
	if result.ErrorCode != "" {
		errorValue := submissionStatusErrorForCode(result.ErrorCode, result.ProcessingState)
		status.Errors = append(status.Errors, errorValue)
	}
	if (status.ProcessingState == "rejected" || status.ProcessingState == "failed") && len(status.Errors) == 0 {
		fallback := submissionStatusErrorForCode("", status.ProcessingState)
		status.Errors = append(status.Errors, fallback)
	}
	return status
}

func (s *lifecycleService) RetractEvidence(
	ctx context.Context,
	req RetractEvidenceRequest,
) (*RetractEvidenceResult, error) {
	if s.evidence == nil {
		return nil, errors.New("memory lifecycle: evidence repository is required")
	}
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.OwnerID == uuid.Nil {
		return nil, ErrLifecycleAuthContext
	}
	requestHash, err := retractEvidenceRequestHash(req)
	if err != nil {
		return nil, err
	}
	result, err := s.evidence.RetractEvidence(ctx, repository.RetractEvidenceInput{
		TeamID:         actor.TeamID.String(),
		OwnerProfileID: actor.OwnerID.String(),
		EvidenceIDs:    append([]string(nil), req.EvidenceIDs...),
		Reason:         req.Reason,
		IdempotencyKey: req.IdempotencyKey,
		RequestHash:    requestHash,
	})
	if err != nil {
		return nil, translateEvidenceLifecycleError(err)
	}
	return &RetractEvidenceResult{
		DecisionID:                      result.DecisionID,
		ProcessingState:                 result.ProcessingState,
		RetractedEvidenceIDs:            append([]string(nil), result.RetractedEvidenceIDs...),
		AffectedRelationshipCount:       result.AffectedRelationshipCount,
		PendingRelationshipCount:        result.PendingRelationshipCount,
		RetainedActiveRelationshipCount: result.RetainedActiveRelationshipCount,
	}, nil
}

func retractEvidenceRequestHash(req RetractEvidenceRequest) (string, error) {
	evidenceIDs := make([]string, len(req.EvidenceIDs))
	for index, evidenceID := range req.EvidenceIDs {
		evidenceIDs[index] = strings.TrimSpace(evidenceID)
	}
	sort.Strings(evidenceIDs)
	payload, err := json.Marshal(map[string]any{
		"contract_version": legacyRequestHashContractVersion,
		"evidence_ids":     evidenceIDs,
		"reason":           strings.TrimSpace(req.Reason),
		"idempotency_key":  strings.TrimSpace(req.IdempotencyKey),
	})
	if err != nil {
		return "", fmt.Errorf("memory lifecycle: canonical retract request hash: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func translateEvidenceLifecycleError(err error) error {
	switch {
	case errors.Is(err, repository.ErrEvidenceLifecycleNotFound), errors.Is(err, repository.ErrTeamInactive):
		return httperr.New(httperr.NOT_FOUND, "evidence not found")
	case errors.Is(err, repository.ErrEvidenceLifecycleConflict), errors.Is(err, repository.ErrIdempotencyConflict):
		return httperr.New(httperr.CONFLICT, "evidence lifecycle conflict")
	default:
		return err
	}
}
