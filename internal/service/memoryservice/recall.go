package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/community"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

const (
	defaultRecallResultLimit        = 10
	maxRecallResultLimit            = 50
	defaultRelatedHypothesisLimit   = 5
	defaultRelatedRelationshipLimit = 5
	maxRelatedRelationshipLimit     = 20
	defaultCommunityPathLimit       = 3
	maxCommunityPathLimit           = 10
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
	Metrics         observability.DiscoverabilityMetrics
}

type RecallSearchRepository interface {
	repository.RecallRepository
	GetActiveSearchContract(ctx context.Context) (*repository.ActiveSearchContract, error)
}

type RecallHypothesisRepository interface {
	RecallHypotheses(ctx context.Context, input repository.RecallHypothesesInput) ([]repository.HypothesisRecord, error)
}

type RecallCommunityRepository interface {
	RecallCommunityDiscovery(ctx context.Context, input repository.CommunityDiscoveryInput) ([]repository.CommunityDiscoveryPath, error)
	RefreshCommunityStaleness(ctx context.Context, input repository.CommunityStalenessInput) (int, error)
}

type RecallCommunitySnapshotRepository interface {
	RecallCommunities(ctx context.Context, input repository.CommunityRecallInput) ([]repository.CommunityRecallRecord, error)
}

type RecallCommunityCoverageRepository interface {
	ListCommunitySemanticGroups(ctx context.Context, input repository.CommunityCoverageInput) ([]string, error)
}

type RecallCommunityRunRepository interface {
	LatestCommunityRun(ctx context.Context, teamID string) (*repository.CommunityRun, error)
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
	metrics         observability.DiscoverabilityMetrics
}

func NewRecallService(deps RecallDependencies) RecallService {
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	return &recallService{
		search:          deps.Search,
		provider:        deps.Provider,
		hypotheses:      deps.Hypotheses,
		communities:     deps.Communities,
		communityConfig: deps.CommunityConfig,
		metrics:         metrics,
	}
}

type RecallRequest struct {
	Query                      string     `json:"query"`
	Limit                      int        `json:"limit,omitempty"`
	IncludeHypotheses          bool       `json:"-"`
	RelationshipLimit          *int       `json:"relationship_limit,omitempty"`
	CommunityLimit             *int       `json:"community_limit,omitempty"`
	CommunityRelationshipLimit *int       `json:"community_relationship_limit,omitempty"`
	ValidAt                    *time.Time `json:"valid_at,omitempty"`
	KnownAt                    *time.Time `json:"known_at,omitempty"`
	KnownEvidenceIDs           []string   `json:"known_evidence_ids,omitempty"`
	KnownRelationshipIDs       []string   `json:"known_relationship_ids,omitempty"`
	ExpandFromEntityIDs        []string   `json:"expand_from_entity_ids,omitempty"`
	recallContract             *repository.ActiveSearchContract
	recallEmbedding            []float32
	recallEmbeddingDegradation *RecallDegradationResult
	recallEmbeddingReady       bool
}

type RecallResult struct {
	RecallID             string                       `json:"recall_id"`
	Results              []RecallResultItem           `json:"results"`
	Conflicts            []RecallConflictSummary      `json:"conflicts"`
	RelatedRelationships []RelatedRelationshipSummary `json:"related_relationships"`
	RelatedCommunities   []RecallDiscoveryPath        `json:"related_communities"`
	RelatedHypotheses    []RelatedHypothesisSummary   `json:"related_hypotheses"`
	SearchStates         RecallSearchStates           `json:"search_states"`
	Degradations         []RecallDegradationResult    `json:"degradations"`
	SuggestedActions     []RecallSuggestedAction      `json:"suggested_actions"`

	DiscoveryPaths    []RecallDiscoveryPath    `json:"-"`
	DiscoveryGuidance string                   `json:"-"`
	Degradation       *RecallDegradationResult `json:"-"`
	SearchState       string                   `json:"-"`
}

type RecallSuggestedAction struct {
	Tool          string   `json:"tool"`
	Guidance      string   `json:"guidance"`
	RecallEventID string   `json:"recall_event_id,omitempty"`
	HypothesisIDs []string `json:"hypothesis_ids,omitempty"`
}

