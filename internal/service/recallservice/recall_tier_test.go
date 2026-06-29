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
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
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

func TestRecallService_UsesPlainTextKeywordBranchQuery(t *testing.T) {
	reader := &recallKeywordReader{}
	svc := NewRecallService(
		&stubEmbedding{DimensionsResult: 4},
		&fakeSemanticSearcher{},
		keywordsearch.NewFragmentSearcher(reader),
		&fakeHydrator{},
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{
		Query: "GitVibe workflows / git-vibe command",
		Limit: 3,
	})

	require.NoError(t, err)
	require.Empty(t, out)
	require.Equal(t, "GitVibe workflows git vibe command", reader.lastParams["searchQuery"])
}

type recallKeywordReader struct {
	lastParams map[string]any
}

func (r *recallKeywordReader) ScopedRead(_ context.Context, _ string, _ string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
	r.lastParams = params
	return nil, nil, nil
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

func TestRecallService_EnrichTierHitsHydratesEvidenceOnlyForCurrentness(t *testing.T) {
	run := func(query string) int32 {
		hydrator := &fakeHydrator{}
		svc := &recallService{
			factSearcher: &fakeFactSearcher{results: []FactRecallResult{
				{FactID: "fact-1", ProfileID: "pA"},
			}},
			factGet: &fakeFactGetter{facts: map[string]*domain.Fact{
				"fact-1": {
					FactID:     "fact-1",
					ProfileID:  "pA",
					Subject:    "service OBS-001",
					Predicate:  "owner",
					Object:     "team-blue",
					RecordedAt: time.Now().UTC(),
					TruthScore: 0.9,
					Evidence:   []domain.Evidence{{FragmentID: "fragment-fact"}},
				},
			}},
			claimSearcher: &fakeClaimSearcher{results: []ClaimRecallResult{
				{ClaimID: "claim-1", ProfileID: "pA"},
			}},
			claimGet: &fakeClaimGetter{claims: map[string]*domain.Claim{
				"claim-1": {
					ClaimID:     "claim-1",
					ProfileID:   "pA",
					Subject:     "service OBS-001",
					Predicate:   "owner",
					Object:      "team-blue",
					RecordedAt:  time.Now().UTC(),
					ExtractConf: 0.8,
					Evidence:    []domain.Evidence{{FragmentID: "fragment-claim"}},
				},
			}},
			hydrator:    hydrator,
			claimWeight: 1,
		}

		out := svc.enrichTierHits(context.Background(), "pA", 10, RecallRequest{Query: query})
		require.Len(t, out, 2)
		return atomic.LoadInt32(&hydrator.batchCallCount) + atomic.LoadInt32(&hydrator.callCount)
	}

	require.Equal(t, int32(0), run("deployment owner for service OBS-001"))
	require.Equal(t, int32(2), run("current deployment owner for service OBS-001"))
}

func TestRecallService_CurrentnessRanksNewerFactsAndClaimsWithinTier(t *testing.T) {
	oldTime := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	newTime := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	factSearcher := &fakeFactSearcher{results: []FactRecallResult{
		{FactID: "fact-old", ProfileID: "pA", ValidFrom: &oldTime, RecordedAt: oldTime},
		{FactID: "fact-new", ProfileID: "pA", ValidFrom: &newTime, RecordedAt: newTime},
	}}
	claimSearcher := &fakeClaimSearcher{results: []ClaimRecallResult{
		{ClaimID: "claim-old", ProfileID: "pA", ValidFrom: &oldTime, RecordedAt: oldTime},
		{ClaimID: "claim-new", ProfileID: "pA", ValidFrom: &newTime, RecordedAt: newTime},
	}}
	factGetter := &fakeFactGetter{facts: map[string]*domain.Fact{
		"fact-old": {FactID: "fact-old", ProfileID: "pA", Subject: "service OBS-001 owner", Predicate: "uses", Object: "owner-alice", ValidFrom: &oldTime, RecordedAt: oldTime, TruthScore: 0.99},
		"fact-new": {FactID: "fact-new", ProfileID: "pA", Subject: "service OBS-001 owner", Predicate: "uses", Object: "owner-bob", ValidFrom: &newTime, RecordedAt: newTime, TruthScore: 0.20},
	}}
	claimGetter := &fakeClaimGetter{claims: map[string]*domain.Claim{
		"claim-old": {ClaimID: "claim-old", ProfileID: "pA", Subject: "service OBS-001 owner", Predicate: "uses", Object: "owner-alice", ValidFrom: &oldTime, RecordedAt: oldTime, ExtractConf: 0.99},
		"claim-new": {ClaimID: "claim-new", ProfileID: "pA", Subject: "service OBS-001 owner", Predicate: "uses", Object: "owner-bob", ValidFrom: &newTime, RecordedAt: newTime, ExtractConf: 0.20},
	}}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		&fakeSemanticSearcher{},
		&fakeKeywordSearcher{},
		&fakeHydrator{},
		factSearcher,
		factGetter,
		claimSearcher,
		claimGetter,
		0,
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "What is the current deployment owner for service OBS-001?", Limit: 4})

	require.NoError(t, err)
	require.Len(t, out, 4)
	require.Equal(t, "fact-new", out[0].Fact.FactID)
	require.Equal(t, "fact-old", out[1].Fact.FactID)
	require.Equal(t, "claim-new", out[2].Claim.ClaimID)
	require.Equal(t, "claim-old", out[3].Claim.ClaimID)
}

