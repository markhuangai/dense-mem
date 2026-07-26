package memoryservice

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

const (
	defaultRecallResultLimit      = 10
	maxRecallResultLimit          = 50
	defaultRelatedHypothesisLimit = 5
	defaultCommunityPathLimit     = 5
)

var ErrRecallAuthContext = errors.New("recall: authenticated actor context is required")

type RecallService interface {
	Recall(ctx context.Context, req RecallRequest) (*RecallResult, error)
}

type RecallDependencies struct {
	Search          RecallSearchRepository
	Provider        embedding.EmbeddingProviderInterface
	Hypotheses      RecallHypothesisRepository
	Communities     RecallCommunityRepository
	CommunityConfig RecallCommunityConfigProvider
}

type RecallSearchRepository interface {
	repository.RecallRepository
	GetActiveSearchContract(ctx context.Context) (*repository.ActiveSearchContract, error)
}

type RecallHypothesisRepository interface {
	RecallHypotheses(ctx context.Context, input repository.RecallHypothesesInput) ([]repository.HypothesisRecord, error)
	RefreshHypothesisStaleness(ctx context.Context, input repository.RefreshHypothesisStalenessInput) (int, error)
}

type RecallCommunityRepository interface {
	RecallCommunityDiscovery(ctx context.Context, input repository.CommunityDiscoveryInput) ([]repository.CommunityDiscoveryPath, error)
	RefreshCommunityStaleness(ctx context.Context, input repository.CommunityStalenessInput) (int, error)
}

type RecallCommunityConfigProvider interface {
	CommunityDetectionRuntimeConfig(ctx context.Context) (domain.CommunityDetectionRuntimeConfig, error)
}

type recallService struct {
	search          RecallSearchRepository
	provider        embedding.EmbeddingProviderInterface
	hypotheses      RecallHypothesisRepository
	communities     RecallCommunityRepository
	communityConfig RecallCommunityConfigProvider
}

func NewRecallService(deps RecallDependencies) RecallService {
	return &recallService{
		search:          deps.Search,
		provider:        deps.Provider,
		hypotheses:      deps.Hypotheses,
		communities:     deps.Communities,
		communityConfig: deps.CommunityConfig,
	}
}

type RecallRequest struct {
	ContractVersion      string     `json:"contract_version"`
	Query                string     `json:"query"`
	Limit                int        `json:"limit,omitempty"`
	ValidAt              *time.Time `json:"valid_at,omitempty"`
	KnownAt              *time.Time `json:"known_at,omitempty"`
	KnownEvidenceIDs     []string   `json:"known_evidence_ids,omitempty"`
	KnownRelationshipIDs []string   `json:"known_relationship_ids,omitempty"`
	ExpandFromEntityIDs  []string   `json:"expand_from_entity_ids,omitempty"`
}

type RecallResult struct {
	RecallID          string                     `json:"recall_id"`
	Results           []RecallResultItem         `json:"results"`
	Conflicts         []RecallConflictSummary    `json:"conflicts"`
	DiscoveryPaths    []RecallDiscoveryPath      `json:"discovery_paths"`
	DiscoveryGuidance string                     `json:"discovery_guidance"`
	RelatedHypotheses []RelatedHypothesisSummary `json:"related_hypotheses"`
	Degradation       *RecallDegradationResult   `json:"degradation,omitempty"`
	SearchState       string                     `json:"search_state"`
}

type RecallResultItem struct {
	EvidenceID      string   `json:"evidence_id"`
	RelationshipIDs []string `json:"relationship_ids,omitempty"`
	Rank            int      `json:"rank"`
	Context         string   `json:"context,omitempty"`
}

type RecallDiscoveryPath struct {
	Relationships []RecallRelationshipHandle `json:"relationships"`
	EvidenceIDs   []string                   `json:"evidence_ids"`
}

