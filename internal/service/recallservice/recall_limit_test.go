package recallservice

import (
	"context"
	"testing"

	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
)

func TestRecallService_TruncatesToLimit(t *testing.T) {
	sem := &fakeSemanticSearcher{}
	for i := 0; i < 20; i++ {
		sem.hits = append(sem.hits, semanticsearch.SearchHit{ID: "f" + itoa(i), Type: "fragment"})
	}
	kw := &fakeKeywordSearcher{}
	emb := &stubEmbedding{DimensionsResult: 4}
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "q", Limit: 5})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(out) != 5 {
		t.Fatalf("len(out) = %d; want 5 (capped by limit)", len(out))
	}
}
