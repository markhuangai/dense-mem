package recallservice

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/tools/keywordsearch"
	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
	"github.com/stretchr/testify/require"
)

func TestRecallService_ReturnsClassifiedBranchErrors(t *testing.T) {
	t.Run("semantic branch error returns embedding unavailable", func(t *testing.T) {
		logger := &fakeLogger{}
		svc := NewRecallService(
			&stubEmbedding{DimensionsResult: 4},
			&fakeSemanticSearcher{errFunc: func() error { return errors.New("vector index down") }},
			&fakeKeywordSearcher{},
			&fakeHydrator{},
			logger,
			nil,
		)

		_, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 3})

		require.ErrorIs(t, err, ErrEmbeddingUnavailable)
		require.Equal(t, int32(1), atomic.LoadInt32(&logger.warns))
	})

	t.Run("keyword branch error returns keyword unavailable", func(t *testing.T) {
		logger := &fakeLogger{}
		svc := NewRecallService(
			&stubEmbedding{DimensionsResult: 4},
			&fakeSemanticSearcher{},
			&fakeKeywordSearcher{errFunc: func() error { return errors.New("fulltext down") }},
			&fakeHydrator{},
			logger,
			nil,
		)

		_, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 3})

		require.ErrorIs(t, err, ErrKeywordUnavailable)
		require.Equal(t, int32(1), atomic.LoadInt32(&logger.errors))
	})
}

func TestRecallService_HydrationFallbacksAndMisses(t *testing.T) {
	t.Run("batch miss skips missing fragment", func(t *testing.T) {
		logger := &fakeLogger{}
		hydrator := &fakeHydrator{missIDs: map[string]bool{"f-missing": true}}
		svc := NewRecallService(
			&stubEmbedding{DimensionsResult: 4},
			&fakeSemanticSearcher{hits: []semanticsearch.SearchHit{
				{ID: "f-present", Type: "fragment"},
				{ID: "f-missing", Type: "fragment"},
			}},
			&fakeKeywordSearcher{},
			hydrator,
			logger,
			nil,
		)

		out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 3})

		require.NoError(t, err)
		require.Len(t, out, 1)
		require.Equal(t, "f-present", out[0].Fragment.FragmentID)
		require.Equal(t, int32(1), atomic.LoadInt32(&hydrator.batchCallCount))
		require.Equal(t, int32(1), atomic.LoadInt32(&logger.warns))
	})

	t.Run("batch error falls back to single fragment get", func(t *testing.T) {
		hydrator := &fakeHydrator{batchErr: errors.New("batch failed")}
		svc := NewRecallService(
			&stubEmbedding{DimensionsResult: 4},
			&fakeSemanticSearcher{hits: []semanticsearch.SearchHit{{ID: "f1", Type: "fragment"}}},
			&fakeKeywordSearcher{},
			hydrator,
			nil,
			nil,
		)

		out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 3})

		require.NoError(t, err)
		require.Len(t, out, 1)
		require.Equal(t, int32(1), atomic.LoadInt32(&hydrator.batchCallCount))
		require.Equal(t, int32(1), atomic.LoadInt32(&hydrator.callCount))
	})

	t.Run("non-batch hydrator uses single fragment get", func(t *testing.T) {
		hydrator := &getOnlyFragmentHydrator{}
		svc := NewRecallService(
			&stubEmbedding{DimensionsResult: 4},
			&fakeSemanticSearcher{hits: []semanticsearch.SearchHit{{ID: "f1", Type: "fragment"}}},
			&fakeKeywordSearcher{},
			hydrator,
			nil,
			nil,
		)

		out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 3})

		require.NoError(t, err)
		require.Len(t, out, 1)
		require.Equal(t, int32(1), atomic.LoadInt32(&hydrator.callCount))
	})

	t.Run("empty merge has no batch hydration", func(t *testing.T) {
		svc := &recallService{hydrator: &fakeHydrator{}}

		fragments, ok := svc.batchHydrateFragments(context.Background(), "pA", nil)

		require.False(t, ok)
		require.Nil(t, fragments)
	})
}