type RecallConflictSummary struct {
	ConflictID          string                   `json:"conflict_id"`
	Version             int                      `json:"version"`
	Kind                string                   `json:"kind"`
	Status              string                   `json:"status"`
	Question            string                   `json:"question"`
	ReviewDueAt         *time.Time               `json:"review_due_at"`
	EffectiveAt         *time.Time               `json:"effective_at"`
	EffectiveTimeBasis  string                   `json:"effective_time_basis,omitempty"`
	PreferredPositionID string                   `json:"preferred_position_id,omitempty"`
	Positions           []RecallConflictPosition `json:"positions"`
	PositionsTruncated  bool                     `json:"positions_truncated"`
}

type RecallConflictPosition struct {
	PositionID        string   `json:"position_id"`
	Disposition       string   `json:"disposition"`
	RelationshipIDs   []string `json:"relationship_ids"`
	OwnerProfileIDs   []string `json:"owner_profile_ids"`
	ResultEvidenceIDs []string `json:"result_evidence_ids"`
}

type RecallRelationshipHandle struct {
	RelationshipID string         `json:"relationship_id"`
	Subject        EntityHandle   `json:"subject"`
	Predicate      string         `json:"predicate"`
	Object         SemanticObject `json:"object"`
	Polarity       string         `json:"polarity"`
}

type EntityHandle struct {
	EntityID string `json:"entity_id"`
	Name     string `json:"name"`
}

type SemanticObject struct {
	EntityID string `json:"entity_id,omitempty"`
	ValueID  string `json:"value_id,omitempty"`
	Name     string `json:"name,omitempty"`
	Type     string `json:"type,omitempty"`
	Value    any    `json:"value,omitempty"`
	Display  string `json:"display,omitempty"`
	Unit     string `json:"unit,omitempty"`
}

type RelatedHypothesisSummary struct {
	HypothesisID          string    `json:"hypothesis_id"`
	SubjectEntityID       string    `json:"subject_entity_id"`
	PredicateKey          string    `json:"predicate_key"`
	ObjectEntityID        string    `json:"object_entity_id,omitempty"`
	ObjectValueID         string    `json:"object_value_id,omitempty"`
	Statement             string    `json:"statement"`
	Status                string    `json:"status"`
	SourceRelationshipIDs []string  `json:"source_relationship_ids"`
	GeneratorKind         string    `json:"generator_kind"`
	GeneratorVersion      string    `json:"generator_version"`
	CreatedAt             time.Time `json:"created_at"`
}

type RecallDegradationResult struct {
	RequiredFailure bool   `json:"required_failure,omitempty"`
	Optional        bool   `json:"optional,omitempty"`
	Code            string `json:"code"`
	Message         string `json:"message"`
}

func (s *recallService) Recall(ctx context.Context, req RecallRequest) (*RecallResult, error) {
	if s.search == nil {
		return nil, errors.New("recall: search repository is required")
	}
	if strings.TrimSpace(req.ContractVersion) != domain.ContractVersion {
		return nil, fmt.Errorf("recall: invalid contract_version %q", req.ContractVersion)
	}
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.ProfileID == uuid.Nil {
		return nil, ErrRecallAuthContext
	}
	req = normalizeRecallRequest(req)
	contract, err := s.search.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	var degradation *RecallDegradationResult
	queryEmbedding := []float32(nil)
	if req.Query != "" {
		vector, vectorDegradation := s.queryEmbedding(ctx, contract, req.Query)
		queryEmbedding = vector
		degradation = vectorDegradation
	}
	recalled, err := s.search.RecallEvidence(ctx, repository.RecallEvidenceInput{
		TeamID:               actor.TeamID.String(),
		Query:                req.Query,
		QueryEmbedding:       queryEmbedding,
		Limit:                req.Limit,
		ValidAt:              req.ValidAt,
		KnownAt:              req.KnownAt,
		KnownEvidenceIDs:     req.KnownEvidenceIDs,
		KnownRelationshipIDs: req.KnownRelationshipIDs,
		ExpandFromEntityIDs:  req.ExpandFromEntityIDs,
	})
	if err != nil {
		return nil, err
	}
	result := recallResultFromRepository(recalled, degradation)
	paths, communityDegradation := s.recallCommunityDiscovery(ctx, actor.TeamID.String(), req, len(result.Results))
	result.DiscoveryPaths = paths
	if len(paths) > 0 {
		result.DiscoveryGuidance = "Community discovery found derived relationship paths; verify details with trace_memory before using them as support."
	}
	if result.Degradation == nil && communityDegradation != nil {
		result.Degradation = communityDegradation
	}
	related, relatedDegradation := s.recallRelatedHypotheses(ctx, actor.TeamID.String(), actor.ProfileID.String(), req.Query)
	result.RelatedHypotheses = related
	if result.Degradation == nil && relatedDegradation != nil {
		result.Degradation = relatedDegradation
	}
	return result, nil
}