type RecallResultItem struct {
	EvidenceID      string     `json:"evidence_id"`
	RelationshipIDs []string   `json:"relationship_ids,omitempty"`
	Rank            int        `json:"rank"`
	Context         string     `json:"context,omitempty"`
	Source          string     `json:"source,omitempty"`
	SourceType      string     `json:"source_type,omitempty"`
	CreatedAt       *time.Time `json:"created_at,omitempty"`
	SpaceKind       string     `json:"space_kind,omitempty"`
}

type RecallDiscoveryPath struct {
	Relationships          []RecallRelationshipHandle   `json:"relationships"`
	EvidenceIDs            []string                     `json:"evidence_ids"`
	CommunityID            string                       `json:"community_id,omitempty"`
	LogicalCommunityID     string                       `json:"logical_community_id,omitempty"`
	Rank                   int                          `json:"rank,omitempty"`
	Summary                string                       `json:"summary,omitempty"`
	TopEntities            []EntityHandle               `json:"top_entities,omitempty"`
	TopPredicates          []string                     `json:"top_predicates,omitempty"`
	EntityCount            int                          `json:"entity_count,omitempty"`
	RelationshipCount      int                          `json:"relationship_count,omitempty"`
	CommunityRelationships []RelatedRelationshipSummary `json:"-"`
	RelationshipsTruncated bool                         `json:"relationships_truncated,omitempty"`
}

// MarshalJSON keeps the transitional in-process path adapter private while
// emitting the exact first-class community contract for snapshot records.
func (p RecallDiscoveryPath) MarshalJSON() ([]byte, error) {
	if p.CommunityID != "" {
		return json.Marshal(struct {
			CommunityID            string                       `json:"community_id"`
			LogicalCommunityID     string                       `json:"logical_community_id"`
			Rank                   int                          `json:"rank"`
			Summary                string                       `json:"summary"`
			TopEntities            []EntityHandle               `json:"top_entities"`
			TopPredicates          []string                     `json:"top_predicates"`
			EntityCount            int                          `json:"entity_count"`
			RelationshipCount      int                          `json:"relationship_count"`
			Relationships          []RelatedRelationshipSummary `json:"relationships"`
			RelationshipsTruncated bool                         `json:"relationships_truncated"`
		}{p.CommunityID, p.LogicalCommunityID, p.Rank, p.Summary, p.TopEntities, p.TopPredicates, p.EntityCount, p.RelationshipCount, p.CommunityRelationships, p.RelationshipsTruncated})
	}
	return json.Marshal(struct {
		Relationships []RecallRelationshipHandle `json:"relationships"`
		EvidenceIDs   []string                   `json:"evidence_ids"`
	}{p.Relationships, p.EvidenceIDs})
}