func TestRecallService_EnrichTierHitsFiltersAndStripsEvidence(t *testing.T) {
	base := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	after := base.Add(time.Hour)
	factSearcher := &fakeFactSearcher{results: []FactRecallResult{
		{FactID: "", ProfileID: "pA", RecordedAt: base},
		{FactID: "fact-other-profile", ProfileID: "pB", RecordedAt: base},
		{FactID: "fact-future-candidate", ProfileID: "pA", ValidFrom: &after, RecordedAt: base},
		{FactID: "fact-future-hydrated", ProfileID: "pA", RecordedAt: base},
		{FactID: "fact-missing", ProfileID: "pA", RecordedAt: base},
		{FactID: "fact-good", ProfileID: "pA", RecordedAt: base},
	}}
	claimSearcher := &fakeClaimSearcher{results: []ClaimRecallResult{
		{ClaimID: "", ProfileID: "pA", RecordedAt: base},
		{ClaimID: "claim-other-profile", ProfileID: "pB", RecordedAt: base},
		{ClaimID: "claim-future-candidate", ProfileID: "pA", ValidFrom: &after, RecordedAt: base},
		{ClaimID: "claim-future-hydrated", ProfileID: "pA", RecordedAt: base},
		{ClaimID: "claim-missing", ProfileID: "pA", RecordedAt: base},
		{ClaimID: "claim-good", ProfileID: "pA", RecordedAt: base},
	}}
	factGetter := &fakeFactGetter{facts: map[string]*domain.Fact{
		"fact-future-hydrated": {
			FactID:     "fact-future-hydrated",
			ProfileID:  "pA",
			ValidFrom:  &after,
			RecordedAt: base,
			TruthScore: 0.4,
		},
		"fact-good": {
			FactID:     "fact-good",
			ProfileID:  "pA",
			RecordedAt: base,
			TruthScore: 0.9,
			Evidence:   []domain.Evidence{{FragmentID: "fragment-1"}},
		},
	}}
	claimGetter := &fakeClaimGetter{claims: map[string]*domain.Claim{
		"claim-future-hydrated": {
			ClaimID:     "claim-future-hydrated",
			ProfileID:   "pA",
			ValidFrom:   &after,
			RecordedAt:  base,
			ExtractConf: 0.4,
		},
		"claim-good": {
			ClaimID:     "claim-good",
			ProfileID:   "pA",
			RecordedAt:  base,
			ExtractConf: 0.8,
			Evidence:    []domain.Evidence{{FragmentID: "fragment-2"}},
		},
	}}
	logger := &fakeLogger{}
	svc := &recallService{
		factSearcher:  factSearcher,
		factGet:       factGetter,
		claimSearcher: claimSearcher,
		claimGet:      claimGetter,
		claimWeight:   DefaultRecallValidatedClaimWeight,
		logger:        logger,
	}

	out := svc.enrichTierHits(context.Background(), "pA", 10, RecallRequest{
		Query:   "q",
		ValidAt: &base,
		KnownAt: &base,
	})

	require.Len(t, out, 2)
	require.NotNil(t, out[0].Fact)
	require.Equal(t, "fact-good", out[0].Fact.FactID)
	require.Nil(t, out[0].Fact.Evidence)
	require.NotNil(t, out[1].Claim)
	require.Equal(t, "claim-good", out[1].Claim.ClaimID)
	require.Nil(t, out[1].Claim.Evidence)
	require.Equal(t, int32(1), atomic.LoadInt32(&factGetter.batchCallCount))
	require.Equal(t, int32(1), atomic.LoadInt32(&claimGetter.batchCallCount))
	require.Equal(t, int32(2), atomic.LoadInt32(&logger.warns))
}

func TestRecallService_EnrichTierHitsRetainsEvidenceWhenRequested(t *testing.T) {
	factGetter := &fakeFactGetter{facts: map[string]*domain.Fact{
		"fact-1": {
			FactID:     "fact-1",
			ProfileID:  "pA",
			RecordedAt: time.Now().UTC(),
			TruthScore: 0.9,
			Evidence:   []domain.Evidence{{FragmentID: "fragment-1"}},
		},
	}}
	claimGetter := &fakeClaimGetter{claims: map[string]*domain.Claim{
		"claim-1": {
			ClaimID:     "claim-1",
			ProfileID:   "pA",
			RecordedAt:  time.Now().UTC(),
			ExtractConf: 0.8,
			Evidence:    []domain.Evidence{{FragmentID: "fragment-2"}},
		},
	}}
	svc := &recallService{
		factSearcher:  &fakeFactSearcher{results: []FactRecallResult{{FactID: "fact-1", ProfileID: "pA"}}},
		factGet:       factGetter,
		claimSearcher: &fakeClaimSearcher{results: []ClaimRecallResult{{ClaimID: "claim-1", ProfileID: "pA"}}},
		claimGet:      claimGetter,
		claimWeight:   1,
	}

	out := svc.enrichTierHits(context.Background(), "pA", 10, RecallRequest{
		Query:           "q",
		IncludeEvidence: true,
	})

	require.Len(t, out, 2)
	require.NotEmpty(t, out[0].Fact.Evidence)
	require.NotEmpty(t, out[1].Claim.Evidence)
	require.Equal(t, 0.8, out[1].Score)
}

