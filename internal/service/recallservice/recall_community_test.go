package recallservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
	"github.com/stretchr/testify/require"
)

func TestRecallService_CommunityExpansionDisabledByDefault(t *testing.T) {
	sem := &fakeSemanticSearcher{hits: []semanticsearch.SearchHit{{ID: "f-direct", Type: "fragment"}}}
	kw := &fakeKeywordSearcher{}
	expander := &fakeCommunityExpander{
		expansion: CommunityExpansion{
			SelectedCommunities: 1,
			Fragments: []CommunityFragmentRecallResult{
				{FragmentID: "f-community", ProfileID: "pA", Score: 0.8},
			},
		},
	}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		sem,
		kw,
		&fakeHydrator{},
		nil,
		nil,
		nil,
		nil,
		0,
		nil,
		nil,
		WithCommunityExpander(expander),
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 5})
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, "f-direct", out[0].Fragment.FragmentID)
	require.Equal(t, 0, expander.calls)
}

func TestRecallService_CommunityExpansionNoCommunityFallback(t *testing.T) {
	sem := &fakeSemanticSearcher{hits: []semanticsearch.SearchHit{{ID: "f-direct", Type: "fragment"}}}
	kw := &fakeKeywordSearcher{}
	expander := &fakeCommunityExpander{}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		sem,
		kw,
		&fakeHydrator{},
		nil,
		nil,
		nil,
		nil,
		0,
		nil,
		nil,
		WithCommunityExpander(expander),
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 5, UseCommunities: true})
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, "f-direct", out[0].Fragment.FragmentID)
	require.Equal(t, 1, expander.calls)
	require.Equal(t, "pA", expander.lastProfile)
}

func TestRecallService_CommunityExpansionFiltersProfile(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	expander := &fakeCommunityExpander{
		expansion: CommunityExpansion{
			SelectedCommunities: 1,
			Facts: []FactRecallResult{
				{FactID: "fact-other", ProfileID: "pB", RecordedAt: now},
				{FactID: "fact-own", ProfileID: "pA", RecordedAt: now},
			},
			Fragments: []CommunityFragmentRecallResult{
				{FragmentID: "frag-other", ProfileID: "pB", Score: 0.5},
				{FragmentID: "frag-own", ProfileID: "pA", Score: 0.4},
			},
		},
	}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		&fakeSemanticSearcher{},
		&fakeKeywordSearcher{},
		&fakeHydrator{frags: map[string]*domain.Fragment{
			"frag-other": {FragmentID: "frag-other", ProfileID: "pB"},
			"frag-own":   {FragmentID: "frag-own", ProfileID: "pA"},
		}},
		nil,
		&fakeFactGetter{facts: map[string]*domain.Fact{
			"fact-other": {FactID: "fact-other", ProfileID: "pB", Status: domain.FactStatusActive, RecordedAt: now, TruthScore: 1},
			"fact-own":   {FactID: "fact-own", ProfileID: "pA", Status: domain.FactStatusActive, RecordedAt: now, TruthScore: 0.9},
		}},
		nil,
		nil,
		0,
		nil,
		nil,
		WithCommunityExpander(expander),
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 5, UseCommunities: true})
	require.NoError(t, err)
	require.Len(t, out, 2)
	require.NotNil(t, out[0].Fact)
	require.Equal(t, "fact-own", out[0].Fact.FactID)
	require.NotNil(t, out[1].Fragment)
	require.Equal(t, "frag-own", out[1].Fragment.FragmentID)
}