func TestRecallService_CurrentnessDemotesExpiredFactsAndClaimsWithinTier(t *testing.T) {
	currentStart := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	expiredStart := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)
	expiredEnd := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	earlierRecordedAt := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	currentRecordedAt := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	factSearcher := &fakeFactSearcher{results: []FactRecallResult{
		{FactID: "fact-expired", ProfileID: "pA", ValidFrom: &expiredStart, ValidTo: &expiredEnd, RecordedAt: earlierRecordedAt},
		{FactID: "fact-current", ProfileID: "pA", ValidFrom: &currentStart, RecordedAt: currentRecordedAt},
	}}
	claimSearcher := &fakeClaimSearcher{results: []ClaimRecallResult{
		{ClaimID: "claim-expired", ProfileID: "pA", ValidFrom: &expiredStart, ValidTo: &expiredEnd, RecordedAt: earlierRecordedAt},
		{ClaimID: "claim-current", ProfileID: "pA", ValidFrom: &currentStart, RecordedAt: currentRecordedAt},
	}}
	factGetter := &fakeFactGetter{facts: map[string]*domain.Fact{
		"fact-expired": {FactID: "fact-expired", ProfileID: "pA", Subject: "service OBS-002 owner", Predicate: "uses", Object: "owner-old-override", ValidFrom: &expiredStart, ValidTo: &expiredEnd, RecordedAt: earlierRecordedAt, TruthScore: 0.99},
		"fact-current": {FactID: "fact-current", ProfileID: "pA", Subject: "service OBS-002 owner", Predicate: "uses", Object: "owner-current", ValidFrom: &currentStart, RecordedAt: currentRecordedAt, TruthScore: 0.20},
	}}
	claimGetter := &fakeClaimGetter{claims: map[string]*domain.Claim{
		"claim-expired": {ClaimID: "claim-expired", ProfileID: "pA", Subject: "service OBS-002 pager", Predicate: "uses", Object: "pager-old-override", ValidFrom: &expiredStart, ValidTo: &expiredEnd, RecordedAt: earlierRecordedAt, ExtractConf: 0.99},
		"claim-current": {ClaimID: "claim-current", ProfileID: "pA", Subject: "service OBS-002 pager", Predicate: "uses", Object: "pager-current", ValidFrom: &currentStart, RecordedAt: currentRecordedAt, ExtractConf: 0.20},
	}}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		&fakeSemanticSearcher{},
		&fakeKeywordSearcher{},
		&fakeHydrator{},
		factSearcher,
		factGetter,
		claimSearcher,
		claimGetter,
		0,
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "What is the current owner and pager for service OBS-002?", Limit: 4})

	require.NoError(t, err)
	require.Len(t, out, 4)
	require.Equal(t, "fact-current", out[0].Fact.FactID)
	require.Equal(t, "fact-expired", out[1].Fact.FactID)
	require.Equal(t, "claim-current", out[2].Claim.ClaimID)
	require.Equal(t, "claim-expired", out[3].Claim.ClaimID)
}

