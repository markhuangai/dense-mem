// Package recallservice implements high-level memory recall for a single
// profile. Facts and validated claims are query-matched via full-text search;
// fragment recall remains a hybrid semantic + keyword flow merged with RRF.
//
// Merge strategy: Reciprocal Rank Fusion (RRF). For each candidate fragment
// we compute:
//
//	score(id) = Σ_branch 1 / (RRFConstant + rank_in_branch)
//
// RRF is used because it does not require score normalization across branches
// and is robust to the difference in scale between BM25 (keyword) and cosine
// similarity (semantic). Alternative merge strategies (weighted sum, Borda
// count) are explicitly deferred per AC-51.
//
// Embedding failure policy: fail-closed. If the embedding provider errors we
// surface a sanitized error to the caller. We deliberately do NOT fall back
// to keyword-only recall because that would silently degrade result quality
// and make the degradation invisible to callers (AC-40).
//
// The query embedding is used only within Recall and is never persisted
// (AC-40). Branch results are post-filtered by team_id as defense in
// depth, even though each branch already enforces the filter at the query
// layer. Only SourceFragment-typed hits are kept (AC-39).
package recallservice

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/tools/keywordsearch"
	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
)

// Tuning constants.
const (
	// OverfetchMultiplier sets how many times the requested limit each branch
	// fetches before merge. The global vector index is shared across profiles,
	// so we overfetch and post-filter for profile isolation (AC-40).
	OverfetchMultiplier = 10
	// IdentifierOverfetchFloor is the minimum branch candidate pool for exact-ID
	// queries. Low-limit exact-ID queries need enough candidates for rerank to
	// see the same-ID row in crowded reusable eval teams.
	IdentifierOverfetchFloor = 50
	// RRFConstant is the k parameter in Reciprocal Rank Fusion.
	RRFConstant = 60
	// DefaultLimit is used when RecallRequest.Limit is zero.
	DefaultLimit = 10
	// MinLimit and MaxLimit bound the effective result count.
	MinLimit = 1
	MaxLimit = 50

	// Tier constants for knowledge-pipeline recall enrichment.

	// TierActiveFact is the recall tier for active Fact nodes (highest authority).
	TierActiveFact = "1"
	// TierConflict is the recall tier for active facts with overlay/conflict context.
	TierConflict = "1.25"
	// TierValidatedClaim is the recall tier for validated Claim nodes.
	TierValidatedClaim = "1.5"
	// TierFragment is the recall tier for SourceFragment nodes (raw evidence).
	TierFragment = "2"

	// DefaultRecallValidatedClaimWeight scales validated-claim scores so that
	// active facts outrank equivalent claims under the default weight.
	DefaultRecallValidatedClaimWeight = 0.5

	// DefaultCommunityExpansionCommunityLimit caps the number of communities
	// selected for an opt-in community-aware recall expansion.
	DefaultCommunityExpansionCommunityLimit = 3
	// DefaultCommunityExpansionMembersPerCommunity caps fan-out per community.
	DefaultCommunityExpansionMembersPerCommunity = 10
	// DefaultCommunityExpansionScanLimit caps the summary rows scored in memory.
	DefaultCommunityExpansionScanLimit = 50
	// DefaultCommunityExpansionMinScore is the minimum summary match score.
	DefaultCommunityExpansionMinScore = 0.2
	// MaxKnownRelationshipIDs bounds stateless follow-up discovery requests.
	MaxKnownRelationshipIDs = 200
	// MaxKnownEvidenceIDs bounds stateless follow-up evidence suppression.
	MaxKnownEvidenceIDs = 200
	// MaxExpandFromEntityIDs bounds explicit graph expansion anchors.
	MaxExpandFromEntityIDs = 50
)

// ErrEmbeddingUnavailable is returned to callers when the embedding provider
// fails. The underlying provider error is logged (scrubbed) but never
// returned verbatim so provider keys / URLs cannot leak through the API.
var ErrEmbeddingUnavailable = errors.New("recall: embedding provider unavailable")

// ErrKeywordUnavailable is returned when the keyword branch fails.
var ErrKeywordUnavailable = errors.New("recall: keyword search unavailable")