type RecallCommunity struct {
	CommunityID            string                       `json:"community_id"`
	LogicalCommunityID     string                       `json:"logical_community_id"`
	Rank                   int                          `json:"rank"`
	Summary                string                       `json:"summary"`
	TopEntities            []EntityHandle               `json:"top_entities"`
	TopPredicates          []string                     `json:"top_predicates"`
	EntityCount            int                          `json:"entity_count"`
	RelationshipCount      int                          `json:"relationship_count"`
	Relationships          []RelatedRelationshipSummary `json:"relationships"`
	RelationshipsTruncated bool                         `json:"relationships_truncated"`
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

type RecallRelationshipHandle struct {
	RelationshipID string         `json:"relationship_id"`
	Subject        EntityHandle   `json:"subject"`
	Predicate      string         `json:"predicate"`
	Object         SemanticObject `json:"object"`
	Polarity       string         `json:"polarity"`
}

type RelatedRelationshipSummary struct {
	RelationshipID            string         `json:"relationship_id"`
	EquivalentRelationshipIDs []string       `json:"equivalent_relationship_ids"`
	SemanticGroupKey          string         `json:"-"`
	Subject                   EntityHandle   `json:"subject"`
	Predicate                 string         `json:"predicate"`
	Object                    SemanticObject `json:"object"`
	Polarity                  string         `json:"polarity"`
	EvidenceIDs               []string       `json:"evidence_ids"`
	SearchState               string         `json:"search_state,omitempty"`
	SpaceKind                 string         `json:"space_kind,omitempty"`
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
	Frontier        string `json:"frontier,omitempty"`
	RequiredFailure bool   `json:"required_failure,omitempty"`
	Optional        bool   `json:"optional,omitempty"`
	Code            string `json:"code"`
	Message         string `json:"message"`
}

type RecallSearchStates struct {
	Evidence      string `json:"evidence"`
	Relationships string `json:"relationships"`
}

func (s *recallService) Recall(ctx context.Context, req RecallRequest) (result *RecallResult, err error) {
	if s.search == nil {
		return nil, errors.New("recall: search repository is required")
	}
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.OwnerID == uuid.Nil {
		return nil, ErrRecallAuthContext
	}
	if _, branchSelected := recallBranchFromContext(ctx); !branchSelected && len(actor.AllowedSpaces) > 1 {
		return s.recallAcrossSpaces(ctx, req, actor)
	}
	branch, _ := recallBranchFromContext(ctx)
	spaceKind := branchKind(branch)
	// Private ingestion is not supported here, so graph and hypothesis expansion stays on shared data.
	teamSharedBranch := spaceKind == string(domain.MemorySpaceTeamShared)
	if !recallBranchMetricsSuppressed(ctx) {
		started := time.Now()
		defer func() {
			outcome := "ok"
			resultCount := 0
			if err != nil {
				outcome = "error"
			}
			if result != nil {
				resultCount = len(result.Results)
			}
			observability.RecordRecall(ctx, s.metrics, float64(time.Since(started).Microseconds())/1000, resultCount, outcome)
		}()
	}
	req = normalizeRecallRequest(req)
	contract := req.recallContract
	if contract == nil {
		contract, err = s.search.GetActiveSearchContract(ctx)
		if err != nil {
			return nil, err
		}
	}
	degradations := []RecallDegradationResult{}
	queryEmbedding := append([]float32(nil), req.recallEmbedding...)
	if req.recallEmbeddingReady {
		if req.recallEmbeddingDegradation != nil {
			degradations = append(degradations, *req.recallEmbeddingDegradation)
		}
	} else if req.Query != "" {
		embedCtx := observability.WithAIOperation(ctx, observability.AIOperationRecallEmbedding, 1)
		vector, vectorDegradation := s.queryEmbedding(embedCtx, contract, req.Query)
		queryEmbedding = vector
		if vectorDegradation != nil {
			degradations = append(degradations, *vectorDegradation)
		}
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
		SpaceID:              branchID(branch),
		SpaceKind:            spaceKind,
	})
	if err != nil {
		return nil, err
	}
	if err := validateRecallConflictTeams(recalled, actor.TeamID.String()); err != nil {
		return nil, err
	}
	result = recallResultFromRepository(recalled, degradations)
	appendEvidenceVectorFailureDegradation(result, recalled.SearchState)
	returnedEvidenceIDs := recallResultEvidenceIDs(result.Results)
	coveredGroups := map[string]struct{}{}
	coverageAvailable := true
	var coverageDegradation *RecallDegradationResult
	if teamSharedBranch {
		coveredGroups, coverageAvailable, coverageDegradation = s.resolveCommunityCoverage(ctx, actor.TeamID.String(), req.KnownEvidenceIDs, returnedEvidenceIDs, req.KnownRelationshipIDs)
	}
	if coverageDegradation != nil {
		result.Degradations = append(result.Degradations, *coverageDegradation)
	}
	relationships, relationshipState, relationshipDegradation, directGroups := s.recallRelatedRelationships(ctx, actor.TeamID.String(), req, queryEmbedding, coveredGroups)
	result.RelatedRelationships = relationships
	result.SearchStates.Relationships = relationshipState
	if relationshipDegradation != nil {
		result.Degradations = append(result.Degradations, *relationshipDegradation)
	}
	communities, paths, communityDegradation := []RecallDiscoveryPath{}, []RecallDiscoveryPath{}, (*RecallDegradationResult)(nil)
	if teamSharedBranch {
		if _, snapshotRepo := s.communities.(RecallCommunitySnapshotRepository); snapshotRepo || len(result.Results) < req.Limit {
			evidenceIDs := make([]string, 0, len(result.Results))
			evidenceIDs = append(evidenceIDs, returnedEvidenceIDs...)
			communityGroups := cloneGroupSet(coveredGroups)
			for group := range directGroups {
				communityGroups[group] = struct{}{}
			}
			seedRelationshipIDs := relationshipSummaryIDs(relationships)
			communities, paths, communityDegradation = s.recallCommunities(ctx, actor.TeamID.String(), req, communityGroups, evidenceIDs, seedRelationshipIDs, coverageAvailable)
		}
	}
	result.RelatedCommunities = communities
	result.DiscoveryPaths = paths
	result.DiscoveryGuidance = "No additional discovery guidance."
	if len(communities) > 0 || len(paths) > 0 {
		result.DiscoveryGuidance = "Community discovery found derived relationship paths; verify details with trace_memory before using them as support."
	}
	if communityDegradation != nil {
		result.Degradations = append(result.Degradations, *communityDegradation)
	}
	if len(communities) > 0 {
		communityGroups := cloneGroupSet(coveredGroups)
		for group := range directGroups {
			communityGroups[group] = struct{}{}
		}
		for _, community := range communities {
			for _, relationship := range community.CommunityRelationships {
				if relationship.SemanticGroupKey != "" {
					communityGroups[relationship.SemanticGroupKey] = struct{}{}
				}
			}
		}
		result.RelatedRelationships = filterRelatedRelationshipsByGroups(result.RelatedRelationships, communityGroups)
	}
	if !recallBranchMetricsSuppressed(ctx) {
		recordRecallCommunityMetric(ctx, s.metrics, result)
	}
	result.RelatedHypotheses = []RelatedHypothesisSummary{}
	if teamSharedBranch && req.IncludeHypotheses {
		related, relatedDegradation := s.recallRelatedHypotheses(ctx, actor.TeamID.String(), actor.OwnerID.String(), req.Query)
		result.RelatedHypotheses = related
		if relatedDegradation != nil {
			result.Degradations = append(result.Degradations, *relatedDegradation)
		}
	}
	if len(result.Degradations) > 0 {
		result.Degradation = &result.Degradations[0]
	}
	applyRecallSpaceKind(result, spaceKind)
	return result, nil
}

