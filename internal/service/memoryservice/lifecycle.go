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
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

var (
	ErrLifecycleAuthContext           = errors.New("memory lifecycle: authenticated actor context is required")
	ErrLifecyclePersistence           = errors.New("memory lifecycle: persistence failed")
	ErrLifecycleEmbeddingUnavailable  = errors.New("memory lifecycle: embedding provider unavailable")
	ErrLifecycleEmbeddingInvalid      = errors.New("memory lifecycle: embedding response invalid")
	ErrLifecycleEmbeddingTimeout      = errors.New("memory lifecycle: embedding provider timed out")
	errLifecycleCorrectionCommitFence = errors.New("memory lifecycle: correction commit search fence conflict")
)

// CorrectionConfirmationInvalidReason identifies a pending confirmation that
// remains awaiting confirmation after the legacy lifecycle rejects a token or
// candidate selection.
const CorrectionConfirmationInvalidReason = "confirmation_invalid"

type LifecycleService interface {
	CorrectRelationship(ctx context.Context, req CorrectRelationshipRequest) (*CorrectRelationshipReceipt, error)
	RetractEvidence(ctx context.Context, req RetractEvidenceRequest) (*RetractEvidenceResult, error)
}

type LifecycleDependencies struct {
	Semantic                   LifecycleSemanticRepository
	Evidence                   LifecycleEvidenceRepository
	CorrectionExecutor         LifecycleCorrectionExecutor
	CorrectionEmbeddingTimeout time.Duration
}

type LifecycleSemanticRepository interface {
	PlanRelationshipCorrectionEmbeddings(ctx context.Context, input repository.CorrectRelationshipInput) (*repository.RelationshipCorrectionEmbeddingPlan, error)
	CorrectRelationshipWithEmbeddings(ctx context.Context, input repository.CorrectRelationshipInput, embeddings []repository.RelationshipCorrectionEmbedding) (*repository.CorrectRelationshipResult, error)
}

type LifecycleCorrectionExecutor interface {
	Execute(context.Context, semanticwrite.Plan) (semanticwrite.Result, error)
}

type LifecycleEvidenceRepository interface {
	RetractEvidence(ctx context.Context, input repository.RetractEvidenceInput) (*repository.EvidenceLifecycleResult, error)
}

type lifecycleService struct {
	semantic         LifecycleSemanticRepository
	evidence         LifecycleEvidenceRepository
	executor         LifecycleCorrectionExecutor
	embeddingTimeout time.Duration
}

