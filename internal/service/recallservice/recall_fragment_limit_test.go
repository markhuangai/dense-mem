package recallservice

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
	"github.com/stretchr/testify/require"
)

func TestRecallService_TierHitDisplacesFragmentBeforeHydration(t *testing.T) {
	sem := &fakeSemanticSearcher{hits: []semanticsearch.SearchHit{{ID: "fragment-1", ProfileID: "pA"}}}
	kw := &fakeKeywordSearcher{}
	hydrator := &fakeHydrator{}
	factSearcher := &fakeFactSearcher{
		results: []FactRecallResult{{FactID: "fact-1", ProfileID: "pA"}},
	}
	factGetter := &fakeFactGetter{
		facts: map[string]*domain.Fact{
			"fact-1": {
				FactID:     "fact-1",
				ProfileID:  "pA",
				Subject:    "mars mission",
				Status:     domain.FactStatusActive,
				TruthScore: 0.95,
				RecordedAt: time.Now().UTC(),
			},
		},
	}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		sem,
		kw,
		hydrator,
		factSearcher,
		factGetter,
		nil,
		nil,
		0,
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "mars mission", Limit: 1})

	require.NoError(t, err)
	require.Len(t, out, 1)
	require.NotNil(t, out[0].Fact)
	require.Equal(t, "fact-1", out[0].Fact.FactID)
	require.Equal(t, int32(0), atomic.LoadInt32(&hydrator.batchCallCount))
	require.Equal(t, int32(0), atomic.LoadInt32(&hydrator.callCount))
}

func TestRecallService_UnrelatedTierHitDoesNotDisplaceGenericFragment(t *testing.T) {
	query := "Radioiodine treatment of non-toxic multinodular goitre reduces thyroid volume."
	sem := &fakeSemanticSearcher{hits: []semanticsearch.SearchHit{{
		ID:        "fragment-1",
		ProfileID: "pA",
		Content:   query,
	}}}
	kw := &fakeKeywordSearcher{}
	hydrator := &fakeHydrator{}
	factSearcher := &fakeFactSearcher{
		results: []FactRecallResult{{FactID: "fact-dm412", ProfileID: "pA"}},
	}
	factGetter := &fakeFactGetter{
		facts: map[string]*domain.Fact{
			"fact-dm412": {
				FactID:     "fact-dm412",
				ProfileID:  "pA",
				Subject:    "issue DM-412",
				Predicate:  "profile_fact",
				Object:     "low recall ratings require deterministic identifier extraction at write and recall time",
				Status:     domain.FactStatusActive,
				TruthScore: 0.95,
				RecordedAt: time.Now().UTC(),
			},
		},
	}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		sem,
		kw,
		hydrator,
		factSearcher,
		factGetter,
		nil,
		nil,
		0,
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 1})

	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Nil(t, out[0].Fact)
	require.NotNil(t, out[0].Fragment)
	require.Equal(t, "fragment-1", out[0].Fragment.FragmentID)
}

func TestRecallService_NaturalLanguageIdentifierTokensDoNotGateFragments(t *testing.T) {
	query := "TCR/CD3 microdomains are required to induce the immunologic synapse to activate T cells."
	semHits := make([]semanticsearch.SearchHit, 10)
	frags := make(map[string]*domain.Fragment, len(semHits))
	for i := range semHits {
		id := fmt.Sprintf("fragment-%02d", i+1)
		content := fmt.Sprintf("generic immunology candidate %02d", i+1)
		semHits[i] = semanticsearch.SearchHit{
			ID:        id,
			ProfileID: "pA",
			Content:   content,
		}
		frags[id] = &domain.Fragment{
			FragmentID: id,
			ProfileID:  "pA",
			Content:    content,
		}
	}
	svc := NewRecallService(
		&stubEmbedding{DimensionsResult: 4},
		&fakeSemanticSearcher{hits: semHits},
		&fakeKeywordSearcher{},
		&fakeHydrator{frags: frags},
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 10})

	require.NoError(t, err)
	require.Len(t, out, 10)
}

func TestRecallService_ProjectIdentifierHardGateFiltersFragments(t *testing.T) {
	sem := &fakeSemanticSearcher{hits: []semanticsearch.SearchHit{
		{
			ID:        "good",
			ProfileID: "pA",
			Content:   "Implementation note for PR #56 at commit 2e4b0cc.",
		},
		{
			ID:        "wrong-pr",
			ProfileID: "pA",
			Content:   "Implementation note for PR #65 at commit 2e4b0cc.",
		},
		{
			ID:        "missing-commit",
			ProfileID: "pA",
			Content:   "Implementation note for PR #56 without the target commit.",
		},
	}}
	hydrator := &fakeHydrator{frags: map[string]*domain.Fragment{
		"good":           {FragmentID: "good", ProfileID: "pA", Content: "Implementation note for PR #56 at commit 2e4b0cc."},
		"wrong-pr":       {FragmentID: "wrong-pr", ProfileID: "pA", Content: "Implementation note for PR #65 at commit 2e4b0cc."},
		"missing-commit": {FragmentID: "missing-commit", ProfileID: "pA", Content: "Implementation note for PR #56 without the target commit."},
	}}
	svc := NewRecallService(
		&stubEmbedding{DimensionsResult: 4},
		sem,
		&fakeKeywordSearcher{},
		hydrator,
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "What changed in PR #56 commit 2e4b0cc?", Limit: 10})

	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, "good", out[0].Fragment.FragmentID)
}

func TestRecallService_RankingAnchorsPreserveFragmentTierPromotion(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		content string
	}{
		{
			name:    "context qualified ID",
			query:   "What timeout should job UNT-013 use?",
			content: "Runtime configuration says job UNT-013 uses 23 minutes.",
		},
		{
			name:    "recognized filename",
			query:   "What is thumbs.db used for?",
			content: "Windows uses thumbs.db as a thumbnail cache.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sem := &fakeSemanticSearcher{hits: []semanticsearch.SearchHit{{
				ID:        "fragment-1",
				ProfileID: "pA",
				Content:   tt.content,
			}}}
			factSearcher := &fakeFactSearcher{
				results: []FactRecallResult{{FactID: "fact-1", ProfileID: "pA"}},
			}
			factGetter := &fakeFactGetter{
				facts: map[string]*domain.Fact{
					"fact-1": {
						FactID:     "fact-1",
						ProfileID:  "pA",
						Subject:    tt.content,
						Status:     domain.FactStatusActive,
						TruthScore: 0.95,
						RecordedAt: time.Now().UTC(),
					},
				},
			}
			svc := NewRecallServiceWithTiers(
				&stubEmbedding{DimensionsResult: 4},
				sem,
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

			out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: tt.query, Limit: 2})

			require.NoError(t, err)
			require.Len(t, out, 2)
			require.NotNil(t, out[0].Fragment)
			require.Equal(t, "fragment-1", out[0].Fragment.FragmentID)
			require.NotNil(t, out[1].Fact)
			require.Equal(t, "fact-1", out[1].Fact.FactID)
		})
	}
}