func TestRecallService_CommunityExpansionFillsUnusedSlotsAfterDirectRecall(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	sem := &fakeSemanticSearcher{hits: []semanticsearch.SearchHit{{ID: "f-direct", Type: "fragment"}}}
	expander := &fakeCommunityExpander{
		expansion: CommunityExpansion{
			SelectedCommunities: 1,
			Facts: []FactRecallResult{
				{FactID: "fact-community", ProfileID: "pA", RecordedAt: now},
			},
			Claims: []ClaimRecallResult{
				{ClaimID: "claim-community", ProfileID: "pA", RecordedAt: now},
			},
			Fragments: []CommunityFragmentRecallResult{
				{FragmentID: "fragment-community", ProfileID: "pA", Score: 0.7},
			},
		},
	}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		sem,
		&fakeKeywordSearcher{},
		&fakeHydrator{frags: map[string]*domain.Fragment{
			"f-direct":           {FragmentID: "f-direct", ProfileID: "pA"},
			"fragment-community": {FragmentID: "fragment-community", ProfileID: "pA"},
		}},
		nil,
		&fakeFactGetter{facts: map[string]*domain.Fact{
			"fact-community": {FactID: "fact-community", ProfileID: "pA", Status: domain.FactStatusActive, RecordedAt: now, TruthScore: 0.9},
		}},
		nil,
		&fakeClaimGetter{claims: map[string]*domain.Claim{
			"claim-community": {ClaimID: "claim-community", ProfileID: "pA", Status: domain.StatusValidated, RecordedAt: now, ExtractConf: 0.8},
		}},
		0,
		nil,
		nil,
		WithCommunityExpander(expander),
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 3, UseCommunities: true})
	require.NoError(t, err)
	require.Len(t, out, 3)
	require.NotNil(t, out[0].Fact)
	require.Equal(t, "fact-community", out[0].Fact.FactID)
	require.NotNil(t, out[1].Claim)
	require.Equal(t, "claim-community", out[1].Claim.ClaimID)
	require.NotNil(t, out[2].Fragment)
	require.Equal(t, "f-direct", out[2].Fragment.FragmentID)
	require.Equal(t, DefaultCommunityExpansionCommunityLimit, expander.lastOptions.CommunityLimit)
	require.Equal(t, DefaultCommunityExpansionMembersPerCommunity, expander.lastOptions.MembersPerCommunity)
	require.Equal(t, DefaultCommunityExpansionCommunityLimit*DefaultCommunityExpansionMembersPerCommunity, expander.lastOptions.MaxCandidates)
}

func TestCommunityExpansionHelpersCoverConversionsAndScoring(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	row := map[string]any{
		"community_id":       "community-1",
		"team_id":            "pA",
		"level":              int64(2),
		"summary":            "Alpha beta owner facts",
		"summary_version":    "v1",
		"member_count":       float64(3),
		"top_entities":       []any{"alpha", 100, "beta"},
		"top_predicates":     []string{"owns"},
		"last_summarized_at": now,
	}
	community := recallRowToCommunity(row)

	require.NotNil(t, community)
	require.Equal(t, "community-1", community.CommunityID)
	require.Equal(t, 2, community.Level)
	require.Equal(t, 3, community.MemberCount)
	require.Equal(t, []string{"alpha", "beta"}, community.TopEntities)
	require.Equal(t, []string{"owns"}, community.TopPredicates)
	require.Equal(t, now, community.LastSummarizedAt)
	require.Nil(t, recallRowToCommunity(map[string]any{}))

	require.Equal(t, 1, recallInt(map[string]any{"v": 1}, "v"))
	require.Equal(t, 2, recallInt(map[string]any{"v": int64(2)}, "v"))
	require.Equal(t, 3, recallInt(map[string]any{"v": int32(3)}, "v"))
	require.Equal(t, 4, recallInt(map[string]any{"v": float64(4.8)}, "v"))
	require.Equal(t, 5, recallInt(map[string]any{"v": float32(5.8)}, "v"))
	require.Zero(t, recallInt(map[string]any{"v": "6"}, "v"))

	require.Equal(t, 1.5, recallFloat64(map[string]any{"v": float64(1.5)}, "v"))
	require.Equal(t, float64(float32(2.5)), recallFloat64(map[string]any{"v": float32(2.5)}, "v"))
	require.Equal(t, 3.0, recallFloat64(map[string]any{"v": 3}, "v"))
	require.Equal(t, 4.0, recallFloat64(map[string]any{"v": int64(4)}, "v"))
	require.Equal(t, 5.0, recallFloat64(map[string]any{"v": int32(5)}, "v"))
	require.Zero(t, recallFloat64(map[string]any{"v": "6"}, "v"))

	opts := normalizeCommunityExpansionOptions(CommunityExpansionOptions{
		CommunityLimit:      20,
		MembersPerCommunity: 50,
		ScanLimit:           1,
		MaxCandidates:       101,
		MinScore:            -1,
	})
	require.Equal(t, 10, opts.CommunityLimit)
	require.Equal(t, 25, opts.MembersPerCommunity)
	require.Equal(t, 10, opts.ScanLimit)
	require.Equal(t, 100, opts.MaxCandidates)
	require.Equal(t, DefaultCommunityExpansionMinScore, opts.MinScore)

	require.Zero(t, scoreCommunitySummary(nil, []string{"alpha"}, "alpha"))
	require.Zero(t, scoreCommunitySummary(&domain.Community{Summary: "alpha", MemberCount: 0}, []string{"alpha"}, "alpha"))
	require.Zero(t, scoreCommunitySummary(&domain.Community{Summary: "gamma", MemberCount: 1}, []string{"alpha"}, "alpha"))
	require.Equal(t, 1.0, scoreCommunitySummary(community, []string{"alpha", "beta"}, "alpha beta"))
	require.Equal(t, []string{"alpha", "beta"}, communityQueryTokens("What alpha, alpha, a, and beta?"))
	require.Equal(t, 0.5, communityMemberScore(0, 0.5))
	require.Equal(t, 0.7, communityMemberScore(0.7, 0))
	require.Equal(t, 0.21, communityMemberScore(0.7, 0.3))
}

