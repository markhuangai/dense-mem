package memoryservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

var (
	ErrLifecycleAuthContext          = errors.New("memory lifecycle: authenticated actor context is required")
	ErrLifecyclePersistence          = errors.New("memory lifecycle: persistence failed")
	ErrLifecycleEmbeddingUnavailable = errors.New("memory lifecycle: embedding provider unavailable")
	ErrLifecycleEmbeddingInvalid     = errors.New("memory lifecycle: embedding response invalid")
	ErrLifecycleEmbeddingTimeout     = errors.New("memory lifecycle: embedding request timed out")
	ErrLifecycleEmbeddingCancelled   = errors.New("memory lifecycle: embedding request cancelled")
)

type LifecycleService interface {
	CorrectRelationship(ctx context.Context, req CorrectRelationshipRequest) (*CorrectRelationshipReceipt, error)
	RetractEvidence(ctx context.Context, req RetractEvidenceRequest) (*RetractEvidenceResult, error)
}

type LifecycleDependencies struct {
	Semantic LifecycleSemanticRepository
	Evidence LifecycleEvidenceRepository
	Search   repository.SearchRepository
	Embedder embedding.EmbeddingProviderInterface
}

type LifecycleSemanticRepository interface {
	CorrectRelationship(ctx context.Context, input repository.CorrectRelationshipInput) (*repository.CorrectRelationshipResult, error)
	GetRelationshipCorrection(ctx context.Context, input repository.GetRelationshipCorrectionInput) (*repository.RelationshipCorrectionStatus, error)
}

type synchronousCorrectionSemanticRepository interface {
	PlanRelationshipCorrectionEmbeddings(context.Context, repository.CorrectRelationshipInput) (*repository.RelationshipCorrectionEmbeddingPlan, error)
	CorrectRelationshipWithEmbeddings(context.Context, repository.CorrectRelationshipInput, []repository.InlineEmbeddingResult) (*repository.CorrectRelationshipResult, error)
}

type LifecycleEvidenceRepository interface {
	RetractEvidence(ctx context.Context, input repository.RetractEvidenceInput) (*repository.EvidenceLifecycleResult, error)
}

type lifecycleService struct {
	semantic LifecycleSemanticRepository
	evidence LifecycleEvidenceRepository
	search   repository.SearchRepository
	embedder embedding.EmbeddingProviderInterface
}

func NewLifecycleService(deps LifecycleDependencies) LifecycleService {
	return &lifecycleService{semantic: deps.Semantic, evidence: deps.Evidence, search: deps.Search, embedder: deps.Embedder}
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
	CheckAfterSeconds    int                                      `json:"-"`
	StatusTool           string                                   `json:"-"`
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
	semanticInput := repository.CorrectRelationshipInput{
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
	var result *repository.CorrectRelationshipResult
	var err error
	if synchronous, ok := s.semantic.(synchronousCorrectionSemanticRepository); ok && s.embedder != nil {
		var plan *repository.RelationshipCorrectionEmbeddingPlan
		plan, err = synchronous.PlanRelationshipCorrectionEmbeddings(ctx, semanticInput)
		if err == nil && plan == nil {
			err = errors.New("memory lifecycle: nil relationship correction embedding plan")
		}
		if err == nil && len(plan.Documents) > 0 {
			var embedded []repository.SearchDocumentEmbedding
			embedded, err = s.embedRelationshipDocumentBatch(ctx, plan.Documents)
			if err == nil {
				results := make([]repository.InlineEmbeddingResult, 0, len(embedded))
				for _, embedding := range embedded {
					results = append(results, repository.InlineEmbeddingResult{
						DocumentHash: embedding.DocumentHash,
						Embedding:    append([]float32(nil), embedding.Embedding...),
					})
				}
				result, err = synchronous.CorrectRelationshipWithEmbeddings(ctx, semanticInput, results)
			}
		} else if err == nil {
			result, err = s.semantic.CorrectRelationship(ctx, semanticInput)
		}
	} else {
		semanticCtx := repository.WithInlineEmbeddingWrites(ctx)
		if s.embedder != nil {
			semanticCtx = repository.WithInlineEmbeddingBatch(semanticCtx, s.embedRelationshipDocumentBatch)
		}
		result, err = s.semantic.CorrectRelationship(semanticCtx, semanticInput)
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
		Errors:           []SubmissionStatusError{},
		CorrelationID:    correlation.FromContext(ctx),
		CorrectionResult: result.Correction,
	}
	if receipt.SearchState == "" {
		receipt.SearchState = string(domain.SearchProjectionNotRequired)
	}
	if result.Confirmation != nil {
		receipt.AwaitingConfirmation = &SubmissionAwaitingConfirmation{
			ConfirmationToken: result.Confirmation.Token,
			ExpiresAt:         result.Confirmation.ExpiresAt,
			Candidates:        append([]repository.RelationshipCorrectionCandidate(nil), result.Confirmation.Candidates...),
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

func (s *lifecycleService) embedRelationshipDocumentBatch(
	ctx context.Context,
	documents []repository.SearchDocumentForEmbedding,
) ([]repository.SearchDocumentEmbedding, error) {
	if len(documents) == 0 {
		return []repository.SearchDocumentEmbedding{}, nil
	}
	if len(documents) > 256 {
		return nil, fmt.Errorf("%w: document limit exceeded", ErrLifecycleEmbeddingInvalid)
	}
	if s.embedder == nil || !s.embedder.IsAvailable() {
		return nil, ErrLifecycleEmbeddingUnavailable
	}
	texts := make([]string, len(documents))
	for index := range documents {
		texts[index] = documents[index].DocumentText
	}
	embedCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	vectors, model, err := s.embedder.EmbedBatch(embedCtx, texts)
	if err != nil {
		if errors.Is(embedCtx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return nil, ErrLifecycleEmbeddingCancelled
		}
		if errors.Is(embedCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return nil, ErrLifecycleEmbeddingTimeout
		}
		return nil, fmt.Errorf("%w: provider request failed", ErrLifecycleEmbeddingUnavailable)
	}
	if len(vectors) != len(documents) || strings.TrimSpace(model) == "" || model != strings.TrimSpace(s.embedder.ModelName()) {
		return nil, ErrLifecycleEmbeddingInvalid
	}
	completed := make([]repository.SearchDocumentEmbedding, len(documents))
	for index, document := range documents {
		if len(vectors[index]) != document.EmbeddingDimensions {
			return nil, ErrLifecycleEmbeddingInvalid
		}
		for _, value := range vectors[index] {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return nil, ErrLifecycleEmbeddingInvalid
			}
		}
		completed[index] = repository.SearchDocumentEmbedding{
			SearchDocumentID: document.SearchDocumentID, SourceVersion: document.SourceVersion,
			DocumentHash:     document.DocumentHash,
			ProjectionFormat: document.ProjectionFormat, ProjectionGenerationID: document.ProjectionGenerationID,
			DocumentVersion: document.DocumentVersion, EmbeddingContractID: document.EmbeddingContractID,
			EmbeddingDimensions: document.EmbeddingDimensions, Embedding: vectors[index], SpaceID: document.SpaceID,
			SpaceGeneration: document.SpaceGeneration,
		}
	}
	return completed, nil
}

func translateRelationshipCorrectionError(err error) error {
	if errors.Is(err, ErrLifecycleEmbeddingUnavailable) ||
		errors.Is(err, ErrLifecycleEmbeddingInvalid) ||
		errors.Is(err, ErrLifecycleEmbeddingTimeout) ||
		errors.Is(err, ErrLifecycleEmbeddingCancelled) {
		return err
	}
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