// RecallRequest is the validated input to Recall. Validator tags are used by
// HTTP handlers via the shared BindAndValidate middleware; the service also
// enforces the clamp + non-empty invariants defensively.
type RecallRequest struct {
	Query                string     `json:"query" validate:"required,max=512"`
	Limit                int        `json:"limit" validate:"gte=0,lte=50"`
	ValidAt              *time.Time `json:"valid_at,omitempty"`
	KnownAt              *time.Time `json:"known_at,omitempty"`
	KnownEvidenceIDs     []string   `json:"known_evidence_ids,omitempty" validate:"max=200,dive,uuid"`
	ExpandFromEntityIDs  []string   `json:"expand_from_entity_ids,omitempty" validate:"max=50,dive,uuid"`
	KnownRelationshipIDs []string   `json:"known_relationship_ids,omitempty" validate:"max=200,dive,uuid"`

	// Legacy-only fields remain for internal context assembly and the old
	// graph-backed recall implementation. They are intentionally not public JSON.
	IncludeEvidence        bool     `json:"-"`
	UseCommunities         bool     `json:"-"`
	ExcludeRelationshipIDs []string `json:"-"`
}

// RecallHit is one merged, hydrated recall result.
//
// Tier classifies the knowledge-pipeline level:
//   - TierActiveFact    ("1")   – promoted, active Fact node
//   - TierValidatedClaim ("1.5") – validated Claim node
//   - TierFragment       ("2")   – raw SourceFragment (RRF-ranked)
//
// Fragment, Claim, and Fact are mutually exclusive: exactly one is non-nil per hit.
// SemanticRank, KeywordRank, and FinalScore are populated for TierFragment hits
// and preserved for backward compatibility.
type RecallHit struct {
	Fragment      *domain.Fragment                     `json:"fragment,omitempty"`
	Claim         *domain.Claim                        `json:"claim,omitempty"` // tier 1.5
	Fact          *domain.Fact                         `json:"fact,omitempty"`  // tier 1
	Relationship  *domain.SemanticRelationship         `json:"relationship,omitempty"`
	Relationships []domain.SemanticRelationship        `json:"relationships,omitempty"`
	Evidence      *domain.SemanticEvidenceFragment     `json:"evidence,omitempty"`
	Evidences     []domain.SemanticEvidenceFragment    `json:"evidences,omitempty"`
	Supports      []domain.SemanticRelationshipSupport `json:"supports,omitempty"`
	Frontier      []domain.SemanticFrontierHint        `json:"frontier,omitempty"`
	Tier          string                               `json:"tier,omitempty"`
	Score         float64                              `json:"score,omitempty"`
	SemanticRank  int                                  `json:"semantic_rank"` // 1-based; 0 if absent from that branch
	KeywordRank   int                                  `json:"keyword_rank"`  // 1-based; 0 if absent from that branch
	FinalScore    float64                              `json:"final_score"`
	fragmentID    string
	temporalRank  time.Time
	sortTier      string
}

// RecallService is the external contract consumed by handlers and the tool
// registry.
type RecallService interface {
	Recall(ctx context.Context, profileID string, req RecallRequest) ([]RecallHit, error)
}

// EmbeddingProvider is the narrow slice of embedding.EmbeddingProviderInterface
// used by recall. Restated locally so tests can stub without pulling the full
// provider surface.
type EmbeddingProvider interface {
	Embed(ctx context.Context, text string) ([]float32, string, error)
}

// SemanticSearcher is the narrow slice of the vector index searcher.
type SemanticSearcher interface {
	QueryVectorIndex(ctx context.Context, profileID string, embedding []float32, limit int) ([]semanticsearch.SearchHit, error)
}

// KeywordSearcher is the narrow slice of the BM25 fragment searcher used for
// the fragment tier of recall.
type KeywordSearcher interface {
	SearchContent(ctx context.Context, profileID string, query string, labels []string, limit int) ([]keywordsearch.FragmentSearchResult, error)
}

// FragmentHydrator loads the full domain.Fragment for a winning id.
type FragmentHydrator interface {
	GetByID(ctx context.Context, profileID, fragmentID string) (*domain.Fragment, error)
}

type fragmentBatchHydrator interface {
	GetByIDs(ctx context.Context, profileID string, fragmentIDs []string) (map[string]*domain.Fragment, error)
}

