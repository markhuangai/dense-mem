package contract

import (
	"context"
	"testing"
)

type batchProviderStub struct{}

func (batchProviderStub) EmbedBatch(context.Context, []string) ([]IndexedEmbedding, string, error) {
	return nil, "", nil
}

func (batchProviderStub) ModelName() string { return "" }
func (batchProviderStub) Dimensions() int   { return 0 }
func (batchProviderStub) IsAvailable() bool { return false }

var _ BatchProvider = batchProviderStub{}

func TestPlanPreservesDocumentOrderAndFence(t *testing.T) {
	plan := Plan{
		Documents: []Document{{Hash: "first"}, {Hash: "second"}},
		Fence:     Fence{EmbeddingContractID: "contract-1", Dimensions: 3},
	}
	if got := plan.Documents[1].Hash; got != "second" {
		t.Fatalf("document order changed: %q", got)
	}
	if plan.Fence.EmbeddingContractID != "contract-1" || plan.Fence.Dimensions != 3 {
		t.Fatalf("fence changed: %#v", plan.Fence)
	}
}