func (s *recallService) recallCommunityDiscovery(
	ctx context.Context,
	teamID string,
	req RecallRequest,
	resultCount int,
) ([]RecallDiscoveryPath, *RecallDegradationResult) {
	if s.communities == nil || s.communityConfig == nil || resultCount >= req.Limit {
		return []RecallDiscoveryPath{}, nil
	}
	cfg, err := s.communityConfig.CommunityDetectionRuntimeConfig(ctx)
	if err != nil {
		return []RecallDiscoveryPath{}, communityDiscoveryDegradation()
	}
	if !cfg.Enabled {
		return []RecallDiscoveryPath{}, nil
	}
	if _, err := s.communities.RefreshCommunityStaleness(ctx, repository.CommunityStalenessInput{
		TeamID: teamID,
		Limit:  200,
	}); err != nil {
		return []RecallDiscoveryPath{}, communityDiscoveryDegradation()
	}
	remaining := req.Limit - resultCount
	if remaining > defaultCommunityPathLimit {
		remaining = defaultCommunityPathLimit
	}
	records, err := s.communities.RecallCommunityDiscovery(ctx, repository.CommunityDiscoveryInput{
		TeamID:               teamID,
		Query:                req.Query,
		ValidAt:              req.ValidAt,
		KnownAt:              req.KnownAt,
		KnownRelationshipIDs: req.KnownRelationshipIDs,
		ExpandFromEntityIDs:  req.ExpandFromEntityIDs,
		Limit:                remaining,
	})
	if err != nil {
		return []RecallDiscoveryPath{}, communityDiscoveryDegradation()
	}
	return communityDiscoveryPaths(records), nil
}

func communityDiscoveryDegradation() *RecallDegradationResult {
	return &RecallDegradationResult{
		Optional: true,
		Code:     "community_discovery_unavailable",
		Message:  "community discovery was unavailable; primary evidence recall was used",
	}
}

func (s *recallService) recallRelatedHypotheses(
	ctx context.Context,
	teamID string,
	ownerProfileID string,
	query string,
) ([]RelatedHypothesisSummary, *RecallDegradationResult) {
	if s.hypotheses == nil || strings.TrimSpace(query) == "" {
		return []RelatedHypothesisSummary{}, nil
	}
	_, err := s.hypotheses.RefreshHypothesisStaleness(ctx, repository.RefreshHypothesisStalenessInput{
		TeamID:         teamID,
		OwnerProfileID: ownerProfileID,
		Limit:          200,
	})
	if err != nil {
		return []RelatedHypothesisSummary{}, relatedHypothesisDegradation()
	}
	records, err := s.hypotheses.RecallHypotheses(ctx, repository.RecallHypothesesInput{
		TeamID: teamID,
		Query:  query,
		Limit:  defaultRelatedHypothesisLimit,
	})
	if err != nil {
		return []RelatedHypothesisSummary{}, relatedHypothesisDegradation()
	}
	return relatedHypothesisSummaries(records), nil
}

func relatedHypothesisDegradation() *RecallDegradationResult {
	return &RecallDegradationResult{
		Optional: true,
		Code:     "related_hypotheses_unavailable",
		Message:  "related hypotheses were unavailable; primary evidence recall was used",
	}
}