// FactRecallResult is one query-matched tier-1 candidate before hydration.
type FactRecallResult struct {
	FactID         string
	ProfileID      string
	Score          float64
	ValidFrom      *time.Time
	ValidTo        *time.Time
	RecordedAt     time.Time
	RecordedTo     *time.Time
	AuthorityState string
}

// ClaimRecallResult is one query-matched tier-1.5 candidate before hydration.
type ClaimRecallResult struct {
	ClaimID    string
	ProfileID  string
	Score      float64
	ValidFrom  *time.Time
	ValidTo    *time.Time
	RecordedAt time.Time
	RecordedTo *time.Time
}

// FactSearcher fetches query-matched active facts for tier-1 recall.
// Implementations must scope all queries to profileID.
type FactSearcher interface {
	SearchActive(ctx context.Context, profileID string, query string, limit int) ([]FactRecallResult, error)
}

// ClaimSearcher fetches query-matched validated claims for tier-1.5 recall.
// Implementations must scope all queries to profileID.
type ClaimSearcher interface {
	SearchValidated(ctx context.Context, profileID string, query string, limit int) ([]ClaimRecallResult, error)
}

// FactHydrator loads one fact by ID for the recall tier response.
type FactHydrator interface {
	Get(ctx context.Context, profileID string, factID string) (*domain.Fact, error)
}

type factBatchHydrator interface {
	GetByIDs(ctx context.Context, profileID string, factIDs []string) (map[string]*domain.Fact, error)
}

// ClaimHydrator loads one claim by ID for the recall tier response.
type ClaimHydrator interface {
	Get(ctx context.Context, profileID string, claimID string) (*domain.Claim, error)
}

type claimBatchHydrator interface {
	GetByIDs(ctx context.Context, profileID string, claimIDs []string) (map[string]*domain.Claim, error)
}

// CommunityExpansionOptions bounds opt-in expansion through persisted
// community summaries.
type CommunityExpansionOptions struct {
	CommunityLimit      int
	MembersPerCommunity int
	ScanLimit           int
	MaxCandidates       int
	MinScore            float64
}

// CommunityFragmentRecallResult is one community-member fragment before
// hydration.
type CommunityFragmentRecallResult struct {
	FragmentID string
	ProfileID  string
	Score      float64
}

// CommunityExpansion is the bounded set of original graph members selected by
// community-aware recall.
type CommunityExpansion struct {
	SelectedCommunities int
	Facts               []FactRecallResult
	Claims              []ClaimRecallResult
	Fragments           []CommunityFragmentRecallResult
}

// CommunityExpander selects matching persisted communities and returns their
// original member nodes. Implementations must scope all reads to profileID.
type CommunityExpander interface {
	Expand(ctx context.Context, profileID string, query string, opts CommunityExpansionOptions) (CommunityExpansion, error)
}

// RecallServiceOption mutates optional recall dependencies while preserving the
// stable constructor surface.
type RecallServiceOption func(*recallService)

// WithCommunityExpander enables opt-in community-aware recall expansion.
func WithCommunityExpander(expander CommunityExpander) RecallServiceOption {
	return func(s *recallService) {
		s.communityExpander = expander
	}
}

// recallService implements RecallService.
type recallService struct {
	embedder          EmbeddingProvider
	semantic          SemanticSearcher
	keyword           KeywordSearcher
	hydrator          FragmentHydrator
	factSearcher      FactSearcher // optional; nil → tier-1 results omitted
	factGet           FactHydrator
	claimSearcher     ClaimSearcher // optional; nil → tier-1.5 results omitted
	claimGet          ClaimHydrator
	claimWeight       float64 // weight applied to claim scores (default DefaultRecallValidatedClaimWeight)
	communityExpander CommunityExpander
	logger            observability.LogProvider
	metrics           observability.DiscoverabilityMetrics
}

var _ RecallService = (*recallService)(nil)

// NewRecallService constructs a RecallService. All dependencies are required
// except logger (may be nil — logging becomes a no-op) and metrics (may be
// nil — a noop recorder is substituted so call sites never need nil checks).
//
// Tier expansion (facts / claims) is disabled. Use NewRecallServiceWithTiers
// when tier-1 and tier-1.5 enrichment is desired.
func NewRecallService(
	embedder EmbeddingProvider,
	semantic SemanticSearcher,
	keyword KeywordSearcher,
	hydrator FragmentHydrator,
	logger observability.LogProvider,
	metrics observability.DiscoverabilityMetrics,
) RecallService {
	return NewRecallServiceWithTiers(embedder, semantic, keyword, hydrator, nil, nil, nil, nil, 0, logger, metrics)
}

