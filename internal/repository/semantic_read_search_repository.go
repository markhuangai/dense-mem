package repository

import (
	"context"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

func loadTraceSearchDocuments(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	spaceID string,
	relationshipID string,
	fragmentIDs []string,
	limit int,
) ([]TraceSearchDocument, error) {
	query := `
		SELECT search_document_id::text, owner_profile_id::text, source_kind,
		       source_id::text, source_version, document_version,
		       embedding_contract_id::text, embedding_dimensions, search_state,
		       document_hash, created_at, updated_at
		FROM search_documents AS document
		WHERE document.team_id = ?::uuid
		  AND document.space_id = ?::uuid
		  AND document.source_kind = 'relationship'
		  AND document.source_id = ?::uuid
		  AND ` + activeSemanticSpaceGenerationSQL("document") + `
		ORDER BY document.source_kind ASC, document.updated_at DESC, document.search_document_id ASC
		LIMIT ?
	`
	args := []any{teamID, spaceID, relationshipID, limit}
	if len(fragmentIDs) > 0 {
		query = `
			SELECT search_document_id::text, owner_profile_id::text, source_kind,
			       source_id::text, source_version, document_version,
			       embedding_contract_id::text, embedding_dimensions, search_state,
			       document_hash, created_at, updated_at
			FROM search_documents AS document
			WHERE document.team_id = ?::uuid
			  AND document.space_id = ?::uuid
			  AND ` + activeSemanticSpaceGenerationSQL("document") + `
			  AND (
			    (document.source_kind = 'relationship' AND document.source_id = ?::uuid)
			    OR (document.source_kind = 'evidence' AND document.source_id = ANY(?::uuid[]))
			  )
			ORDER BY document.source_kind ASC, document.updated_at DESC, document.search_document_id ASC
			LIMIT ?
		`
		args = []any{teamID, spaceID, relationshipID, pq.Array(fragmentIDs), limit}
	}
	rows, err := tx.WithContext(ctx).Raw(query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TraceSearchDocument
	for rows.Next() {
		var row TraceSearchDocument
		if err := rows.Scan(
			&row.SearchDocumentID, &row.OwnerProfileID, &row.SourceKind,
			&row.SourceID, &row.SourceVersion, &row.DocumentVersion,
			&row.EmbeddingContractID, &row.EmbeddingDimensions, &row.SearchState,
			&row.DocumentHash, &row.CreatedAt, &row.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
