package recallservice

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
	"github.com/stretchr/testify/require"
)

func TestRecallService_TierHitDisplacesFragmentBeforeHydration(t *testing.T) {
	sem := &fakeSemanticSearcher{hits: []semanticsearch.SearchHit{{ID: "fragment-1"}}}
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
