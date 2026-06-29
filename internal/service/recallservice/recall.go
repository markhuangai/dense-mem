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
	"github.com/markhuangai/dense-mem/internal/embedding"
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
	Query           string     `json:"query" validate:"required,max=512"`
	Limit           int        `json:"limit" validate:"gte=0,lte=50"`
	ValidAt         *time.Time `json:"valid_at,omitempty"`
	KnownAt         *time.Time `json:"known_at,omitempty"`
	IncludeEvidence bool       `json:"include_evidence,omitempty"`
	UseCommunities  bool       `json:"use_communities,omitempty"`
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
	Fragment     *domain.Fragment `json:"fragment,omitempty"`
	Claim        *domain.Claim    `json:"claim,omitempty"` // tier 1.5
	Fact         *domain.Fact     `json:"fact,omitempty"`  // tier 1
	Tier         string           `json:"tier,omitempty"`
	Score        float64          `json:"score,omitempty"`
	SemanticRank int              `json:"semantic_rank"` // 1-based; 0 if absent from that branch
	KeywordRank  int              `json:"keyword_rank"`  // 1-based; 0 if absent from that branch
	FinalScore   float64          `json:"final_score"`
	fragmentID   string
	temporalRank time.Time
	sortTier     string
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
	overfetch := limit * OverfetchMultiplier

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
					authorityState = "authoritative"
				}
				if !req.IncludeEvidence {
					factCopy := *f
					factCopy.Evidence = nil
					f = &factCopy
				}
				f.AuthorityState = authorityState
				tier := TierActiveFact
				if authorityState != "authoritative" {
					tier = TierConflict
				}
				hits = append(hits, RecallHit{
					Fact:         f,
					Tier:         tier,
					Score:        f.TruthScore,
					temporalRank: temporalRankTimeForRecall(req.Query, f.ValidFrom, f.RecordedAt),
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
					temporalRank: temporalRankTimeForRecall(req.Query, c.ValidFrom, c.RecordedAt),
				})
			}
		}
	}

	return hits
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
		mergeRRFEntryTimes(e, h.CreatedAt, h.UpdatedAt)
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

func applyIdentifierSpecificityAdjustments(query string, entries []rrfEntry) {
	queryText := rerankText(query)
	if !isUnitValueQueryText(queryText) {
		return
	}
	if len(rerankIdentifiers(queryText)) == 0 {
		return
	}
	for i := range entries {
		entries[i].FinalScore += identifierSpecificityAdjustment(queryText, entries[i].Content)
	}
}

func isUnitValueQueryText(queryText string) bool {
	return strings.Contains(queryText, " timeout ") &&
		strings.Contains(queryText, " job ") &&
		(strings.Contains(queryText, " use ") || strings.Contains(queryText, " should "))
}

func identifierSpecificityAdjustment(queryText, content string) float64 {
	contentText := rerankText(content)
	if queryText == "" || contentText == "" {
		return 0
	}
	if !matchesQueryIdentifiers(queryText, contentText) {
		return 0
	}
	return 0.004
}

func applyCurrentnessAdjustments(query string, entries []rrfEntry) {
	if !isCurrentnessQuery(query) {
		return
	}
	temporalFrame := currentnessTemporalFrameFor(query, entries)
	for i := range entries {
		lexicalAdjustment := currentnessAdjustment(query, entries[i].Content)
		if temporalFrame.hasContentDate && lexicalAdjustment > 0 && latestTemporalDateInEntry(entries[i]).IsZero() {
			lexicalAdjustment = 0
		}
		entries[i].FinalScore += lexicalAdjustment
		entries[i].FinalScore += currentnessTemporalAdjustment(query, entries[i], temporalFrame)
		if entries[i].FinalScore < 0 {
			entries[i].FinalScore = 0
		}
	}
}

func applyCueAdjustments(query string, entries []rrfEntry) {
	if !isSelectionRecallQuery(query) {
		return
	}
	frame := selectionCueFrameFor(query, entries)
	for i := range entries {
		entries[i].FinalScore += cueAdjustment(query, entries[i].Content)
		entries[i].FinalScore += historicalSelectionAdjustment(query, entries[i].Content, frame)
		if entries[i].FinalScore < 0 {
			entries[i].FinalScore = 0
		}
	}
}

func applyAuthorityAdjustments(query string, entries []rrfEntry) {
	if !isAuthorityRecallQuery(query) {
		return
	}
	for i := range entries {
		entries[i].FinalScore += authorityAdjustment(query, entries[i].Content)
		if entries[i].FinalScore < 0 {
			entries[i].FinalScore = 0
		}
	}
}