func recordRecallCommunityMetric(ctx context.Context, metrics observability.DiscoverabilityMetrics, result *RecallResult) {
	if result == nil {
		return
	}
	outcome := "ok"
	for _, degradation := range result.Degradations {
		if degradation.Frontier == "communities" {
			outcome = "degraded"
			break
		}
	}
	communityRelationships := 0
	for _, community := range result.RelatedCommunities {
		communityRelationships += len(community.CommunityRelationships)
	}
	observability.RecordCommunityRecall(ctx, metrics, outcome, len(result.RelatedCommunities), communityRelationships)
}

func (s *recallService) resolveCommunityCoverage(
	ctx context.Context,
	teamID string,
	knownEvidenceIDs []string,
	returnedEvidenceIDs []string,
	knownRelationshipIDs []string,
) (map[string]struct{}, bool, *RecallDegradationResult) {
	groups := map[string]struct{}{}
	coverageRepo, ok := s.communities.(RecallCommunityCoverageRepository)
	if !ok {
		return groups, true, nil
	}
	evidenceIDs := append(append([]string(nil), knownEvidenceIDs...), returnedEvidenceIDs...)
	covered, err := coverageRepo.ListCommunitySemanticGroups(ctx, repository.CommunityCoverageInput{
		TeamID: teamID, EvidenceIDs: evidenceIDs, RelationshipIDs: knownRelationshipIDs,
	})
	if err != nil {
		return groups, false, communitySnapshotDegradation("community_snapshot_unavailable", "community coverage was unavailable; direct relationship fallback was used")
	}
	for _, group := range covered {
		if strings.TrimSpace(group) != "" {
			groups[group] = struct{}{}
		}
	}
	return groups, true, nil
}

func (s *recallService) recallCommunityDiscovery(
	ctx context.Context,
	teamID string,
	req RecallRequest,
) ([]RecallDiscoveryPath, *RecallDegradationResult) {
	communityLimit := recallOptionalLimitValue(req.CommunityLimit)
	if s.communities == nil || s.communityConfig == nil || communityLimit <= 0 {
		return []RecallDiscoveryPath{}, nil
	}
	cfg, err := s.communityConfig.CommunityDetectionRuntimeConfig(ctx)
	if err != nil {
		return []RecallDiscoveryPath{}, communityDiscoveryDegradation()
	}
	if !cfg.Enabled {
		return []RecallDiscoveryPath{}, nil
	}
	records, err := s.communities.RecallCommunityDiscovery(ctx, repository.CommunityDiscoveryInput{
		TeamID:               teamID,
		Query:                req.Query,
		ValidAt:              req.ValidAt,
		KnownAt:              req.KnownAt,
		KnownRelationshipIDs: req.KnownRelationshipIDs,
		ExpandFromEntityIDs:  req.ExpandFromEntityIDs,
		Limit:                communityLimit,
	})
	if err != nil {
		return []RecallDiscoveryPath{}, communityDiscoveryDegradation()
	}
	return communityDiscoveryPaths(records), nil
}

