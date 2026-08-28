package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

type relationshipCorrectionEmbeddingsContextKey struct{}

func withRelationshipCorrectionEmbeddings(ctx context.Context, values []RelationshipCorrectionEmbedding) context.Context {
	copyValues := make([]RelationshipCorrectionEmbedding, len(values))
	for index, value := range values {
		copyValues[index] = value
		copyValues[index].Embedding = append([]float32(nil), value.Embedding...)
	}
	return context.WithValue(ctx, relationshipCorrectionEmbeddingsContextKey{}, copyValues)
}

func relationshipCorrectionEmbeddings(ctx context.Context) ([]RelationshipCorrectionEmbedding, bool) {
	values, ok := ctx.Value(relationshipCorrectionEmbeddingsContextKey{}).([]RelationshipCorrectionEmbedding)
	return values, ok
}

func applyRelationshipCorrectionSearchDocuments(ctx context.Context, tx *gorm.DB, row *relationshipCorrectionSubmissionRow, original, successor *RelationshipRecord) error {
	values, provided := relationshipCorrectionEmbeddings(ctx)
	if !provided {
		return ErrSearchEmbeddingRequired
	}
	contract, err := loadActiveSearchContractInTx(ctx, tx)
	if err != nil {
		return err
	}
	byHash := make(map[string][]float32, len(values))
	for _, value := range values {
		if strings.TrimSpace(value.DocumentHash) == "" || len(value.Embedding) == 0 {
			return fmt.Errorf("%w: correction embedding requires hash and vector", ErrSearchEmbeddingRequired)
		}
		if value.EmbeddingContractID != contract.EmbeddingContractID || value.EmbeddingDimensions != contract.EmbeddingDimensions ||
			value.EmbeddingModel != contract.EmbeddingModel || value.SearchIndexGenerationID != contract.SearchIndexGenerationID || value.IndexGeneration != contract.IndexGeneration {
			return ErrSearchContractMismatch
		}
		if _, exists := byHash[value.DocumentHash]; exists {
			return fmt.Errorf("%w: duplicate correction embedding hash", ErrSearchEmbeddingRequired)
		}
		byHash[value.DocumentHash] = append([]float32(nil), value.Embedding...)
	}

	commit := CommitPlacementSemanticInput{TeamID: row.TeamID, OwnerProfileID: row.OwnerProfileID}
	if _, err := markRelationshipSearchDocumentNotRequired(ctx, tx, commit, original); err != nil {
		return err
	}
	if err := staleCorrectionEmbeddingJobs(ctx, tx, row.TeamID, original.RelationshipID, contract.EmbeddingContractID); err != nil {
		return err
	}
	document, documentHash, err := upsertCorrectionRelationshipSearchDocument(ctx, tx, row, successor, contract)
	if err != nil {
		return err
	}
	vector, ok := byHash[documentHash]
	if !ok {
		return fmt.Errorf("%w: successor document was not embedded", ErrSearchEmbeddingRequired)
	}
	if len(vector) != document.EmbeddingDimensions {
		return ErrSearchContractMismatch
	}
	literal, err := vectorLiteral(vector)
	if err != nil {
		return err
	}
	updated := tx.WithContext(ctx).Exec(`
		UPDATE search_documents
		SET embedding = ?::vector, search_state = 'current', embedding_updated_at = now(), embedding_error = '', updated_at = now()
		WHERE team_id = ?::uuid AND search_document_id = ?::uuid AND owner_profile_id = ?::uuid
		  AND source_version = ? AND projection_format_version = ?
		  AND projection_generation_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND document_version = ? AND embedding_contract_id = ?::uuid AND embedding_dimensions = ?
		  AND space_id = ?::uuid AND space_generation = ? AND document_hash = ?
	`, literal, row.TeamID, document.SearchDocumentID, row.OwnerProfileID, document.SourceVersion, document.ProjectionFormat,
		document.ProjectionGenerationID, document.DocumentVersion, contract.EmbeddingContractID, contract.EmbeddingDimensions,
		document.SpaceID, document.SpaceGeneration, documentHash)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrSearchStaleVersion
	}
	if err := staleCorrectionEmbeddingJobs(ctx, tx, row.TeamID, successor.RelationshipID, contract.EmbeddingContractID); err != nil {
		return err
	}
	if document.ProjectionGenerationID != "" {
		if err := refreshRelationshipProjectionGeneration(ctx, tx, row.TeamID, document.ProjectionGenerationID); err != nil {
			return err
		}
	}
	return nil
}

func upsertCorrectionRelationshipSearchDocument(ctx context.Context, tx *gorm.DB, row *relationshipCorrectionSubmissionRow, relationship *RelationshipRecord, contract *ActiveSearchContract) (*SearchDocumentResult, string, error) {
	if !relationshipSearchEligible(relationship) {
		return nil, "", errors.New("correction successor is not search eligible")
	}
	text, err := placementRelationshipSearchText(ctx, tx, relationship)
	if err != nil {
		return nil, "", err
	}
	previousGenerationID, err := relationshipSearchDocumentProjectionGenerationID(ctx, tx, row.TeamID, relationship.RelationshipID, contract.EmbeddingContractID)
	if err != nil {
		return nil, "", err
	}
	metadata, err := relationshipForegroundSearchMetadata(ctx, tx, row.TeamID)
	if err != nil {
		return nil, "", err
	}
	input := normalizeUpsertSearchDocumentInput(UpsertSearchDocumentInput{TeamID: row.TeamID, OwnerProfileID: row.OwnerProfileID, SourceKind: "relationship", SourceID: relationship.RelationshipID, SourceVersion: int64(relationship.Version), ProjectionFormat: 2, DocumentText: text, Metadata: metadata, SpaceID: relationship.SpaceID, SpaceGeneration: relationship.SpaceGeneration})
	if err := validateUpsertSearchDocumentInput(input); err != nil {
		return nil, "", err
	}
	document, err := upsertSearchDocumentRecordInTx(ctx, tx, input, contract)
	if err != nil {
		return nil, "", err
	}
	if err := refreshPreviousRelationshipProjectionGeneration(ctx, tx, row.TeamID, previousGenerationID); err != nil {
		return nil, "", err
	}
	return document, input.DocumentHash, nil
}

func staleCorrectionEmbeddingJobs(ctx context.Context, tx *gorm.DB, teamID, relationshipID, contractID string) error {
	return tx.WithContext(ctx).Exec(`
		UPDATE embedding_jobs SET status = 'stale', error = 'relationship correction completed synchronously', completed_at = COALESCE(completed_at, now()), lease_until = NULL, worker_id = '', updated_at = now()
		WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid AND embedding_contract_id = ?::uuid
		  AND status IN ('queued', 'processing', 'failed')
	`, teamID, relationshipID, contractID).Error
}