func currentnessAdjustment(query, content string) float64 {
	queryText := rerankText(query)
	contentText := rerankText(content)
	if queryText == "" || contentText == "" {
		return 0
	}

	adjustment := 0.0
	hasCurrentCue := containsAnyRerankCue(contentText, currentnessPositiveCues)
	if hasCurrentCue && rerankMatchesQueryIdentifiers(queryText, contentText) {
		adjustment += 0.024
	}
	if containsAnyRerankCue(contentText, currentnessStrongStaleCues) {
		if hasCurrentCue {
			adjustment -= 0.006
		} else {
			adjustment -= 0.024
		}
	}
	if containsAnyRerankCue(contentText, currentnessWeakStaleCues) {
		adjustment -= 0.012
	}

	if adjustment > 0.028 {
		return 0.028
	}
	if adjustment < -0.028 {
		return -0.028
	}
	return adjustment
}

type currentnessTemporalFrame struct {
	hasContentDate       bool
	newestContentDate    time.Time
	useFragmentTimestamp bool
	newestFragmentTime   time.Time
}

func currentnessTemporalAdjustment(query string, entry rrfEntry, frame currentnessTemporalFrame) float64 {
	if !frame.hasContentDate && !frame.useFragmentTimestamp {
		return 0
	}
	queryText := rerankText(query)
	contentText := rerankText(entry.Content)
	if queryText == "" || contentText == "" || !rerankMatchesQueryIdentifiers(queryText, contentText) {
		return 0
	}

	if frame.hasContentDate {
		contentDate := latestTemporalDateInEntry(entry)
		if contentDate.IsZero() {
			return -0.006
		}
		return contentDateTemporalDelta(frame.newestContentDate.Sub(contentDate))
	}

	fragmentTime := latestFragmentTimestamp(entry.CreatedAt, entry.UpdatedAt)
	if fragmentTime.IsZero() {
		return 0
	}
	return fragmentTimestampTemporalDelta(frame.newestFragmentTime.Sub(fragmentTime))
}

func contentDateTemporalDelta(age time.Duration) float64 {
	if age < 0 {
		age = 0
	}
	switch {
	case age == 0:
		return 0.028
	case age <= 72*time.Hour:
		return 0.014
	case age >= 7*24*time.Hour:
		return -0.026
	case age >= 24*time.Hour:
		return -0.018
	default:
		return 0
	}
}

func fragmentTimestampTemporalDelta(age time.Duration) float64 {
	if age < 0 {
		age = 0
	}
	switch {
	case age == 0:
		return 0.014
	case age <= 72*time.Hour:
		return 0.007
	case age >= 7*24*time.Hour:
		return -0.014
	case age >= 24*time.Hour:
		return -0.010
	default:
		return 0
	}
}

func currentnessTemporalFrameFor(query string, entries []rrfEntry) currentnessTemporalFrame {
	queryText := rerankText(query)
	var frame currentnessTemporalFrame
	var oldestFragmentTime time.Time
	for _, entry := range entries {
		contentText := rerankText(entry.Content)
		if queryText == "" || contentText == "" || !rerankMatchesQueryIdentifiers(queryText, contentText) {
			continue
		}
		if contentDate := latestTemporalDateInEntry(entry); !contentDate.IsZero() {
			frame.hasContentDate = true
			if frame.newestContentDate.IsZero() || contentDate.After(frame.newestContentDate) {
				frame.newestContentDate = contentDate
			}
		}
		fragmentTime := latestFragmentTimestamp(entry.CreatedAt, entry.UpdatedAt)
		if fragmentTime.IsZero() {
			continue
		}
		if oldestFragmentTime.IsZero() || fragmentTime.Before(oldestFragmentTime) {
			oldestFragmentTime = fragmentTime
		}
		if frame.newestFragmentTime.IsZero() || fragmentTime.After(frame.newestFragmentTime) {
			frame.newestFragmentTime = fragmentTime
		}
	}
	if !frame.hasContentDate && !oldestFragmentTime.IsZero() && frame.newestFragmentTime.Sub(oldestFragmentTime) >= 24*time.Hour {
		frame.useFragmentTimestamp = true
	}
	return frame
}

func latestFragmentTimestamp(createdAt, updatedAt time.Time) time.Time {
	latest := createdAt
	if updatedAt.After(latest) {
		latest = updatedAt
	}
	if latest.IsZero() {
		return time.Time{}
	}
	return latest.UTC()
}