// NewRecallServiceWithTiers constructs a RecallService with optional tier-1
// (active facts) and tier-1.5 (validated claims) enrichment.
//
// factSearcher/factGet and claimSearcher/claimGet may be nil — those tiers are
// silently omitted.
// claimWeight is the multiplier applied to claim scores; pass 0 to use
// DefaultRecallValidatedClaimWeight (0.5). This ensures active facts outrank
// validated claims of equivalent base confidence under the default weight.
//
// logger may be nil (no-op). metrics may be nil (noop recorder substituted).
func NewRecallServiceWithTiers(
	embedder EmbeddingProvider,
	semantic SemanticSearcher,
	keyword KeywordSearcher,
	hydrator FragmentHydrator,
	factSearcher FactSearcher,
	factGet FactHydrator,
	claimSearcher ClaimSearcher,
	claimGet ClaimHydrator,
	claimWeight float64,
	logger observability.LogProvider,
	metrics observability.DiscoverabilityMetrics,
	options ...RecallServiceOption,
) RecallService {
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	if claimWeight <= 0 {
		claimWeight = DefaultRecallValidatedClaimWeight
	}
	svc := &recallService{
		embedder:      embedder,
		semantic:      semantic,
		keyword:       keyword,
		hydrator:      hydrator,
		factSearcher:  factSearcher,
		factGet:       factGet,
		claimSearcher: claimSearcher,
		claimGet:      claimGet,
		claimWeight:   claimWeight,
		logger:        logger,
		metrics:       metrics,
	}
	for _, option := range options {
		if option != nil {
			option(svc)
		}
	}
	return svc
}

// Recall runs both branches in parallel, merges via RRF, and returns the top
// `limit` hydrated fragments for the caller's profile.
func (s *recallService) Recall(ctx context.Context, profileID string, req RecallRequest) ([]RecallHit, error) {
	start := time.Now()
	outcome := "ok"
	resultCount := 0
	defer func() {
		observability.RecordRecall(ctx, s.metrics, float64(time.Since(start).Milliseconds()), resultCount, outcome)
	}()

	if profileID == "" {
		outcome = "validation_error"
		return nil, errors.New("recall: profile id is required")
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		outcome = "validation_error"
		return nil, errors.New("recall: query is required")
	}

	limit := clampLimit(req.Limit)
	overfetch := recallOverfetchLimit(query, limit)

	var (
		wg      sync.WaitGroup
		semHits []semanticsearch.SearchHit
		semErr  error
		kwHits  []keywordsearch.FragmentSearchResult
		kwErr   error
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		vec, _, err := s.embedder.Embed(ctx, query)
		if err != nil {
			semErr = sanitizeEmbeddingError(err)
			return
		}
		// vec is request-scoped: used only for this kNN query and never
		// written to any store (AC-40 explicit).
		hits, err := s.semantic.QueryVectorIndex(ctx, profileID, vec, overfetch)
		if err != nil {
			semErr = fmt.Errorf("recall: semantic branch: %w", err)
			return
		}
		semHits = hits
	}()
	go func() {
		defer wg.Done()
		hits, err := s.keyword.SearchContent(ctx, profileID, query, nil, overfetch)
		if err != nil {
			kwErr = fmt.Errorf("recall: keyword branch: %w", err)
			return
		}
		kwHits = hits
	}()
	wg.Wait()

	if semErr != nil {
		s.logEmbeddingError(semErr)
		outcome = "embedding_unavailable"
		return nil, ErrEmbeddingUnavailable
	}
	if kwErr != nil {
		s.logKeywordError(kwErr)
		outcome = "keyword_unavailable"
		return nil, ErrKeywordUnavailable
	}

	filteredSem := filterSemanticFragments(semHits, profileID)
	filteredKw := filterKeywordFragments(kwHits, profileID)
	filteredSem = filterSemanticFragmentsByWindow(filteredSem, req.ValidAt, req.KnownAt)
	filteredKw = filterKeywordFragmentsByWindow(filteredKw, req.ValidAt, req.KnownAt)

	merged := rrfMerge(filteredSem, filteredKw)
	applyIdentifierSpecificityAdjustments(query, merged)
	applyCurrentnessAdjustments(query, merged)
	applyCueAdjustments(query, merged)
	applyAuthorityAdjustments(query, merged)
	merged = filterNonPositiveRRFEntries(merged)

	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].FinalScore != merged[j].FinalScore {
			return merged[i].FinalScore > merged[j].FinalScore
		}
		return merged[i].id < merged[j].id
	})

	// Collect tier-1 (active facts) and tier-1.5 (validated claims) enrichment.
	tierHits := s.enrichTierHits(ctx, profileID, overfetch, req)

	evidenceIntent := isEvidenceSourceQuery(query)

	// Merge tier hits with unhydrated fragment candidates, sort by (tier ASC,
	// score DESC), then hydrate only selected fragment winners.
	all := append([]RecallHit{}, tierHits...)
	for _, m := range merged {
		sortTier := ""
		if evidenceIntent {
			sortTier = "0.5"
		}
		all = append(all, RecallHit{
			Tier:         TierFragment,
			Score:        m.FinalScore,
			SemanticRank: m.SemanticRank,
			KeywordRank:  m.KeywordRank,
			FinalScore:   m.FinalScore,
			fragmentID:   m.id,
			sortTier:     sortTier,
		})
	}
	sortRecallHits(all)

	if len(all) > limit {
		all = all[:limit]
	}
	all = s.hydrateSelectedFragments(ctx, profileID, all)
	if req.UseCommunities && len(all) < limit {
		all = append(all, s.enrichCommunityHits(ctx, profileID, limit-len(all), req, all)...)
		sortRecallHits(all)
		if len(all) > limit {
			all = all[:limit]
		}
	}
	resultCount = len(all)
	return all, nil
}