func TestRecallService_TierCurrentnessOverfetchesBeforeFinalLimit(t *testing.T) {
	oldTime := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	newTime := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)

	t.Run("fact", func(t *testing.T) {
		factSearcher := &fakeFactSearcher{
			respectLimit: true,
			results: []FactRecallResult{
				{FactID: "fact-old", ProfileID: "pA", ValidFrom: &oldTime, RecordedAt: oldTime},
				{FactID: "fact-new", ProfileID: "pA", ValidFrom: &newTime, RecordedAt: newTime},
			},
		}
		factGetter := &fakeFactGetter{facts: map[string]*domain.Fact{
			"fact-old": {FactID: "fact-old", ProfileID: "pA", Subject: "service TIER-F-001 owner", Predicate: "uses", Object: "owner-alice", ValidFrom: &oldTime, RecordedAt: oldTime, TruthScore: 0.99},
			"fact-new": {FactID: "fact-new", ProfileID: "pA", Subject: "service TIER-F-001 owner", Predicate: "uses", Object: "owner-bob", ValidFrom: &newTime, RecordedAt: newTime, TruthScore: 0.20},
		}}
		svc := NewRecallServiceWithTiers(
			&stubEmbedding{DimensionsResult: 4},
			&fakeSemanticSearcher{},
			&fakeKeywordSearcher{},
			&fakeHydrator{},
			factSearcher,
			factGetter,
			nil,
			nil,
			0,
			nil,
			nil,
		)

		out, err := svc.Recall(context.Background(), "pA", RecallRequest{
			Query: "What is the current owner for service TIER-F-001?",
			Limit: 1,
		})

		require.NoError(t, err)
		require.Len(t, out, 1)
		require.Equal(t, recallOverfetchLimit("What is the current owner for service TIER-F-001?", 1), factSearcher.lastLimit)
		require.Equal(t, "fact-new", out[0].Fact.FactID)
	})

	t.Run("claim", func(t *testing.T) {
		claimSearcher := &fakeClaimSearcher{
			respectLimit: true,
			results: []ClaimRecallResult{
				{ClaimID: "claim-old", ProfileID: "pA", ValidFrom: &oldTime, RecordedAt: oldTime},
				{ClaimID: "claim-new", ProfileID: "pA", ValidFrom: &newTime, RecordedAt: newTime},
			},
		}
		claimGetter := &fakeClaimGetter{claims: map[string]*domain.Claim{
			"claim-old": {ClaimID: "claim-old", ProfileID: "pA", Subject: "service TIER-C-001 pager", Predicate: "uses", Object: "pager-red", ValidFrom: &oldTime, RecordedAt: oldTime, ExtractConf: 0.99},
			"claim-new": {ClaimID: "claim-new", ProfileID: "pA", Subject: "service TIER-C-001 pager", Predicate: "uses", Object: "pager-green", ValidFrom: &newTime, RecordedAt: newTime, ExtractConf: 0.20},
		}}
		svc := NewRecallServiceWithTiers(
			&stubEmbedding{DimensionsResult: 4},
			&fakeSemanticSearcher{},
			&fakeKeywordSearcher{},
			&fakeHydrator{},
			nil,
			nil,
			claimSearcher,
			claimGetter,
			1,
			nil,
			nil,
		)

		out, err := svc.Recall(context.Background(), "pA", RecallRequest{
			Query: "What is the latest pager for service TIER-C-001?",
			Limit: 1,
		})

		require.NoError(t, err)
		require.Len(t, out, 1)
		require.Equal(t, recallOverfetchLimit("What is the latest pager for service TIER-C-001?", 1), claimSearcher.lastLimit)
		require.Equal(t, "claim-new", out[0].Claim.ClaimID)
	})
}

