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
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

var (
	ErrLifecycleAuthContext = errors.New("memory lifecycle: authenticated actor context is required")
	ErrLifecyclePersistence = errors.New("memory lifecycle: persistence failed")
)

type LifecycleService interface {
	ResolveMemoryPlacement(ctx context.Context, req ResolveMemoryPlacementRequest) (*ResolveMemoryPlacementResult, error)
	CorrectEntityResolution(ctx context.Context, req CorrectEntityResolutionRequest) (*CorrectEntityResolutionResult, error)
	RetractEvidence(ctx context.Context, req RetractEvidenceRequest) (*RetractEvidenceResult, error)
}

type LifecycleDependencies struct {
	Semantic  LifecycleSemanticRepository
	Placement LifecyclePlacementRepository
	Evidence  LifecycleEvidenceRepository
	Auditor   SecurityRejectionAuditor
	Metrics   observability.DiscoverabilityMetrics
}

type LifecycleSemanticRepository interface {
	RetractRelationship(ctx context.Context, input repository.RetractRelationshipInput) (*repository.RelationshipTransitionResult, error)
	CorrectEntityResolution(ctx context.Context, input repository.CorrectEntityResolutionInput) (*repository.CorrectEntityResolutionResult, error)
}

type LifecyclePlacementRepository interface {
	ResolvePlacementReview(ctx context.Context, input repository.ResolvePlacementReviewInput) (*repository.ResolvePlacementReviewResult, error)
}

type LifecycleEvidenceRepository interface {
	RetractEvidence(ctx context.Context, input repository.RetractEvidenceInput) (*repository.EvidenceLifecycleResult, error)
}

type lifecycleService struct {
	semantic  LifecycleSemanticRepository
	placement LifecyclePlacementRepository
	evidence  LifecycleEvidenceRepository
	auditor   SecurityRejectionAuditor
	metrics   observability.DiscoverabilityMetrics
}

func NewLifecycleService(deps LifecycleDependencies) LifecycleService {
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	return &lifecycleService{semantic: deps.Semantic, placement: deps.Placement, evidence: deps.Evidence, auditor: deps.Auditor, metrics: metrics}
}

type ResolveMemoryPlacementRequest struct {
	ContractVersion      string                  `json:"contract_version"`
	Action               domain.ResolveAction    `json:"action"`
	IngestID             string                  `json:"ingest_id,omitempty"`
	PlacementItemID      string                  `json:"placement_item_id,omitempty"`
	PlacementItemVersion int                     `json:"placement_item_version,omitempty"`
	ObservationID        string                  `json:"observation_id,omitempty"`
	RelationshipID       string                  `json:"relationship_id,omitempty"`
	EntityRef            string                  `json:"entity_ref,omitempty"`
	CandidateEntityID    string                  `json:"candidate_entity_id,omitempty"`
	PredicateKey         string                  `json:"predicate_key,omitempty"`
	PredicateVersion     int                     `json:"predicate_version,omitempty"`
	Message              string                  `json:"message,omitempty"`
	Evidence             []RememberEvidenceInput `json:"evidence,omitempty"`
	IdempotencyKey       string                  `json:"idempotency_key,omitempty"`
}