func (s *recallService) hydrateSelectedFragments(ctx context.Context, profileID string, hits []RecallHit) []RecallHit {
	entries := make([]rrfEntry, 0, len(hits))
	for _, hit := range hits {
		if hit.Tier == TierFragment && hit.Fragment == nil && hit.fragmentID != "" {
			entries = append(entries, rrfEntry{id: hit.fragmentID})
		}
	}
	fragmentsByID, batchFragments := s.batchHydrateFragments(ctx, profileID, entries)

	out := make([]RecallHit, 0, len(hits))
	for _, hit := range hits {
		if hit.Tier != TierFragment || hit.Fragment != nil {
			out = append(out, hit)
			continue
		}
		if hit.fragmentID == "" {
			continue
		}

		var frag *domain.Fragment
		if batchFragments {
			frag = fragmentsByID[hit.fragmentID]
			if frag == nil {
				s.logHydrateError(hit.fragmentID, errors.New("fragment not found"))
				continue
			}
		} else {
			var err error
			frag, err = s.hydrator.GetByID(ctx, profileID, hit.fragmentID)
			if err != nil {
				// A winning id may vanish due to a concurrent delete or retraction
				// (AC-44). In both cases we skip the id rather than failing the whole
				// recall so that the remaining results are still returned to the caller.
				s.logHydrateError(hit.fragmentID, err)
				continue
			}
		}

		hit.Fragment = frag
		hit.fragmentID = ""
		out = append(out, hit)
	}
	return out
}

func (s *recallService) batchHydrateFragments(ctx context.Context, profileID string, merged []rrfEntry) (map[string]*domain.Fragment, bool) {
	batch, ok := s.hydrator.(fragmentBatchHydrator)
	if !ok || len(merged) == 0 {
		return nil, false
	}
	ids := make([]string, 0, len(merged))
	for _, entry := range merged {
		if entry.id != "" {
			ids = append(ids, entry.id)
		}
	}
	if len(ids) == 0 {
		return nil, false
	}
	fragments, err := batch.GetByIDs(ctx, profileID, ids)
	if err != nil {
		s.logHydrateError("batch", err)
		return nil, false
	}
	return fragments, true
}