func temporalRankTimeForRecall(query string, validFrom *time.Time, recordedAt time.Time) time.Time {
	if !isCurrentnessQuery(query) {
		return time.Time{}
	}
	if validFrom != nil && !validFrom.IsZero() {
		return validFrom.UTC()
	}
	if !recordedAt.IsZero() {
		return recordedAt.UTC()
	}
	return time.Time{}
}

func factMatchesQueryIdentifiers(query string, fact *domain.Fact) bool {
	if fact == nil {
		return false
	}
	return knowledgeTripleMatchesQueryIdentifiers(query, fact.Subject, fact.Predicate, fact.Object)
}

func claimMatchesQueryIdentifiers(query string, claim *domain.Claim) bool {
	if claim == nil {
		return false
	}
	return knowledgeTripleMatchesQueryIdentifiers(query, claim.Subject, claim.Predicate, claim.Object)
}

func knowledgeTripleMatchesQueryIdentifiers(query string, parts ...string) bool {
	queryText := rerankText(query)
	if len(rerankIdentifiers(queryText)) == 0 {
		return true
	}
	return rerankMatchesQueryIdentifiers(queryText, rerankText(strings.Join(parts, " ")))
}
func latestISODateInText(value string) time.Time {
	var latest time.Time
	for _, field := range strings.Fields(value) {
		token := strings.Trim(field, ".,:;!?()[]{}\"'")
		if len(token) != len("2006-01-02") {
			continue
		}
		parsed, err := time.Parse("2006-01-02", token)
		if err != nil {
			continue
		}
		parsed = parsed.UTC()
		if latest.IsZero() || parsed.After(latest) {
			latest = parsed
		}
	}
	return latest
}

func latestTemporalDateInEntry(entry rrfEntry) time.Time {
	latest := latestISODateInText(entry.Content)
	relative := latestRelativeDateInText(entry.Content, latestFragmentTimestamp(entry.CreatedAt, entry.UpdatedAt))
	if !relative.IsZero() && (latest.IsZero() || relative.After(latest)) {
		latest = relative
	}
	return latest
}

func latestRelativeDateInText(value string, anchor time.Time) time.Time {
	if anchor.IsZero() {
		return time.Time{}
	}
	text := rerankText(value)
	if text == "" {
		return time.Time{}
	}
	anchorDate := utcDate(anchor)
	latest := time.Time{}
	add := func(candidate time.Time) {
		if !candidate.IsZero() && (latest.IsZero() || candidate.After(latest)) {
			latest = candidate
		}
	}
	fields := strings.Fields(text)
	for i, field := range fields {
		switch field {
		case "today":
			add(anchorDate)
		case "yesterday":
			add(anchorDate.AddDate(0, 0, -1))
		case "ago":
			if i >= 2 && (fields[i-1] == "day" || fields[i-1] == "days") {
				if days, ok := relativeDayCount(fields[i-2]); ok {
					add(anchorDate.AddDate(0, 0, -days))
				}
			}
		case "week":
			if i >= 1 && fields[i-1] == "last" {
				add(anchorDate.AddDate(0, 0, -7))
			}
		}
	}
	return latest
}