func TestRecallService_TierIdentifierFilterSkipsCrossIdentifierFact(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	factSearcher := &fakeFactSearcher{results: []FactRecallResult{
		{FactID: "fact-other", ProfileID: "pA", RecordedAt: now},
	}}
	claimSearcher := &fakeClaimSearcher{results: []ClaimRecallResult{
		{ClaimID: "claim-right", ProfileID: "pA", RecordedAt: now},
	}}
	factGetter := &fakeFactGetter{facts: map[string]*domain.Fact{
		"fact-other": {
			FactID:     "fact-other",
			ProfileID:  "pA",
			Subject:    "service TIER-F-001 owner",
			Predicate:  "uses",
			Object:     "owner-bob",
			RecordedAt: now,
			TruthScore: 0.9,
		},
	}}
	claimGetter := &fakeClaimGetter{claims: map[string]*domain.Claim{
		"claim-right": {
			ClaimID:     "claim-right",
			ProfileID:   "pA",
			Subject:     "service TIER-C-001 pager",
			Predicate:   "uses",
			Object:      "pager-green",
			RecordedAt:  now,
			ExtractConf: 0.7,
		},
	}}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		&fakeSemanticSearcher{},
		&fakeKeywordSearcher{},
		&fakeHydrator{},
		factSearcher,
		factGetter,
		claimSearcher,
		claimGetter,
		1,
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{
		Query: "What is the latest pager for service TIER-C-001?",
		Limit: 1,
	})

	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Nil(t, out[0].Fact)
	require.Equal(t, "claim-right", out[0].Claim.ClaimID)
}

func TestRecallService_NonCurrentTierOrderingKeepsScoreOrder(t *testing.T) {
	oldTime := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	newTime := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	factSearcher := &fakeFactSearcher{results: []FactRecallResult{
		{FactID: "fact-new", ProfileID: "pA", ValidFrom: &newTime, RecordedAt: newTime},
		{FactID: "fact-old", ProfileID: "pA", ValidFrom: &oldTime, RecordedAt: oldTime},
	}}
	claimSearcher := &fakeClaimSearcher{results: []ClaimRecallResult{
		{ClaimID: "claim-new", ProfileID: "pA", ValidFrom: &newTime, RecordedAt: newTime},
		{ClaimID: "claim-old", ProfileID: "pA", ValidFrom: &oldTime, RecordedAt: oldTime},
	}}
	factGetter := &fakeFactGetter{facts: map[string]*domain.Fact{
		"fact-old": {FactID: "fact-old", ProfileID: "pA", Subject: "service OBS-001 owner", Predicate: "uses", Object: "owner-alice", ValidFrom: &oldTime, RecordedAt: oldTime, TruthScore: 0.99},
		"fact-new": {FactID: "fact-new", ProfileID: "pA", Subject: "service OBS-001 owner", Predicate: "uses", Object: "owner-bob", ValidFrom: &newTime, RecordedAt: newTime, TruthScore: 0.20},
	}}
	claimGetter := &fakeClaimGetter{claims: map[string]*domain.Claim{
		"claim-old": {ClaimID: "claim-old", ProfileID: "pA", Subject: "service OBS-001 owner", Predicate: "uses", Object: "owner-alice", ValidFrom: &oldTime, RecordedAt: oldTime, ExtractConf: 0.99},
		"claim-new": {ClaimID: "claim-new", ProfileID: "pA", Subject: "service OBS-001 owner", Predicate: "uses", Object: "owner-bob", ValidFrom: &newTime, RecordedAt: newTime, ExtractConf: 0.20},
	}}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		&fakeSemanticSearcher{},
		&fakeKeywordSearcher{},
		&fakeHydrator{},
		factSearcher,
		factGetter,
		claimSearcher,
		claimGetter,
		0,
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "deployment owner for service OBS-001", Limit: 4})

	require.NoError(t, err)
	require.Len(t, out, 4)
	require.Equal(t, "fact-old", out[0].Fact.FactID)
	require.Equal(t, "fact-new", out[1].Fact.FactID)
	require.Equal(t, "claim-old", out[2].Claim.ClaimID)
	require.Equal(t, "claim-new", out[3].Claim.ClaimID)
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
