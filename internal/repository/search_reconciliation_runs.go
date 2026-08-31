package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	searchReconciliationBatchLimit = 256
	searchReconciliationStaleAfter = 15 * time.Minute
)

// ReserveSearchReconciliationRun records ownership of one document repair
// pass without holding a database transaction across provider work. A recent
// running row prevents concurrent instances from embedding the same snapshot;
// an old running row is terminalized as an expired maintenance run.
func (r *SearchRepositoryImpl) ReserveSearchReconciliationRun(
	ctx context.Context,
	input SearchReconciliationRunInput,
) (*SearchReconciliationRun, bool, error) {
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
		return nil, false, fmt.Errorf("search: reconciliation contract is invalid: %w", err)
	}
	if input.EmbeddingDimensions < 1 {
		return nil, false, errors.New("search: reconciliation dimensions must be positive")
	}
	now := input.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	staleAfter := input.StaleAfter
	if staleAfter <= 0 {
		staleAfter = searchReconciliationStaleAfter
	}
	if staleAfter > 24*time.Hour {
		return nil, false, errors.New("search: reconciliation stale window exceeds one day")
	}

	var run *SearchReconciliationRun
	claimed := false
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		var lockAcquired bool
		if err := tx.WithContext(ctx).Raw(`SELECT pg_try_advisory_xact_lock(hashtext(?))`, "dense-mem.search-reconciliation").Scan(&lockAcquired).Error; err != nil {
			return err
		}
		if !lockAcquired {
			return nil
		}
		if err := tx.WithContext(ctx).Exec(`
			UPDATE search_reconciliation_runs
			SET status = 'failed',
			    last_error = 'previous reconciliation run expired',
			    completed_at = COALESCE(completed_at, clock_timestamp()),
			    updated_at = clock_timestamp()
			WHERE status = 'running'
			  AND started_at IS NOT NULL
			  AND started_at < ?
		`, now.Add(-staleAfter)).Error; err != nil {
			return err
		}
		var runningID string
		err := tx.WithContext(ctx).Raw(`
			SELECT reconciliation_run_id::text
			FROM search_reconciliation_runs
			WHERE status = 'running'
			ORDER BY started_at DESC NULLS LAST, reconciliation_run_id DESC
			LIMIT 1
		`).Row().Scan(&runningID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var value SearchReconciliationRun
		var localDate time.Time
		var startedAt, completedAt sql.NullTime
		err = tx.WithContext(ctx).Raw(`
			INSERT INTO search_reconciliation_runs (
				embedding_contract_id, embedding_dimensions, local_run_date,
				status, selected_count, embedded_count, updated_count,
				drifted_count, last_error, started_at, created_at, updated_at
			) VALUES (?, ?, ?, 'running', 0, 0, 0, 0, '', ?, ?, ?)
			RETURNING reconciliation_run_id::text, local_run_date, status,
			           selected_count, embedded_count, updated_count,
			           drifted_count, last_error, started_at, completed_at, updated_at
		`, input.EmbeddingContractID, input.EmbeddingDimensions, now.Format("2006-01-02"), now, now, now).Row().Scan(
			&value.RunID, &localDate, &value.Status,
			&value.SelectedCount, &value.EmbeddedCount, &value.UpdatedCount,
			&value.DriftedCount, &value.LastError, &startedAt, &completedAt, &value.UpdatedAt,
		)
		if err != nil {
			return err
		}
		value.LocalRunDate = localDate
		if startedAt.Valid {
			value.StartedAt = searchTimePointer(startedAt.Time)
		}
		if completedAt.Valid {
			value.CompletedAt = searchTimePointer(completedAt.Time)
		}
		run = &value
		claimed = true
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("search: reserve reconciliation run: %w", err)
	}
	return run, claimed, nil
}

