// Package memoryservice orchestrates high-level memory workflows on top of
// Dense-Mem's lower-level fragment, claim, verification, and fact services.
package memoryservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/placementreview"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/assertionservice"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

const (
	defaultReflectLimit = 20
	maxReflectLimit     = 100
)

// PersonalPredicates is the curated predicate allow-list for high-level
// personal memory promotion. Low-level claim tools remain available for
// advanced pipelines.
var PersonalPredicates = map[string]struct{}{
	"prefers":         {},
	"identity_is":     {},
	"profile_fact":    {},
	"works_on":        {},
	"has_goal":        {},
	"corrected":       {},
	"has_skill":       {},
	"knows":           {},
	"relationship_to": {},
	"uses":            {},
	"likes":           {},
	"works_at":        {},
}

// Service coordinates high-level memory ingestion, reflection, and
// clarification confirmation.
type Service interface {
	Remember(ctx context.Context, profileID string, req RememberRequest) (*RememberResult, error)
	GetMemoryPlacement(ctx context.Context, profileID string, req PlacementStatusRequest) (*PlacementStatusResult, error)
	DisputeMemoryPlacement(ctx context.Context, profileID string, req DisputeRequest) (*DisputeResult, error)
	ImportMemories(ctx context.Context, profileID string, req ImportRequest) (*RememberResult, error)
	Reflect(ctx context.Context, profileID string, req ReflectRequest) (*ReflectResult, error)
	ConfirmMemory(ctx context.Context, profileID string, req ConfirmRequest) (*ConfirmResult, error)
}

type PlacementResolver interface {
	ResolveMemoryPlacement(ctx context.Context, profileID string, req ResolvePlacementRequest) (*ResolvePlacementResult, error)
}

// Dependencies are the lower-level services used by the memory orchestrator.
type Dependencies struct {
	FragmentCreate     fragmentservice.CreateFragmentService
	FragmentQuarantine fragmentservice.QuarantinedFragmentCreateService
	ClaimCreate        claimservice.CreateClaimService
	ClaimVerify        claimservice.VerifyClaimService
	ClaimGet           claimservice.GetClaimService
	ClaimList          claimservice.ListClaimsService
	FactPromote        factservice.PromoteClaimService
	FactConfirm        factservice.ConfirmMemoryService
	FactList           factservice.ListFactsService
	PlacementStore     repository.MemoryPlacementRepository
	Assertions         *assertionservice.Service
	GraphReviewer      placementreview.Reviewer
	Verifier           verifier.Verifier
	Embedder           embedding.EmbeddingProviderInterface
	VerifierModel      string
	Metrics            observability.DiscoverabilityMetrics
	Logger             *slog.Logger
}

// New constructs a high-level memory Service.
func New(deps Dependencies) *service {
	if deps.Metrics == nil {
		deps.Metrics = observability.NoopDiscoverabilityMetrics()
	}
	return &service{
		deps:          deps,
		placementKick: make(chan struct{}, 1),
	}
}

type service struct {
	deps          Dependencies
	placementKick chan struct{}
}

// TypedClaimInput is a trusted migration or verifier-extracted memory
// candidate. Normal clients submit only evidence through remember.
type TypedClaimInput struct {
	Subject           string         `json:"subject"`
	Predicate         string         `json:"predicate"`
	Object            string         `json:"object"`
	Modality          string         `json:"modality,omitempty"`
	Polarity          string         `json:"polarity,omitempty"`
	Speaker           string         `json:"speaker,omitempty"`
	ExtractConf       float64        `json:"extract_conf"`
	ResolutionConf    float64        `json:"resolution_conf"`
	IdempotencyKey    string         `json:"idempotency_key,omitempty"`
	ValidFrom         *time.Time     `json:"valid_from,omitempty"`
	ValidTo           *time.Time     `json:"valid_to,omitempty"`
	SupportedBy       []string       `json:"supported_by,omitempty"`
	ExtractionModel   string         `json:"extraction_model,omitempty"`
	ExtractionVersion string         `json:"extraction_version,omitempty"`
	PipelineRunID     string         `json:"pipeline_run_id,omitempty"`
	Classification    map[string]any `json:"classification,omitempty"`
}

