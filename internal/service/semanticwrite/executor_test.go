package semanticwrite

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type providerStub struct {
	available bool
	model     string
	dims      int
	vectors   []IndexedEmbedding
	returned  string
	err       error
	calls     int
	texts     []string
}

func (p *providerStub) EmbedBatch(_ context.Context, texts []string) ([]IndexedEmbedding, string, error) {
	p.calls++
	p.texts = append([]string(nil), texts...)
	return p.vectors, p.returned, p.err
}
func (p *providerStub) ModelName() string { return p.model }
func (p *providerStub) Dimensions() int   { return p.dims }
func (p *providerStub) IsAvailable() bool { return p.available }

func validPlan() Plan {
	return Plan{
		Documents: []Document{{Hash: "hash-a", Text: "a"}, {Hash: "hash-b", Text: "b"}},
		Fence: Fence{
			Model: "model", Dimensions: 2, EmbeddingContractID: "contract",
			SearchGenerationID: "generation", SearchGenerationVersion: 1,
		},
		Timeout: time.Second,
	}
}

func validProvider() *providerStub {
	return &providerStub{
		available: true, model: "model", dims: 2, returned: "model",
		vectors: []IndexedEmbedding{{Index: 0, Vector: []float32{1, 2}}, {Index: 1, Vector: []float32{3, 4}}},
	}
}

func TestExecutorAssociatesOrderedVectorsWithHashes(t *testing.T) {
	provider := validProvider()
	result, err := NewExecutor(provider).Execute(context.Background(), validPlan())
	require.NoError(t, err)
	require.Equal(t, 1, provider.calls)
	require.Equal(t, []string{"a", "b"}, provider.texts)
	require.Equal(t, []Embedding{{DocumentHash: "hash-a", Vector: []float32{1, 2}}, {DocumentHash: "hash-b", Vector: []float32{3, 4}}}, result.Embeddings)
	provider.vectors[0].Vector[0] = 99
	require.Equal(t, float32(1), result.Embeddings[0].Vector[0])
}

func TestExecutorRejectsMissingDuplicateUnknownAndOutOfOrderIndices(t *testing.T) {
	cases := []struct {
		name string
		edit func([]IndexedEmbedding) []IndexedEmbedding
	}{
		{"missing index", func(values []IndexedEmbedding) []IndexedEmbedding {
			return []IndexedEmbedding{{Index: 0, Vector: []float32{1, 2}}}
		}},
		{"duplicate index", func(values []IndexedEmbedding) []IndexedEmbedding {
			return []IndexedEmbedding{{Index: 0, Vector: []float32{1, 2}}, {Index: 0, Vector: []float32{3, 4}}}
		}},
		{"unknown index", func(values []IndexedEmbedding) []IndexedEmbedding {
			return []IndexedEmbedding{{Index: 0, Vector: []float32{1, 2}}, {Index: 2, Vector: []float32{3, 4}}}
		}},
		{"out of order", func(values []IndexedEmbedding) []IndexedEmbedding {
			return []IndexedEmbedding{{Index: 1, Vector: []float32{1, 2}}, {Index: 0, Vector: []float32{3, 4}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := validProvider()
			provider.vectors = tc.edit(provider.vectors)
			_, err := NewExecutor(provider).Execute(context.Background(), validPlan())
			require.ErrorIs(t, err, ErrProviderResponseInvalid)
		})
	}
}

func TestExecutorRejectsMalformedPlansBeforeProvider(t *testing.T) {
	provider := validProvider()
	cases := []struct {
		name string
		plan Plan
	}{
		{"missing fence", Plan{Documents: validPlan().Documents, Timeout: time.Second}},
		{"duplicate hash", func() Plan { plan := validPlan(); plan.Documents[1].Hash = plan.Documents[0].Hash; return plan }()},
		{"non-canonical hash", func() Plan { plan := validPlan(); plan.Documents[0].Hash = " hash-a "; return plan }()},
		{"non-canonical model", func() Plan { plan := validPlan(); plan.Fence.Model = " model "; return plan }()},
		{"blank text", func() Plan { plan := validPlan(); plan.Documents[0].Text = " "; return plan }()},
		{"zero timeout", func() Plan { plan := validPlan(); plan.Timeout = 0; return plan }()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewExecutor(provider).Execute(context.Background(), tc.plan)
			require.ErrorIs(t, err, ErrInvalidPlan)
			require.Zero(t, provider.calls)
		})
	}
}

func TestExecutorRejectsProviderAvailabilityAndContractMismatches(t *testing.T) {
	cases := []struct {
		name string
		edit func(*providerStub)
		want error
	}{
		{"unavailable", func(p *providerStub) { p.available = false }, ErrProviderUnavailable},
		{"configured model", func(p *providerStub) { p.model = "other" }, ErrProviderResponseInvalid},
		{"configured dimensions", func(p *providerStub) { p.dims = 3 }, ErrProviderResponseInvalid},
		{"returned model", func(p *providerStub) { p.returned = "other" }, ErrProviderResponseInvalid},
		{"count", func(p *providerStub) { p.vectors = p.vectors[:1] }, ErrProviderResponseInvalid},
		{"dimensions", func(p *providerStub) { p.vectors[0].Vector = []float32{1} }, ErrProviderResponseInvalid},
		{"non-finite", func(p *providerStub) { p.vectors[1].Vector[1] = float32(math.NaN()) }, ErrProviderResponseInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := validProvider()
			tc.edit(provider)
			_, err := NewExecutor(provider).Execute(context.Background(), validPlan())
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestExecutorBoundsProviderFailureAndHonorsContextDeadline(t *testing.T) {
	provider := validProvider()
	provider.err = errors.New("provider failed")
	_, err := NewExecutor(provider).Execute(context.Background(), validPlan())
	require.ErrorIs(t, err, ErrProviderUnavailable)
	require.NotContains(t, err.Error(), "provider failed")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider.err = nil
	_, err = NewExecutor(provider).Execute(ctx, validPlan())
	require.Error(t, err)
}

func TestExecutorAllowsEmptyNotRequiredPlanWithoutProviderCall(t *testing.T) {
	provider := validProvider()
	plan := validPlan()
	plan.Documents = nil
	result, err := NewExecutor(provider).Execute(context.Background(), plan)
	require.NoError(t, err)
	require.Empty(t, result.Embeddings)
	require.Zero(t, provider.calls)
}