// SelectSearchReconciliationDocuments takes a bounded, system-scoped drift
// snapshot. The returned versions and hash form the fence used after the
// provider call.
func (r *SearchRepositoryImpl) SelectSearchReconciliationDocuments(
	ctx context.Context,
	input SearchReconciliationSelectionInput,
) ([]SearchDocumentForEmbedding, error) {
	input.RunID = strings.TrimSpace(input.RunID)
	if input.RunID != "" {
		if _, err := uuid.Parse(input.RunID); err != nil {
			return nil, fmt.Errorf("search: reconciliation run id is invalid: %w", err)
		}
	}
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
		return nil, fmt.Errorf("search: reconciliation contract is invalid: %w", err)
	}
	if input.EmbeddingDimensions < 1 {
		return nil, errors.New("search: reconciliation dimensions must be positive")
	}
	limit := input.Limit
	if limit <= 0 {
		limit = searchReconciliationBatchLimit
	}
	if limit > searchReconciliationBatchLimit {
		return nil, fmt.Errorf("search: reconciliation batch exceeds %d documents", searchReconciliationBatchLimit)
	}
	result := make([]SearchDocumentForEmbedding, 0, limit)
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		type observedDocument struct {
			item          SearchDocumentForEmbedding
			vectorCurrent bool
		}
		var cursor struct {
			TeamID           sql.NullString
			SourceKind       sql.NullString
			SourceID         sql.NullString
			SearchDocumentID sql.NullString
		}
		if input.RunID != "" {
			cursorQuery := tx.WithContext(ctx).Raw(`
				SELECT selection_cursor_team_id::text,
				       selection_cursor_source_kind,
				       selection_cursor_source_id::text,
				       selection_cursor_search_document_id::text
				FROM search_reconciliation_runs
				WHERE embedding_contract_id = ?::uuid
				  AND embedding_dimensions = ?
				  AND reconciliation_run_id <> ?::uuid
				ORDER BY updated_at DESC, reconciliation_run_id DESC
				LIMIT 1
			`, input.EmbeddingContractID, input.EmbeddingDimensions, input.RunID).Row()
			if err := cursorQuery.Scan(&cursor.TeamID, &cursor.SourceKind, &cursor.SourceID, &cursor.SearchDocumentID); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		documents := make([]observedDocument, 0)
		query := `
			SELECT document.team_id::text, document.search_document_id::text,
			       document.owner_profile_id::text, document.source_kind,
			       document.source_id::text, document.source_version,
			       document.projection_format_version,
			       COALESCE(document.projection_generation_id::text, ''),
			       document.document_version, document.embedding_contract_id::text,
			       document.embedding_dimensions, document.search_state,
			       document.space_id::text, document.space_generation,
			       document.document_text, document.document_hash,
			       document.embedding IS NOT NULL
			         AND vector_dims(document.embedding) = document.embedding_dimensions
			FROM search_documents AS document
			JOIN teams AS team
			  ON team.id = document.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			JOIN embedding_contracts AS contract
			  ON contract.embedding_contract_id = document.embedding_contract_id
			 AND contract.dimensions = document.embedding_dimensions
			 AND contract.lifecycle_state = 'active'
			WHERE document.embedding_contract_id = ?::uuid
			  AND document.embedding_dimensions = ?
			  AND EXISTS (
			      SELECT 1
			      FROM search_index_generations AS generation
			      WHERE generation.embedding_contract_id = document.embedding_contract_id
			        AND generation.embedding_dimensions = document.embedding_dimensions
			        AND generation.activation_state = 'active'
			  )
		`
		args := []any{input.EmbeddingContractID, input.EmbeddingDimensions}
		cursorReady := cursor.TeamID.Valid && cursor.SourceKind.Valid && cursor.SourceID.Valid && cursor.SearchDocumentID.Valid
		if cursorReady {
			query += `
			  AND (document.team_id, document.source_kind, document.source_id, document.search_document_id)
			      > (?::uuid, ?, ?::uuid, ?::uuid)
			`
			args = append(args, cursor.TeamID.String, cursor.SourceKind.String, cursor.SourceID.String, cursor.SearchDocumentID.String)
		}
		query += `
			ORDER BY document.team_id, document.source_kind, document.source_id, document.search_document_id
			LIMIT ?
		`
		args = append(args, limit)
		rows, err := tx.WithContext(ctx).Raw(query, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item SearchDocumentForEmbedding
			var vectorCurrent bool
			if err := rows.Scan(
				&item.TeamID, &item.SearchDocumentID, &item.OwnerProfileID,
				&item.SourceKind, &item.SourceID, &item.SourceVersion,
				&item.ProjectionFormat, &item.ProjectionGenerationID,
				&item.DocumentVersion, &item.EmbeddingContractID,
				&item.EmbeddingDimensions, &item.SearchState, &item.SpaceID,
				&item.SpaceGeneration, &item.DocumentText, &item.DocumentHash, &vectorCurrent,
			); err != nil {
				return err
			}
			item.StoredDocumentHash = item.DocumentHash
			documents = append(documents, observedDocument{item: item, vectorCurrent: vectorCurrent})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		// Advance the durable keyset before canonical hydration. The page is
		// bounded by limit, and the cursor lets later runs move past healthy
		// documents instead of re-reading the same prefix forever.
		if input.RunID != "" {
			var update *gorm.DB
			if len(documents) == 0 {
				update = tx.WithContext(ctx).Exec(`
					UPDATE search_reconciliation_runs
					SET selection_cursor_observed_at = NULL,
					    selection_cursor_team_id = NULL,
					    selection_cursor_source_kind = NULL,
					    selection_cursor_source_id = NULL,
					    selection_cursor_search_document_id = NULL,
					    updated_at = clock_timestamp()
					WHERE reconciliation_run_id = ?::uuid
				`, input.RunID)
			} else {
				last := documents[len(documents)-1].item
				update = tx.WithContext(ctx).Exec(`
					UPDATE search_reconciliation_runs
					SET selection_cursor_observed_at = clock_timestamp(),
					    selection_cursor_team_id = ?::uuid,
					    selection_cursor_source_kind = ?,
					    selection_cursor_source_id = ?::uuid,
					    selection_cursor_search_document_id = ?::uuid,
					    updated_at = clock_timestamp()
					WHERE reconciliation_run_id = ?::uuid
				`, last.TeamID, last.SourceKind, last.SourceID, last.SearchDocumentID, input.RunID)
			}
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return gorm.ErrRecordNotFound
			}
		}
		for _, observed := range documents {
			if len(result) >= limit {
				break
			}
			item := observed.item
			expected, known, err := canonicalSearchDocument(ctx, tx, item)
			if err != nil {
				return err
			}
			if known && expected == nil {
				item.Retired = true
				result = append(result, item)
				continue
			}
			if known && expected != nil {
				if searchDocumentMatchesCanonical(item, *expected) && item.SearchState == string(domain.SearchProjectionCurrent) && observed.vectorCurrent {
					continue
				}
				item = *expected
			} else if item.SearchState == string(domain.SearchProjectionCurrent) && observed.vectorCurrent {
				continue
			}
			result = append(result, item)
		}
		if len(result) < limit {
			contract := &ActiveSearchContract{
				EmbeddingContractID: input.EmbeddingContractID,
				EmbeddingDimensions: input.EmbeddingDimensions,
			}
			if err := selectMissingCanonicalSearchDocuments(ctx, tx, contract, limit, &result); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("search: select reconciliation documents: %w", err)
	}
	return result, nil
}

