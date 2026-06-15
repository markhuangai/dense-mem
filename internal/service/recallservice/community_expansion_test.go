package recallservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestCommunityExpansionEdgeBranches(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	svc := &recallService{}
	req := RecallRequest{Query: "q", KnownAt: &now}

	require.Nil(t, svc.enrichCommunityHits(context.Background(), "pA", 1, req, nil))
	require.Nil(t, svc.communityFactHits(context.Background(), "pA", req, nil, []FactRecallResult{{FactID: "fact-1", ProfileID: "pA"}}))
	require.Nil(t, svc.communityClaimHits(context.Background(), "pA", req, nil, []ClaimRecallResult{{ClaimID: "claim-1", ProfileID: "pA"}}))
	require.Nil(t, svc.communityFragmentHits(context.Background(), "pA", nil, []CommunityFragmentRecallResult{{FragmentID: "frag-1", ProfileID: "pA"}}))

	logger := &fakeLogger{}
	svc = &recallService{
		communityExpander: &fakeCommunityExpander{err: errors.New("expand failed")},
		logger:            logger,
	}
	require.Nil(t, svc.enrichCommunityHits(context.Background(), "pA", 1, req, nil))
	require.Equal(t, int32(1), logger.warns)

	future := now.Add(time.Hour)
	svc = &recallService{
		factGet: &getOnlyFactGetter{facts: map[string]*domain.Fact{
			"fact-good": {FactID: "fact-good", ProfileID: "pA", RecordedAt: now, TruthScore: 0.9},
		}},
		claimGet: &getOnlyClaimGetter{claims: map[string]*domain.Claim{
			"claim-good": {ClaimID: "claim-good", ProfileID: "pA", RecordedAt: now, ExtractConf: 0.8},
		}},
		claimWeight: DefaultRecallValidatedClaimWeight,
		hydrator: &getOnlyFragmentHydrator{frags: map[string]*domain.Fragment{
			"frag-good":      {FragmentID: "frag-good", ProfileID: "pA"},
			"frag-wrong":     {FragmentID: "frag-wrong", ProfileID: "pB"},
			"frag-retracted": {FragmentID: "frag-retracted", ProfileID: "pA", Status: domain.FragmentStatusRetracted},
		}},
	}
	seen := map[string]struct{}{
		recallCandidateKey("fact", "fact-seen"):     {},
		recallCandidateKey("claim", "claim-seen"):   {},
		recallCandidateKey("fragment", "frag-seen"): {},
	}

	factHits := svc.communityFactHits(context.Background(), "pA", req, seen, []FactRecallResult{
		{FactID: "fact-seen", ProfileID: "pA", RecordedAt: now},
		{FactID: "fact-wrong", ProfileID: "pB", RecordedAt: now},
		{FactID: "fact-future", ProfileID: "pA", RecordedAt: future},
		{FactID: "fact-missing", ProfileID: "pA", RecordedAt: now},
		{FactID: "fact-good", ProfileID: "pA", RecordedAt: now},
	})
	require.Len(t, factHits, 1)
	require.Equal(t, "fact-good", factHits[0].Fact.FactID)

	claimHits := svc.communityClaimHits(context.Background(), "pA", req, seen, []ClaimRecallResult{
		{ClaimID: "claim-seen", ProfileID: "pA", RecordedAt: now},
		{ClaimID: "claim-wrong", ProfileID: "pB", RecordedAt: now},
		{ClaimID: "claim-future", ProfileID: "pA", RecordedAt: future},
		{ClaimID: "claim-missing", ProfileID: "pA", RecordedAt: now},
		{ClaimID: "claim-good", ProfileID: "pA", RecordedAt: now},
	})
	require.Len(t, claimHits, 1)
	require.Equal(t, "claim-good", claimHits[0].Claim.ClaimID)

	fragmentHits := svc.communityFragmentHits(context.Background(), "pA", seen, []CommunityFragmentRecallResult{
		{FragmentID: "frag-seen", ProfileID: "pA", Score: 0.5},
		{FragmentID: "frag-wrong", ProfileID: "pA", Score: 0.5},
		{FragmentID: "frag-retracted", ProfileID: "pA", Score: 0.5},
		{FragmentID: "frag-good", ProfileID: "pA", Score: 0.5},
	})
	require.Len(t, fragmentHits, 1)
	require.Equal(t, "frag-good", fragmentHits[0].Fragment.FragmentID)
}

func TestRecallHitKeyVariants(t *testing.T) {
	key, ok := recallHitKey(RecallHit{Fact: &domain.Fact{FactID: "fact-1"}})
	require.True(t, ok)
	require.Equal(t, "fact:fact-1", key)

	key, ok = recallHitKey(RecallHit{Claim: &domain.Claim{ClaimID: "claim-1"}})
	require.True(t, ok)
	require.Equal(t, "claim:claim-1", key)

	key, ok = recallHitKey(RecallHit{Fragment: &domain.Fragment{FragmentID: "frag-1"}})
	require.True(t, ok)
	require.Equal(t, "fragment:frag-1", key)

	key, ok = recallHitKey(RecallHit{})
	require.False(t, ok)
	require.Empty(t, key)
}