func (s *recallService) recallCommunities(
	ctx context.Context,
	teamID string,
	req RecallRequest,
	excludedGroups map[string]struct{},
	returnedEvidenceIDs []string,
	seedRelationshipIDs []string,
	coverageAvailable bool,
) ([]RecallDiscoveryPath, []RecallDiscoveryPath, *RecallDegradationResult) {
	communityLimit := recallOptionalLimitValue(req.CommunityLimit)
	if communityLimit <= 0 || s.communities == nil || s.communityConfig == nil {
		return []RecallDiscoveryPath{}, []RecallDiscoveryPath{}, nil
	}
	cfg, err := s.communityConfig.CommunityDetectionRuntimeConfig(ctx)
	if err != nil {
		return []RecallDiscoveryPath{}, []RecallDiscoveryPath{}, communityDiscoveryDegradation()
	}
	if !cfg.Enabled {
		return []RecallDiscoveryPath{}, []RecallDiscoveryPath{}, nil
	}
	if req.ValidAt != nil || req.KnownAt != nil {
		return []RecallDiscoveryPath{}, []RecallDiscoveryPath{}, communitySnapshotDegradation(
			"community_temporal_not_supported",
			"community snapshots are current-only; temporal recall used direct relationship fallback",
		)
	}
	if !coverageAvailable {
		return []RecallDiscoveryPath{}, []RecallDiscoveryPath{}, communitySnapshotDegradation("community_snapshot_unavailable", "community coverage was unavailable; direct relationship fallback was used")
	}
	if runRepo, ok := s.communities.(RecallCommunityRunRepository); ok {
		latest, runErr := runRepo.LatestCommunityRun(ctx, teamID)
		if runErr != nil {
			return []RecallDiscoveryPath{}, []RecallDiscoveryPath{}, communitySnapshotDegradation("community_snapshot_unavailable", "community run status was unavailable; direct relationship fallback was used")
		}
		if latest != nil {
			switch latest.Status {
			case "failed", "cancelled":
				return []RecallDiscoveryPath{}, []RecallDiscoveryPath{}, communitySnapshotDegradation("community_detection_failed", "community detection failed; direct relationship fallback was used")
			case "too_large":
				return []RecallDiscoveryPath{}, []RecallDiscoveryPath{}, communitySnapshotDegradation("community_graph_too_large", "community graph exceeded the configured bound; direct relationship fallback was used")
			case "running":
				return []RecallDiscoveryPath{}, []RecallDiscoveryPath{}, communitySnapshotDegradation("community_snapshot_unavailable", "community snapshot generation is in progress; direct relationship fallback was used")
			}
			if latest.Status != "completed" || !communitySnapshotRunCompatible(latest) {
				return []RecallDiscoveryPath{}, []RecallDiscoveryPath{}, communitySnapshotDegradation("community_snapshot_unavailable", "community snapshot metadata was incompatible; direct relationship fallback was used")
			}
		}
	}
	if staler, ok := s.communities.(interface {
		RefreshCommunityStaleness(context.Context, repository.CommunityStalenessInput) (int, error)
	}); ok {
		if staleCount, staleErr := staler.RefreshCommunityStaleness(ctx, repository.CommunityStalenessInput{TeamID: teamID, Limit: 200}); staleErr != nil || staleCount > 0 {
			return []RecallDiscoveryPath{}, []RecallDiscoveryPath{}, communitySnapshotDegradation(
				"community_snapshot_stale",
				"community snapshot sources changed; direct relationship fallback was used",
			)
		}
	}
	if snapshotRepo, ok := s.communities.(RecallCommunitySnapshotRepository); ok {
		groups := make([]string, 0, len(excludedGroups))
		for group := range excludedGroups {
			groups = append(groups, group)
		}
		sort.Strings(groups)
		records, recallErr := snapshotRepo.RecallCommunities(ctx, repository.CommunityRecallInput{
			TeamID: teamID, Query: req.Query, ValidAt: req.ValidAt, KnownAt: req.KnownAt,
			ReturnedEvidenceIDs: returnedEvidenceIDs, SeedRelationshipIDs: seedRelationshipIDs,
			KnownEvidenceIDs: req.KnownEvidenceIDs, KnownRelationshipIDs: req.KnownRelationshipIDs,
			ExpandFromEntityIDs: req.ExpandFromEntityIDs, ExcludedGroupKeys: groups,
			Limit: communityLimit, RelationshipLimit: recallOptionalLimitValue(req.CommunityRelationshipLimit),
			CoveredGroupKeys: groups,
		})
		if recallErr != nil {
			return []RecallDiscoveryPath{}, []RecallDiscoveryPath{}, communitySnapshotDegradation("community_snapshot_unavailable", "community snapshot was unavailable; direct relationship fallback was used")
		}
		return recallCommunitiesFromRepository(records), []RecallDiscoveryPath{}, nil
	}
	// Transitional adapters remain usable for callers that only implement the
	// old path reader; production wiring uses RecallCommunities above.
	paths, degradation := s.recallCommunityDiscovery(ctx, teamID, req)
	return []RecallDiscoveryPath{}, paths, degradation
}

