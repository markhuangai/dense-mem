package repository

import (
	"context"
	"database/sql"

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
		FROM search_documents
		WHERE team_id = ?::uuid
		  AND space_id = ?::uuid
		  AND source_kind = 'relationship'
		  AND source_id = ?::uuid
		ORDER BY source_kind ASC, updated_at DESC, search_document_id ASC
		LIMIT ?
	`
	args := []any{teamID, spaceID, relationshipID, limit}
	if len(fragmentIDs) > 0 {
		query = `
			SELECT search_document_id::text, owner_profile_id::text, source_kind,
			       source_id::text, source_version, document_version,
			       embedding_contract_id::text, embedding_dimensions, search_state,
			       document_hash, created_at, updated_at
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND space_id = ?::uuid
			  AND (
			    (source_kind = 'relationship' AND source_id = ?::uuid)
			    OR (source_kind = 'evidence' AND source_id = ANY(?::uuid[]))
			  )
			ORDER BY source_kind ASC, updated_at DESC, search_document_id ASC
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

func loadTraceEmbeddingJobs(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	spaceID string,
	searchDocumentIDs []string,
	limit int,
) ([]TraceEmbeddingJob, error) {
	if len(searchDocumentIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT embedding_job_id::text, search_document_id::text, owner_profile_id::text,
		       source_kind, source_id::text, source_version, document_version,
		       embedding_contract_id::text, embedding_dimensions, status, attempts,
		       error, created_at, updated_at, completed_at
		FROM embedding_jobs
		WHERE team_id = ?::uuid
		  AND space_id = ?::uuid
		  AND search_document_id = ANY(?::uuid[])
		ORDER BY created_at ASC, embedding_job_id ASC
		LIMIT ?
	`, teamID, spaceID, pq.Array(searchDocumentIDs), limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TraceEmbeddingJob
	for rows.Next() {
		var row TraceEmbeddingJob
		var completedAt sql.NullTime
		if err := rows.Scan(
			&row.EmbeddingJobID, &row.SearchDocumentID, &row.OwnerProfileID,
			&row.SourceKind, &row.SourceID, &row.SourceVersion, &row.DocumentVersion,
			&row.EmbeddingContractID, &row.EmbeddingDimensions, &row.Status,
			&row.Attempts, &row.Error, &row.CreatedAt, &row.UpdatedAt, &completedAt,
		); err != nil {
			return nil, err
		}
		row.CompletedAt = timePtr(completedAt)
		out = append(out, row)
	}
	return out, rows.Err()
}
