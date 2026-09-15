//go:build integration

package postgres

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

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

var dreamSearchTestContractSequence atomic.Int32

func insertSearchTestContract(t *testing.T, db *gorm.DB, rls *storagepostgres.RLS, prefix string, dimensions int, strategy string, indexName string) string {
	t.Helper()
	sequence := int(dreamSearchTestContractSequence.Add(1))
	contractKey := fmt.Sprintf("%s-%s", prefix, strings.ReplaceAll(uuid.NewString(), "-", "")[:8])
	contractID, generationID := uuid.NewString(), uuid.NewString()
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO embedding_contracts (embedding_contract_id, contract_key, version, provider, model, dimensions, distance_metric, vector_normalization, document_format_version, query_format_version, lifecycle_state) VALUES (?::uuid, ?, ?, 'test', 'test-model', ?, 'cosine', 'provider', 1, 1, 'active')`, contractID, contractKey, sequence, dimensions).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO search_index_generations (search_index_generation_id, generation, embedding_contract_id, embedding_dimensions, ann_strategy, operator_class, indexed_expression, physical_index_name, exact_max_rows, allow_exact_fallback, activation_state, activated_at) VALUES (?::uuid, ?, ?::uuid, ?, ?, '', '', ?, 10000, false, 'active', now())`, generationID, sequence, contractID, dimensions, strategy, indexName).Error
	}))
	return contractID
}

type dreamSearchFixtureStore struct {
	db         *gorm.DB
	rls        storagepostgres.RLSHelper
	read       *searchpostgres.Store
	projection *knowledgepostgres.Store
}

func newDreamSearchFixtureStore(db *gorm.DB, rls storagepostgres.RLSHelper) *dreamSearchFixtureStore {
	return &dreamSearchFixtureStore{db: db, rls: rls, read: searchpostgres.NewStore(db, rls), projection: knowledgepostgres.NewStore(db, rls, knowledgecontract.ConflictRuntimeConfig{})}
}
func (s *dreamSearchFixtureStore) GetActiveSearchContract(ctx context.Context) (*searchcontract.ActiveSearchContract, error) {
	return s.read.GetActiveSearchContract(ctx)
}
func (s *dreamSearchFixtureStore) CheckSearchReadiness(ctx context.Context) (*searchcontract.SearchReadiness, error) {
	return s.read.CheckSearchReadiness(ctx)
}
func (s *dreamSearchFixtureStore) SearchFullText(ctx context.Context, input searchcontract.FullTextSearchInput) ([]searchcontract.SearchHit, error) {
	return s.read.SearchFullText(ctx, input)
}
func (s *dreamSearchFixtureStore) SearchExactVector(ctx context.Context, input searchcontract.ExactVectorSearchInput) ([]searchcontract.SearchHit, error) {
	return s.read.SearchExactVector(ctx, input)
}
func (s *dreamSearchFixtureStore) UpsertSearchDocument(ctx context.Context, input knowledgecontract.UpsertSearchDocumentInput) (*knowledgecontract.SearchDocumentResult, error) {
	return s.projection.UpsertSearchDocument(ctx, input)
}
func (s *dreamSearchFixtureStore) CompleteSearchDocumentsWithEmbeddings(ctx context.Context, input knowledgecontract.CompleteSearchDocumentsWithEmbeddingsInput) error {
	return s.projection.CompleteSearchDocumentsWithEmbeddings(ctx, input)
}

func completeSearchDocumentsForTest(t *testing.T, repo *dreamSearchFixtureStore, teamID string, vectors map[string][]float32) {
	t.Helper()
	type row struct {
		id, owner, hash, contract, space, generation                        string
		sourceVersion, documentVersion, format, dimensions, spaceGeneration int64
	}
	rowsOut := []row{}
	require.NoError(t, repo.rls.WithSystemTx(context.Background(), repo.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`SELECT search_document_id::text, owner_profile_id::text, document_hash, embedding_contract_id::text, COALESCE(space_id::text, ''), source_version, document_version, projection_format_version, COALESCE(projection_generation_id::text, ''), embedding_dimensions, COALESCE(space_generation, 0) FROM search_documents WHERE team_id = ?::uuid AND search_document_id = ANY(?::uuid[])`, teamID, pq.Array(stringKeys(vectors))).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item row
			if err := rows.Scan(&item.id, &item.owner, &item.hash, &item.contract, &item.space, &item.sourceVersion, &item.documentVersion, &item.format, &item.generation, &item.dimensions, &item.spaceGeneration); err != nil {
				return err
			}
			rowsOut = append(rowsOut, item)
		}
		return rows.Err()
	}))
	byOwner := map[string][]knowledgecontract.SearchDocumentEmbedding{}
	for _, item := range rowsOut {
		byOwner[item.owner] = append(byOwner[item.owner], knowledgecontract.SearchDocumentEmbedding{SearchDocumentID: item.id, DocumentHash: item.hash, SourceVersion: item.sourceVersion, DocumentVersion: item.documentVersion, ProjectionFormat: int(item.format), ProjectionGenerationID: item.generation, EmbeddingContractID: item.contract, EmbeddingDimensions: int(item.dimensions), Embedding: vectors[item.id], SpaceID: item.space, SpaceGeneration: item.spaceGeneration})
	}
	for owner, docs := range byOwner {
		require.NoError(t, repo.CompleteSearchDocumentsWithEmbeddings(context.Background(), knowledgecontract.CompleteSearchDocumentsWithEmbeddingsInput{TeamID: teamID, OwnerProfileID: owner, Documents: docs}))
	}
}
func stringKeys(values map[string][]float32) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
