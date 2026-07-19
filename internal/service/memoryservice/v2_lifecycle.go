package memoryservice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

var ErrV2LifecycleAuthContext = errors.New("v2 lifecycle: authenticated actor context is required")

type V2LifecycleService interface {
	ResolveMemoryPlacementV2(ctx context.Context, req V2ResolveMemoryPlacementRequest) (*V2ResolveMemoryPlacementResult, error)
	CorrectEntityResolutionV2(ctx context.Context, req V2CorrectEntityResolutionRequest) (*V2CorrectEntityResolutionResult, error)
}

type V2LifecycleDependencies struct {
	Semantic  V2LifecycleSemanticRepository
	Placement V2LifecyclePlacementRepository
}

type V2LifecycleSemanticRepository interface {
	RetractRelationship(ctx context.Context, input repository.V2RetractRelationshipInput) (*repository.V2RelationshipTransitionResult, error)
	CorrectEntityResolution(ctx context.Context, input repository.V2CorrectEntityResolutionInput) (*repository.V2CorrectEntityResolutionResult, error)
}

type V2LifecyclePlacementRepository interface {
	ResolvePlacementReview(ctx context.Context, input repository.V2ResolvePlacementReviewInput) (*repository.V2ResolvePlacementReviewResult, error)
}

type v2LifecycleService struct {
	semantic  V2LifecycleSemanticRepository
	placement V2LifecyclePlacementRepository
}

func NewV2LifecycleService(deps V2LifecycleDependencies) V2LifecycleService {
	return &v2LifecycleService{semantic: deps.Semantic, placement: deps.Placement}
}

type V2ResolveMemoryPlacementRequest struct {
	ContractVersion      string                    `json:"contract_version"`
	Action               domain.V2ResolveAction    `json:"action"`
	IngestID             string                    `json:"ingest_id,omitempty"`
	PlacementItemID      string                    `json:"placement_item_id,omitempty"`
	PlacementItemVersion int                       `json:"placement_item_version,omitempty"`
	ObservationID        string                    `json:"observation_id,omitempty"`
	RelationshipID       string                    `json:"relationship_id,omitempty"`
	EntityRef            string                    `json:"entity_ref,omitempty"`
	CandidateEntityID    string                    `json:"candidate_entity_id,omitempty"`
	PredicateKey         string                    `json:"predicate_key,omitempty"`
	PredicateVersion     int                       `json:"predicate_version,omitempty"`
	Message              string                    `json:"message,omitempty"`
	Evidence             []V2RememberEvidenceInput `json:"evidence,omitempty"`
	IdempotencyKey       string                    `json:"idempotency_key,omitempty"`
}

type V2ResolveMemoryPlacementResult struct {
	DecisionID        string `json:"decision_id,omitempty"`
	IngestID          string `json:"ingest_id,omitempty"`
	ProcessingState   string `json:"processing_state"`
	ImpactSummary     string `json:"impact_summary,omitempty"`
	CheckAfterSeconds int    `json:"check_after_seconds,omitempty"`
}

type V2CorrectEntityResolutionRequest struct {
	ContractVersion     string                          `json:"contract_version"`
	Operation           domain.V2EntityCorrectionAction `json:"operation"`
	SourceEntityID      string                          `json:"source_entity_id"`
	TargetEntityID      string                          `json:"target_entity_id,omitempty"`
	OwnedObservationIDs []string                        `json:"owned_observation_ids,omitempty"`
	DryRun              bool                            `json:"dry_run"`
	ImpactToken         string                          `json:"impact_token,omitempty"`
	Evidence            []V2RememberEvidenceInput       `json:"evidence,omitempty"`
	IdempotencyKey      string                          `json:"idempotency_key,omitempty"`
}

type V2CorrectEntityResolutionResult struct {
	DryRun                          bool             `json:"dry_run"`
	ImpactToken                     string           `json:"impact_token,omitempty"`
	SelectedObservationIDs          []string         `json:"selected_observation_ids"`
	BlockedObservationIDs           []string         `json:"blocked_observation_ids"`
	RelationshipChanges             []map[string]any `json:"relationship_changes"`
	EntityCandidates                []map[string]any `json:"entity_candidates"`
	UnchangedCrossProfileReferences []map[string]any `json:"unchanged_cross_profile_references"`
}

