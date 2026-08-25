package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

func (r *SearchRepositoryImpl) LoadSearchDocumentsForEmbedding(
	ctx context.Context,
	input LoadSearchDocumentsForEmbeddingInput,
) ([]SearchDocumentForEmbedding, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("search: team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return nil, fmt.Errorf("search: owner_profile_id is required: %w", err)
	}
	if len(input.SearchDocumentIDs) == 0 {
		return []SearchDocumentForEmbedding{}, nil
	}
	ids := make([]string, 0, len(input.SearchDocumentIDs))
	seen := make(map[string]struct{}, len(input.SearchDocumentIDs))
	for _, raw := range input.SearchDocumentIDs {
		id := strings.TrimSpace(raw)
		if _, err := uuid.Parse(id); err != nil {
			return nil, fmt.Errorf("search: search_document_id is invalid: %w", err)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) > 256 {
		return nil, errors.New("search: inline embedding document limit exceeded")
	}
	var result []SearchDocumentForEmbedding
	err := r.withActiveTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT document.team_id::text, document.search_document_id::text,
			       document.owner_profile_id::text, document.source_kind,
			       document.source_id::text, document.source_version,
			       document.projection_format_version,
			       COALESCE(document.projection_generation_id::text, ''),
			       document.document_version, document.embedding_contract_id::text,
			       document.embedding_dimensions, document.search_state,
			       document.space_id::text, document.space_generation,
			       document.document_text, document.document_hash
			FROM search_documents AS document
			WHERE document.team_id = ?::uuid
			  AND document.owner_profile_id = ?::uuid
			  AND document.search_document_id = ANY(?::uuid[])
			ORDER BY array_position(?::uuid[], document.search_document_id)
		`, input.TeamID, input.OwnerProfileID, pq.Array(ids), pq.Array(ids)).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item SearchDocumentForEmbedding
			if err := rows.Scan(
				&item.TeamID, &item.SearchDocumentID, &item.OwnerProfileID,
				&item.SourceKind, &item.SourceID, &item.SourceVersion,
				&item.ProjectionFormat, &item.ProjectionGenerationID,
				&item.DocumentVersion, &item.EmbeddingContractID,
				&item.EmbeddingDimensions, &item.SearchState, &item.SpaceID,
				&item.SpaceGeneration, &item.DocumentText, &item.DocumentHash,
			); err != nil {
				return err
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("search: load inline embedding documents: %w", err)
	}
	if len(result) != len(ids) {
		return nil, fmt.Errorf("search: inline embedding document set changed")
	}
	return result, nil
}

// loadSearchDocumentsForEmbeddingTx is the transaction-local form used by a
// synchronous semantic commit. It intentionally reads through the caller's
// transaction so provider failure can abort the same authoritative write.
func loadSearchDocumentsForEmbeddingTx(
	ctx context.Context,
	tx *gorm.DB,
	input LoadSearchDocumentsForEmbeddingInput,
) ([]SearchDocumentForEmbedding, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("search: team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return nil, fmt.Errorf("search: owner_profile_id is required: %w", err)
	}
	if len(input.SearchDocumentIDs) == 0 {
		return []SearchDocumentForEmbedding{}, nil
	}
	ids := make([]string, 0, len(input.SearchDocumentIDs))
	seen := make(map[string]struct{}, len(input.SearchDocumentIDs))
	for _, raw := range input.SearchDocumentIDs {
		id := strings.TrimSpace(raw)
		if _, err := uuid.Parse(id); err != nil {
			return nil, fmt.Errorf("search: search_document_id is invalid: %w", err)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) > 256 {
		return nil, errors.New("search: inline embedding document limit exceeded")
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT document.team_id::text, document.search_document_id::text,
		       document.owner_profile_id::text, document.source_kind,
		       document.source_id::text, document.source_version,
		       document.projection_format_version,
		       COALESCE(document.projection_generation_id::text, ''),
		       document.document_version, document.embedding_contract_id::text,
		       document.embedding_dimensions, document.search_state,
		       document.space_id::text, document.space_generation,
		       document.document_text, document.document_hash
		FROM search_documents AS document
		WHERE document.team_id = ?::uuid
		  AND document.owner_profile_id = ?::uuid
		  AND document.search_document_id = ANY(?::uuid[])
		ORDER BY array_position(?::uuid[], document.search_document_id)
	`, input.TeamID, input.OwnerProfileID, pq.Array(ids), pq.Array(ids)).Rows()
	if err != nil {
		return nil, fmt.Errorf("search: load inline embedding documents: %w", err)
	}
	defer rows.Close()
	result := make([]SearchDocumentForEmbedding, 0, len(ids))
	for rows.Next() {
		var item SearchDocumentForEmbedding
		if err := rows.Scan(
			&item.TeamID, &item.SearchDocumentID, &item.OwnerProfileID,
			&item.SourceKind, &item.SourceID, &item.SourceVersion,
			&item.ProjectionFormat, &item.ProjectionGenerationID,
			&item.DocumentVersion, &item.EmbeddingContractID,
			&item.EmbeddingDimensions, &item.SearchState, &item.SpaceID,
			&item.SpaceGeneration, &item.DocumentText, &item.DocumentHash,
		); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(ids) {
		return nil, fmt.Errorf("search: inline embedding document set changed")
	}
	return result, nil
}

func loadSearchDocumentsForSourcesTx(
	ctx context.Context,
	tx *gorm.DB,
	input LoadSearchDocumentsForSourcesInput,
) ([]SearchDocumentForEmbedding, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("search: team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return nil, fmt.Errorf("search: owner_profile_id is required: %w", err)
	}
	if input.SourceKind == "" || len(input.SourceIDs) == 0 {
		return []SearchDocumentForEmbedding{}, nil
	}
	ids := make([]string, 0, len(input.SourceIDs))
	seen := make(map[string]struct{}, len(input.SourceIDs))
	for _, raw := range input.SourceIDs {
		id := strings.TrimSpace(raw)
		if _, err := uuid.Parse(id); err != nil {
			return nil, fmt.Errorf("search: source_id is invalid: %w", err)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT document.search_document_id::text
		FROM search_documents AS document
		WHERE document.team_id = ?::uuid
		  AND document.owner_profile_id = ?::uuid
		  AND document.source_kind = ?
		  AND document.source_id = ANY(?::uuid[])
		ORDER BY array_position(?::uuid[], document.source_id), document.updated_at DESC, document.search_document_id
	`, input.TeamID, input.OwnerProfileID, input.SourceKind, pq.Array(ids), pq.Array(ids)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	documentIDs := make([]string, 0, len(ids))
	seenDocuments := make(map[string]struct{})
	for rows.Next() {
		var documentID string
		if err := rows.Scan(&documentID); err != nil {
			return nil, err
		}
		if _, ok := seenDocuments[documentID]; ok {
			continue
		}
		seenDocuments[documentID] = struct{}{}
		documentIDs = append(documentIDs, documentID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return loadSearchDocumentsForEmbeddingTx(ctx, tx, LoadSearchDocumentsForEmbeddingInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, SearchDocumentIDs: documentIDs,
	})
}

func (r *SearchRepositoryImpl) LoadSearchDocumentsForSources(
	ctx context.Context,
	input LoadSearchDocumentsForSourcesInput,
) ([]SearchDocumentForEmbedding, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("search: team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return nil, fmt.Errorf("search: owner_profile_id is required: %w", err)
	}
	if input.SourceKind == "" || len(input.SourceIDs) == 0 {
		return []SearchDocumentForEmbedding{}, nil
	}
	ids := make([]string, 0, len(input.SourceIDs))
	seen := make(map[string]struct{}, len(input.SourceIDs))
	for _, raw := range input.SourceIDs {
		id := strings.TrimSpace(raw)
		if _, err := uuid.Parse(id); err != nil {
			return nil, fmt.Errorf("search: source_id is invalid: %w", err)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) > 256 {
		return nil, errors.New("search: inline embedding document limit exceeded")
	}
	var result []SearchDocumentForEmbedding
	err := r.withActiveTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT document.team_id::text, document.search_document_id::text,
			       document.owner_profile_id::text, document.source_kind,
			       document.source_id::text, document.source_version,
			       document.projection_format_version,
			       COALESCE(document.projection_generation_id::text, ''),
			       document.document_version, document.embedding_contract_id::text,
			       document.embedding_dimensions, document.search_state,
			       document.space_id::text, document.space_generation,
			       document.document_text, document.document_hash
			FROM search_documents AS document
			WHERE document.team_id = ?::uuid
			  AND document.owner_profile_id = ?::uuid
			  AND document.source_kind = ?
			  AND document.source_id = ANY(?::uuid[])
			ORDER BY array_position(?::uuid[], document.source_id), document.updated_at DESC
			`, input.TeamID, input.OwnerProfileID, input.SourceKind, pq.Array(ids), pq.Array(ids)).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item SearchDocumentForEmbedding
			if err := rows.Scan(
				&item.TeamID, &item.SearchDocumentID, &item.OwnerProfileID,
				&item.SourceKind, &item.SourceID, &item.SourceVersion,
				&item.ProjectionFormat, &item.ProjectionGenerationID,
				&item.DocumentVersion, &item.EmbeddingContractID,
				&item.EmbeddingDimensions, &item.SearchState, &item.SpaceID,
				&item.SpaceGeneration, &item.DocumentText, &item.DocumentHash,
			); err != nil {
				return err
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("search: load source embedding documents: %w", err)
	}
	return result, nil
}

func (r *SearchRepositoryImpl) CompleteSearchDocumentsWithEmbeddings(
	ctx context.Context,
	input CompleteSearchDocumentsWithEmbeddingsInput,
) error {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("search: team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("search: owner_profile_id is required: %w", err)
	}
	if len(input.Documents) == 0 {
		return nil
	}
	if len(input.Documents) > 256 {
		return errors.New("search: inline embedding document limit exceeded")
	}
	err := r.withActiveTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		for _, document := range input.Documents {
			if _, err := uuid.Parse(strings.TrimSpace(document.SearchDocumentID)); err != nil {
				return fmt.Errorf("search: search_document_id is invalid: %w", err)
			}
			if document.SourceVersion < 1 || document.DocumentVersion < 1 || document.EmbeddingDimensions < 1 || len(document.Embedding) != document.EmbeddingDimensions {
				return fmt.Errorf("search: inline embedding version or dimension mismatch")
			}
			vector, err := vectorLiteral(document.Embedding)
			if err != nil {
				return fmt.Errorf("search: validate inline embedding: %w", err)
			}
			result := tx.WithContext(ctx).Exec(`
				UPDATE search_documents
				SET embedding = ?::vector,
				    search_state = 'current',
				    embedding_updated_at = now(),
				    embedding_error = '',
				    updated_at = now()
				WHERE team_id = ?::uuid
				  AND search_document_id = ?::uuid
				  AND owner_profile_id = ?::uuid
				  AND source_version = ?
				  AND projection_format_version = ?
				  AND projection_generation_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
				  AND document_version = ?
				  AND embedding_contract_id = ?::uuid
				  AND embedding_dimensions = ?
				  AND space_id = COALESCE(NULLIF(?, '')::uuid, space_id)
				  AND space_generation = ?
				  AND search_state IN ('pending', 'failed')
			`, vector, input.TeamID, document.SearchDocumentID, input.OwnerProfileID,
				document.SourceVersion, document.ProjectionFormat, document.ProjectionGenerationID,
				document.DocumentVersion, document.EmbeddingContractID, document.EmbeddingDimensions,
				document.SpaceID, document.SpaceGeneration)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				// A matching current row is an idempotent completion. Any other
				// mismatch is a fence failure and must not be silently overwritten.
				var state string
				err := tx.WithContext(ctx).Raw(`
					SELECT search_state FROM search_documents
					WHERE team_id = ?::uuid AND search_document_id = ?::uuid
				`, input.TeamID, document.SearchDocumentID).Row().Scan(&state)
				if err == nil && state == "current" {
					continue
				}
				if errors.Is(err, sql.ErrNoRows) {
					return ErrSearchStaleVersion
				}
				if err != nil {
					return err
				}
				return ErrSearchStaleVersion
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("search: complete inline embeddings: %w", err)
	}
	return nil
}

// completeSearchDocumentsWithEmbeddingsInTx applies a validated provider
// batch without opening a second transaction. Any fence mismatch returns an
// error so the caller's semantic transaction rolls back in full.
func completeSearchDocumentsWithEmbeddingsInTx(
	ctx context.Context,
	tx *gorm.DB,
	input CompleteSearchDocumentsWithEmbeddingsInput,
) error {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("search: team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("search: owner_profile_id is required: %w", err)
	}
	if len(input.Documents) == 0 {
		return nil
	}
	if len(input.Documents) > 256 {
		return errors.New("search: inline embedding document limit exceeded")
	}
	for _, document := range input.Documents {
		if _, err := uuid.Parse(strings.TrimSpace(document.SearchDocumentID)); err != nil {
			return fmt.Errorf("search: search_document_id is invalid: %w", err)
		}
		if document.SourceVersion < 1 || document.DocumentVersion < 1 || document.EmbeddingDimensions < 1 || len(document.Embedding) != document.EmbeddingDimensions {
			return fmt.Errorf("search: inline embedding version or dimension mismatch")
		}
		vector, err := vectorLiteral(document.Embedding)
		if err != nil {
			return fmt.Errorf("search: validate inline embedding: %w", err)
		}
		result := tx.WithContext(ctx).Exec(`
			UPDATE search_documents
			SET embedding = ?::vector,
			    search_state = 'current',
			    embedding_updated_at = now(),
			    embedding_error = '',
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND search_document_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND source_version = ?
			  AND projection_format_version = ?
			  AND projection_generation_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
			  AND document_version = ?
			  AND embedding_contract_id = ?::uuid
			  AND embedding_dimensions = ?
			  AND space_id = COALESCE(NULLIF(?, '')::uuid, space_id)
			  AND space_generation = ?
			  AND search_state IN ('pending', 'failed')
		`, vector, input.TeamID, document.SearchDocumentID, input.OwnerProfileID,
			document.SourceVersion, document.ProjectionFormat, document.ProjectionGenerationID,
			document.DocumentVersion, document.EmbeddingContractID, document.EmbeddingDimensions,
			document.SpaceID, document.SpaceGeneration)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			var state string
			err := tx.WithContext(ctx).Raw(`
				SELECT search_state FROM search_documents
				WHERE team_id = ?::uuid AND search_document_id = ?::uuid
			`, input.TeamID, document.SearchDocumentID).Row().Scan(&state)
			if err == nil && state == "current" {
				continue
			}
			if errors.Is(err, sql.ErrNoRows) {
				return ErrSearchStaleVersion
			}
			if err != nil {
				return err
			}
			return ErrSearchStaleVersion
		}
	}
	return nil
}
