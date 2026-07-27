package repository

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestDeriveSearchGenerationSpecFromEmbeddingDimensions(t *testing.T) {
	const contractID = "00112233-4455-6677-8899-aabbccddeeff"
	tests := []struct {
		name              string
		dimensions        int
		wantStrategy      string
		wantOperatorClass string
		wantExpression    string
		wantPhysicalName  string
	}{
		{
			name:              "1536 full precision hnsw",
			dimensions:        1536,
			wantStrategy:      string(domain.VectorIndexVectorHNSW),
			wantOperatorClass: "vector_cosine_ops",
			wantExpression:    "embedding::vector(1536)",
			wantPhysicalName:  "search_001122334455_1536_vector_hnsw_idx",
		},
		{
			name:              "3072 halfvec hnsw",
			dimensions:        3072,
			wantStrategy:      string(domain.VectorIndexHalfvecHNSW),
			wantOperatorClass: "halfvec_cosine_ops",
			wantExpression:    "embedding::halfvec(3072)",
			wantPhysicalName:  "search_001122334455_3072_halfvec_hnsw_idx",
		},
		{
			name:              "over hnsw limit exact",
			dimensions:        5000,
			wantStrategy:      string(domain.VectorIndexExact),
			wantOperatorClass: "",
			wantExpression:    "",
			wantPhysicalName:  "",
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
			assert.Equal(t, tt.wantPhysicalName, spec.PhysicalIndexName)
		})
	}
}

func TestValidateActiveContractMatchesConfigAcceptsLegacyPhysicalIndexName(t *testing.T) {
	const contractID = "00112233-4455-6677-8899-aabbccddeeff"
	input := normalizeEnsureActiveSearchContractInput(EnsureActiveSearchContractInput{
		Provider:   "openai",
		Model:      "text-embedding-3-large",
		Dimensions: 3072,
	})
	spec := deriveSearchGenerationSpec(contractID, input)
	tests := []struct {
		name              string
		physicalIndexName string
		wantError         bool
	}{
		{
			name:              "canonical name",
			physicalIndexName: spec.PhysicalIndexName,
		},
		{
			name:              "legacy name",
			physicalIndexName: "v2_" + spec.PhysicalIndexName,
		},
		{
			name:              "unrelated legacy name",
			physicalIndexName: "v2_search_deadbeefcafe_3072_halfvec_hnsw_idx",
			wantError:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contract := &ActiveSearchContract{
				EmbeddingContractID:   contractID,
				EmbeddingDimensions:   input.Dimensions,
				EmbeddingProvider:     input.Provider,
				EmbeddingModel:        input.Model,
				DistanceMetric:        string(domain.VectorDistanceCosine),
				VectorNormalization:   input.VectorNormalization,
				DocumentFormatVersion: input.DocumentFormatVersion,
				QueryFormatVersion:    input.QueryFormatVersion,
				IndexStrategy:         spec.AnnStrategy,
				OperatorClass:         spec.OperatorClass,
				IndexedExpression:     spec.IndexedExpression,
				PhysicalIndexName:     tt.physicalIndexName,
			}

			err := validateActiveContractMatchesConfig(contract, input)
			if !tt.wantError {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrSearchContractMismatch)
			assert.Contains(t, err.Error(), "physical_index_name")
			assert.Contains(t, err.Error(), tt.physicalIndexName)
			assert.Contains(t, err.Error(), spec.PhysicalIndexName)
		})
	}
}

func TestValidateSearchGenerationMatchesSpecAcceptsLegacyPhysicalIndexName(t *testing.T) {
	const contractID = "00112233-4455-6677-8899-aabbccddeeff"
	input := normalizeEnsureActiveSearchContractInput(EnsureActiveSearchContractInput{
		Provider:   "openai",
		Model:      "text-embedding-3-large",
		Dimensions: 3072,
	})
	spec := deriveSearchGenerationSpec(contractID, input)
	generation := spec
	generation.PhysicalIndexName = "v2_" + spec.PhysicalIndexName

	require.NoError(t, validateSearchGenerationMatchesSpec(generation, spec))

	generation.PhysicalIndexName = "v2_search_deadbeefcafe_3072_halfvec_hnsw_idx"
	err := validateSearchGenerationMatchesSpec(generation, spec)
	require.ErrorIs(t, err, ErrSearchContractMismatch)
	assert.Contains(t, err.Error(), generation.PhysicalIndexName)
	assert.Contains(t, err.Error(), spec.PhysicalIndexName)
}
