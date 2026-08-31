package repository

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

var searchTestContractSequence atomic.Int32

func insertSearchTestContract(t *testing.T, db *gorm.DB, rls *storagepostgres.RLS, prefix string, dimensions int, strategy string, indexName string) string {
	return insertSearchTestContractWithOptions(t, db, rls, prefix, dimensions, strategy, indexName, 10000, false)
}

func insertSearchTestContractWithOptions(t *testing.T, db *gorm.DB, rls *storagepostgres.RLS, prefix string, dimensions int, strategy string, indexName string, exactMaxRows int, allowExactFallback bool) string {
	t.Helper()
	sequence := int(searchTestContractSequence.Add(1))
	contractKey := fmt.Sprintf("%s-%s", prefix, strings.ReplaceAll(uuid.NewString(), "-", "")[:8])
	contractID := uuid.NewString()
	searchGenerationID := uuid.NewString()
	operatorClass, indexedExpression := "", ""
	switch strategy {
	case "vector_hnsw":
		operatorClass, indexedExpression = "vector_cosine_ops", fmt.Sprintf("embedding::vector(%d)", dimensions)
	case "halfvec_hnsw":
		operatorClass, indexedExpression = "halfvec_cosine_ops", fmt.Sprintf("embedding::halfvec(%d)", dimensions)
	case "binary_hnsw":
		operatorClass, indexedExpression = "bit_hamming_ops", fmt.Sprintf("binary_quantize(embedding)::bit(%d)", dimensions)
	}
	err := rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO embedding_contracts (
			    embedding_contract_id, contract_key, version, provider, model,
			    dimensions, distance_metric, vector_normalization,
			    document_format_version, query_format_version, lifecycle_state
			) VALUES (?::uuid, ?, ?, 'test', ?, ?, 'cosine', 'provider', 1, 1, 'active')
		`, contractID, contractKey, sequence, "test-model", dimensions).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO search_index_generations (
			    search_index_generation_id, generation, embedding_contract_id,
			    embedding_dimensions, ann_strategy, operator_class,
			    indexed_expression, physical_index_name, exact_max_rows,
			    allow_exact_fallback, activation_state, activated_at
			) VALUES (?::uuid, ?, ?::uuid, ?, ?, ?, ?, ?, ?, ?, 'active', now())
		`, searchGenerationID, sequence, contractID, dimensions, strategy,
			operatorClass, indexedExpression, indexName, exactMaxRows, allowExactFallback).Error
	})
	require.NoError(t, err)
	return contractID
}

func upsertSearchDocumentForTest(t *testing.T, repo *SearchRepositoryImpl, teamID string, ownerID string, text string, sourceVersion int64) *SearchDocumentResult {
	t.Helper()
	doc, err := repo.UpsertSearchDocument(context.Background(), UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence",
		SourceID: uuid.NewString(), SourceVersion: sourceVersion, DocumentText: text,
	})
	require.NoError(t, err)
	require.NotEmpty(t, doc.SearchDocumentID)
	require.Equal(t, "pending", doc.SearchState)
	return doc
}

func completeSearchDocumentsForTest(t *testing.T, repo *SearchRepositoryImpl, teamID string, vectorsByDocumentID map[string][]float32) {
	t.Helper()
	type document struct {
		ID, Owner, Hash, Contract, Space              string
		SourceVersion, DocumentVersion                int64
		ProjectionFormat, Dimensions, SpaceGeneration int
		ProjectionGeneration                          string
	}
	var documents []document
	require.NoError(t, repo.withSystemTx(context.Background(), func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT search_document_id::text, owner_profile_id::text, document_hash,
			       embedding_contract_id::text, COALESCE(space_id::text, ''), source_version,
			       document_version, projection_format_version,
			       COALESCE(projection_generation_id::text, ''), embedding_dimensions,
			       COALESCE(space_generation, 0)
			FROM search_documents
			WHERE team_id = ?::uuid AND search_document_id = ANY(?::uuid[])
		`, teamID, pq.Array(stringKeys(vectorsByDocumentID))).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item document
			if err := rows.Scan(&item.ID, &item.Owner, &item.Hash, &item.Contract, &item.Space, &item.SourceVersion,
				&item.DocumentVersion, &item.ProjectionFormat, &item.ProjectionGeneration,
				&item.Dimensions, &item.SpaceGeneration); err != nil {
				return err
			}
			documents = append(documents, item)
		}
		return rows.Err()
	}))
	require.Len(t, documents, len(vectorsByDocumentID))
	byOwner := make(map[string][]SearchDocumentEmbedding)
	for _, item := range documents {
		byOwner[item.Owner] = append(byOwner[item.Owner], SearchDocumentEmbedding{
			SearchDocumentID: item.ID, DocumentHash: item.Hash, SourceVersion: item.SourceVersion,
			DocumentVersion: item.DocumentVersion, ProjectionFormat: item.ProjectionFormat,
			ProjectionGenerationID: item.ProjectionGeneration, EmbeddingContractID: item.Contract,
			EmbeddingDimensions: item.Dimensions, Embedding: vectorsByDocumentID[item.ID],
			SpaceID: item.Space, SpaceGeneration: int64(item.SpaceGeneration),
		})
	}
	for owner, docs := range byOwner {
		require.NoError(t, repo.CompleteSearchDocumentsWithEmbeddings(context.Background(), CompleteSearchDocumentsWithEmbeddingsInput{
			TeamID: teamID, OwnerProfileID: owner, Documents: docs,
		}))
	}
}

func stringKeys(values map[string][]float32) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
