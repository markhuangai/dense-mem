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
		wg            sync.WaitGroup
		semHits       []semanticsearch.SearchHit
		semErr        error
		kwHits        []keywordsearch.FragmentSearchResult
		kwErr         error
		assertionHits []AssertionRecallResult
		assertionErr  error
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
		if s.assertionSearcher != nil {
			assertionHits, assertionErr = s.assertionSearcher.SearchActive(ctx, profileID, query, vec, overfetch, req.ValidAt, req.KnownAt)
		}
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
	if assertionErr != nil {
		if s.logger != nil {
			s.logger.Error("recall: semantic assertion search failed", assertionErr)
		}
		outcome = "assertion_unavailable"
		return nil, ErrAssertionUnavailable
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
	for i := range assertionHits {
		candidate := assertionHits[i]
		tier := assertionRecallTier(candidate.Assertion.Tier)
		all = append(all, RecallHit{
			Assertion:    &candidate.Assertion,
			Paths:        []SemanticPath{candidate.Path},
			Frontier:     candidate.Frontier,
			Tier:         tier,
			Score:        candidate.Score,
			FinalScore:   candidate.Score,
			sortTier:     tier,
			temporalRank: candidate.Assertion.RecordedAt,
		})
	}
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

func assertionRecallTier(tier domain.AssertionTier) string {
	switch tier {
	case domain.AssertionTierFact:
		return TierAssertionFact
	case domain.AssertionTierValidatedClaim:
		return TierAssertionValidated
	default:
		return TierAssertionCandidate
	}
}
