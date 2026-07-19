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
	defaultV2RecallResultLimit      = 10
	maxV2RecallResultLimit          = 50
	defaultV2RelatedHypothesisLimit = 5
)

var ErrV2RecallAuthContext = errors.New("v2 recall: authenticated actor context is required")

type V2RecallService interface {
	RecallV2(ctx context.Context, req V2RecallRequest) (*V2RecallResult, error)
}

type V2RecallDependencies struct {
	Search     V2RecallSearchRepository
	Provider   embedding.EmbeddingProviderInterface
	Hypotheses V2RecallHypothesisRepository
}

type V2RecallSearchRepository interface {
	repository.V2RecallRepository
	GetActiveSearchContract(ctx context.Context) (*repository.V2ActiveSearchContract, error)
}

type V2RecallHypothesisRepository interface {
	RecallV2Hypotheses(ctx context.Context, input repository.V2RecallHypothesesInput) ([]repository.V2HypothesisRecord, error)
	RefreshV2HypothesisStaleness(ctx context.Context, input repository.V2RefreshHypothesisStalenessInput) (int, error)
}

type v2RecallService struct {
	search     V2RecallSearchRepository
	provider   embedding.EmbeddingProviderInterface
	hypotheses V2RecallHypothesisRepository
}

func NewV2RecallService(deps V2RecallDependencies) V2RecallService {
	return &v2RecallService{
		search:     deps.Search,
		provider:   deps.Provider,
		hypotheses: deps.Hypotheses,
	}
}

type V2RecallRequest struct {
	ContractVersion      string     `json:"contract_version"`
	Query                string     `json:"query"`
	Limit                int        `json:"limit,omitempty"`
	ValidAt              *time.Time `json:"valid_at,omitempty"`
	KnownAt              *time.Time `json:"known_at,omitempty"`
	KnownEvidenceIDs     []string   `json:"known_evidence_ids,omitempty"`
	KnownRelationshipIDs []string   `json:"known_relationship_ids,omitempty"`
	ExpandFromEntityIDs  []string   `json:"expand_from_entity_ids,omitempty"`
	IncludeEvidence      bool       `json:"include_evidence,omitempty"`
	UseCommunities       bool       `json:"use_communities,omitempty"`
}

type V2RecallResult struct {
	RecallID          string                       `json:"recall_id"`
	Results           []V2RecallResultItem         `json:"results"`
	DiscoveryPaths    []V2RecallDiscoveryPath      `json:"discovery_paths"`
	DiscoveryGuidance string                       `json:"discovery_guidance"`
	RelatedHypotheses []V2RelatedHypothesisSummary `json:"related_hypotheses"`
	Degradation       *V2RecallDegradationResult   `json:"degradation,omitempty"`
	SearchState       string                       `json:"search_state"`
}

type V2RecallResultItem struct {
	EvidenceID      string   `json:"evidence_id"`
	RelationshipIDs []string `json:"relationship_ids,omitempty"`
	Rank            int      `json:"rank"`
	Context         string   `json:"context,omitempty"`
}

type V2RecallDiscoveryPath struct {
	Relationships []V2RecallRelationshipHandle `json:"relationships"`
	EvidenceIDs   []string                     `json:"evidence_ids"`
}

type V2RecallRelationshipHandle struct {
	RelationshipID string           `json:"relationship_id"`
	Subject        V2EntityHandle   `json:"subject"`
	Predicate      string           `json:"predicate"`
	Object         V2SemanticObject `json:"object"`
	Polarity       string           `json:"polarity"`
}

type V2EntityHandle struct {
	EntityID string `json:"entity_id"`
	Name     string `json:"name"`
}

type V2SemanticObject struct {
	EntityID string `json:"entity_id,omitempty"`
	ValueID  string `json:"value_id,omitempty"`
	Type     string `json:"type,omitempty"`
	Value    any    `json:"value,omitempty"`
	Display  string `json:"display,omitempty"`
	Unit     string `json:"unit,omitempty"`
}

