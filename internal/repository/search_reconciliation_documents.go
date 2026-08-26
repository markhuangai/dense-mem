package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// canonicalSearchDocument renders the current canonical source for one stored
// search document. The boolean reports whether the source kind is governed by
// the canonical semantic projection; a known source that no longer exists or
// is no longer searchable returns a nil expected document.
func canonicalSearchDocument(ctx context.Context, tx *gorm.DB, document SearchDocumentForEmbedding) (*SearchDocumentForEmbedding, bool, error) {
	switch document.SourceKind {
	case "evidence":
		var content, spaceID string
		var spaceGeneration int64
		err := tx.WithContext(ctx).Raw(`
			SELECT fragment.content, fragment.space_id::text, COALESCE(fragment.space_generation, 0)
			FROM evidence_fragments AS fragment
			WHERE fragment.team_id = ?::uuid
			  AND fragment.owner_profile_id = ?::uuid
			  AND fragment.fragment_id = ?::uuid
			  AND NOT EXISTS (
			      SELECT 1 FROM evidence_quarantines AS quarantine
			      WHERE quarantine.team_id = fragment.team_id
			        AND quarantine.fragment_id = fragment.fragment_id
			        AND quarantine.status = 'active'
			  )
		`, document.TeamID, document.OwnerProfileID, document.SourceID).Row().Scan(&content, &spaceID, &spaceGeneration)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, true, nil
		}
		if err != nil {
			return nil, true, err
		}
		expected := document
		expected.SourceVersion = 1
		expected.ProjectionFormat = defaultProjectionFormat("evidence")
		expected.ProjectionGenerationID = ""
		expected.DocumentText = strings.TrimSpace(content)
		expected.DocumentHash = searchDocumentHash(expected.DocumentText)
		expected.SpaceID = strings.TrimSpace(spaceID)
		expected.SpaceGeneration = spaceGeneration
		return &expected, true, nil
	case "relationship":
		relationship, err := loadRelationshipRecord(ctx, tx, document.TeamID, document.SourceID)
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, true, nil
		}
		if err != nil {
			return nil, true, err
		}
		if !relationshipSearchEligible(relationship) {
			return nil, true, nil
		}
		text, err := semanticRelationshipSearchText(ctx, tx, relationship)
		if err != nil {
			return nil, true, err
		}
		generationID, err := relationshipForegroundRecallGenerationID(ctx, tx, document.TeamID)
		if err != nil {
			return nil, true, err
		}
		expected := document
		expected.SourceVersion = int64(relationship.Version)
		expected.ProjectionFormat = 2
		expected.ProjectionGenerationID = generationID
		expected.DocumentText = strings.TrimSpace(text)
		expected.DocumentHash = searchDocumentHash(expected.DocumentText)
		expected.SpaceID = relationship.SpaceID
		expected.SpaceGeneration = relationship.SpaceGeneration
		return &expected, true, nil
	default:
		return nil, false, nil
	}
}

func searchDocumentMatchesCanonical(document, expected SearchDocumentForEmbedding) bool {
	return document.SourceVersion == expected.SourceVersion &&
		document.ProjectionFormat == expected.ProjectionFormat &&
		strings.TrimSpace(document.ProjectionGenerationID) == strings.TrimSpace(expected.ProjectionGenerationID) &&
		document.DocumentText == expected.DocumentText &&
		document.DocumentHash == expected.DocumentHash &&
		strings.TrimSpace(document.SpaceID) == strings.TrimSpace(expected.SpaceID) &&
		document.SpaceGeneration == expected.SpaceGeneration
}