func communitySnapshotRunCompatible(run *repository.CommunityRun) bool {
	if run == nil {
		return false
	}
	return run.AlgorithmKind == community.AlgorithmKind &&
		run.AlgorithmVersion == community.AlgorithmVersion &&
		run.ProfileVersion == repository.CommunityProfileVersion &&
		run.ConfigurationHash == community.ConfigurationHash(community.DefaultSeed)
}

func communitySnapshotDegradation(code, message string) *RecallDegradationResult {
	return &RecallDegradationResult{Frontier: "communities", Optional: true, Code: code, Message: message}
}

func communityDiscoveryDegradation() *RecallDegradationResult {
	return &RecallDegradationResult{
		Frontier: "communities",
		Optional: true,
		Code:     "community_discovery_unavailable",
		Message:  "community discovery was unavailable; primary evidence recall was used",
	}
}

func relationshipVectorDegradation(state string) *RecallDegradationResult {
	code := "relationship_vector_warming"
	message := "Relationship vector projection is warming; lexical discovery was used."
	if state == string(domain.SearchProjectionFailed) {
		code = "relationship_vector_failed"
		message = "Relationship vector projection failed; lexical discovery was used."
	}
	return &RecallDegradationResult{
		Frontier: "relationships",
		Optional: true,
		Code:     code,
		Message:  message,
	}
}