// CompleteSearchReconciliationDocuments applies one validated provider batch
// atomically. Rows that changed while the provider was running are skipped by
// the source/document/hash fence; they are never overwritten by an old vector.
func (r *SearchRepositoryImpl) CompleteSearchReconciliationDocuments(
	ctx context.Context,
	input ApplySearchReconciliationInput,
) (*SearchReconciliationApplyResult, error) {
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
		return nil, fmt.Errorf("search: reconciliation contract is invalid: %w", err)
	}
	if input.EmbeddingDimensions < 1 {
		return nil, errors.New("search: reconciliation dimensions must be positive")
	}
	if len(input.Documents) > searchReconciliationBatchLimit {
		return nil, fmt.Errorf("search: reconciliation batch exceeds %d documents", searchReconciliationBatchLimit)
	}
	result := &SearchReconciliationApplyResult{}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		for _, document := range input.Documents {
			if document.Retired {
				if strings.TrimSpace(document.SearchDocumentID) == "" {
					return errors.New("search: retired reconciliation document id is required")
				}
				placeholder := SearchDocumentForEmbedding{
					SearchDocumentResult: SearchDocumentResult{
						TeamID: document.TeamID, SearchDocumentID: document.SearchDocumentID,
						OwnerProfileID: document.OwnerProfileID, SourceKind: document.SourceKind,
						SourceID: document.SourceID, SourceVersion: document.SourceVersion,
						ProjectionFormat: document.ProjectionFormat, ProjectionGenerationID: document.ProjectionGenerationID,
						DocumentVersion: document.DocumentVersion, EmbeddingContractID: input.EmbeddingContractID,
						EmbeddingDimensions: input.EmbeddingDimensions, SpaceID: document.SpaceID,
						SpaceGeneration: document.SpaceGeneration,
					},
					DocumentText: document.DocumentText, DocumentHash: document.DocumentHash,
				}
				expected, known, err := canonicalSearchDocument(ctx, tx, placeholder)
				if err != nil {
					return err
				}
				if !known || expected != nil {
					result.SkippedCount++
					continue
				}
				updated, err := markSearchDocumentNotRequired(ctx, tx, placeholder)
				if err != nil {
					return err
				}
				if updated {
					result.UpdatedCount++
				} else {
					result.SkippedCount++
				}
				continue
			}
			missingDocument := strings.TrimSpace(document.SearchDocumentID) == ""
			if !missingDocument {
				if _, err := uuid.Parse(strings.TrimSpace(document.SearchDocumentID)); err != nil {
					return fmt.Errorf("search: reconciliation document id is invalid: %w", err)
				}
			}
			if _, err := uuid.Parse(strings.TrimSpace(document.TeamID)); err != nil {
				return fmt.Errorf("search: reconciliation team id is invalid: %w", err)
			}
			if _, err := uuid.Parse(strings.TrimSpace(document.OwnerProfileID)); err != nil {
				return fmt.Errorf("search: reconciliation owner id is invalid: %w", err)
			}
			if document.SourceVersion < 1 || document.DocumentVersion < 1 || document.EmbeddingDimensions != input.EmbeddingDimensions || len(document.Embedding) != input.EmbeddingDimensions || strings.TrimSpace(document.DocumentHash) == "" {
				return errors.New("search: reconciliation document fence or vector is invalid")
			}
			if missingDocument {
				updated, err := completeMissingCanonicalSearchDocument(ctx, tx, &ActiveSearchContract{
					EmbeddingContractID: input.EmbeddingContractID,
					EmbeddingDimensions: input.EmbeddingDimensions,
				}, document)
				if err != nil {
					return err
				}
				if updated {
					result.UpdatedCount++
				} else {
					result.SkippedCount++
				}
				continue
			}
			storedHash := strings.TrimSpace(document.StoredDocumentHash)
			if storedHash == "" {
				storedHash = document.DocumentHash
			}
			var current SearchDocumentForEmbedding
			rows, err := tx.WithContext(ctx).Raw(`
				SELECT document.team_id::text, document.search_document_id::text,
				       document.owner_profile_id::text, document.source_kind,
				       document.source_id::text, document.source_version,
				       document.projection_format_version,
				       COALESCE(document.projection_generation_id::text, ''),
				       document.document_version, document.embedding_contract_id::text,
				       document.embedding_dimensions, document.search_state,
				       COALESCE(document.space_id::text, ''), COALESCE(document.space_generation, 0),
				       document.document_text, document.document_hash
				FROM search_documents AS document
				WHERE document.team_id = ?::uuid
				  AND document.search_document_id = ?::uuid
				  AND document.owner_profile_id = ?::uuid
				  AND document.embedding_contract_id = ?::uuid
				  AND document.embedding_dimensions = ?
				FOR UPDATE
			`, document.TeamID, document.SearchDocumentID, document.OwnerProfileID,
				input.EmbeddingContractID, document.EmbeddingDimensions).Rows()
			if err != nil {
				return err
			}
			found := rows.Next()
			if found {
				err = rows.Scan(
					&current.TeamID, &current.SearchDocumentID, &current.OwnerProfileID,
					&current.SourceKind, &current.SourceID, &current.SourceVersion,
					&current.ProjectionFormat, &current.ProjectionGenerationID,
					&current.DocumentVersion, &current.EmbeddingContractID,
					&current.EmbeddingDimensions, &current.SearchState, &current.SpaceID,
					&current.SpaceGeneration, &current.DocumentText, &current.DocumentHash,
				)
			} else {
				err = rows.Err()
			}
			closeErr := rows.Close()
			if err == nil && closeErr != nil {
				err = closeErr
			}
			if errors.Is(err, sql.ErrNoRows) || (!found && err == nil) {
				result.SkippedCount++
				continue
			}
			if err != nil {
				return err
			}
			if current.DocumentVersion != document.DocumentVersion || current.DocumentHash != storedHash {
				result.SkippedCount++
				continue
			}
			expected, known, err := canonicalSearchDocument(ctx, tx, current)
			if err != nil {
				return err
			}
			if known && expected == nil {
				result.SkippedCount++
				continue
			}
			target := &current
			if known {
				if expected.DocumentHash != document.DocumentHash ||
					expected.SourceVersion != document.SourceVersion ||
					expected.ProjectionFormat != document.ProjectionFormat ||
					strings.TrimSpace(expected.ProjectionGenerationID) != strings.TrimSpace(document.ProjectionGenerationID) ||
					expected.SpaceGeneration != document.SpaceGeneration ||
					strings.TrimSpace(expected.SpaceID) != strings.TrimSpace(document.SpaceID) {
					result.SkippedCount++
					continue
				}
				target = expected
			} else if current.DocumentHash != document.DocumentHash ||
				current.SourceVersion != document.SourceVersion ||
				current.ProjectionFormat != document.ProjectionFormat ||
				strings.TrimSpace(current.ProjectionGenerationID) != strings.TrimSpace(document.ProjectionGenerationID) ||
				current.SpaceGeneration != document.SpaceGeneration ||
				strings.TrimSpace(current.SpaceID) != strings.TrimSpace(document.SpaceID) {
				result.SkippedCount++
				continue
			}
			vector, err := vectorLiteral(document.Embedding)
			if err != nil {
				return fmt.Errorf("search: validate reconciliation vector: %w", err)
			}
			updated := tx.WithContext(ctx).Exec(`
				UPDATE search_documents
				SET source_version = ?,
				    projection_format_version = ?,
				    projection_generation_id = NULLIF(?, '')::uuid,
				    space_id = NULLIF(?, '')::uuid,
				    space_generation = NULLIF(?, 0)::bigint,
				    document_text = ?,
				    document_hash = ?,
				    document_version = document_version + CASE
				        WHEN document_hash IS DISTINCT FROM ?
				          OR projection_format_version IS DISTINCT FROM ?
				          OR projection_generation_id IS DISTINCT FROM NULLIF(?, '')::uuid
				          OR space_id IS DISTINCT FROM NULLIF(?, '')::uuid
				          OR space_generation IS DISTINCT FROM NULLIF(?, 0)::bigint
				        THEN 1 ELSE 0 END,
				    embedding = ?::vector,
				    search_state = 'current',
				    embedding_updated_at = clock_timestamp(),
				    embedding_error = '',
				    updated_at = clock_timestamp()
				WHERE team_id = ?::uuid
				  AND search_document_id = ?::uuid
				  AND owner_profile_id = ?::uuid
				  AND document_version = ?
				  AND document_hash = ?
				  AND embedding_contract_id = ?::uuid
				  AND embedding_dimensions = ?
				  AND search_state <> 'not_required'
				  AND (
				      search_state <> 'current'
				      OR embedding IS NULL
				      OR vector_dims(embedding) <> embedding_dimensions
				      OR source_version IS DISTINCT FROM ?
				      OR projection_format_version IS DISTINCT FROM ?
				      OR projection_generation_id IS DISTINCT FROM NULLIF(?, '')::uuid
				      OR space_id IS DISTINCT FROM NULLIF(?, '')::uuid
				      OR space_generation IS DISTINCT FROM NULLIF(?, 0)::bigint
				      OR document_hash IS DISTINCT FROM ?
				  )
			`, target.SourceVersion, target.ProjectionFormat, target.ProjectionGenerationID,
				target.SpaceID, target.SpaceGeneration, target.DocumentText, target.DocumentHash, target.DocumentHash,
				target.ProjectionFormat, target.ProjectionGenerationID, target.SpaceID, target.SpaceGeneration, vector,
				document.TeamID, document.SearchDocumentID, document.OwnerProfileID,
				document.DocumentVersion, storedHash, input.EmbeddingContractID,
				document.EmbeddingDimensions, target.SourceVersion, target.ProjectionFormat,
				target.ProjectionGenerationID, target.SpaceID, target.SpaceGeneration,
				target.DocumentHash)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected == 1 {
				result.UpdatedCount++
			} else {
				result.SkippedCount++
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("search: complete reconciliation documents: %w", err)
	}
	// The operator convergence endpoint owns the exact global projection. Keep
	// this run result bounded to the documents selected for this repair batch.
	result.RemainingDriftedCount = result.SkippedCount
	return result, nil
}