// enrichTierHits fetches tier-1 (active facts) and tier-1.5 (validated claims)
// hits that actually match the recall query. Errors are logged and swallowed so
// that a failing tier enrichment does not prevent fragment recall from
// completing.
func (s *recallService) enrichTierHits(ctx context.Context, profileID string, limit int, req RecallRequest) []RecallHit {
	var hits []RecallHit

	if s.factSearcher != nil && s.factGet != nil {
		facts, err := s.factSearcher.SearchActive(ctx, profileID, req.Query, limit)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("recall: tier-1 fact search failed",
					observability.String("error", err.Error()),
				)
			}
		} else {
			filtered := make([]FactRecallResult, 0, len(facts))
			for _, candidate := range facts {
				if candidate.FactID == "" || candidate.ProfileID != "" && candidate.ProfileID != profileID {
					continue
				}
				if !factCandidateMatchesRecallWindow(candidate, req.ValidAt, req.KnownAt) {
					continue
				}
				filtered = append(filtered, candidate)
			}
			factsByID, batchFacts := s.batchHydrateFacts(ctx, profileID, filtered)
			hydrated := make([]hydratedFactRecallCandidate, 0, len(filtered))
			for _, candidate := range filtered {
				var f *domain.Fact
				if batchFacts {
					f = factsByID[candidate.FactID]
					if f == nil {
						s.logHydrateError(candidate.FactID, errors.New("fact not found"))
						continue
					}
				} else {
					var err error
					f, err = s.factGet.Get(ctx, profileID, candidate.FactID)
					if err != nil {
						s.logHydrateError(candidate.FactID, err)
						continue
					}
				}
				if !factMatchesRecallWindow(f, req.ValidAt, req.KnownAt) {
					continue
				}
				if !factMatchesQueryIdentifiers(req.Query, f) {
					continue
				}
				authorityState := candidate.AuthorityState
				if authorityState == "" {
					authorityState = f.AuthorityState
				}
				if authorityState == "" {
					authorityState = "authoritative"
				}
				hydrated = append(hydrated, hydratedFactRecallCandidate{Fact: f, AuthorityState: authorityState})
			}
			var evidenceFragments map[string]*domain.Fragment
			var temporalFrame typedCurrentnessTemporalFrame
			if isCurrentnessQuery(req.Query) {
				evidenceFragments = s.batchHydrateEvidenceFragments(ctx, profileID, factEvidenceSets(hydrated))
				temporalFrame = typedCurrentnessTemporalFrameForFacts(req.Query, hydrated, evidenceFragments)
			}
			for _, candidate := range hydrated {
				f := candidate.Fact
				f.AuthorityState = candidate.AuthorityState
				tier := TierActiveFact
				if candidate.AuthorityState != "authoritative" {
					tier = TierConflict
				}
				temporalRank := typedTemporalRankTimeForRecallWithEvidence(req.Query, f.ValidFrom, f.ValidTo, f.RecordedAt, f.Evidence, evidenceFragments, temporalFrame, f.Subject, f.Predicate, f.Object)
				if !req.IncludeEvidence {
					factCopy := *f
					factCopy.Evidence = nil
					f = &factCopy
				}
				hits = append(hits, RecallHit{
					Fact:         f,
					Tier:         tier,
					Score:        f.TruthScore,
					temporalRank: temporalRank,
				})
			}
		}
	}

	if s.claimSearcher != nil && s.claimGet != nil {
		claims, err := s.claimSearcher.SearchValidated(ctx, profileID, req.Query, limit)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("recall: tier-1.5 claim search failed",
					observability.String("error", err.Error()),
				)
			}
		} else {
			filtered := make([]ClaimRecallResult, 0, len(claims))
			for _, candidate := range claims {
				if candidate.ClaimID == "" || candidate.ProfileID != "" && candidate.ProfileID != profileID {
					continue
				}
				if !claimCandidateMatchesRecallWindow(candidate, req.ValidAt, req.KnownAt) {
					continue
				}
				filtered = append(filtered, candidate)
			}
			claimsByID, batchClaims := s.batchHydrateClaims(ctx, profileID, filtered)
			hydrated := make([]*domain.Claim, 0, len(filtered))
			for _, candidate := range filtered {
				var c *domain.Claim
				if batchClaims {
					c = claimsByID[candidate.ClaimID]
					if c == nil {
						s.logHydrateError(candidate.ClaimID, errors.New("claim not found"))
						continue
					}
				} else {
					var err error
					c, err = s.claimGet.Get(ctx, profileID, candidate.ClaimID)
					if err != nil {
						s.logHydrateError(candidate.ClaimID, err)
						continue
					}
				}
				if !claimMatchesRecallWindow(c, req.ValidAt, req.KnownAt) {
					continue
				}
				if !claimMatchesQueryIdentifiers(req.Query, c) {
					continue
				}
				hydrated = append(hydrated, c)
			}
			var evidenceFragments map[string]*domain.Fragment
			var temporalFrame typedCurrentnessTemporalFrame
			if isCurrentnessQuery(req.Query) {
				evidenceFragments = s.batchHydrateEvidenceFragments(ctx, profileID, claimEvidenceSets(hydrated))
				temporalFrame = typedCurrentnessTemporalFrameForClaims(req.Query, hydrated, evidenceFragments)
			}
			for _, c := range hydrated {
				temporalRank := typedTemporalRankTimeForRecallWithEvidence(req.Query, c.ValidFrom, c.ValidTo, c.RecordedAt, c.Evidence, evidenceFragments, temporalFrame, c.Subject, c.Predicate, c.Object)
				if !req.IncludeEvidence {
					claimCopy := *c
					claimCopy.Evidence = nil
					c = &claimCopy
				}
				score := c.ExtractConf * s.claimWeight
				hits = append(hits, RecallHit{
					Claim:        c,
					Tier:         TierValidatedClaim,
					Score:        score,
					temporalRank: temporalRank,
				})
			}
		}
	}

	return hits
}