func TestRecallService_EnrichTierHitsSearchErrorsAreLoggedAndSkipped(t *testing.T) {
	logger := &fakeLogger{}
	svc := &recallService{
		factSearcher:  &fakeFactSearcher{err: errors.New("fact search failed")},
		factGet:       &fakeFactGetter{},
		claimSearcher: &fakeClaimSearcher{err: errors.New("claim search failed")},
		claimGet:      &fakeClaimGetter{},
		logger:        logger,
	}

	out := svc.enrichTierHits(context.Background(), "pA", 10, RecallRequest{Query: "q"})

	require.Empty(t, out)
	require.Equal(t, int32(2), atomic.LoadInt32(&logger.warns))
}

func TestRecallService_TierBatchErrorsFallBackToSingleGetters(t *testing.T) {
	factGetter := &fakeFactGetter{
		batchErr: errors.New("fact batch failed"),
		facts: map[string]*domain.Fact{
			"fact-1": {
				FactID:     "fact-1",
				ProfileID:  "pA",
				RecordedAt: time.Now().UTC(),
				TruthScore: 0.9,
			},
		},
	}
	claimGetter := &fakeClaimGetter{
		batchErr: errors.New("claim batch failed"),
		claims: map[string]*domain.Claim{
			"claim-1": {
				ClaimID:     "claim-1",
				ProfileID:   "pA",
				RecordedAt:  time.Now().UTC(),
				ExtractConf: 0.8,
			},
		},
	}
	svc := &recallService{
		factSearcher:  &fakeFactSearcher{results: []FactRecallResult{{FactID: "fact-1", ProfileID: "pA"}}},
		factGet:       factGetter,
		claimSearcher: &fakeClaimSearcher{results: []ClaimRecallResult{{ClaimID: "claim-1", ProfileID: "pA"}}},
		claimGet:      claimGetter,
		claimWeight:   1,
	}

	out := svc.enrichTierHits(context.Background(), "pA", 10, RecallRequest{Query: "q"})

	require.Len(t, out, 2)
	require.Equal(t, int32(1), atomic.LoadInt32(&factGetter.batchCallCount))
	require.Equal(t, int32(1), atomic.LoadInt32(&factGetter.callCount))
	require.Equal(t, int32(1), atomic.LoadInt32(&claimGetter.batchCallCount))
	require.Equal(t, int32(1), atomic.LoadInt32(&claimGetter.callCount))
}

func TestRecallService_TierNonBatchGettersAndEmptyBatchInputs(t *testing.T) {
	factGetter := &getOnlyFactGetter{facts: map[string]*domain.Fact{
		"fact-1": {
			FactID:     "fact-1",
			ProfileID:  "pA",
			RecordedAt: time.Now().UTC(),
			TruthScore: 0.9,
		},
	}}
	claimGetter := &getOnlyClaimGetter{claims: map[string]*domain.Claim{
		"claim-1": {
			ClaimID:     "claim-1",
			ProfileID:   "pA",
			RecordedAt:  time.Now().UTC(),
			ExtractConf: 0.8,
		},
	}}
	svc := &recallService{
		factSearcher:  &fakeFactSearcher{results: []FactRecallResult{{FactID: "fact-1", ProfileID: "pA"}}},
		factGet:       factGetter,
		claimSearcher: &fakeClaimSearcher{results: []ClaimRecallResult{{ClaimID: "claim-1", ProfileID: "pA"}}},
		claimGet:      claimGetter,
		claimWeight:   1,
	}

	out := svc.enrichTierHits(context.Background(), "pA", 10, RecallRequest{Query: "q"})

	require.Len(t, out, 2)
	require.Equal(t, int32(1), atomic.LoadInt32(&factGetter.callCount))
	require.Equal(t, int32(1), atomic.LoadInt32(&claimGetter.callCount))

	emptySvc := &recallService{factGet: &fakeFactGetter{}, claimGet: &fakeClaimGetter{}}
	facts, ok := emptySvc.batchHydrateFacts(context.Background(), "pA", nil)
	require.False(t, ok)
	require.Nil(t, facts)
	claims, ok := emptySvc.batchHydrateClaims(context.Background(), "pA", nil)
	require.False(t, ok)
	require.Nil(t, claims)
}

func TestRecallService_FilterKeywordFragmentsDropsCrossProfileHits(t *testing.T) {
	out := filterKeywordFragments([]keywordsearch.FragmentSearchResult{
		{FragmentID: "own", ProfileID: "pA"},
		{FragmentID: "other", ProfileID: "pB"},
	}, "pA")

	require.Equal(t, []keywordsearch.FragmentSearchResult{{FragmentID: "own", ProfileID: "pA"}}, out)
}