type V2RelatedHypothesisSummary struct {
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

type V2RecallDegradationResult struct {
	RequiredFailure bool   `json:"required_failure,omitempty"`
	Optional        bool   `json:"optional,omitempty"`
	Code            string `json:"code"`
	Message         string `json:"message"`
}

func (s *v2RecallService) RecallV2(ctx context.Context, req V2RecallRequest) (*V2RecallResult, error) {
	if s.search == nil {
		return nil, errors.New("v2 recall: search repository is required")
	}
	if strings.TrimSpace(req.ContractVersion) != domain.V2ContractVersion {
		return nil, fmt.Errorf("v2 recall: invalid contract_version %q", req.ContractVersion)
	}
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.ProfileID == uuid.Nil {
		return nil, ErrV2RecallAuthContext
	}
	req = normalizeV2RecallRequest(req)
	contract, err := s.search.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	var degradation *V2RecallDegradationResult
	queryEmbedding := []float32(nil)
	if req.Query != "" {
		vector, vectorDegradation := s.queryEmbedding(ctx, contract, req.Query)
		queryEmbedding = vector
		degradation = vectorDegradation
	}
	if req.UseCommunities && degradation == nil {
		degradation = &V2RecallDegradationResult{
			Optional: true,
			Code:     "community_recall_unavailable",
			Message:  "community expansion is not available in the V2 recall path",
		}
	}
	recalled, err := s.search.RecallEvidence(ctx, repository.V2RecallEvidenceInput{
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
	result := v2RecallResultFromRepository(recalled, degradation)
	related, relatedDegradation := s.recallRelatedHypotheses(ctx, actor.TeamID.String(), actor.ProfileID.String(), req.Query)
	result.RelatedHypotheses = related
	if result.Degradation == nil && relatedDegradation != nil {
		result.Degradation = relatedDegradation
	}
	return result, nil
}

func (s *v2RecallService) recallRelatedHypotheses(
	ctx context.Context,
	teamID string,
	ownerProfileID string,
	query string,
) ([]V2RelatedHypothesisSummary, *V2RecallDegradationResult) {
	if s.hypotheses == nil || strings.TrimSpace(query) == "" {
		return []V2RelatedHypothesisSummary{}, nil
	}
	_, err := s.hypotheses.RefreshV2HypothesisStaleness(ctx, repository.V2RefreshHypothesisStalenessInput{
		TeamID:         teamID,
		OwnerProfileID: ownerProfileID,
		Limit:          200,
	})
	if err != nil {
		return []V2RelatedHypothesisSummary{}, v2RelatedHypothesisDegradation()
	}
	records, err := s.hypotheses.RecallV2Hypotheses(ctx, repository.V2RecallHypothesesInput{
		TeamID: teamID,
		Query:  query,
		Limit:  defaultV2RelatedHypothesisLimit,
	})
	if err != nil {
		return []V2RelatedHypothesisSummary{}, v2RelatedHypothesisDegradation()
	}
	return v2RelatedHypothesisSummaries(records), nil
}

func v2RelatedHypothesisDegradation() *V2RecallDegradationResult {
	return &V2RecallDegradationResult{
		Optional: true,
		Code:     "related_hypotheses_unavailable",
		Message:  "related hypotheses were unavailable; primary evidence recall was used",
	}
}

func (s *v2RecallService) queryEmbedding(
	ctx context.Context,
	contract *repository.V2ActiveSearchContract,
	query string,
) ([]float32, *V2RecallDegradationResult) {
	if s.provider == nil || !s.provider.IsAvailable() {
		return nil, &V2RecallDegradationResult{
			Optional: true,
			Code:     string(domain.V2ErrorProviderUnavailable),
			Message:  "vector recall is unavailable; full-text evidence recall was used",
		}
	}
	if got := s.provider.Dimensions(); got != 0 && got != contract.EmbeddingDimensions {
		return nil, &V2RecallDegradationResult{
			Optional: true,
			Code:     string(domain.V2ErrorProviderMalformed),
			Message:  "vector recall provider dimensions do not match the active search contract",
		}
	}
	if got := strings.TrimSpace(s.provider.ModelName()); got != "" && got != contract.EmbeddingModel {
		return nil, &V2RecallDegradationResult{
			Optional: true,
			Code:     string(domain.V2ErrorProviderMalformed),
			Message:  "vector recall provider model does not match the active search contract",
		}
	}
	vector, model, err := s.provider.Embed(ctx, query)
	if err != nil {
		return nil, &V2RecallDegradationResult{
			Optional: true,
			Code:     string(domain.V2ErrorProviderUnavailable),
			Message:  "vector recall provider failed; full-text evidence recall was used",
		}
	}
	if model != "" && model != contract.EmbeddingModel {
		return nil, &V2RecallDegradationResult{
			Optional: true,
			Code:     string(domain.V2ErrorProviderMalformed),
			Message:  "vector recall provider returned the wrong model",
		}
	}
	if err := validateV2RecallEmbedding(vector, contract.EmbeddingDimensions); err != nil {
		return nil, &V2RecallDegradationResult{
			Optional: true,
			Code:     string(domain.V2ErrorProviderMalformed),
			Message:  "vector recall provider returned an invalid embedding",
		}
	}
	return vector, nil
}

func normalizeV2RecallRequest(req V2RecallRequest) V2RecallRequest {
	req.ContractVersion = strings.TrimSpace(req.ContractVersion)
	req.Query = strings.TrimSpace(req.Query)
	if req.Limit <= 0 {
		req.Limit = defaultV2RecallResultLimit
	}
	if req.Limit > maxV2RecallResultLimit {
		req.Limit = maxV2RecallResultLimit
	}
	req.KnownEvidenceIDs = normalizeV2RecallRequestIDs(req.KnownEvidenceIDs)
	req.KnownRelationshipIDs = normalizeV2RecallRequestIDs(req.KnownRelationshipIDs)
	req.ExpandFromEntityIDs = normalizeV2RecallRequestIDs(req.ExpandFromEntityIDs)
	return req
}

func normalizeV2RecallRequestIDs(values []string) []string {
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

func validateV2RecallEmbedding(vector []float32, dims int) error {
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

func v2RecallResultFromRepository(
	recalled *repository.V2RecallEvidenceResult,
	degradation *V2RecallDegradationResult,
) *V2RecallResult {
	searchState := string(domain.V2SearchProjectionCurrent)
	results := []V2RecallResultItem{}
	if recalled != nil {
		searchState = recalled.SearchState
		results = make([]V2RecallResultItem, 0, len(recalled.Results))
		for _, item := range recalled.Results {
			results = append(results, V2RecallResultItem{
				EvidenceID:      item.EvidenceID,
				RelationshipIDs: append([]string(nil), item.RelationshipIDs...),
				Rank:            item.Rank,
				Context:         item.Context,
			})
		}
	}
	return &V2RecallResult{
		RecallID:          "rec_" + uuid.NewString(),
		Results:           results,
		DiscoveryPaths:    []V2RecallDiscoveryPath{},
		DiscoveryGuidance: "No additional discovery guidance.",
		RelatedHypotheses: []V2RelatedHypothesisSummary{},
		Degradation:       degradation,
		SearchState:       searchState,
	}
}

func v2RelatedHypothesisSummaries(records []repository.V2HypothesisRecord) []V2RelatedHypothesisSummary {
	out := make([]V2RelatedHypothesisSummary, 0, len(records))
	for _, record := range records {
		out = append(out, V2RelatedHypothesisSummary{
			HypothesisID:          record.HypothesisID,
			SubjectEntityID:       record.SubjectEntityID,
			PredicateKey:          record.PredicateKey,
			ObjectEntityID:        record.ObjectEntityID,
			ObjectValueID:         record.ObjectValueID,
			Statement:             record.Statement,
			Status:                record.Status,
			SourceRelationshipIDs: v2RelatedHypothesisSourceIDs(record.SourceRefs),
			GeneratorKind:         v2PublicHypothesisGeneratorKind(record.GeneratorKind),
			GeneratorVersion:      record.GeneratorVersion,
			CreatedAt:             record.CreatedAt,
		})
	}
	return out
}

func v2RelatedHypothesisSourceIDs(refs []map[string]any) []string {
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

func v2PublicHypothesisGeneratorKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case "provider":
		return "provider"
	default:
		return "deterministic"
	}
}