func utcDate(value time.Time) time.Time {
	value = value.UTC()
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func relativeDayCount(token string) (int, bool) {
	switch token {
	case "one", "a", "1":
		return 1, true
	case "two", "2":
		return 2, true
	case "three", "3":
		return 3, true
	case "four", "4":
		return 4, true
	case "five", "5":
		return 5, true
	case "six", "6":
		return 6, true
	case "seven", "7":
		return 7, true
	default:
		return 0, false
	}
}

func cueAdjustment(query, content string) float64 {
	queryText := rerankText(query)
	contentText := rerankText(content)
	if queryText == "" || contentText == "" {
		return 0
	}

	adjustment := 0.0
	boostAllowed := matchesQueryIdentifiers(queryText, contentText)
	if boostAllowed {
		if containsAnyCue(contentText, directiveCues) {
			adjustment += 0.022
		}
		if containsAnyCue(contentText, canonicalCues) {
			adjustment += 0.004
		}
	}
	if containsAnyCue(contentText, strongDisqualifierCues) {
		adjustment -= 0.022
	}
	if containsAnyCue(contentText, weakDisqualifierCues) {
		adjustment -= 0.012
	}
	if boostAllowed && containsAnyCue(queryText, conditionalQueryCues) && containsAnyCue(contentText, conditionalQueryCues) && containsAnyCue(contentText, directiveCues) {
		adjustment += 0.006
	}

	if adjustment > 0.026 {
		return 0.026
	}
	if adjustment < -0.026 {
		return -0.026
	}
	return adjustment
}

type selectionCueFrame struct {
	hasDirectiveMatch bool
}

func selectionCueFrameFor(query string, entries []rrfEntry) selectionCueFrame {
	queryText := rerankText(query)
	var frame selectionCueFrame
	for _, entry := range entries {
		contentText := rerankText(entry.Content)
		if queryText == "" || contentText == "" || !matchesQueryIdentifiers(queryText, contentText) {
			continue
		}
		if containsAnyCue(contentText, directiveCues) {
			frame.hasDirectiveMatch = true
			return frame
		}
	}
	return frame
}

func historicalSelectionAdjustment(query, content string, frame selectionCueFrame) float64 {
	if !frame.hasDirectiveMatch {
		return 0
	}
	queryText := rerankText(query)
	contentText := rerankText(content)
	if queryText == "" || contentText == "" || !matchesQueryIdentifiers(queryText, contentText) {
		return 0
	}
	if historicalSelectionActionCue(contentText) {
		return -0.034
	}
	return 0
}

func historicalSelectionActionCue(contentText string) bool {
	return strings.Contains(contentText, " before ") && strings.Contains(contentText, " used ")
}

func authorityAdjustment(query, content string) float64 {
	queryText := rerankText(query)
	contentText := rerankText(content)
	if queryText == "" || contentText == "" {
		return 0
	}

	adjustment := 0.0
	boostAllowed := authorityMatchesQueryIdentifiers(queryText, contentText)
	if boostAllowed {
		if containsAnyAuthorityCue(contentText, authorityPositiveCues) {
			adjustment += 0.026
		}
		if containsAnyAuthorityCue(contentText, authorityDirectiveCues) {
			adjustment += 0.006
		}
	}
	if containsAnyAuthorityCue(contentText, authorityStrongNegativeCues) {
		adjustment -= 0.028
	}
	if containsAnyAuthorityCue(contentText, authorityWeakNegativeCues) {
		adjustment -= 0.018
	}

	if adjustment > 0.034 {
		return 0.034
	}
	if adjustment < -0.034 {
		return -0.034
	}
	return adjustment
}

func isCurrentnessQuery(query string) bool {
	text := rerankText(query)
	return strings.Contains(text, " current ") ||
		strings.Contains(text, " as of ") ||
		strings.Contains(text, " now ") ||
		strings.Contains(text, " latest ") ||
		strings.Contains(text, " active ")
}

func isSelectionRecallQuery(query string) bool {
	text := rerankText(query)
	return strings.Contains(text, " which ") && (strings.Contains(text, " use ") || strings.Contains(text, " should "))
}

func rerankMatchesQueryIdentifiers(queryText, contentText string) bool {
	identifiers := rerankIdentifiers(queryText)
	return identifiersMatchContent(identifiers, contentText)
}

func matchesQueryIdentifiers(queryText, contentText string) bool {
	return rerankMatchesQueryIdentifiers(queryText, contentText)
}

func isAuthorityRecallQuery(query string) bool {
	text := rerankText(query)
	return strings.Contains(text, " authoritative ") ||
		strings.Contains(text, " canonical ") ||
		strings.Contains(text, " require ") ||
		strings.Contains(text, " requires ") ||
		strings.Contains(text, " required ")
}

func isEvidenceSourceQuery(query string) bool {
	text := rerankText(query)
	return strings.Contains(text, " which source ") ||
		strings.Contains(text, " what source ") ||
		strings.Contains(text, " source note ") ||
		strings.Contains(text, " source document ") ||
		strings.Contains(text, " supporting evidence ") ||
		strings.Contains(text, " raw evidence ") ||
		strings.Contains(text, " original evidence ") ||
		strings.Contains(text, " raw fragment ") ||
		strings.Contains(text, " source fragment ") ||
		strings.Contains(text, " which note ") ||
		strings.Contains(text, " what note ") ||
		strings.Contains(text, " note says ") ||
		strings.Contains(text, " note said ") ||
		strings.Contains(text, " mentioned ")
}

func authorityMatchesQueryIdentifiers(queryText, contentText string) bool {
	identifiers := rerankIdentifiers(queryText)
	return identifiersMatchContent(identifiers, contentText)
}

func identifiersMatchContent(identifiers []string, contentText string) bool {
	if len(identifiers) == 0 {
		return true
	}
	for _, identifier := range identifiers {
		if !strings.Contains(contentText, " "+identifier+" ") {
			return false
		}
	}
	return true
}

func rerankIdentifiers(text string) []string {
	fields := strings.Fields(text)
	out := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if _, ok := seen[field]; ok {
			continue
		}
		if rerankIdentifierToken(field) {
			out = append(out, field)
			seen[field] = struct{}{}
		}
	}
	return out
}

