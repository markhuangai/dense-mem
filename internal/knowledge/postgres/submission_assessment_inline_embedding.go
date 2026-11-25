package postgres

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func searchDocumentHash(text string) string {
	return strings.TrimPrefix(sha256Hex(strings.TrimSpace(text)), "sha256:")
}

func applyInlineSubmissionEmbeddings(ctx context.Context, tx *gorm.DB, input CommitSubmissionAssessmentInput, semanticResult *submissionSemanticCommitState, inlineEmbeddings []InlineEmbeddingResult) error {
	searchDocumentIDs := make([]string, 0, len(semanticResult.SearchDocuments))
	for _, document := range semanticResult.SearchDocuments {
		if document.SearchState == string(domain.SearchProjectionPending) || document.SearchState == string(domain.SearchProjectionFailed) {
			searchDocumentIDs = append(searchDocumentIDs, document.SearchDocumentID)
		}
	}
	if len(searchDocumentIDs) == 0 {
		return nil
	}
	completedIDs, err := completeInlineEmbeddingResultsInTx(ctx, tx, input.TeamID, input.OwnerProfileID, searchDocumentIDs, inlineEmbeddings)
	if err != nil {
		return err
	}
	for index := range semanticResult.SearchDocuments {
		if _, ok := completedIDs[semanticResult.SearchDocuments[index].SearchDocumentID]; ok {
			semanticResult.SearchDocuments[index].SearchState = string(domain.SearchProjectionCurrent)
		}
	}
	return nil
}

func completeInlineEmbeddingResultsInTx(ctx context.Context, tx *gorm.DB, teamID, ownerID string, searchDocumentIDs []string, inlineEmbeddings []InlineEmbeddingResult) (map[string]struct{}, error) {
	if err := validateRememberEmbeddingContractFence(inlineEmbeddings); err != nil {
		return nil, err
	}
	contract, err := loadActiveSearchContractInTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := validateRememberEmbeddingContractAgainstActive(inlineEmbeddings, contract); err != nil {
		return nil, err
	}
	byHash := make(map[string][]float32, len(inlineEmbeddings))
	for _, embedding := range inlineEmbeddings {
		hash := strings.TrimSpace(embedding.DocumentHash)
		if hash == "" || len(embedding.Embedding) == 0 {
			return nil, fmt.Errorf("%w: provider result has no document hash or vector", ErrInlineEmbeddingPlanMismatch)
		}
		if _, exists := byHash[hash]; exists {
			return nil, fmt.Errorf("%w: duplicate provider result for document hash", ErrInlineEmbeddingPlanMismatch)
		}
		byHash[hash] = append([]float32(nil), embedding.Embedding...)
	}
	documents, err := loadSearchDocumentsForEmbeddingTx(ctx, tx, LoadSearchDocumentsForEmbeddingInput{TeamID: teamID, OwnerProfileID: ownerID, SearchDocumentIDs: searchDocumentIDs})
	if err != nil {
		return nil, err
	}
	if len(documents) > 256 {
		return nil, fmt.Errorf("%w: more than 256 rendered search documents", ErrInlineEmbeddingPlanMismatch)
	}
	completed := make([]SearchDocumentEmbedding, 0, len(documents))
	for _, document := range documents {
		vector, ok := byHash[document.DocumentHash]
		if !ok {
			return nil, fmt.Errorf("%w: rendered document hash %q was not embedded", ErrInlineEmbeddingPlanMismatch, document.DocumentHash)
		}
		if len(vector) != document.EmbeddingDimensions {
			return nil, fmt.Errorf("%w: vector dimensions do not match rendered document", ErrInlineEmbeddingPlanMismatch)
		}
		completed = append(completed, SearchDocumentEmbedding{
			TeamID: document.TeamID, OwnerProfileID: document.OwnerProfileID,
			SearchDocumentID: document.SearchDocumentID, DocumentHash: document.DocumentHash,
			SourceVersion: document.SourceVersion, ProjectionFormat: document.ProjectionFormat,
			ProjectionGenerationID: document.ProjectionGenerationID, DocumentVersion: document.DocumentVersion,
			EmbeddingContractID: document.EmbeddingContractID, EmbeddingDimensions: document.EmbeddingDimensions,
			Embedding: vector, SpaceID: document.SpaceID, SpaceGeneration: document.SpaceGeneration,
		})
	}
	if err := completeSearchDocumentsWithEmbeddingsInTx(ctx, tx, CompleteSearchDocumentsWithEmbeddingsInput{TeamID: teamID, OwnerProfileID: ownerID, Documents: completed}); err != nil {
		return nil, err
	}
	completedIDs := make(map[string]struct{}, len(completed))
	for _, embedding := range completed {
		completedIDs[embedding.SearchDocumentID] = struct{}{}
	}
	return completedIDs, nil
}