func NewLifecycleService(deps LifecycleDependencies) LifecycleService {
	timeout := deps.CorrectionEmbeddingTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &lifecycleService{semantic: deps.Semantic, evidence: deps.Evidence, executor: deps.CorrectionExecutor, embeddingTimeout: timeout}
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
	ContractVersion      string                                   `json:"contract_version"`
	SubmissionID         string                                   `json:"submission_id"`
	SubmissionKind       string                                   `json:"submission_kind"`
	ProcessingState      string                                   `json:"processing_state"`
	SearchState          string                                   `json:"search_state"`
	CorrelationID        string                                   `json:"correlation_id"`
	AwaitingConfirmation *SubmissionAwaitingConfirmation          `json:"awaiting_confirmation,omitempty"`
	CorrectionResult     *repository.RelationshipCorrectionResult `json:"correction_result,omitempty"`
	Errors               []SubmissionStatusError                  `json:"errors"`
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
	input := repository.CorrectRelationshipInput{
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
	}
	plan, err := s.semantic.PlanRelationshipCorrectionEmbeddings(ctx, input)
	if err == nil && plan == nil {
		err = ErrLifecyclePersistence
	}
	var embeddings []repository.RelationshipCorrectionEmbedding
	if err == nil && len(plan.Documents) > 0 {
		if s.executor == nil {
			err = ErrLifecycleEmbeddingUnavailable
		} else {
			executionCtx := observability.WithMetricIdentity(ctx, input.TeamID, input.OwnerProfileID)
			embeddingCtx, cancel := context.WithTimeout(executionCtx, s.embeddingTimeout)
			result, executeErr := s.executor.Execute(embeddingCtx, semanticwrite.Plan{
				Documents: correctionPlanDocuments(plan.Documents),
				Fence:     semanticwrite.Fence{Model: plan.EmbeddingModel, Dimensions: plan.EmbeddingDimensions, EmbeddingContractID: plan.EmbeddingContractID, SearchGenerationID: plan.SearchIndexGenerationID, SearchGenerationVersion: int64(plan.IndexGeneration)},
				Timeout:   s.embeddingTimeout,
			})
			if executeErr != nil {
				err = translateCorrectionEmbeddingErrorWithContext(ctx, embeddingCtx, executeErr)
			} else {
				embeddings = correctionEmbeddingsFromResult(result)
			}
			cancel()
		}
	}
	var result *repository.CorrectRelationshipResult
	if err == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		} else {
			result, err = s.semantic.CorrectRelationshipWithEmbeddings(ctx, input, embeddings)
			if isCorrectionCommitSearchFenceError(err) {
				err = fmt.Errorf("%w: %w", errLifecycleCorrectionCommitFence, err)
			}
		}
	}
	if err != nil {
		return nil, translateRelationshipCorrectionError(err)
	}
	if result == nil {
		return nil, ErrLifecyclePersistence
	}
	receipt := &CorrectRelationshipReceipt{
		ContractVersion:  domain.ContractVersion,
		SubmissionID:     result.SubmissionID,
		SubmissionKind:   "relationship_correction",
		ProcessingState:  result.ProcessingState,
		SearchState:      result.SearchState,
		CorrelationID:    rememberapp.NormalizeTerminalCorrelationID(correlation.FromContext(ctx)),
		CorrectionResult: result.Correction,
		Errors:           []SubmissionStatusError{},
	}
	if receipt.SearchState == "" {
		receipt.SearchState = string(domain.SearchProjectionNotRequired)
	}
	if result.Confirmation != nil {
		receipt.AwaitingConfirmation = &SubmissionAwaitingConfirmation{
			ConfirmationToken: result.Confirmation.Token,
			ExpiresAt:         result.Confirmation.ExpiresAt,
		}
		for _, candidate := range result.Confirmation.Candidates {
			receipt.AwaitingConfirmation.Candidates = append(receipt.AwaitingConfirmation.Candidates, rememberapp.RelationshipCorrectionCandidate{
				Endpoint: candidate.Endpoint, EntityID: candidate.EntityID, EntityKind: candidate.EntityKind, CanonicalName: candidate.CanonicalName,
			})
		}
	}
	if result.ErrorCode != "" {
		receipt.Errors = append(receipt.Errors, correctionStatusErrorForCode(result.ErrorCode, result.ProcessingState))
	}
	if (result.ProcessingState == "rejected" || result.ProcessingState == "failed") && len(receipt.Errors) == 0 {
		receipt.Errors = append(receipt.Errors, correctionStatusErrorForCode("", result.ProcessingState))
	}
	return receipt, nil
}

func correctionPlanDocuments(documents []repository.RelationshipCorrectionEmbeddingDocument) []semanticwrite.Document {
	result := make([]semanticwrite.Document, 0, len(documents))
	for _, document := range documents {
		result = append(result, semanticwrite.Document{Hash: document.DocumentHash, Text: document.DocumentText})
	}
	return result
}

func correctionEmbeddingsFromResult(result semanticwrite.Result) []repository.RelationshipCorrectionEmbedding {
	embeddings := make([]repository.RelationshipCorrectionEmbedding, 0, len(result.Embeddings))
	for _, embedding := range result.Embeddings {
		embeddings = append(embeddings, repository.RelationshipCorrectionEmbedding{DocumentHash: embedding.DocumentHash, Embedding: append([]float32(nil), embedding.Vector...), EmbeddingContractID: result.Fence.EmbeddingContractID, EmbeddingDimensions: result.Fence.Dimensions, EmbeddingModel: result.Fence.Model, SearchIndexGenerationID: result.Fence.SearchGenerationID, IndexGeneration: int(result.Fence.SearchGenerationVersion)})
	}
	return embeddings
}

func translateCorrectionEmbeddingError(err error) error {
	switch {
	case errors.Is(err, semanticwrite.ErrProviderUnavailable):
		return ErrLifecycleEmbeddingUnavailable
	case errors.Is(err, semanticwrite.ErrProviderResponseInvalid), errors.Is(err, semanticwrite.ErrInvalidPlan):
		return ErrLifecycleEmbeddingInvalid
	case errors.Is(err, semanticwrite.ErrProviderTimeout):
		return ErrLifecycleEmbeddingTimeout
	default:
		return err
	}
}