func selectMissingCanonicalSearchDocuments(
	ctx context.Context,
	tx *gorm.DB,
	contract *ActiveSearchContract,
	limit int,
	result *[]SearchDocumentForEmbedding,
) error {
	if limit <= 0 {
		return nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT fragment.team_id::text, fragment.owner_profile_id::text,
		       fragment.fragment_id::text, fragment.content,
		       COALESCE(fragment.space_id::text, ''), COALESCE(fragment.space_generation, 0)
		FROM evidence_fragments AS fragment
		JOIN teams AS team
		  ON team.id = fragment.team_id
		 AND team.status = 'active'
		 AND team.deleted_at IS NULL
		WHERE NOT EXISTS (
		          SELECT 1
		          FROM evidence_quarantines AS quarantine
		          WHERE quarantine.team_id = fragment.team_id
		            AND quarantine.fragment_id = fragment.fragment_id
		            AND quarantine.status = 'active'
		      )
		  AND NOT EXISTS (
		          SELECT 1
		          FROM search_documents AS document
		          WHERE document.team_id = fragment.team_id
		            AND document.source_kind = 'evidence'
		            AND document.source_id = fragment.fragment_id
		            AND document.embedding_contract_id = ?::uuid
		      )
		ORDER BY fragment.created_at, fragment.team_id, fragment.fragment_id
		LIMIT ?
	`, contract.EmbeddingContractID, limit).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item SearchDocumentForEmbedding
		if err := rows.Scan(
			&item.TeamID, &item.OwnerProfileID, &item.SourceID, &item.DocumentText,
			&item.SpaceID, &item.SpaceGeneration,
		); err != nil {
			return err
		}
		item.SourceKind = "evidence"
		item.SourceVersion = 1
		item.ProjectionFormat = defaultProjectionFormat("evidence")
		item.EmbeddingContractID = contract.EmbeddingContractID
		item.EmbeddingDimensions = contract.EmbeddingDimensions
		item.DocumentVersion = 1
		item.DocumentText = strings.TrimSpace(item.DocumentText)
		item.DocumentHash = searchDocumentHash(item.DocumentText)
		item.StoredDocumentHash = ""
		*result = append(*result, item)
		if len(*result) >= limit {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	remaining := limit - len(*result)
	if remaining <= 0 {
		return nil
	}
	rows, err = tx.WithContext(ctx).Raw(`
		SELECT relationship.team_id::text, relationship.owner_profile_id::text,
		       relationship.relationship_id::text
		FROM relationship_records AS relationship
		JOIN teams AS team
		  ON team.id = relationship.team_id
		 AND team.status = 'active'
		 AND team.deleted_at IS NULL
		WHERE relationship.status = 'active'
		  AND relationship.support_count > 0
		  AND relationship.identity_alias_of_relationship_id IS NULL
		  AND NOT EXISTS (
		          SELECT 1
		          FROM search_documents AS document
		          WHERE document.team_id = relationship.team_id
		            AND document.source_kind = 'relationship'
		            AND document.source_id = relationship.relationship_id
		            AND document.embedding_contract_id = ?::uuid
		      )
		ORDER BY relationship.created_at, relationship.team_id, relationship.relationship_id
		LIMIT ?
	`, contract.EmbeddingContractID, remaining).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item SearchDocumentForEmbedding
		if err := rows.Scan(&item.TeamID, &item.OwnerProfileID, &item.SourceID); err != nil {
			return err
		}
		item.SourceKind = "relationship"
		item.SourceVersion = 1
		item.ProjectionFormat = 2
		item.EmbeddingContractID = contract.EmbeddingContractID
		item.EmbeddingDimensions = contract.EmbeddingDimensions
		item.DocumentVersion = 1
		expected, known, err := canonicalSearchDocument(ctx, tx, item)
		if err != nil {
			return err
		}
		if !known || expected == nil {
			continue
		}
		expected.StoredDocumentHash = ""
		*result = append(*result, *expected)
		if len(*result) >= limit {
			return nil
		}
	}
	return rows.Err()
}

func markSearchDocumentNotRequired(ctx context.Context, tx *gorm.DB, document SearchDocumentForEmbedding) (bool, error) {
	result := tx.WithContext(ctx).Exec(`
		UPDATE search_documents
		SET search_state = 'not_required',
		    embedding = NULL,
		    embedding_updated_at = NULL,
		    embedding_error = '',
		    updated_at = clock_timestamp()
		WHERE team_id = ?::uuid
		  AND search_document_id = ?::uuid
		  AND source_version = ?
		  AND projection_format_version = ?
		  AND projection_generation_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND document_version = ?
		  AND document_hash = ?
		  AND space_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND space_generation IS NOT DISTINCT FROM NULLIF(?, 0)::bigint
		  AND search_state <> 'not_required'
	`, document.TeamID, document.SearchDocumentID, document.SourceVersion, document.ProjectionFormat,
		document.ProjectionGenerationID, document.DocumentVersion, document.DocumentHash,
		document.SpaceID, document.SpaceGeneration)
	return result.RowsAffected == 1, result.Error
}

func completeMissingCanonicalSearchDocument(
	ctx context.Context,
	tx *gorm.DB,
	contract *ActiveSearchContract,
	document SearchDocumentEmbedding,
) (bool, error) {
	if strings.TrimSpace(document.SourceKind) == "" {
		return false, errors.New("search: missing reconciliation source kind")
	}
	if _, err := uuid.Parse(strings.TrimSpace(document.SourceID)); err != nil {
		return false, fmt.Errorf("search: missing reconciliation source id is invalid: %w", err)
	}
	placeholder := SearchDocumentForEmbedding{
		SearchDocumentResult: SearchDocumentResult{
			TeamID:                 document.TeamID,
			OwnerProfileID:         document.OwnerProfileID,
			SourceKind:             document.SourceKind,
			SourceID:               document.SourceID,
			SourceVersion:          document.SourceVersion,
			ProjectionFormat:       document.ProjectionFormat,
			ProjectionGenerationID: document.ProjectionGenerationID,
			DocumentVersion:        document.DocumentVersion,
			EmbeddingContractID:    contract.EmbeddingContractID,
			EmbeddingDimensions:    contract.EmbeddingDimensions,
			SpaceID:                document.SpaceID,
			SpaceGeneration:        document.SpaceGeneration,
		},
		DocumentText: document.DocumentText,
		DocumentHash: document.DocumentHash,
	}
	expected, known, err := canonicalSearchDocument(ctx, tx, placeholder)
	if err != nil {
		return false, err
	}
	if !known || expected == nil || expected.DocumentHash != document.DocumentHash ||
		expected.SourceVersion != document.SourceVersion ||
		expected.ProjectionFormat != document.ProjectionFormat ||
		strings.TrimSpace(expected.ProjectionGenerationID) != strings.TrimSpace(document.ProjectionGenerationID) ||
		expected.SpaceGeneration != document.SpaceGeneration ||
		strings.TrimSpace(expected.SpaceID) != strings.TrimSpace(document.SpaceID) {
		return false, nil
	}
	loaded, err := upsertSearchDocumentInTx(
		WithInlineEmbeddingResults(ctx, []InlineEmbeddingResult{}),
		tx,
		UpsertSearchDocumentInput{
			TeamID:                 document.TeamID,
			OwnerProfileID:         document.OwnerProfileID,
			SourceKind:             document.SourceKind,
			SourceID:               document.SourceID,
			SourceVersion:          document.SourceVersion,
			ProjectionFormat:       document.ProjectionFormat,
			ProjectionGenerationID: document.ProjectionGenerationID,
			DocumentText:           document.DocumentText,
			DocumentHash:           document.DocumentHash,
			EmbeddingContractID:    contract.EmbeddingContractID,
			SpaceID:                document.SpaceID,
			SpaceGeneration:        document.SpaceGeneration,
		},
		contract,
	)
	if err != nil {
		return false, err
	}
	if loaded == nil {
		// A canonical writer won the source/space fence after the snapshot was
		// selected. The stale reconciliation item is skipped on this pass.
		return false, nil
	}
	vector, err := vectorLiteral(document.Embedding)
	if err != nil {
		return false, fmt.Errorf("search: validate missing reconciliation vector: %w", err)
	}
	updated := tx.WithContext(ctx).Exec(`
		UPDATE search_documents
		SET embedding = ?::vector,
		    search_state = 'current',
		    embedding_updated_at = clock_timestamp(),
		    embedding_error = '',
		    updated_at = clock_timestamp()
		WHERE team_id = ?::uuid
		  AND search_document_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND source_version = ?
		  AND projection_format_version = ?
		  AND projection_generation_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND document_version = ?
		  AND document_hash = ?
		  AND embedding_contract_id = ?::uuid
		  AND embedding_dimensions = ?
		  AND space_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND space_generation IS NOT DISTINCT FROM NULLIF(?, 0)::bigint
		  AND search_state <> 'not_required'
		  AND (
		      search_state <> 'current'
		      OR embedding IS NULL
		      OR vector_dims(embedding) <> embedding_dimensions
		  )
	`, vector, loaded.TeamID, loaded.SearchDocumentID, loaded.OwnerProfileID,
		loaded.SourceVersion, loaded.ProjectionFormat, loaded.ProjectionGenerationID,
		loaded.DocumentVersion, document.DocumentHash, loaded.EmbeddingContractID,
		loaded.EmbeddingDimensions, loaded.SpaceID, loaded.SpaceGeneration)
	if updated.Error != nil {
		return false, updated.Error
	}
	if updated.RowsAffected == 1 {
		return true, nil
	}
	if loaded.SearchState == string(domain.SearchProjectionCurrent) {
		return false, nil
	}
	return false, ErrSearchStaleVersion
}