type hydratedFactRecallCandidate struct {
	Fact           *domain.Fact
	AuthorityState string
}

func (s *recallService) batchHydrateFacts(ctx context.Context, profileID string, candidates []FactRecallResult) (map[string]*domain.Fact, bool) {
	batch, ok := s.factGet.(factBatchHydrator)
	if !ok || len(candidates) == 0 {
		return nil, false
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.FactID != "" {
			ids = append(ids, candidate.FactID)
		}
	}
	if len(ids) == 0 {
		return nil, false
	}
	facts, err := batch.GetByIDs(ctx, profileID, ids)
	if err != nil {
		s.logHydrateError("facts batch", err)
		return nil, false
	}
	return facts, true
}

func (s *recallService) batchHydrateClaims(ctx context.Context, profileID string, candidates []ClaimRecallResult) (map[string]*domain.Claim, bool) {
	batch, ok := s.claimGet.(claimBatchHydrator)
	if !ok || len(candidates) == 0 {
		return nil, false
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ClaimID != "" {
			ids = append(ids, candidate.ClaimID)
		}
	}
	if len(ids) == 0 {
		return nil, false
	}
	claims, err := batch.GetByIDs(ctx, profileID, ids)
	if err != nil {
		s.logHydrateError("claims batch", err)
		return nil, false
	}
	return claims, true
}

func (s *recallService) batchHydrateEvidenceFragments(ctx context.Context, profileID string, evidenceSets [][]domain.Evidence) map[string]*domain.Fragment {
	if s.hydrator == nil {
		return nil
	}
	seen := make(map[string]struct{})
	entries := make([]rrfEntry, 0)
	for _, evidence := range evidenceSets {
		for _, item := range evidence {
			if item.FragmentID == "" {
				continue
			}
			if _, ok := seen[item.FragmentID]; ok {
				continue
			}
			seen[item.FragmentID] = struct{}{}
			entries = append(entries, rrfEntry{id: item.FragmentID})
		}
	}
	if len(entries) == 0 {
		return nil
	}
	fragments, ok := s.batchHydrateFragments(ctx, profileID, entries)
	if ok {
		return fragments
	}
	out := make(map[string]*domain.Fragment, len(entries))
	for _, entry := range entries {
		frag, err := s.hydrator.GetByID(ctx, profileID, entry.id)
		if err != nil {
			s.logHydrateError(entry.id, err)
			continue
		}
		if frag != nil {
			out[entry.id] = frag
		}
	}
	return out
}

func factEvidenceSets(candidates []hydratedFactRecallCandidate) [][]domain.Evidence {
	sets := make([][]domain.Evidence, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Fact != nil {
			sets = append(sets, candidate.Fact.Evidence)
		}
	}
	return sets
}

func claimEvidenceSets(claims []*domain.Claim) [][]domain.Evidence {
	sets := make([][]domain.Evidence, 0, len(claims))
	for _, claim := range claims {
		if claim != nil {
			sets = append(sets, claim.Evidence)
		}
	}
	return sets
}