func (s *recallService) recallRelatedHypotheses(
	ctx context.Context,
	teamID string,
	_ string,
	query string,
) ([]RelatedHypothesisSummary, *RecallDegradationResult) {
	if s.hypotheses == nil || strings.TrimSpace(query) == "" {
		return []RelatedHypothesisSummary{}, nil
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
		Frontier: "hypotheses",
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

func recallCommunitiesFromRepository(records []repository.CommunityRecallRecord) []RecallDiscoveryPath {
	out := make([]RecallDiscoveryPath, 0, len(records))
	for _, record := range records {
		if len(record.Relationships) == 0 {
			continue
		}
		community := RecallDiscoveryPath{
			CommunityID: record.CommunityID, LogicalCommunityID: record.LogicalCommunityID, Rank: record.Rank, Summary: record.Summary,
			TopPredicates: append([]string(nil), record.TopPredicates...), EntityCount: record.EntityCount,
			RelationshipCount: record.RelationshipCount, RelationshipsTruncated: record.RelationshipsTruncated,
			TopEntities:            make([]EntityHandle, 0, len(record.TopEntities)),
			CommunityRelationships: make([]RelatedRelationshipSummary, 0, len(record.Relationships)),
		}
		for _, entity := range record.TopEntities {
			community.TopEntities = append(community.TopEntities, EntityHandle{EntityID: entity.EntityID, Name: entity.Name})
		}
		for _, relationship := range record.Relationships {
			community.CommunityRelationships = append(community.CommunityRelationships, RelatedRelationshipSummary{
				RelationshipID:            relationship.RelationshipID,
				EquivalentRelationshipIDs: append([]string{}, relationship.EquivalentRelationshipIDs...),
				SemanticGroupKey:          relationship.SemanticGroupKey,
				Subject:                   EntityHandle{EntityID: relationship.SubjectEntityID, Name: relationship.SubjectName},
				Predicate:                 relationship.PredicateKey, Object: recallRelationshipObject(relationship),
				Polarity: relationship.Polarity, EvidenceIDs: append([]string(nil), relationship.EvidenceIDs...),
				SearchState: relationship.SearchState,
			})
		}
		out = append(out, community)
	}
	return out
}

func relatedRelationshipSummaries(recalled *repository.RecallRelationshipsResult) []RelatedRelationshipSummary {
	if recalled == nil {
		return []RelatedRelationshipSummary{}
	}
	out := make([]RelatedRelationshipSummary, 0, len(recalled.Results))
	for _, record := range recalled.Results {
		out = append(out, RelatedRelationshipSummary{
			RelationshipID:            record.RelationshipID,
			EquivalentRelationshipIDs: append([]string{}, record.EquivalentRelationshipIDs...),
			SemanticGroupKey:          record.SemanticGroupKey,
			Subject: EntityHandle{
				EntityID: record.SubjectEntityID,
				Name:     record.SubjectName,
			},
			Predicate:   record.PredicateKey,
			Object:      recallRelationshipObject(record),
			Polarity:    record.Polarity,
			EvidenceIDs: append([]string(nil), record.EvidenceIDs...),
			SearchState: record.SearchState,
			SpaceKind:   record.SpaceKind,
		})
	}
	return out
}

func sortedGroupKeys(groups map[string]struct{}) []string {
	keys := make([]string, 0, len(groups))
	for group := range groups {
		if strings.TrimSpace(group) != "" {
			keys = append(keys, group)
		}
	}
	sort.Strings(keys)
	return keys
}

func cloneGroupSet(groups map[string]struct{}) map[string]struct{} {
	cloned := make(map[string]struct{}, len(groups))
	for group := range groups {
		cloned[group] = struct{}{}
	}
	return cloned
}

func recallResultEvidenceIDs(results []RecallResultItem) []string {
	ids := make([]string, 0, len(results))
	seen := map[string]struct{}{}
	for _, result := range results {
		if result.EvidenceID == "" {
			continue
		}
		if _, ok := seen[result.EvidenceID]; ok {
			continue
		}
		seen[result.EvidenceID] = struct{}{}
		ids = append(ids, result.EvidenceID)
	}
	return ids
}

func relationshipSummaryIDs(values []RelatedRelationshipSummary) []string {
	ids := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if value.RelationshipID == "" {
			continue
		}
		if _, ok := seen[value.RelationshipID]; ok {
			continue
		}
		seen[value.RelationshipID] = struct{}{}
		ids = append(ids, value.RelationshipID)
	}
	return ids
}

func recallRelationshipObject(record repository.RecallRelationshipHit) SemanticObject {
	if record.ObjectEntityID != "" {
		return SemanticObject{
			EntityID: record.ObjectEntityID,
			Name:     record.ObjectName,
		}
	}
	return SemanticObject{
		ValueID: record.ObjectValueID,
		Type:    record.ObjectValueType,
		Value:   record.ObjectValue,
		Display: record.ObjectName,
	}
}

func (s *recallService) queryEmbedding(
	ctx context.Context,
	contract *repository.ActiveSearchContract,
	query string,
) ([]float32, *RecallDegradationResult) {
	if s.provider == nil || !s.provider.IsAvailable() {
		return nil, &RecallDegradationResult{
			Frontier: "evidence",
			Optional: true,
			Code:     string(domain.ErrorProviderUnavailable),
			Message:  "vector recall is unavailable; full-text evidence recall was used",
		}
	}
	if got := s.provider.Dimensions(); got != 0 && got != contract.EmbeddingDimensions {
		return nil, &RecallDegradationResult{
			Frontier: "evidence",
			Optional: true,
			Code:     string(domain.ErrorProviderMalformed),
			Message:  "vector recall provider dimensions do not match the active search contract",
		}
	}
	if got := strings.TrimSpace(s.provider.ModelName()); got != "" && got != contract.EmbeddingModel {
		return nil, &RecallDegradationResult{
			Frontier: "evidence",
			Optional: true,
			Code:     string(domain.ErrorProviderMalformed),
			Message:  "vector recall provider model does not match the active search contract",
		}
	}
	vector, model, err := s.provider.Embed(ctx, query)
	if err != nil {
		return nil, &RecallDegradationResult{
			Frontier: "evidence",
			Optional: true,
			Code:     string(domain.ErrorProviderUnavailable),
			Message:  "vector recall provider failed; full-text evidence recall was used",
		}
	}
	if model != "" && model != contract.EmbeddingModel {
		return nil, &RecallDegradationResult{
			Frontier: "evidence",
			Optional: true,
			Code:     string(domain.ErrorProviderMalformed),
			Message:  "vector recall provider returned the wrong model",
		}
	}
	if err := validateRecallEmbedding(vector, contract.EmbeddingDimensions); err != nil {
		return nil, &RecallDegradationResult{
			Frontier: "evidence",
			Optional: true,
			Code:     string(domain.ErrorProviderMalformed),
			Message:  "vector recall provider returned an invalid embedding",
		}
	}
	return vector, nil
}

func normalizeRecallRequest(req RecallRequest) RecallRequest {
	req.Query = strings.TrimSpace(req.Query)
	if req.Limit <= 0 {
		req.Limit = defaultRecallResultLimit
	}
	if req.Limit > maxRecallResultLimit {
		req.Limit = maxRecallResultLimit
	}
	req.RelationshipLimit = normalizeRecallOptionalLimit(req.RelationshipLimit, defaultRelatedRelationshipLimit, maxRelatedRelationshipLimit)
	req.CommunityLimit = normalizeRecallOptionalLimit(req.CommunityLimit, defaultCommunityPathLimit, maxCommunityPathLimit)
	req.CommunityRelationshipLimit = normalizeRecallPositiveLimit(req.CommunityRelationshipLimit, defaultRelatedRelationshipLimit, maxRelatedRelationshipLimit)
	req.KnownEvidenceIDs = normalizeRecallRequestIDs(req.KnownEvidenceIDs)
	req.KnownRelationshipIDs = normalizeRecallRequestIDs(req.KnownRelationshipIDs)
	req.ExpandFromEntityIDs = normalizeRecallRequestIDs(req.ExpandFromEntityIDs)
	return req
}

func normalizeRecallOptionalLimit(value *int, defaultValue int, maxValue int) *int {
	if value == nil {
		normalized := defaultValue
		return &normalized
	}
	normalized := *value
	if normalized < 0 {
		normalized = 0
	}
	if normalized > maxValue {
		normalized = maxValue
	}
	return &normalized
}

func normalizeRecallPositiveLimit(value *int, defaultValue int, maxValue int) *int {
	normalized := normalizeRecallOptionalLimit(value, defaultValue, maxValue)
	if *normalized < 1 {
		*normalized = 1
	}
	return normalized
}

func recallOptionalLimitValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
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
	degradations []RecallDegradationResult,
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
				Source:          item.Source,
				SourceType:      item.SourceType,
				CreatedAt:       recallCreatedAt(item.CreatedAt),
			})
		}
		conflicts = append(conflicts, recallConflictSummaries(recalled.Conflicts)...)
		conflicts = append(conflicts, recallEvidenceConflictSummaries(recalled.EvidenceConflicts)...)
		conflicts = limitRecallConflictSummaries(conflicts, 20)
	}
	result := &RecallResult{
		RecallID:             "rec_" + uuid.NewString(),
		Results:              results,
		Conflicts:            conflicts,
		RelatedRelationships: []RelatedRelationshipSummary{},
		RelatedCommunities:   []RecallDiscoveryPath{},
		RelatedHypotheses:    []RelatedHypothesisSummary{},
		SearchStates: RecallSearchStates{
			Evidence:      searchState,
			Relationships: string(domain.SearchProjectionCurrent),
		},
		Degradations:      append([]RecallDegradationResult(nil), degradations...),
		DiscoveryPaths:    []RecallDiscoveryPath{},
		DiscoveryGuidance: "No additional discovery guidance.",
		SearchState:       searchState,
	}
	if len(result.Degradations) > 0 {
		result.Degradation = &result.Degradations[0]
	}
	return result
}

func recallCreatedAt(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	createdAt := value.UTC()
	return &createdAt
}