func (s *v2LifecycleService) ResolveMemoryPlacementV2(
	ctx context.Context,
	req V2ResolveMemoryPlacementRequest,
) (*V2ResolveMemoryPlacementResult, error) {
	if strings.TrimSpace(req.ContractVersion) != domain.V2ContractVersion {
		return nil, fmt.Errorf("v2 lifecycle: invalid contract_version %q", req.ContractVersion)
	}
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.ProfileID == uuid.Nil {
		return nil, ErrV2LifecycleAuthContext
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return nil, errors.New("v2 lifecycle: idempotency_key is required")
	}
	switch req.Action {
	case domain.V2ResolveForget:
		return s.forgetRelationship(ctx, actor, req)
	case domain.V2ResolveAcknowledge,
		domain.V2ResolveSelectEntity,
		domain.V2ResolveConfirmNewEntity,
		domain.V2ResolveSelectPredicate,
		domain.V2ResolveAccept,
		domain.V2ResolveReject,
		domain.V2ResolveCorrect,
		domain.V2ResolveReleaseQuarantine:
		return s.resolvePlacementReview(ctx, actor, req)
	default:
		return nil, fmt.Errorf("v2 lifecycle: unsupported action %q", req.Action)
	}
}

func (s *v2LifecycleService) resolvePlacementReview(
	ctx context.Context,
	actor requestctx.ActorProfile,
	req V2ResolveMemoryPlacementRequest,
) (*V2ResolveMemoryPlacementResult, error) {
	if s.placement == nil {
		return nil, errors.New("v2 lifecycle: placement repository is required")
	}
	credential, _ := requestctx.ActorCredentialFromContext(ctx)
	if req.Action == domain.V2ResolveReleaseQuarantine && !v2LifecycleCanReleaseQuarantine(credential.Role) {
		return nil, errors.New("v2 lifecycle: manager role is required to release quarantine")
	}
	resolved, err := s.placement.ResolvePlacementReview(ctx, repository.V2ResolvePlacementReviewInput{
		TeamID:               actor.TeamID.String(),
		OwnerProfileID:       actor.ProfileID.String(),
		ActorRole:            credential.Role,
		Action:               string(req.Action),
		IngestID:             req.IngestID,
		PlacementItemID:      req.PlacementItemID,
		PlacementItemVersion: req.PlacementItemVersion,
		ObservationID:        req.ObservationID,
		EntityRef:            req.EntityRef,
		CandidateEntityID:    req.CandidateEntityID,
		PredicateKey:         req.PredicateKey,
		PredicateVersion:     req.PredicateVersion,
		Message:              req.Message,
		Evidence:             v2LifecycleEvidenceFromRequest(req.Evidence),
		IdempotencyKey:       req.IdempotencyKey,
	})
	if err != nil {
		return nil, err
	}
	return &V2ResolveMemoryPlacementResult{
		DecisionID:        resolved.DecisionID,
		IngestID:          resolved.IngestID,
		ProcessingState:   resolved.Status,
		ImpactSummary:     resolved.ImpactSummary,
		CheckAfterSeconds: resolved.CheckAfterSeconds,
	}, nil
}

func (s *v2LifecycleService) forgetRelationship(
	ctx context.Context,
	actor requestctx.ActorProfile,
	req V2ResolveMemoryPlacementRequest,
) (*V2ResolveMemoryPlacementResult, error) {
	if s.semantic == nil {
		return nil, errors.New("v2 lifecycle: semantic repository is required")
	}
	relationshipID := strings.TrimSpace(req.RelationshipID)
	if relationshipID == "" {
		return nil, errors.New("v2 lifecycle: relationship_id is required")
	}
	reason := strings.TrimSpace(req.Message)
	if reason == "" {
		return nil, errors.New("v2 lifecycle: message is required")
	}
	if len(req.Evidence) == 0 {
		return nil, errors.New("v2 lifecycle: evidence is required")
	}
	transition, err := s.semantic.RetractRelationship(ctx, repository.V2RetractRelationshipInput{
		TeamID:         actor.TeamID.String(),
		OwnerProfileID: actor.ProfileID.String(),
		RelationshipID: relationshipID,
		Reason:         reason,
		IdempotencyKey: strings.TrimSpace(req.IdempotencyKey),
	})
	if err != nil {
		return nil, err
	}
	return &V2ResolveMemoryPlacementResult{
		DecisionID:      transition.TransitionID,
		ProcessingState: string(domain.V2PlacementRunCompleted),
		ImpactSummary:   fmt.Sprintf("relationship %s retracted from active semantic graph; nodes and append-only history preserved", relationshipID),
	}, nil
}