// rrfEntry is the internal accumulator keyed by fragment id.
type rrfEntry struct {
	id           string
	Content      string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	SemanticRank int
	KeywordRank  int
	FinalScore   float64
}

// rrfMerge computes score(id) = Σ 1 / (RRFConstant + rank) across branches.
// Each branch contributes the 1-based rank of the id within that branch.
func rrfMerge(sem []semanticsearch.SearchHit, kw []keywordsearch.FragmentSearchResult) []rrfEntry {
	byID := make(map[string]*rrfEntry, len(sem)+len(kw))
	for i, h := range sem {
		rank := i + 1
		e, ok := byID[h.ID]
		if !ok {
			e = &rrfEntry{id: h.ID}
			byID[h.ID] = e
		}
		if e.Content == "" {
			e.Content = h.Content
		}
		mergeRRFEntryTimes(e, semanticHitTime(h.CreatedAt), semanticHitTime(h.UpdatedAt))
		if e.SemanticRank == 0 || rank < e.SemanticRank {
			e.SemanticRank = rank
		}
		e.FinalScore += 1.0 / float64(RRFConstant+rank)
	}
	for i, h := range kw {
		rank := i + 1
		e, ok := byID[h.FragmentID]
		if !ok {
			e = &rrfEntry{id: h.FragmentID}
			byID[h.FragmentID] = e
		}
		if e.Content == "" {
			e.Content = h.Content
		}
		mergeRRFEntryTimes(e, h.CreatedAt, h.UpdatedAt)
		if e.KeywordRank == 0 || rank < e.KeywordRank {
			e.KeywordRank = rank
		}
		e.FinalScore += 1.0 / float64(RRFConstant+rank)
	}
	out := make([]rrfEntry, 0, len(byID))
	for _, e := range byID {
		out = append(out, *e)
	}
	return out
}

func semanticHitTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}

func filterNonPositiveRRFEntries(entries []rrfEntry) []rrfEntry {
	hasPositive := false
	for _, entry := range entries {
		if entry.FinalScore > 0 {
			hasPositive = true
			break
		}
	}
	if !hasPositive {
		return entries
	}

	out := entries[:0]
	for _, entry := range entries {
		if entry.FinalScore > 0 {
			out = append(out, entry)
		}
	}
	return out
}

func mergeRRFEntryTimes(entry *rrfEntry, createdAt, updatedAt time.Time) {
	if entry == nil {
		return
	}
	if !createdAt.IsZero() && (entry.CreatedAt.IsZero() || createdAt.Before(entry.CreatedAt)) {
		entry.CreatedAt = createdAt
	}
	if !updatedAt.IsZero() && (entry.UpdatedAt.IsZero() || updatedAt.After(entry.UpdatedAt)) {
		entry.UpdatedAt = updatedAt
	}
}

// filterSemanticFragments drops hits outside the caller's profile and any
// non-fragment hits (defense-in-depth; the searcher already restricts both).
func filterSemanticFragments(hits []semanticsearch.SearchHit, profileID string) []semanticsearch.SearchHit {
	out := make([]semanticsearch.SearchHit, 0, len(hits))
	for _, h := range hits {
		if h.ProfileID != profileID {
			continue
		}
		if h.Type != "" && h.Type != "fragment" {
			continue
		}
		out = append(out, h)
	}
	return out
}

// filterKeywordFragments drops any hit not belonging to the caller's profile.
func filterKeywordFragments(hits []keywordsearch.FragmentSearchResult, profileID string) []keywordsearch.FragmentSearchResult {
	out := make([]keywordsearch.FragmentSearchResult, 0, len(hits))
	for _, h := range hits {
		if h.ProfileID != profileID {
			continue
		}
		out = append(out, h)
	}
	return out
}

// clampLimit enforces the [MinLimit, MaxLimit] bound and defaults zero to
// DefaultLimit.
func clampLimit(req int) int {
	if req <= 0 {
		return DefaultLimit
	}
	if req > MaxLimit {
		return MaxLimit
	}
	if req < MinLimit {
		return MinLimit
	}
	return req
}

func recallOverfetchLimit(query string, limit int) int {
	overfetch := limit * OverfetchMultiplier
	if len(rerankIdentifiers(rerankText(query))) > 0 && overfetch < IdentifierOverfetchFloor {
		return IdentifierOverfetchFloor
	}
	return overfetch
}
