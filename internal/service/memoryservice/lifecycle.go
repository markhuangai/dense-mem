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

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

var ErrLifecycleAuthContext = errors.New("memory lifecycle: authenticated actor context is required")

type LifecycleService interface {
	CorrectEntityResolution(ctx context.Context, req CorrectEntityResolutionRequest) (*CorrectEntityResolutionResult, error)
	RetractEvidence(ctx context.Context, req RetractEvidenceRequest) (*RetractEvidenceResult, error)
	RetractRelationship(ctx context.Context, req RetractRelationshipRequest) (*RetractRelationshipResult, error)
}

type LifecycleDependencies struct {
	Semantic LifecycleSemanticRepository
	Evidence LifecycleEvidenceRepository
}

type LifecycleSemanticRepository interface {
	CorrectEntityResolution(ctx context.Context, input repository.CorrectEntityResolutionInput) (*repository.CorrectEntityResolutionResult, error)
	RetractRelationship(ctx context.Context, input repository.RetractRelationshipInput) (*repository.RelationshipTransitionResult, error)
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

type CorrectEntityResolutionRequest struct {
	ContractVersion     string                        `json:"contract_version"`
	Operation           domain.EntityCorrectionAction `json:"operation"`
	SourceEntityID      string                        `json:"source_entity_id"`
	TargetEntityID      string                        `json:"target_entity_id,omitempty"`
	OwnedObservationIDs []string                      `json:"owned_observation_ids,omitempty"`
	DryRun              bool                          `json:"dry_run"`
	ImpactToken         string                        `json:"impact_token,omitempty"`
	Evidence            []RememberEvidenceInput       `json:"evidence,omitempty"`
	IdempotencyKey      string                        `json:"idempotency_key,omitempty"`
}

type CorrectEntityResolutionResult struct {
	DryRun                          bool             `json:"dry_run"`
	ImpactToken                     string           `json:"impact_token,omitempty"`
	SelectedObservationIDs          []string         `json:"selected_observation_ids"`
	BlockedObservationIDs           []string         `json:"blocked_observation_ids"`
	RelationshipChanges             []map[string]any `json:"relationship_changes"`
	EntityCandidates                []map[string]any `json:"entity_candidates"`
	UnchangedCrossProfileReferences []map[string]any `json:"unchanged_cross_profile_references"`
}

type RetractEvidenceRequest struct {
	ContractVersion string   `json:"contract_version"`
	EvidenceIDs     []string `json:"evidence_ids"`
	Reason          string   `json:"reason"`
	IdempotencyKey  string   `json:"idempotency_key"`
}

type RetractEvidenceResult struct {
	DecisionID                      string   `json:"decision_id"`
	ProcessingState                 string   `json:"processing_state"`
	RetractedEvidenceIDs            []string `json:"retracted_evidence_ids"`
	AffectedRelationshipCount       int      `json:"affected_relationship_count"`
	PendingRelationshipCount        int      `json:"pending_relationship_count"`
	RetainedActiveRelationshipCount int      `json:"retained_active_relationship_count"`
}

type RetractRelationshipRequest struct {
	ContractVersion string `json:"contract_version"`
	RelationshipID  string `json:"relationship_id"`
	Reason          string `json:"reason"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type RetractRelationshipResult struct {
	TransitionID   string `json:"transition_id"`
	RelationshipID string `json:"relationship_id"`
	FromStatus     string `json:"from_status"`
	ToStatus       string `json:"to_status"`
}

func (s *lifecycleService) CorrectEntityResolution(
	ctx context.Context,
	req CorrectEntityResolutionRequest,
) (*CorrectEntityResolutionResult, error) {
	if s.semantic == nil {
		return nil, errors.New("memory lifecycle: semantic repository is required")
	}
	if strings.TrimSpace(req.ContractVersion) != domain.ContractVersion {
		return nil, fmt.Errorf("memory lifecycle: invalid contract_version %q", req.ContractVersion)
	}
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.ProfileID == uuid.Nil {
		return nil, ErrLifecycleAuthContext
	}
	evidence, err := correctionEvidenceFromRequest(req.Evidence)
	if err != nil {
		return nil, err
	}
	result, err := s.semantic.CorrectEntityResolution(ctx, repository.CorrectEntityResolutionInput{
		TeamID:                 actor.TeamID.String(),
		OwnerProfileID:         actor.ProfileID.String(),
		Action:                 string(req.Operation),
		SourceEntityID:         req.SourceEntityID,
		TargetEntityID:         req.TargetEntityID,
		SelectedObservationIDs: req.OwnedObservationIDs,
		DryRun:                 req.DryRun,
		PlanToken:              req.ImpactToken,
		Evidence:               evidence,
		IdempotencyKey:         req.IdempotencyKey,
	})
	if err != nil {
		return nil, err
	}
	return &CorrectEntityResolutionResult{
		DryRun:                          result.DryRun,
		ImpactToken:                     result.PlanToken,
		SelectedObservationIDs:          result.SelectedObservationIDs,
		BlockedObservationIDs:           result.BlockedObservationIDs,
		RelationshipChanges:             []map[string]any{},
		EntityCandidates:                []map[string]any{},
		UnchangedCrossProfileReferences: []map[string]any{},
	}, nil
}

func (s *lifecycleService) RetractEvidence(
	ctx context.Context,
	req RetractEvidenceRequest,
) (*RetractEvidenceResult, error) {
	if s.evidence == nil {
		return nil, errors.New("memory lifecycle: evidence repository is required")
	}
	if strings.TrimSpace(req.ContractVersion) != domain.ContractVersion {
		return nil, fmt.Errorf("memory lifecycle: invalid contract_version %q", req.ContractVersion)
	}
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.ProfileID == uuid.Nil {
		return nil, ErrLifecycleAuthContext
	}
	requestHash, err := retractEvidenceRequestHash(req)
	if err != nil {
		return nil, err
	}
	result, err := s.evidence.RetractEvidence(ctx, repository.RetractEvidenceInput{
		TeamID:         actor.TeamID.String(),
		OwnerProfileID: actor.ProfileID.String(),
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

func (s *lifecycleService) RetractRelationship(
	ctx context.Context,
	req RetractRelationshipRequest,
) (*RetractRelationshipResult, error) {
	if s.semantic == nil {
		return nil, errors.New("memory lifecycle: semantic repository is required")
	}
	if strings.TrimSpace(req.ContractVersion) != domain.ContractVersion {
		return nil, fmt.Errorf("memory lifecycle: invalid contract_version %q", req.ContractVersion)
	}
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.ProfileID == uuid.Nil {
		return nil, ErrLifecycleAuthContext
	}
	result, err := s.semantic.RetractRelationship(ctx, repository.RetractRelationshipInput{
		TeamID:         actor.TeamID.String(),
		OwnerProfileID: actor.ProfileID.String(),
		RelationshipID: req.RelationshipID,
		Reason:         req.Reason,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		return nil, translateRelationshipLifecycleError(err)
	}
	return &RetractRelationshipResult{
		TransitionID:   result.TransitionID,
		RelationshipID: result.RelationshipID,
		FromStatus:     result.FromStatus,
		ToStatus:       result.ToStatus,
	}, nil
}

func retractEvidenceRequestHash(req RetractEvidenceRequest) (string, error) {
	evidenceIDs := make([]string, len(req.EvidenceIDs))
	for index, evidenceID := range req.EvidenceIDs {
		evidenceIDs[index] = strings.TrimSpace(evidenceID)
	}
	sort.Strings(evidenceIDs)
	payload, err := json.Marshal(map[string]any{
		"contract_version": req.ContractVersion,
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

func translateRelationshipLifecycleError(err error) error {
	switch {
	case errors.Is(err, repository.ErrSemanticOwnerMismatch), errors.Is(err, repository.ErrTeamInactive):
		return httperr.New(httperr.NOT_FOUND, "relationship not found")
	case errors.Is(err, repository.ErrSemanticIdempotencyConflict):
		return httperr.New(httperr.CONFLICT, "relationship lifecycle conflict")
	default:
		return err
	}
}

func correctionEvidenceFromRequest(evidence []RememberEvidenceInput) ([]repository.CorrectionEvidenceInput, error) {
	if len(evidence) == 0 {
		return nil, nil
	}
	out := make([]repository.CorrectionEvidenceInput, 0, len(evidence))
	for _, item := range evidence {
		if _, err := ScanSubmissionEvidence(item.Content); err != nil {
			return nil, err
		}
		out = append(out, repository.CorrectionEvidenceInput{
			Content:     item.Content,
			SourceType:  item.SourceType,
			Authority:   item.Authority,
			SourceGroup: correctionEvidenceSourceGroup(item),
			Metadata:    item.Metadata,
		})
	}
	return out, nil
}

func correctionEvidenceSourceGroup(item RememberEvidenceInput) string {
	if value := strings.TrimSpace(item.SourceGroup); value != "" {
		return value
	}
	if value := strings.TrimSpace(item.SourceKey); value != "" {
		return value
	}
	return strings.TrimSpace(item.Source)
}