func TestCommunityExpansionHelpersCoverEmptyErrorAndSortBranches(t *testing.T) {
	ctx := context.Background()
	svc := &recallService{}

	require.Nil(t, svc.enrichCommunityHits(ctx, "pA", 1, RecallRequest{Query: "q"}, nil))
	svc.communityExpander = &fakeCommunityExpander{}
	require.Nil(t, svc.enrichCommunityHits(ctx, "pA", 0, RecallRequest{Query: "q"}, nil))
	svc.communityExpander = &fakeCommunityExpander{err: errors.New("expand failed")}
	require.Nil(t, svc.enrichCommunityHits(ctx, "pA", 1, RecallRequest{Query: "q"}, nil))

	seen := map[string]struct{}{}
	require.Nil(t, svc.communityFactHits(ctx, "pA", RecallRequest{}, seen, []FactRecallResult{{FactID: "fact-1"}}))
	require.Nil(t, svc.communityClaimHits(ctx, "pA", RecallRequest{}, seen, []ClaimRecallResult{{ClaimID: "claim-1"}}))
	require.Nil(t, svc.communityFragmentHits(ctx, "pA", seen, []CommunityFragmentRecallResult{{FragmentID: "fragment-1"}}))

	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	hits := []RecallHit{
		{Fragment: &domain.Fragment{FragmentID: "fragment-1"}, Tier: TierFragment, Score: 0.9},
		{Fact: &domain.Fact{FactID: "fact-1"}, Tier: TierActiveFact, Score: 0.2},
		{Claim: &domain.Claim{ClaimID: "claim-1"}, Tier: TierValidatedClaim, Score: 0.8, temporalRank: now},
		{Claim: &domain.Claim{ClaimID: "claim-2"}, Tier: TierValidatedClaim, Score: 0.7, temporalRank: now.Add(time.Hour)},
	}
	sortRecallHits(hits)
	require.Equal(t, "fact-1", hits[0].Fact.FactID)
	require.Equal(t, "claim-2", hits[1].Claim.ClaimID)
	require.Equal(t, "claim-1", hits[2].Claim.ClaimID)
	require.Equal(t, "fragment-1", hits[3].Fragment.FragmentID)

	keySet := recallHitKeySet(hits)
	require.Contains(t, keySet, "fact:fact-1")
	require.Contains(t, keySet, "claim:claim-1")
	require.Contains(t, keySet, "fragment:fragment-1")
	_, ok := recallHitKey(RecallHit{})
	require.False(t, ok)
	require.Equal(t, "custom", recallSortTier(RecallHit{sortTier: "custom", Tier: TierFragment}))
}

func TestCommunityFragmentHitsFiltersHydratedFragments(t *testing.T) {
	ctx := context.Background()
	svc := &recallService{hydrator: &fakeHydrator{
		frags: map[string]*domain.Fragment{
			"other-profile": {FragmentID: "other-profile", ProfileID: "pB"},
			"retracted":     {FragmentID: "retracted", ProfileID: "pA", Status: domain.FragmentStatusRetracted},
			"ok":            {FragmentID: "ok", ProfileID: "pA"},
		},
		missIDs: map[string]bool{"missing": true},
	}}
	seen := map[string]struct{}{"fragment:duplicate": {}}

	hits := svc.communityFragmentHits(ctx, "pA", seen, []CommunityFragmentRecallResult{
		{FragmentID: ""},
		{FragmentID: "foreign-candidate", ProfileID: "pB"},
		{FragmentID: "duplicate", ProfileID: "pA"},
		{FragmentID: "missing", ProfileID: "pA"},
		{FragmentID: "other-profile", ProfileID: "pA"},
		{FragmentID: "retracted", ProfileID: "pA"},
		{FragmentID: "ok", ProfileID: "pA", Score: 0.7},
	})

	require.Len(t, hits, 1)
	require.Equal(t, "ok", hits[0].Fragment.FragmentID)
	require.Equal(t, 0.7, hits[0].FinalScore)
	require.Contains(t, seen, "fragment:ok")
}
