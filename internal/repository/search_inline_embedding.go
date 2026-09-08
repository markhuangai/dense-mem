package repository

import (
	"context"
	"errors"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

// Inline embedding writes are compatibility facades. The PostgreSQL owner
// keeps their validation, fences, and transaction bodies in one package.
func (r *SearchRepositoryImpl) LoadSearchDocumentsForEmbedding(
	ctx context.Context,
	input LoadSearchDocumentsForEmbeddingInput,
) ([]SearchDocumentForEmbedding, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("search: knowledge write owner is required")
	}
	return owner.LoadSearchDocumentsForEmbedding(ctx, knowledgepostgres.LoadSearchDocumentsForEmbeddingInput(input))
}

func (r *SearchRepositoryImpl) LoadSearchDocumentsForSources(
	ctx context.Context,
	input LoadSearchDocumentsForSourcesInput,
) ([]SearchDocumentForEmbedding, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("search: knowledge write owner is required")
	}
	return owner.LoadSearchDocumentsForSources(ctx, knowledgepostgres.LoadSearchDocumentsForSourcesInput(input))
}

func (r *SearchRepositoryImpl) CompleteSearchDocumentsWithEmbeddings(
	ctx context.Context,
	input CompleteSearchDocumentsWithEmbeddingsInput,
) error {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return errors.New("search: knowledge write owner is required")
	}
	documents := make([]knowledgepostgres.SearchDocumentEmbedding, len(input.Documents))
	for index, document := range input.Documents {
		documents[index] = knowledgepostgres.SearchDocumentEmbedding(document)
	}
	return owner.CompleteSearchDocumentsWithEmbeddings(ctx, knowledgepostgres.CompleteSearchDocumentsWithEmbeddingsInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, Documents: documents,
	})
}
