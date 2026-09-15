//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

var accessSearchTestContractSequence atomic.Int32

func insertSearchTestContract(t *testing.T, db *gorm.DB, rls *storagepostgres.RLS, prefix string, dimensions int, strategy string, indexName string) string {
	t.Helper()
	sequence := int(accessSearchTestContractSequence.Add(1))
	contractKey := fmt.Sprintf("%s-%s", prefix, strings.ReplaceAll(uuid.NewString(), "-", "")[:8])
	contractID, generationID := uuid.NewString(), uuid.NewString()
	operatorClass, indexedExpression := "", ""
	switch strategy {
	case "vector_hnsw":
		operatorClass, indexedExpression = "vector_cosine_ops", fmt.Sprintf("embedding::vector(%d)", dimensions)
	case "halfvec_hnsw":
		operatorClass, indexedExpression = "halfvec_cosine_ops", fmt.Sprintf("embedding::halfvec(%d)", dimensions)
	case "binary_hnsw":
		operatorClass, indexedExpression = "bit_hamming_ops", fmt.Sprintf("binary_quantize(embedding)::bit(%d)", dimensions)
	}
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO embedding_contracts (embedding_contract_id, contract_key, version, provider, model, dimensions, distance_metric, vector_normalization, document_format_version, query_format_version, lifecycle_state) VALUES (?::uuid, ?, ?, 'test', 'test-model', ?, 'cosine', 'provider', 1, 1, 'active')`, contractID, contractKey, sequence, dimensions).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO search_index_generations (search_index_generation_id, generation, embedding_contract_id, embedding_dimensions, ann_strategy, operator_class, indexed_expression, physical_index_name, exact_max_rows, allow_exact_fallback, activation_state, activated_at) VALUES (?::uuid, ?, ?::uuid, ?, ?, ?, ?, ?, 10000, false, 'active', now())`, generationID, sequence, contractID, dimensions, strategy, operatorClass, indexedExpression, indexName).Error
	}))
	return contractID
}