func communityDiscoveryPaths(records []repository.CommunityDiscoveryPath) []RecallDiscoveryPath {
	out := make([]RecallDiscoveryPath, 0, len(records))
	for _, record := range records {
		out = append(out, RecallDiscoveryPath{
			Relationships: []RecallRelationshipHandle{{
				RelationshipID: record.Relationship.RelationshipID,
				Subject: EntityHandle{
					EntityID: record.Relationship.SubjectEntityID,
					Name:     record.Relationship.SubjectName,
				},
				Predicate: record.Relationship.PredicateKey,
				Object: SemanticObject{
					EntityID: record.Relationship.ObjectEntityID,
					Name:     record.Relationship.ObjectName,
				},
				Polarity: record.Relationship.Polarity,
			}},
			EvidenceIDs: append([]string(nil), record.EvidenceIDs...),
		})
	}
	return out
}

func (s *recallService) queryEmbedding(
	ctx context.Context,
	contract *repository.ActiveSearchContract,
	query string,
) ([]float32, *RecallDegradationResult) {
	if s.provider == nil || !s.provider.IsAvailable() {
		return nil, &RecallDegradationResult{
			Optional: true,
			Code:     string(domain.ErrorProviderUnavailable),
			Message:  "vector recall is unavailable; full-text evidence recall was used",
		}
	}
	if got := s.provider.Dimensions(); got != 0 && got != contract.EmbeddingDimensions {
		return nil, &RecallDegradationResult{
			Optional: true,
			Code:     string(domain.ErrorProviderMalformed),
			Message:  "vector recall provider dimensions do not match the active search contract",
		}
	}
	if got := strings.TrimSpace(s.provider.ModelName()); got != "" && got != contract.EmbeddingModel {
		return nil, &RecallDegradationResult{
			Optional: true,
			Code:     string(domain.ErrorProviderMalformed),
			Message:  "vector recall provider model does not match the active search contract",
		}
	}
	vector, model, err := s.provider.Embed(ctx, query)
	if err != nil {
		return nil, &RecallDegradationResult{
			Optional: true,
			Code:     string(domain.ErrorProviderUnavailable),
			Message:  "vector recall provider failed; full-text evidence recall was used",
		}
	}
	if model != "" && model != contract.EmbeddingModel {
		return nil, &RecallDegradationResult{
			Optional: true,
			Code:     string(domain.ErrorProviderMalformed),
			Message:  "vector recall provider returned the wrong model",
		}
	}
	if err := validateRecallEmbedding(vector, contract.EmbeddingDimensions); err != nil {
		return nil, &RecallDegradationResult{
			Optional: true,
			Code:     string(domain.ErrorProviderMalformed),
			Message:  "vector recall provider returned an invalid embedding",
		}
	}
	return vector, nil
}

func normalizeRecallRequest(req RecallRequest) RecallRequest {
	req.ContractVersion = strings.TrimSpace(req.ContractVersion)
	req.Query = strings.TrimSpace(req.Query)
	if req.Limit <= 0 {
		req.Limit = defaultRecallResultLimit
	}
	if req.Limit > maxRecallResultLimit {
		req.Limit = maxRecallResultLimit
	}
	req.KnownEvidenceIDs = normalizeRecallRequestIDs(req.KnownEvidenceIDs)
	req.KnownRelationshipIDs = normalizeRecallRequestIDs(req.KnownRelationshipIDs)
	req.ExpandFromEntityIDs = normalizeRecallRequestIDs(req.ExpandFromEntityIDs)
	return req
}

func normalizeRecallRequestIDs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func validateRecallEmbedding(vector []float32, dims int) error {
	if len(vector) != dims {
		return fmt.Errorf("embedding dimensions %d, expected %d", len(vector), dims)
	}
	for i, value := range vector {
		f := float64(value)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("embedding contains non-finite value at index %d", i)
		}
	}
	return nil
}