// ImportRequest stores summarized historical memory. Auto-promotion defaults
// to false for imports unless explicitly requested.
type ImportRequest struct {
	Summary        string            `json:"summary"`
	Source         string            `json:"source,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Labels         []string          `json:"labels,omitempty"`
	Metadata       map[string]any    `json:"metadata,omitempty"`
	Claims         []TypedClaimInput `json:"claims,omitempty"`
	AutoPromote    *bool             `json:"auto_promote,omitempty"`
}

// ClaimOutcome describes one typed claim's ingestion pipeline result.
type ClaimOutcome struct {
	ClaimID         string       `json:"claim_id,omitempty"`
	Subject         string       `json:"subject"`
	Predicate       string       `json:"predicate"`
	Object          string       `json:"object"`
	Status          string       `json:"status"`
	Duplicate       bool         `json:"duplicate,omitempty"`
	DuplicateOf     string       `json:"duplicate_of,omitempty"`
	Verification    string       `json:"verification,omitempty"`
	Promotion       string       `json:"promotion,omitempty"`
	Fact            *domain.Fact `json:"fact,omitempty"`
	Error           string       `json:"error,omitempty"`
	ClarificationID string       `json:"clarification_id,omitempty"`
}

// FragmentOutcome summarizes the persisted evidence fragment.
type FragmentOutcome struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	DuplicateOf string    `json:"duplicate_of,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// RememberResult is the shared output for remember and import_memories.
type RememberResult struct {
	IngestID          string                       `json:"ingest_id,omitempty"`
	Status            string                       `json:"status,omitempty"`
	CheckAfterSeconds int                          `json:"check_after_seconds,omitempty"`
	StatusTool        string                       `json:"status_tool,omitempty"`
	Evidence          []FragmentOutcome            `json:"evidence,omitempty"`
	Items             []domain.MemoryPlacementItem `json:"items,omitempty"`
	Fragment          FragmentOutcome              `json:"fragment,omitempty"`
	Claims            []ClaimOutcome               `json:"claims,omitempty"`
	Clarifications    []Clarification              `json:"clarifications,omitempty"`
}

// Clarification tells the host LLM what to ask the user.
type Clarification struct {
	ID               string         `json:"id"`
	Type             string         `json:"type"`
	Question         string         `json:"question"`
	ClaimID          string         `json:"claim_id,omitempty"`
	Candidate        *MemoryTriple  `json:"candidate,omitempty"`
	ConflictingFacts []*domain.Fact `json:"conflicting_facts,omitempty"`
	Options          []string       `json:"options,omitempty"`
}

// MemoryTriple is a compact subject-predicate-object statement.
type MemoryTriple struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

// ReflectRequest controls memory reflection output.
type ReflectRequest struct {
	Limit          int `json:"limit,omitempty"`
	StaleAfterDays int `json:"stale_after_days,omitempty"`
}

// ReflectResult summarizes high-level memory state for the profile.
type ReflectResult struct {
	Facts           []*domain.Fact  `json:"facts"`
	CandidateClaims []*domain.Claim `json:"candidate_claims"`
	DisputedClaims  []*domain.Claim `json:"disputed_claims"`
	StaleFacts      []*domain.Fact  `json:"stale_facts"`
	Clarifications  []Clarification `json:"clarifications"`
}

// ConfirmRequest applies the user's answer to a clarification.
type ConfirmRequest struct {
	ClaimID  string `json:"claim_id"`
	Decision string `json:"decision"`
}

// ConfirmResult is returned by confirm_memory.
type ConfirmResult struct {
	ClaimID  string       `json:"claim_id"`
	Decision string       `json:"decision"`
	Status   string       `json:"status"`
	Fact     *domain.Fact `json:"fact,omitempty"`
}

// ImportMemories stores summarized historical memory. Auto-promotion defaults
// to false to keep bulk imports reviewable.
func (s *service) ImportMemories(ctx context.Context, profileID string, req ImportRequest) (*RememberResult, error) {
	autoPromote := false
	if req.AutoPromote != nil {
		autoPromote = *req.AutoPromote
	}
	return s.ingest(ctx, profileID, ingestRequest{
		content:        req.Summary,
		source:         req.Source,
		sourceType:     "document",
		authority:      "secondary",
		idempotencyKey: req.IdempotencyKey,
		labels:         req.Labels,
		metadata:       req.Metadata,
		claims:         req.Claims,
		autoPromote:    autoPromote,
	})
}

type ingestRequest struct {
	content        string
	source         string
	sourceType     string
	authority      string
	idempotencyKey string
	labels         []string
	metadata       map[string]any
	claims         []TypedClaimInput
	autoPromote    bool
}

func (s *service) ingest(ctx context.Context, profileID string, req ingestRequest) (*RememberResult, error) {
	if s.deps.FragmentCreate == nil {
		return nil, errors.New("memory service: fragment create service is required")
	}
	if req.content == "" {
		return nil, errors.New("memory service: content is required")
	}

	fragmentRes, err := s.deps.FragmentCreate.Create(ctx, profileID, &dto.CreateFragmentRequest{
		Content:        req.content,
		SourceType:     req.sourceType,
		Source:         req.source,
		Authority:      req.authority,
		IdempotencyKey: req.idempotencyKey,
		Labels:         req.labels,
		Metadata:       req.metadata,
		SourceQuality:  defaultSourceQuality(req.authority),
	})
	if err != nil {
		return nil, err
	}

	fragmentStatus := "created"
	if fragmentRes.Duplicate {
		fragmentStatus = "duplicate"
	}
	result := &RememberResult{
		Fragment: FragmentOutcome{
			ID:          fragmentRes.Fragment.FragmentID,
			Status:      fragmentStatus,
			DuplicateOf: fragmentRes.DuplicateOf,
			CreatedAt:   fragmentRes.Fragment.CreatedAt,
		},
		Claims: make([]ClaimOutcome, 0, len(req.claims)),
	}

	for _, in := range req.claims {
		outcome, clarifications := s.processClaim(ctx, profileID, fragmentRes.Fragment.FragmentID, in, req.autoPromote)
		result.Claims = append(result.Claims, outcome)
		result.Clarifications = append(result.Clarifications, clarifications...)
	}

	return result, nil
}

func (s *service) processClaim(ctx context.Context, profileID, fragmentID string, in TypedClaimInput, autoPromote bool) (ClaimOutcome, []Clarification) {
	out := ClaimOutcome{
		Subject:   in.Subject,
		Predicate: in.Predicate,
		Object:    in.Object,
	}
	if in.Subject == "" || in.Predicate == "" || in.Object == "" {
		out.Status = "invalid"
		out.Error = "subject, predicate, and object are required"
		return out, nil
	}
	if err := validateTypedClaimInput(in); err != nil {
		out.Status = "invalid"
		out.Error = err.Error()
		return out, nil
	}
	if !IsPersonalPredicate(in.Predicate) {
		out.Status = "predicate_not_supported"
		out.Error = "predicate is not in the high-level personal-memory allow-list"
		return out, nil
	}
	if s.deps.ClaimCreate == nil {
		out.Status = "error"
		out.Error = "claim create service is required"
		return out, nil
	}

	claim := claimFromInput(in, fragmentID)
	createRes, err := s.deps.ClaimCreate.Create(ctx, profileID, claim)
	if err != nil {
		out.Status = "claim_error"
		out.Error = err.Error()
		return out, nil
	}

	out.ClaimID = createRes.Claim.ClaimID
	out.Status = string(createRes.Claim.Status)
	out.Duplicate = createRes.Duplicate
	out.DuplicateOf = createRes.DuplicateOf

	verified := createRes.Claim
	if s.deps.ClaimVerify != nil {
		verified, err = s.deps.ClaimVerify.Verify(ctx, profileID, createRes.Claim.ClaimID)
		if err != nil {
			out.Verification = "error"
			out.Error = err.Error()
			return out, nil
		}
		out.Verification = string(verified.EntailmentVerdict)
		out.Status = string(verified.Status)
	}

	if !autoPromote || s.deps.FactPromote == nil || verified.Status != domain.StatusValidated {
		return out, nil
	}

	fact, err := s.deps.FactPromote.Promote(ctx, profileID, verified.ClaimID)
	if err == nil {
		out.Promotion = "promoted"
		out.Fact = fact
		out.Status = "promoted"
		return out, nil
	}

	out.Promotion = promotionStatus(err)
	out.Error = err.Error()
	if errors.Is(err, factservice.ErrPromotionDeferredDisputed) {
		clarification := s.buildClarification(ctx, profileID, verified)
		out.ClarificationID = clarification.ID
		return out, []Clarification{clarification}
	}
	return out, nil
}

func claimFromInput(in TypedClaimInput, fragmentID string) *domain.Claim {
	modality := domain.ClaimModality(in.Modality)
	if modality == "" {
		modality = domain.ModalityAssertion
	}
	polarity := domain.ClaimPolarity(in.Polarity)
	if polarity == "" {
		polarity = domain.PolarityPlus
	}
	supportedBy := append([]string(nil), in.SupportedBy...)
	if len(supportedBy) == 0 {
		supportedBy = []string{fragmentID}
	}

	return &domain.Claim{
		Subject:           in.Subject,
		Predicate:         in.Predicate,
		Object:            in.Object,
		Modality:          modality,
		Polarity:          polarity,
		Speaker:           in.Speaker,
		ExtractConf:       in.ExtractConf,
		ResolutionConf:    in.ResolutionConf,
		IdempotencyKey:    in.IdempotencyKey,
		ValidFrom:         in.ValidFrom,
		ValidTo:           in.ValidTo,
		SupportedBy:       supportedBy,
		ExtractionModel:   in.ExtractionModel,
		ExtractionVersion: in.ExtractionVersion,
		PipelineRunID:     in.PipelineRunID,
		Classification:    in.Classification,
	}
}

func promotionStatus(err error) string {
	switch {
	case errors.Is(err, factservice.ErrPromotionDeferredDisputed):
		return "clarification_required"
	case errors.Is(err, factservice.ErrPromotionRejected):
		return "rejected_weaker"
	case errors.Is(err, factservice.ErrGateRejected):
		return "gate_rejected"
	case errors.Is(err, factservice.ErrPredicateNotPoliced):
		return "predicate_not_supported"
	case errors.Is(err, factservice.ErrClaimNotValidated):
		return "not_validated"
	default:
		return "error"
	}
}

func (s *service) buildClarification(ctx context.Context, profileID string, claim *domain.Claim) Clarification {
	conflicts := s.activeFacts(ctx, profileID, claim.Subject, claim.Predicate)
	id := fmt.Sprintf("clarify:%s", claim.ClaimID)
	return Clarification{
		ID:       id,
		Type:     "memory_conflict",
		Question: fmt.Sprintf("Which memory should Dense-Mem keep for %s %s?", claim.Subject, claim.Predicate),
		ClaimID:  claim.ClaimID,
		Candidate: &MemoryTriple{
			Subject:   claim.Subject,
			Predicate: claim.Predicate,
			Object:    claim.Object,
		},
		ConflictingFacts: conflicts,
		Options:          []string{"accept_claim", "keep_existing"},
	}
}

func (s *service) activeFacts(ctx context.Context, profileID, subject, predicate string) []*domain.Fact {
	if s.deps.FactList == nil {
		return nil
	}
	facts, _, err := s.deps.FactList.List(ctx, profileID, factservice.FactListFilters{
		Subject:   subject,
		Predicate: predicate,
		Status:    domain.FactStatusActive,
	}, 20, "")
	if err != nil {
		return nil
	}
	return facts
}

// Reflect summarizes facts and unresolved claim states.
func (s *service) Reflect(ctx context.Context, profileID string, req ReflectRequest) (*ReflectResult, error) {
	limit := clampReflectLimit(req.Limit)
	result := &ReflectResult{}

	if s.deps.FactList != nil {
		facts, _, err := s.deps.FactList.List(ctx, profileID, factservice.FactListFilters{}, limit, "")
		if err != nil {
			return nil, err
		}
		result.Facts = facts
		for _, fact := range facts {
			if fact.Status == domain.FactStatusNeedsRevalidation || isStale(fact, req.StaleAfterDays) {
				result.StaleFacts = append(result.StaleFacts, fact)
			}
		}
	}

	if s.deps.ClaimList != nil {
		claims, _, err := s.deps.ClaimList.List(ctx, profileID, limit, 0)
		if err != nil {
			return nil, err
		}
		for _, claim := range claims {
			switch claim.Status {
			case domain.StatusCandidate:
				result.CandidateClaims = append(result.CandidateClaims, claim)
			case domain.StatusDisputed:
				result.DisputedClaims = append(result.DisputedClaims, claim)
				result.Clarifications = append(result.Clarifications, s.buildClarification(ctx, profileID, claim))
			}
		}
	}

	return result, nil
}

// ConfirmMemory applies a user clarification through the fact confirmation
// service.
func (s *service) ConfirmMemory(ctx context.Context, profileID string, req ConfirmRequest) (*ConfirmResult, error) {
	if s.deps.FactConfirm == nil {
		return nil, errors.New("memory service: fact confirm service is required")
	}
	res, err := s.deps.FactConfirm.ConfirmMemory(ctx, profileID, factservice.ConfirmMemoryRequest{
		ClaimID:  req.ClaimID,
		Decision: req.Decision,
	})
	if err != nil {
		return nil, err
	}
	return &ConfirmResult{
		ClaimID:  res.ClaimID,
		Decision: res.Decision,
		Status:   res.Status,
		Fact:     res.Fact,
	}, nil
}

// IsPersonalPredicate reports whether a predicate belongs to the high-level
// personal-memory allow-list.
func IsPersonalPredicate(predicate string) bool {
	_, ok := PersonalPredicates[predicate]
	return ok
}

func validateTypedClaimInput(in TypedClaimInput) error {
	switch {
	case strings.TrimSpace(in.Subject) == "":
		return errors.New("subject is required")
	case len(in.Subject) > 256:
		return errors.New("subject exceeds maximum length of 256")
	case strings.TrimSpace(in.Predicate) == "":
		return errors.New("predicate is required")
	case strings.TrimSpace(in.Object) == "":
		return errors.New("object is required")
	case len(in.Object) > 1024:
		return errors.New("object exceeds maximum length of 1024")
	case len(in.Speaker) > 256:
		return errors.New("speaker exceeds maximum length of 256")
	case len(in.IdempotencyKey) > 128:
		return errors.New("idempotency_key exceeds maximum length of 128")
	case len(in.ExtractionModel) > 128:
		return errors.New("extraction_model exceeds maximum length of 128")
	case len(in.ExtractionVersion) > 64:
		return errors.New("extraction_version exceeds maximum length of 64")
	case len(in.PipelineRunID) > 128:
		return errors.New("pipeline_run_id exceeds maximum length of 128")
	}
	if in.Modality != "" && !domain.ClaimModality(in.Modality).IsValid() {
		return errors.New("modality must be one of [assertion question proposal speculation quoted]")
	}
	if in.Polarity != "" && !domain.ClaimPolarity(in.Polarity).IsValid() {
		return errors.New("polarity must be one of [+ -]")
	}
	if !validConfidence(in.ExtractConf) {
		return errors.New("extract_conf must be between 0 and 1")
	}
	if !validConfidence(in.ResolutionConf) {
		return errors.New("resolution_conf must be between 0 and 1")
	}
	return nil
}

func validConfidence(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func clampReflectLimit(limit int) int {
	if limit <= 0 {
		return defaultReflectLimit
	}
	if limit > maxReflectLimit {
		return maxReflectLimit
	}
	return limit
}

func defaultSourceQuality(authority string) float64 {
	switch authority {
	case "primary", "authoritative":
		return 0.95
	case "secondary":
		return 0.75
	default:
		return 0.5
	}
}

func isStale(fact *domain.Fact, staleAfterDays int) bool {
	if staleAfterDays <= 0 || fact == nil || fact.RecordedAt.IsZero() {
		return false
	}
	return time.Since(fact.RecordedAt) > time.Duration(staleAfterDays)*24*time.Hour
}