func translateCorrectionEmbeddingErrorWithContext(callerCtx, embeddingCtx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) && correctionEmbeddingContextOwnsDeadline(callerCtx, embeddingCtx) {
		return ErrLifecycleEmbeddingTimeout
	}
	return translateCorrectionEmbeddingError(err)
}

func correctionEmbeddingContextOwnsDeadline(callerCtx, embeddingCtx context.Context) bool {
	if !errors.Is(embeddingCtx.Err(), context.DeadlineExceeded) {
		return false
	}
	callerDeadline, callerHasDeadline := callerCtx.Deadline()
	embeddingDeadline, embeddingHasDeadline := embeddingCtx.Deadline()
	if !embeddingHasDeadline {
		return false
	}
	if !callerHasDeadline {
		return true
	}
	return embeddingDeadline.Before(callerDeadline)
}

func translateRelationshipCorrectionError(err error) error {
	if errors.Is(err, ErrLifecycleEmbeddingUnavailable) {
		return httperr.New(httperr.ErrEmbeddingUnavailable, "embedding provider unavailable")
	}
	if errors.Is(err, ErrLifecycleEmbeddingInvalid) {
		return httperr.New(httperr.ErrEmbeddingResponseInvalid, "embedding provider response invalid")
	}
	if errors.Is(err, ErrLifecycleEmbeddingTimeout) {
		return httperr.New(httperr.ErrEmbeddingTimeout, "embedding provider timed out")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, repository.ErrSemanticOwnerMismatch) || errors.Is(err, repository.ErrRelationshipCorrectionNotFound) {
		return httperr.New(httperr.NOT_FOUND, "submission not found")
	}
	if errors.Is(err, repository.ErrSemanticIdempotencyConflict) {
		return httperr.NewWithDetails(httperr.CONFLICT, "relationship correction conflict", []httperr.ErrorDetail{{
			Field: "reason", Message: "idempotency_conflict",
		}})
	}
	if errors.Is(err, repository.ErrRelationshipCorrectionConfirmationExpired) {
		return httperr.NewWithDetails(httperr.CONFLICT, "relationship correction conflict", []httperr.ErrorDetail{{
			Field: "reason", Message: string(SubmissionErrorConfirmationExpired),
		}})
	}
	if errors.Is(err, repository.ErrRelationshipCorrectionConfirmation) {
		return httperr.NewWithDetails(httperr.CONFLICT, "relationship correction conflict", []httperr.ErrorDetail{{
			Field: "reason", Message: CorrectionConfirmationInvalidReason,
		}})
	}
	if errors.Is(err, errLifecycleCorrectionCommitFence) {
		return httperr.NewWithDetails(httperr.CONFLICT, "relationship correction conflict", []httperr.ErrorDetail{{
			Field: "reason", Message: string(rememberapp.TerminalErrorCommitConflict),
		}})
	}
	if errors.Is(err, repository.ErrRelationshipCorrectionConfirmation) ||
		errors.Is(err, repository.ErrRelationshipCorrectionStateConflict) ||
		errors.Is(err, repository.ErrSearchEmbeddingRequired) ||
		errors.Is(err, repository.ErrSearchContractMismatch) ||
		errors.Is(err, repository.ErrSearchStaleVersion) {
		return httperr.New(httperr.CONFLICT, "relationship correction conflict")
	}
	return ErrLifecyclePersistence
}

func isCorrectionCommitSearchFenceError(err error) bool {
	return errors.Is(err, repository.ErrSearchEmbeddingRequired) ||
		errors.Is(err, repository.ErrSearchContractMismatch) ||
		errors.Is(err, repository.ErrSearchStaleVersion)
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

// Retract's unchanged request contract must replay hashes written before v2.6.
const retractEvidenceRequestHashContractVersion = "dense-mem.v2.4"

func retractEvidenceRequestHash(req RetractEvidenceRequest) (string, error) {
	evidenceIDs := make([]string, len(req.EvidenceIDs))
	for index, evidenceID := range req.EvidenceIDs {
		evidenceIDs[index] = strings.TrimSpace(evidenceID)
	}
	sort.Strings(evidenceIDs)
	payload, err := json.Marshal(map[string]any{
		"contract_version": retractEvidenceRequestHashContractVersion,
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