type ResolveMemoryPlacementResult struct {
	DecisionID        string `json:"decision_id,omitempty"`
	IngestID          string `json:"ingest_id,omitempty"`
	ProcessingState   string `json:"processing_state"`
	ImpactSummary     string `json:"impact_summary,omitempty"`
	CheckAfterSeconds int    `json:"check_after_seconds,omitempty"`
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

func (s *lifecycleService) ResolveMemoryPlacement(
	ctx context.Context,
	req ResolveMemoryPlacementRequest,
) (*ResolveMemoryPlacementResult, error) {
	if strings.TrimSpace(req.ContractVersion) != domain.ContractVersion {
		return nil, fmt.Errorf("memory lifecycle: invalid contract_version %q", req.ContractVersion)
	}
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.ProfileID == uuid.Nil {
		return nil, ErrLifecycleAuthContext
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return nil, errors.New("memory lifecycle: idempotency_key is required")
	}
	switch req.Action {
	case domain.ResolveForget:
		return s.forgetRelationship(ctx, actor, req)
	case domain.ResolveAcknowledge,
		domain.ResolveSelectEntity,
		domain.ResolveConfirmNewEntity,
		domain.ResolveSelectPredicate,
		domain.ResolveAccept,
		domain.ResolveReject,
		domain.ResolveCorrect,
		domain.ResolveReleaseQuarantine:
		return s.resolvePlacementReview(ctx, actor, req)
	default:
		return nil, fmt.Errorf("memory lifecycle: unsupported action %q", req.Action)
	}
}

func (s *lifecycleService) resolvePlacementReview(
	ctx context.Context,
	actor requestctx.ActorProfile,
	req ResolveMemoryPlacementRequest,
) (*ResolveMemoryPlacementResult, error) {
	if s.placement == nil {
		return nil, errors.New("memory lifecycle: placement repository is required")
	}
	credential, _ := requestctx.ActorCredentialFromContext(ctx)
	if req.Action == domain.ResolveReleaseQuarantine && !lifecycleCanReleaseQuarantine(credential.Role) {
		return nil, errors.New("memory lifecycle: manager role is required to release quarantine")
	}
	if err := s.rejectUnsafeLifecycleEvidence(ctx, actor, "resolve_memory_placement", req.Evidence); err != nil {
		return nil, err
	}
	resolved, err := s.placement.ResolvePlacementReview(ctx, repository.ResolvePlacementReviewInput{
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
		Evidence:             lifecycleEvidenceFromRequest(req.Evidence),
		IdempotencyKey:       req.IdempotencyKey,
	})
	if err != nil {
		return nil, err
	}
	if resolved.FirstDisposition != nil && resolved.FirstDisposition.IsRemember {
		observability.RecordRememberFirstDisposition(ctx, s.metrics, resolved.FirstDisposition.CompletedAt.Sub(resolved.FirstDisposition.CreatedAt), resolved.FirstDisposition.Status)
	}
	return &ResolveMemoryPlacementResult{
		DecisionID:        resolved.DecisionID,
		IngestID:          resolved.IngestID,
		ProcessingState:   resolved.Status,
		ImpactSummary:     resolved.ImpactSummary,
		CheckAfterSeconds: resolved.CheckAfterSeconds,
	}, nil
}

func (s *lifecycleService) forgetRelationship(
	ctx context.Context,
	actor requestctx.ActorProfile,
	req ResolveMemoryPlacementRequest,
) (*ResolveMemoryPlacementResult, error) {
	if s.semantic == nil {
		return nil, errors.New("memory lifecycle: semantic repository is required")
	}
	relationshipID := strings.TrimSpace(req.RelationshipID)
	if relationshipID == "" {
		return nil, errors.New("memory lifecycle: relationship_id is required")
	}
	reason := strings.TrimSpace(req.Message)
	if reason == "" {
		return nil, errors.New("memory lifecycle: message is required")
	}
	if len(req.Evidence) == 0 {
		return nil, errors.New("memory lifecycle: evidence is required")
	}
	transition, err := s.semantic.RetractRelationship(ctx, repository.RetractRelationshipInput{
		TeamID:         actor.TeamID.String(),
		OwnerProfileID: actor.ProfileID.String(),
		RelationshipID: relationshipID,
		Reason:         reason,
		IdempotencyKey: strings.TrimSpace(req.IdempotencyKey),
	})
	if err != nil {
		return nil, err
	}
	return &ResolveMemoryPlacementResult{
		DecisionID:      transition.TransitionID,
		ProcessingState: string(domain.PlacementRunCompleted),
		ImpactSummary:   fmt.Sprintf("relationship %s retracted from active semantic graph; nodes and append-only history preserved", relationshipID),
	}, nil
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
	if err := s.rejectUnsafeLifecycleEvidence(ctx, actor, "correct_entity_resolution", req.Evidence); err != nil {
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
		Evidence:               correctionEvidenceFromRequest(req.Evidence),
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

func correctionEvidenceFromRequest(evidence []RememberEvidenceInput) []repository.CorrectionEvidenceInput {
	if len(evidence) == 0 {
		return nil
	}
	out := make([]repository.CorrectionEvidenceInput, 0, len(evidence))
	for _, item := range evidence {
		out = append(out, repository.CorrectionEvidenceInput{
			Content:     item.Content,
			SourceType:  item.SourceType,
			Authority:   item.Authority,
			SourceGroup: correctionEvidenceSourceGroup(item),
			Metadata:    item.Metadata,
		})
	}
	return out
}

func (s *lifecycleService) rejectUnsafeLifecycleEvidence(
	ctx context.Context,
	actor requestctx.ActorProfile,
	surface string,
	evidence []RememberEvidenceInput,
) error {
	if len(evidence) == 0 {
		return nil
	}
	contents := make([]string, 0, len(evidence))
	for _, item := range evidence {
		contents = append(contents, item.Content)
	}
	scan, err := ScanSubmissionBatch(contents)
	if err == nil {
		return nil
	}
	if auditErr := recordSubmissionSecurityRejection(ctx, s.auditor, actor, surface, scan, err); auditErr != nil {
		return ErrLifecyclePersistence
	}
	return err
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

func lifecycleEvidenceFromRequest(evidence []RememberEvidenceInput) []repository.EvidenceInput {
	if len(evidence) == 0 {
		return nil
	}
	out := make([]repository.EvidenceInput, 0, len(evidence))
	sourceRevisionHashes := sourceRevisionContentHashes(evidence)
	for _, item := range evidence {
		event := submissionSecurityPassEvent()
		authority, metadata := ledgerAuthorityAndMetadata(item.Authority, item.Metadata)
		metadata = evidenceProcessingIntentMetadata(metadata, item)
		out = append(out, repository.EvidenceInput{
			Content:                       item.Content,
			SourceType:                    evidenceSourceType(item.SourceType),
			Authority:                     authority,
			SourceRef:                     strings.TrimSpace(item.Source),
			SourceKey:                     strings.TrimSpace(item.SourceKey),
			SourceRevisionToken:           strings.TrimSpace(item.SourceRevision),
			ExpectedPreviousRevisionToken: strings.TrimSpace(item.PreviousSourceRevision),
			SourceRevisionContentHash:     sourceRevisionHashes[sourceRevisionBatchKey(item)],
			SourceRevisionEnvelope:        sourceRevisionEnvelope(item),
			SupersedesEvidenceIDs:         append([]string(nil), item.SupersedesEvidenceIDs...),
			IdempotencyKey:                strings.TrimSpace(item.IdempotencyKey),
			Labels:                        append([]string(nil), item.Labels...),
			Metadata:                      metadata,
			InitialEvent:                  &event,
		})
	}
	return out
}

func lifecycleCanReleaseQuarantine(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "manager", "system":
		return true
	default:
		return false
	}
}