// FinishSearchReconciliationRun stores only bounded run metadata. Provider
// and database details are classified by the caller before this boundary.
func (r *SearchRepositoryImpl) FinishSearchReconciliationRun(
	ctx context.Context,
	input FinishSearchReconciliationRunInput,
) error {
	input.RunID = strings.TrimSpace(input.RunID)
	if _, err := uuid.Parse(input.RunID); err != nil {
		return fmt.Errorf("search: reconciliation run id is invalid: %w", err)
	}
	if input.Status != "completed" && input.Status != "failed" {
		return errors.New("search: reconciliation status is invalid")
	}
	if input.SelectedCount < 0 || input.EmbeddedCount < 0 || input.UpdatedCount < 0 || input.DriftedCount < 0 {
		return errors.New("search: reconciliation counts cannot be negative")
	}
	if len(input.LastError) > 256 {
		input.LastError = input.LastError[:256]
	}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		updated := tx.WithContext(ctx).Exec(`
			UPDATE search_reconciliation_runs
			SET status = ?, selected_count = ?, embedded_count = ?,
			    updated_count = ?, drifted_count = ?, last_error = ?,
			    completed_at = COALESCE(completed_at, clock_timestamp()),
			    updated_at = clock_timestamp()
			WHERE reconciliation_run_id = ?::uuid
		`, input.Status, input.SelectedCount, input.EmbeddedCount,
			input.UpdatedCount, input.DriftedCount, input.LastError, input.RunID)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if input.Status != "completed" {
			reset := tx.WithContext(ctx).Exec(`
				UPDATE search_reconciliation_runs
				SET selection_cursor_observed_at = NULL,
				    selection_cursor_team_id = NULL,
				    selection_cursor_source_kind = NULL,
				    selection_cursor_source_id = NULL,
				    selection_cursor_search_document_id = NULL,
				    updated_at = clock_timestamp()
				WHERE reconciliation_run_id = ?::uuid
			`, input.RunID)
			if reset.Error != nil {
				return reset.Error
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("search: finish reconciliation run: %w", err)
	}
	return nil
}