func rerankIdentifierToken(token string) bool {
	if _, err := time.Parse("2006-01-02", token); err == nil {
		return false
	}
	hasDigit := false
	for _, r := range token {
		if r >= '0' && r <= '9' {
			hasDigit = true
			break
		}
	}
	return hasDigit && strings.Contains(token, "-")
}

func rerankText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"\n", " ",
		"\t", " ",
		".", " ",
		",", " ",
		":", " ",
		";", " ",
		"?", " ",
		"!", " ",
		"(", " ",
		")", " ",
	)
	return " " + strings.Join(strings.Fields(replacer.Replace(value)), " ") + " "
}

func containsAnyRerankCue(text string, cues []string) bool {
	for _, cue := range cues {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func containsAnyCue(text string, cues []string) bool {
	return containsAnyRerankCue(text, cues)
}

func containsAnyAuthorityCue(text string, cues []string) bool {
	return containsAnyRerankCue(text, cues)
}

var currentnessPositiveCues = []string{
	" current ",
	" now ",
	" active ",
	" valid on ",
	" update dated ",
}

var currentnessStrongStaleCues = []string{
	" archived ",
	" obsolete ",
	" replaced ",
	" rejected ",
	" not active ",
	" not approved ",
}

var currentnessWeakStaleCues = []string{
	" legacy ",
	" previous ",
	" previously ",
	" before ",
	" older ",
	" old ",
	" draft ",
	" suggested ",
	" copied ",
	" rollback ",
	" incident review ",
	" proposed ",
	" once ",
	" future proposal ",
}

var directiveCues = []string{
	" must use ",
	" must be ",
	" should use ",
	" should be ",
	" is assigned ",
}

var canonicalCues = []string{
	" canonical ",
	" current ",
	" policy ",
	" rule ",
	" registry ",
}

var strongDisqualifierCues = []string{
	" does not apply ",
	" not about ",
	" rejected ",
	" unapproved ",
	" forbidden ",
	" false positive ",
	" false positives ",
}

var weakDisqualifierCues = []string{
	" legacy ",
	" previously ",
	" before ",
	" once ",
	" removed ",
	" fallback ",
	" draft ",
	" rumor ",
	" troubleshooting note ",
}

var conditionalQueryCues = []string{
	" enterprise ",
	" standard ",
	" tenant ",
	" exception ",
}

var authorityPositiveCues = []string{
	" authoritative ",
	" signed by ",
	" approved ",
	" official ",
	" canonical ",
	" source of truth ",
}

var authorityDirectiveCues = []string{
	" requires ",
	" require ",
	" required ",
	" must use ",
	" must be ",
}

var authorityStrongNegativeCues = []string{
	" informal chat ",
	" not approved ",
	" unapproved ",
	" personal checklist ",
	" meeting transcript ",
}

var authorityWeakNegativeCues = []string{
	" suggested ",
	" as an option ",
	" while testing ",
	" before ",
	" draft ",
	" note about ",
}

func factMatchesRecallWindow(f *domain.Fact, validAt, knownAt *time.Time) bool {
	if f == nil {
		return false
	}
	if validAt != nil {
		if f.ValidFrom != nil && f.ValidFrom.After(*validAt) {
			return false
		}
		if f.ValidTo != nil && !f.ValidTo.After(*validAt) {
			return false
		}
	}
	if knownAt != nil {
		if f.RecordedAt.After(*knownAt) {
			return false
		}
		if f.RecordedTo != nil && !f.RecordedTo.After(*knownAt) {
			return false
		}
	}
	return true
}

func claimMatchesRecallWindow(c *domain.Claim, validAt, knownAt *time.Time) bool {
	if c == nil {
		return false
	}
	if validAt != nil {
		if c.ValidFrom != nil && c.ValidFrom.After(*validAt) {
			return false
		}
		if c.ValidTo != nil && !c.ValidTo.After(*validAt) {
			return false
		}
	}
	if knownAt != nil {
		if c.RecordedAt.After(*knownAt) {
			return false
		}
		if c.RecordedTo != nil && !c.RecordedTo.After(*knownAt) {
			return false
		}
	}
	return true
}

func factCandidateMatchesRecallWindow(f FactRecallResult, validAt, knownAt *time.Time) bool {
	if validAt != nil {
		if f.ValidFrom != nil && f.ValidFrom.After(*validAt) {
			return false
		}
		if f.ValidTo != nil && !f.ValidTo.After(*validAt) {
			return false
		}
	}
	if knownAt != nil {
		if f.RecordedAt.After(*knownAt) {
			return false
		}
		if f.RecordedTo != nil && !f.RecordedTo.After(*knownAt) {
			return false
		}
	}
	return true
}

func claimCandidateMatchesRecallWindow(c ClaimRecallResult, validAt, knownAt *time.Time) bool {
	if validAt != nil {
		if c.ValidFrom != nil && c.ValidFrom.After(*validAt) {
			return false
		}
		if c.ValidTo != nil && !c.ValidTo.After(*validAt) {
			return false
		}
	}
	if knownAt != nil {
		if c.RecordedAt.After(*knownAt) {
			return false
		}
		if c.RecordedTo != nil && !c.RecordedTo.After(*knownAt) {
			return false
		}
	}
	return true
}

func filterSemanticFragmentsByWindow(hits []semanticsearch.SearchHit, validAt, knownAt *time.Time) []semanticsearch.SearchHit {
	if validAt == nil && knownAt == nil {
		return hits
	}
	out := hits[:0]
	for _, hit := range hits {
		if fragmentMetadataMatchesRecallWindow(hit.Metadata, validAt, knownAt) {
			out = append(out, hit)
		}
	}
	return out
}

func filterKeywordFragmentsByWindow(hits []keywordsearch.FragmentSearchResult, validAt, knownAt *time.Time) []keywordsearch.FragmentSearchResult {
	if validAt == nil && knownAt == nil {
		return hits
	}
	out := hits[:0]
	for _, hit := range hits {
		if fragmentMetadataMatchesRecallWindow(hit.Metadata, validAt, knownAt) {
			out = append(out, hit)
		}
	}
	return out
}

func fragmentMetadataMatchesRecallWindow(metadata map[string]any, validAt, knownAt *time.Time) bool {
	if validAt != nil {
		if validFrom, ok := metadataTime(metadata, "valid_from", "validFrom"); ok && validFrom.After(*validAt) {
			return false
		}
		if validTo, ok := metadataTime(metadata, "valid_to", "validTo"); ok && !validTo.After(*validAt) {
			return false
		}
	}
	if knownAt != nil {
		if recordedAt, ok := metadataTime(metadata, "recorded_at", "recordedAt"); ok && recordedAt.After(*knownAt) {
			return false
		}
		if recordedTo, ok := metadataTime(metadata, "recorded_to", "recordedTo"); ok && !recordedTo.After(*knownAt) {
			return false
		}
	}
	return true
}

func metadataTime(metadata map[string]any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		value, ok := metadata[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case time.Time:
			return typed.UTC(), true
		case string:
			parsed, err := time.Parse(time.RFC3339Nano, typed)
			if err != nil {
				return time.Time{}, false
			}
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
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

// sanitizeEmbeddingError classifies the provider error type but strips any
// message contents so provider internals never surface to callers.
func sanitizeEmbeddingError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, embedding.ErrEmbeddingTimeout):
		return errors.New("recall: embedding timeout")
	case errors.Is(err, embedding.ErrEmbeddingRateLimit):
		return errors.New("recall: embedding rate limited")
	case errors.Is(err, embedding.ErrEmbeddingProvider):
		return errors.New("recall: embedding provider error")
	}
	return errors.New("recall: embedding unavailable")
}

func (s *recallService) logEmbeddingError(err error) {
	if s.logger == nil {
		return
	}
	s.logger.Warn("recall: embedding provider failed", observability.String("error", err.Error()))
}

func (s *recallService) logKeywordError(err error) {
	if s.logger == nil {
		return
	}
	s.logger.Error("recall: keyword branch failed", err)
}

func (s *recallService) logHydrateError(id string, err error) {
	if s.logger == nil {
		return
	}
	s.logger.Warn("recall: hydrate miss",
		observability.String("fragment_id", id),
		observability.String("error", err.Error()),
	)
}
