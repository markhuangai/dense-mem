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
	defaultV2RecallResultLimit = 10
	maxV2RecallResultLimit     = 50
)

var ErrV2RecallAuthContext = errors.New("v2 recall: authenticated actor context is required")

type V2RecallService interface {
	RecallV2(ctx context.Context, req V2RecallRequest) (*V2RecallResult, error)
}

type V2RecallDependencies struct {
	Search     V2RecallSearchRepository
	Provider   embedding.EmbeddingProviderInterface
	ProfileKey string
}

type V2RecallSearchRepository interface {
	repository.V2RecallRepository
	GetActiveSearchProfile(ctx context.Context, profileKey string) (*repository.V2SearchProfile, error)
}

type v2RecallService struct {
	search     V2RecallSearchRepository
	provider   embedding.EmbeddingProviderInterface
	profileKey string
}

func NewV2RecallService(deps V2RecallDependencies) V2RecallService {
	profileKey := strings.TrimSpace(deps.ProfileKey)
	if profileKey == "" {
		profileKey = "default"
	}
	return &v2RecallService{
		search:     deps.Search,
		provider:   deps.Provider,
		profileKey: profileKey,
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
	RecallID          string                     `json:"recall_id"`
	Results           []V2RecallResultItem       `json:"results"`
	DiscoveryGuidance string                     `json:"discovery_guidance,omitempty"`
	Degradation       *V2RecallDegradationResult `json:"degradation,omitempty"`
	SearchState       string                     `json:"search_state"`
}

type V2RecallResultItem struct {
	EvidenceID      string   `json:"evidence_id"`
	RelationshipIDs []string `json:"relationship_ids,omitempty"`
	Rank            int      `json:"rank"`
	Context         string   `json:"context,omitempty"`
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
	profile, err := s.search.GetActiveSearchProfile(ctx, s.profileKey)
	if err != nil {
		return nil, err
	}
	var degradation *V2RecallDegradationResult
	queryEmbedding := []float32(nil)
	if req.Query != "" {
		vector, vectorDegradation := s.queryEmbedding(ctx, profile, req.Query)
		queryEmbedding = vector
		degradation = vectorDegradation
	}
	recalled, err := s.search.RecallEvidence(ctx, repository.V2RecallEvidenceInput{
		TeamID:               actor.TeamID.String(),
		ProfileKey:           s.profileKey,
		Query:                req.Query,
		QueryEmbedding:       queryEmbedding,
		Limit:                req.Limit,
		ValidAt:              req.ValidAt,
		KnownAt:              req.KnownAt,
		KnownEvidenceIDs:     req.KnownEvidenceIDs,
		KnownRelationshipIDs: req.KnownRelationshipIDs,
		ExpandFromEntityIDs:  req.ExpandFromEntityIDs,
		UseCommunities:       req.UseCommunities,
	})
	if err != nil {
		return nil, err
	}
	if degradation == nil && recalled.OptionalDegradation != nil {
		degradation = &V2RecallDegradationResult{
			Optional: true,
			Code:     recalled.OptionalDegradation.Code,
			Message:  recalled.OptionalDegradation.Message,
		}
	}
	return v2RecallResultFromRepository(recalled, degradation), nil
}

func (s *v2RecallService) queryEmbedding(
	ctx context.Context,
	profile *repository.V2SearchProfile,
	query string,
) ([]float32, *V2RecallDegradationResult) {
	if s.provider == nil || !s.provider.IsAvailable() {
		return nil, &V2RecallDegradationResult{
			Optional: true,
			Code:     string(domain.V2ErrorProviderUnavailable),
			Message:  "vector recall is unavailable; full-text evidence recall was used",
		}
	}
	if got := s.provider.Dimensions(); got != 0 && got != profile.EmbeddingDimensions {
		return nil, &V2RecallDegradationResult{
			Optional: true,
			Code:     string(domain.V2ErrorProviderMalformed),
			Message:  "vector recall provider dimensions do not match the active search profile",
		}
	}
	if got := strings.TrimSpace(s.provider.ModelName()); got != "" && got != profile.EmbeddingModel {
		return nil, &V2RecallDegradationResult{
			Optional: true,
			Code:     string(domain.V2ErrorProviderMalformed),
			Message:  "vector recall provider model does not match the active search profile",
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
	if model != "" && model != profile.EmbeddingModel {
		return nil, &V2RecallDegradationResult{
			Optional: true,
			Code:     string(domain.V2ErrorProviderMalformed),
			Message:  "vector recall provider returned the wrong model",
		}
	}
	if err := validateV2RecallEmbedding(vector, profile.EmbeddingDimensions); err != nil {
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
		RecallID:    "rec_" + uuid.NewString(),
		Results:     results,
		Degradation: degradation,
		SearchState: searchState,
	}
}