func recallResultFromRepository(
	recalled *repository.RecallEvidenceResult,
	degradation *RecallDegradationResult,
) *RecallResult {
	searchState := string(domain.SearchProjectionCurrent)
	results := []RecallResultItem{}
	conflicts := []RecallConflictSummary{}
	if recalled != nil {
		searchState = recalled.SearchState
		results = make([]RecallResultItem, 0, len(recalled.Results))
		for _, item := range recalled.Results {
			results = append(results, RecallResultItem{
				EvidenceID:      item.EvidenceID,
				RelationshipIDs: append([]string(nil), item.RelationshipIDs...),
				Rank:            item.Rank,
				Context:         item.Context,
			})
		}
		conflicts = recallConflictSummaries(recalled.Conflicts)
	}
	return &RecallResult{
		RecallID:          "rec_" + uuid.NewString(),
		Results:           results,
		Conflicts:         conflicts,
		DiscoveryPaths:    []RecallDiscoveryPath{},
		DiscoveryGuidance: "No additional discovery guidance.",
		RelatedHypotheses: []RelatedHypothesisSummary{},
		Degradation:       degradation,
		SearchState:       searchState,
	}
}

func recallConflictSummaries(records []repository.RelationshipConflictCaseRecord) []RecallConflictSummary {
	out := make([]RecallConflictSummary, 0, len(records))
	for _, record := range records {
		reviewDueAt := record.ReviewDueAt
		positions := recallConflictPositions(record.Positions)
		summary := RecallConflictSummary{
			ConflictID:          record.ConflictID,
			Version:             record.Version,
			Kind:                record.Kind,
			Status:              record.Status,
			Question:            record.Question,
			ReviewDueAt:         &reviewDueAt,
			EffectiveAt:         record.EffectiveAt,
			EffectiveTimeBasis:  record.EffectiveTimeBasis,
			PreferredPositionID: record.PreferredPositionID,
			Positions:           positions,
			PositionsTruncated:  len(record.Positions) > recallConflictPositionLimit,
		}
		out = append(out, summary)
	}
	return out
}

const (
	recallConflictPositionLimit         = 10
	recallConflictRelationshipIDLimit   = 20
	recallConflictOwnerProfileIDLimit   = 20
	recallConflictResultEvidenceIDLimit = 50
)

func recallConflictPositions(records []repository.RelationshipConflictPositionRecord) []RecallConflictPosition {
	if len(records) > recallConflictPositionLimit {
		records = records[:recallConflictPositionLimit]
	}
	out := make([]RecallConflictPosition, 0, len(records))
	for _, record := range records {
		out = append(out, RecallConflictPosition{
			PositionID:        record.PositionID,
			Disposition:       record.Disposition,
			RelationshipIDs:   limitStrings(record.RelationshipIDs, recallConflictRelationshipIDLimit),
			OwnerProfileIDs:   limitStrings(record.OwnerProfileIDs, recallConflictOwnerProfileIDLimit),
			ResultEvidenceIDs: limitStrings(record.EvidenceIDs, recallConflictResultEvidenceIDLimit),
		})
	}
	return out
}

func limitStrings(values []string, limit int) []string {
	if limit >= 0 && len(values) > limit {
		values = values[:limit]
	}
	return append([]string(nil), values...)
}

func relatedHypothesisSummaries(records []repository.HypothesisRecord) []RelatedHypothesisSummary {
	out := make([]RelatedHypothesisSummary, 0, len(records))
	for _, record := range records {
		out = append(out, RelatedHypothesisSummary{
			HypothesisID:          record.HypothesisID,
			SubjectEntityID:       record.SubjectEntityID,
			PredicateKey:          record.PredicateKey,
			ObjectEntityID:        record.ObjectEntityID,
			ObjectValueID:         record.ObjectValueID,
			Statement:             record.Statement,
			Status:                record.Status,
			SourceRelationshipIDs: relatedHypothesisSourceIDs(record.SourceRefs),
			GeneratorKind:         publicHypothesisGeneratorKind(record.GeneratorKind),
			GeneratorVersion:      record.GeneratorVersion,
			CreatedAt:             record.CreatedAt,
		})
	}
	return out
}

func relatedHypothesisSourceIDs(refs []map[string]any) []string {
	out := []string{}
	for _, ref := range refs {
		id, _ := ref["id"].(string)
		id = strings.TrimSpace(id)
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

func publicHypothesisGeneratorKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case "provider":
		return "provider"
	default:
		return "deterministic"
	}
}
