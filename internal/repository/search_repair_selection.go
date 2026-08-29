package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

func searchRepairCursorFromInput(cursor *SearchRepairCursor) searchRepairCursor {
	if cursor == nil {
		return searchRepairCursor{}
	}
	return searchRepairCursor{
		observedAt: cursor.ObservedAt, teamID: cursor.TeamID, sourceKind: cursor.SourceKind,
		sourceID: cursor.SourceID, searchDocumentID: cursor.SearchDocumentID,
	}
}

func searchRepairCursorToInput(cursor searchRepairCursor) *SearchRepairCursor {
	if cursor.observedAt.IsZero() {
		return nil
	}
	return &SearchRepairCursor{
		ObservedAt: cursor.observedAt, TeamID: cursor.teamID, SourceKind: cursor.sourceKind,
		SourceID: cursor.sourceID, SearchDocumentID: cursor.searchDocumentID,
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

func persistSearchRepairSelectionCursor(
	ctx context.Context,
	tx *gorm.DB,
	input SearchRepairSelectionInput,
	cursor *SearchRepairCursor,
) error {
	if strings.TrimSpace(input.RunID) == "" {
		return nil
	}
	var args []any
	var cursorObserved any
	var cursorTeam, cursorKind, cursorSource, cursorDocument string
	if cursor != nil && !cursor.ObservedAt.IsZero() {
		cursorObserved = cursor.ObservedAt
		cursorTeam, cursorKind = cursor.TeamID, cursor.SourceKind
		cursorSource, cursorDocument = cursor.SourceID, cursor.SearchDocumentID
	}
	args = append(args, cursorObserved, cursorTeam, cursorKind, cursorSource, cursorDocument, input.RunID)
	query := `
		UPDATE embedding_reconciliation_runs
		SET selection_cursor_observed_at = ?,
		    selection_cursor_team_id = NULLIF(?, '')::uuid,
		    selection_cursor_source_kind = NULLIF(?, ''),
		    selection_cursor_source_id = NULLIF(?, '')::uuid,
		    selection_cursor_search_document_id = NULLIF(?, '')::uuid,
		    updated_at = clock_timestamp()
		WHERE reconciliation_run_id = ?::uuid
		  AND status = 'running'
		  AND lease_token = ?::uuid
		  AND lease_until > clock_timestamp()
	`
	args = append(args, input.LeaseToken)
	result := tx.WithContext(ctx).Exec(query, args...)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *SearchRepositoryImpl) SelectSearchRepairDocuments(ctx context.Context, input SearchRepairSelectionInput) ([]SearchRepairDocument, bool, error) {
	input = normalizeSearchRepairSelectionInput(input)
	if err := validateSearchRepairSelectionInput(input); err != nil {
		return nil, false, err
	}
	limit := input.Limit
	if limit <= 0 {
		limit = searchRepairCandidateLimit
	}
	if limit > searchRepairCandidateLimit {
		return nil, false, fmt.Errorf("search: repair batch exceeds %d documents", searchRepairCandidateLimit)
	}
	items := make([]SearchRepairDocument, 0, limit)
	hasMore := false
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		contract := &ActiveSearchContract{EmbeddingContractID: input.EmbeddingContractID, EmbeddingDimensions: input.EmbeddingDimensions}
		cursor := searchRepairCursorFromInput(input.Cursor)
		resumeCursor := cursor
		pageSize := limit + 1
		if pageSize > searchRepairCandidateLimit {
			pageSize = searchRepairCandidateLimit
		}
		sourceExhausted := false
		scanned := 0
		for !hasMore && !sourceExhausted && scanned < searchRepairSelectionScanLimit {
			pageLimit := pageSize
			if remaining := searchRepairSelectionScanLimit - scanned; pageLimit > remaining {
				pageLimit = remaining
			}
			candidates, err := selectSearchRepairCandidatePage(ctx, tx, input, cursor, pageLimit)
			if err != nil {
				return err
			}
			if len(candidates) == 0 {
				sourceExhausted = true
				break
			}
			scanned += len(candidates)
			pageExhausted := len(candidates) < pageLimit
			for _, candidate := range candidates {
				cursor = searchRepairCursorFrom(candidate)
				item, include, err := hydrateSearchRepairCandidate(ctx, tx, contract, candidate)
				if err != nil {
					return err
				}
				if !include {
					resumeCursor = cursor
					continue
				}
				if len(items) >= limit {
					hasMore = true
					break
				}
				items = append(items, item)
				resumeCursor = cursor
			}
			if pageExhausted && !hasMore {
				sourceExhausted = true
				break
			}
		}
		if scanned >= searchRepairSelectionScanLimit && !sourceExhausted {
			hasMore = true
		}
		if err := persistSearchRepairSelectionCursor(ctx, tx, input, func() *SearchRepairCursor {
			if sourceExhausted {
				return nil
			}
			return searchRepairCursorToInput(resumeCursor)
		}()); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("search: select repair documents: %w", err)
	}
	return items, hasMore, nil
}

func hydrateSearchRepairCandidate(ctx context.Context, tx *gorm.DB, contract *ActiveSearchContract, candidate SearchRepairDocument) (SearchRepairDocument, bool, error) {
	if candidate.SearchDocumentID == "" {
		expected, known, err := canonicalSearchRepairDocument(ctx, tx, contract, candidate)
		if err != nil {
			return SearchRepairDocument{}, false, err
		}
		returnSearch := expected != nil && known
		if !returnSearch {
			return SearchRepairDocument{}, false, nil
		}
		return *expected, true, nil
	}
	item, vectorCurrent, err := loadSearchRepairDocument(ctx, tx, candidate.TeamID, candidate.SearchDocumentID)
	if errors.Is(err, sql.ErrNoRows) {
		return SearchRepairDocument{}, false, nil
	}
	if err != nil {
		return SearchRepairDocument{}, false, err
	}
	item.ObservedAt = candidate.ObservedAt
	item.Retired = candidate.Retired
	item.StoredDocumentHash = item.DocumentHash
	item.StoredOwnerProfileID = item.OwnerProfileID
	if candidate.OwnerProfileID != "" {
		item.OwnerProfileID = candidate.OwnerProfileID
	}
	if candidate.Retired {
		expected, known, err := canonicalSearchRepairDocument(ctx, tx, contract, item)
		if err != nil {
			return SearchRepairDocument{}, false, err
		}
		return item, known && expected == nil && item.SearchState != "not_required", nil
	}
	expected, known, err := canonicalSearchRepairDocument(ctx, tx, contract, item)
	if err != nil {
		return SearchRepairDocument{}, false, err
	}
	if known {
		ownerCurrent := item.StoredOwnerProfileID
		if ownerCurrent == "" {
			ownerCurrent = item.OwnerProfileID
		}
		if expected == nil || (ownerCurrent == expected.OwnerProfileID && searchRepairDocumentMatches(item, *expected) && vectorCurrent) {
			return SearchRepairDocument{}, false, nil
		}
		expected.SearchDocumentID = item.SearchDocumentID
		expected.DocumentVersion = item.DocumentVersion
		expected.StoredDocumentHash = item.StoredDocumentHash
		return *expected, true, nil
	}
	return item, !vectorCurrent, nil
}

func (r *SearchRepositoryImpl) CountSearchRepairDocuments(ctx context.Context, input SearchRepairSelectionInput) (int64, error) {
	input = normalizeSearchRepairSelectionInput(input)
	if err := validateSearchRepairSelectionInput(input); err != nil {
		return 0, err
	}
	var count int64
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Exec("SET LOCAL statement_timeout = '2s'").Error; err != nil {
			return err
		}
		return tx.WithContext(ctx).Raw(
			searchRepairDriftCountSQL, input.EmbeddingContractID, input.EmbeddingDimensions,
		).Scan(&count).Error
	})
	if err != nil {
		return 0, fmt.Errorf("search: count repair documents: %w", err)
	}
	return count, nil
}
