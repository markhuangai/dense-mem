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
	// TierValidatedClaim is the recall tier for validated Claim nodes.
	TierValidatedClaim = "1.5"
	// TierFragment is the recall tier for SourceFragment nodes (raw evidence).
	TierFragment = "2"

	// DefaultRecallValidatedClaimWeight scales validated-claim scores so that
	// active facts outrank equivalent claims under the default weight.
	DefaultRecallValidatedClaimWeight = 0.5
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
	FactID     string
	ProfileID  string
	Score      float64
	ValidFrom  *time.Time
	ValidTo    *time.Time
	RecordedAt time.Time
	RecordedTo *time.Time
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

// recallService implements RecallService.
type recallService struct {
	embedder      EmbeddingProvider
	semantic      SemanticSearcher
	keyword       KeywordSearcher
	hydrator      FragmentHydrator
	factSearcher  FactSearcher // optional; nil → tier-1 results omitted
	factGet       FactHydrator
	claimSearcher ClaimSearcher // optional; nil → tier-1.5 results omitted
	claimGet      ClaimHydrator
	claimWeight   float64 // weight applied to claim scores (default DefaultRecallValidatedClaimWeight)
	logger        observability.LogProvider
	metrics       observability.DiscoverabilityMetrics
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
) RecallService {
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	if claimWeight <= 0 {
		claimWeight = DefaultRecallValidatedClaimWeight
	}
	return &recallService{
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
}

// Recall runs both branches in parallel, merges via RRF, and returns the top
// `limit` hydrated fragments for the caller's profile.
func (s *recallService) Recall(ctx context.Context, profileID string, req RecallRequest) ([]RecallHit, error) {
	start := time.Now()
	defer func() {
		s.metrics.ObserveRecallLatency(float64(time.Since(start).Milliseconds()))
	}()

	if profileID == "" {
		return nil, errors.New("recall: profile id is required")
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
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
		return nil, ErrEmbeddingUnavailable
	}
	if kwErr != nil {
		s.logKeywordError(kwErr)
		return nil, ErrKeywordUnavailable
	}

	filteredSem := filterSemanticFragments(semHits, profileID)
	filteredKw := filterKeywordFragments(kwHits, profileID)

	merged := rrfMerge(filteredSem, filteredKw)

	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].FinalScore != merged[j].FinalScore {
			return merged[i].FinalScore > merged[j].FinalScore
		}
		return merged[i].id < merged[j].id
	})

	if len(merged) > limit {
		merged = merged[:limit]
	}

	// Hydrate fragment hits (tier 2).
	fragmentHits := make([]RecallHit, 0, len(merged))
	fragmentsByID, batchFragments := s.batchHydrateFragments(ctx, profileID, merged)
	for _, m := range merged {
		var frag *domain.Fragment
		if batchFragments {
			frag = fragmentsByID[m.id]
			if frag == nil {
				s.logHydrateError(m.id, errors.New("fragment not found"))
				continue
			}
		} else {
			var err error
			frag, err = s.hydrator.GetByID(ctx, profileID, m.id)
			if err != nil {
				// A winning id may vanish due to a concurrent delete or retraction
				// (AC-44). In both cases we skip the id rather than failing the whole
				// recall so that the remaining results are still returned to the caller.
				s.logHydrateError(m.id, err)
				continue
			}
		}
		fragmentHits = append(fragmentHits, RecallHit{
			Fragment:     frag,
			Tier:         TierFragment,
			Score:        m.FinalScore,
			SemanticRank: m.SemanticRank,
			KeywordRank:  m.KeywordRank,
			FinalScore:   m.FinalScore,
		})
	}

	// Collect tier-1 (active facts) and tier-1.5 (validated claims) enrichment.
	tierHits := s.enrichTierHits(ctx, profileID, limit, req)

	// Merge fragment hits with tier hits, sort by (tier ASC, score DESC).
	all := append(tierHits, fragmentHits...) //nolint:gocritic
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Tier != all[j].Tier {
			return all[i].Tier < all[j].Tier
		}
		return all[i].Score > all[j].Score
	})

	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
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
				if !req.IncludeEvidence {
					factCopy := *f
					factCopy.Evidence = nil
					f = &factCopy
				}
				hits = append(hits, RecallHit{
					Fact:  f,
					Tier:  TierActiveFact,
					Score: f.TruthScore,
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
				if !req.IncludeEvidence {
					claimCopy := *c
					claimCopy.Evidence = nil
					c = &claimCopy
				}
				score := c.ExtractConf * s.claimWeight
				hits = append(hits, RecallHit{
					Claim: c,
					Tier:  TierValidatedClaim,
					Score: score,
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
