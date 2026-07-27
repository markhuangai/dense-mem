package repository

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestDeriveSearchGenerationSpecFromEmbeddingDimensions(t *testing.T) {
	contractID := uuid.NewString()
	tests := []struct {
		name               string
		dimensions         int
		wantStrategy       string
		wantOperatorClass  string
		wantExpression     string
		wantPhysicalPrefix string
	}{
		{
			name:               "1536 full precision hnsw",
			dimensions:         1536,
			wantStrategy:       string(domain.VectorIndexVectorHNSW),
			wantOperatorClass:  "vector_cosine_ops",
			wantExpression:     "embedding::vector(1536)",
			wantPhysicalPrefix: "search_",
		},
		{
			name:               "3072 halfvec hnsw",
			dimensions:         3072,
			wantStrategy:       string(domain.VectorIndexHalfvecHNSW),
			wantOperatorClass:  "halfvec_cosine_ops",
			wantExpression:     "embedding::halfvec(3072)",
			wantPhysicalPrefix: "search_",
		},
		{
			name:               "over hnsw limit exact",
			dimensions:         5000,
			wantStrategy:       string(domain.VectorIndexExact),
			wantOperatorClass:  "",
			wantExpression:     "",
			wantPhysicalPrefix: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := deriveSearchGenerationSpec(contractID, EnsureActiveSearchContractInput{
				Model:      "test-model",
				Dimensions: tt.dimensions,
			})

			assert.Equal(t, tt.wantStrategy, spec.AnnStrategy)
			assert.Equal(t, tt.wantOperatorClass, spec.OperatorClass)
			assert.Equal(t, tt.wantExpression, spec.IndexedExpression)
			if tt.wantPhysicalPrefix == "" {
				assert.Empty(t, spec.PhysicalIndexName)
			} else {
				assert.Contains(t, spec.PhysicalIndexName, tt.wantPhysicalPrefix)
			}
		})
	}
}