func (s *v2LifecycleService) CorrectEntityResolutionV2(
	ctx context.Context,
	req V2CorrectEntityResolutionRequest,
) (*V2CorrectEntityResolutionResult, error) {
	if s.semantic == nil {
		return nil, errors.New("v2 lifecycle: semantic repository is required")
	}
	if strings.TrimSpace(req.ContractVersion) != domain.V2ContractVersion {
		return nil, fmt.Errorf("v2 lifecycle: invalid contract_version %q", req.ContractVersion)
	}
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.ProfileID == uuid.Nil {
		return nil, ErrV2LifecycleAuthContext
	}
	result, err := s.semantic.CorrectEntityResolution(ctx, repository.V2CorrectEntityResolutionInput{
		TeamID:                 actor.TeamID.String(),
		OwnerProfileID:         actor.ProfileID.String(),
		Action:                 string(req.Operation),
		SourceEntityID:         req.SourceEntityID,
		TargetEntityID:         req.TargetEntityID,
		SelectedObservationIDs: req.OwnedObservationIDs,
		DryRun:                 req.DryRun,
		PlanToken:              req.ImpactToken,
		Evidence:               v2CorrectionEvidenceFromRequest(req.Evidence),
		IdempotencyKey:         req.IdempotencyKey,
	})
	if err != nil {
		return nil, err
	}
	return &V2CorrectEntityResolutionResult{
		DryRun:                          result.DryRun,
		ImpactToken:                     result.PlanToken,
		SelectedObservationIDs:          result.SelectedObservationIDs,
		BlockedObservationIDs:           result.BlockedObservationIDs,
		RelationshipChanges:             []map[string]any{},
		EntityCandidates:                []map[string]any{},
		UnchangedCrossProfileReferences: []map[string]any{},
	}, nil
}

func v2CorrectionEvidenceFromRequest(evidence []V2RememberEvidenceInput) []repository.V2CorrectionEvidenceInput {
	if len(evidence) == 0 {
		return nil
	}
	out := make([]repository.V2CorrectionEvidenceInput, 0, len(evidence))
	for _, item := range evidence {
		out = append(out, repository.V2CorrectionEvidenceInput{
			Content:     item.Content,
			SourceType:  item.SourceType,
			Authority:   item.Authority,
			SourceGroup: item.SourceKey,
			Metadata:    item.Metadata,
		})
	}
	return out
}

func v2LifecycleEvidenceFromRequest(evidence []V2RememberEvidenceInput) []repository.V2EvidenceInput {
	if len(evidence) == 0 {
		return nil
	}
	out := make([]repository.V2EvidenceInput, 0, len(evidence))
	sourceRevisionHashes := v2SourceRevisionContentHashes(evidence)
	for _, item := range evidence {
		scan := scanV2Evidence(item.Content)
		authority, metadata := v2LedgerAuthorityAndMetadata(item.Authority, item.Metadata)
		metadata = v2EvidenceProcessingIntentMetadata(metadata, item)
		out = append(out, repository.V2EvidenceInput{
			Content:                       item.Content,
			SourceType:                    v2EvidenceSourceType(item.SourceType),
			Authority:                     authority,
			SourceRef:                     strings.TrimSpace(item.Source),
			SourceKey:                     strings.TrimSpace(item.SourceKey),
			SourceRevisionToken:           strings.TrimSpace(item.SourceRevision),
			ExpectedPreviousRevisionToken: strings.TrimSpace(item.PreviousSourceRevision),
			SourceRevisionContentHash:     sourceRevisionHashes[v2SourceRevisionBatchKey(item)],
			SourceRevisionEnvelope:        v2SourceRevisionEnvelope(item),
			Labels:                        append([]string(nil), item.Labels...),
			Metadata:                      metadata,
			InitialEvent:                  &scan,
		})
	}
	return out
}

func v2LifecycleCanReleaseQuarantine(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "manager", "system":
		return true
	default:
		return false
	}
}
