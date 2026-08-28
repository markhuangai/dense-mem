package repository

import (
	"context"
	"time"

	"gorm.io/gorm"
)

type searchRepairCursor struct {
	observedAt       time.Time
	teamID           string
	sourceKind       string
	sourceID         string
	searchDocumentID string
}

func searchRepairCursorFrom(document SearchRepairDocument) searchRepairCursor {
	return searchRepairCursor{
		observedAt: document.ObservedAt, teamID: document.TeamID, sourceKind: document.SourceKind,
		sourceID: document.SourceID, searchDocumentID: document.SearchDocumentID,
	}
}

func selectSearchRepairCandidatePage(
	ctx context.Context,
	tx *gorm.DB,
	input SearchRepairSelectionInput,
	cursor searchRepairCursor,
	limit int,
) ([]SearchRepairDocument, error) {
	query := searchRepairCandidateSQL
	args := []any{input.EmbeddingContractID, input.EmbeddingDimensions}
	if !cursor.observedAt.IsZero() {
		query += `
WHERE (observed_at, team_id, source_kind, source_id, search_document_id) > (?, ?, ?, ?, ?)`
		args = append(args, cursor.observedAt, cursor.teamID, cursor.sourceKind, cursor.sourceID, cursor.searchDocumentID)
	}
	query += `
ORDER BY observed_at, team_id, source_kind, source_id, search_document_id
LIMIT ?`
	args = append(args, limit)
	rows, err := tx.WithContext(ctx).Raw(query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]SearchRepairDocument, 0, limit)
	for rows.Next() {
		var candidate SearchRepairDocument
		if err := rows.Scan(
			&candidate.TeamID, &candidate.SearchDocumentID, &candidate.OwnerProfileID,
			&candidate.SourceKind, &candidate.SourceID, &candidate.SourceVersion,
			&candidate.ProjectionFormat, &candidate.ProjectionGenerationID,
			&candidate.DocumentVersion, &candidate.EmbeddingContractID,
			&candidate.EmbeddingDimensions, &candidate.SpaceID, &candidate.SpaceGeneration,
			&candidate.Retired, &candidate.ObservedAt,
		); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}
