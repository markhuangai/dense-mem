package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// GetSearchConvergence compares active-contract search documents with the
// canonical semantic projections. A document is current only when its active
// contract vector is present and has the declared dimensions.
func (r *SearchRepositoryImpl) GetSearchConvergence(ctx context.Context, input SearchConvergenceInput) (*SearchConvergence, error) {
	input = normalizeSearchConvergenceInput(input)
	if err := validateSearchConvergenceInput(input); err != nil {
		return nil, err
	}
	contract, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	if input.EmbeddingContractID != "" && input.EmbeddingContractID != contract.EmbeddingContractID {
		return nil, fmt.Errorf("%w: requested convergence contract is not active", ErrSearchContractMismatch)
	}
	if input.EmbeddingDimensions > 0 && input.EmbeddingDimensions != contract.EmbeddingDimensions {
		return nil, fmt.Errorf("%w: requested convergence dimensions are not active", ErrSearchContractMismatch)
	}

	convergence := &SearchConvergence{
		ObservedAt:   time.Now().UTC(),
		Status:       "converged",
		Contract:     contract,
		DriftClasses: []SearchDocumentDriftCount{},
	}
	err = r.withSystemTx(ctx, func(tx *gorm.DB) error {
		// not_required documents have no vector obligation. All other active
		// contract documents are expected to have a current vector.
		if err := tx.WithContext(ctx).Raw(`
			SELECT count(*)
			FROM search_documents AS document
			JOIN teams AS team
			  ON team.id = document.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE document.embedding_contract_id = ?::uuid
			  AND document.embedding_dimensions = ?
			  AND document.search_state <> 'not_required'
		`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Scan(&convergence.ExpectedDocuments).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Raw(`
			SELECT count(*)
			FROM search_documents AS document
			JOIN teams AS team
			  ON team.id = document.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE document.embedding_contract_id = ?::uuid
			  AND document.embedding_dimensions = ?
			  AND document.search_state = 'current'
			  AND document.embedding IS NOT NULL
			  AND vector_dims(document.embedding) = document.embedding_dimensions
		`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Scan(&convergence.CurrentDocuments).Error; err != nil {
			return err
		}
		convergence.DriftedDocuments = convergence.ExpectedDocuments - convergence.CurrentDocuments
		if convergence.DriftedDocuments < 0 {
			convergence.DriftedDocuments = 0
		}
		if err := tx.WithContext(ctx).Raw(`
			SELECT count(DISTINCT document.team_id)
			FROM search_documents AS document
			JOIN teams AS team
			  ON team.id = document.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE document.embedding_contract_id = ?::uuid
			  AND document.embedding_dimensions = ?
			  AND (
			    document.search_state <> 'current'
			    OR document.embedding IS NULL
			    OR vector_dims(document.embedding) <> document.embedding_dimensions
			  )
		`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Scan(&convergence.AffectedTeamCount).Error; err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT CASE
			         WHEN document.embedding IS NULL THEN 'vector_missing'
			         WHEN document.search_state <> 'current' THEN 'state_not_current'
			         WHEN vector_dims(document.embedding) <> document.embedding_dimensions THEN 'vector_dimension_mismatch'
			         ELSE 'current'
			       END AS drift_class, count(*)
			FROM search_documents AS document
			JOIN teams AS team
			  ON team.id = document.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE document.embedding_contract_id = ?::uuid
			  AND document.embedding_dimensions = ?
			  AND (
			    document.search_state <> 'current'
			    OR document.embedding IS NULL
			    OR vector_dims(document.embedding) <> document.embedding_dimensions
			  )
			GROUP BY 1
			ORDER BY 1
		`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item SearchDocumentDriftCount
			if err := rows.Scan(&item.Class, &item.Count); err != nil {
				return err
			}
			convergence.DriftClasses = append(convergence.DriftClasses, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		var oldestSeconds float64
		if err := tx.WithContext(ctx).Raw(`
			SELECT COALESCE(EXTRACT(EPOCH FROM (clock_timestamp() - min(document.updated_at))), 0)
			FROM search_documents AS document
			JOIN teams AS team
			  ON team.id = document.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE document.embedding_contract_id = ?::uuid
			  AND document.embedding_dimensions = ?
			  AND document.search_state <> 'current'
		`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Scan(&oldestSeconds).Error; err != nil {
			return err
		}
		convergence.OldestDriftAge = time.Duration(oldestSeconds * float64(time.Second))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("search: convergence projection: %w", err)
	}
	canonical, err := r.canonicalSearchConvergence(ctx, contract)
	if err != nil {
		return nil, err
	}
	convergence.ExpectedDocuments = canonical.ExpectedDocuments
	convergence.CurrentDocuments = canonical.CurrentDocuments
	convergence.DriftedDocuments = canonical.DriftedDocuments
	convergence.AffectedTeamCount = canonical.AffectedTeamCount
	convergence.OldestDriftAge = canonical.OldestDriftAge
	convergence.DriftClasses = canonical.DriftClasses
	if convergence.DriftedDocuments > 0 {
		convergence.Status = "attention_required"
	}
	convergence.LatestRun, err = r.latestSearchReconciliationRun(ctx)
	if err != nil {
		return nil, err
	}
	return convergence, nil
}

type canonicalSearchConvergence struct {
	ExpectedDocuments int64
	CurrentDocuments  int64
	DriftedDocuments  int64
	AffectedTeamCount int64
	OldestDriftAge    time.Duration
	DriftClasses      []SearchDocumentDriftCount
}

func (r *SearchRepositoryImpl) canonicalSearchConvergence(ctx context.Context, contract *ActiveSearchContract) (*canonicalSearchConvergence, error) {
	result := &canonicalSearchConvergence{}
	classes := map[string]int64{}
	teams := map[string]struct{}{}
	now := time.Now().UTC()
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		type observedDocument struct {
			item          SearchDocumentForEmbedding
			vectorCurrent bool
			updatedAt     time.Time
		}
		documents := make([]observedDocument, 0)
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT document.team_id::text, document.search_document_id::text,
			       document.owner_profile_id::text, document.source_kind,
			       document.source_id::text, document.source_version,
			       document.projection_format_version,
			       COALESCE(document.projection_generation_id::text, ''),
			       document.document_version, document.embedding_contract_id::text,
			       document.embedding_dimensions, document.search_state,
			       COALESCE(document.space_id::text, ''), COALESCE(document.space_generation, 0),
			       document.document_text, document.document_hash,
			       document.embedding IS NOT NULL
			         AND vector_dims(document.embedding) = document.embedding_dimensions,
			       document.updated_at
			FROM search_documents AS document
			JOIN teams AS team
			  ON team.id = document.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE document.embedding_contract_id = ?::uuid
			  AND document.embedding_dimensions = ?
		`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item SearchDocumentForEmbedding
			var vectorCurrent bool
			var updatedAt time.Time
			if err := rows.Scan(
				&item.TeamID, &item.SearchDocumentID, &item.OwnerProfileID,
				&item.SourceKind, &item.SourceID, &item.SourceVersion,
				&item.ProjectionFormat, &item.ProjectionGenerationID,
				&item.DocumentVersion, &item.EmbeddingContractID,
				&item.EmbeddingDimensions, &item.SearchState, &item.SpaceID,
				&item.SpaceGeneration, &item.DocumentText, &item.DocumentHash,
				&vectorCurrent, &updatedAt,
			); err != nil {
				return err
			}
			documents = append(documents, observedDocument{
				item: item, vectorCurrent: vectorCurrent, updatedAt: updatedAt,
			})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, observed := range documents {
			item := observed.item
			vectorCurrent := observed.vectorCurrent
			updatedAt := observed.updatedAt
			expected, known, err := canonicalSearchDocument(ctx, tx, item)
			if err != nil {
				return err
			}
			if !known {
				expected = &item
			}
			if known && expected == nil {
				if item.SearchState == string(domain.SearchProjectionNotRequired) {
					continue
				}
				classes["canonical_source_missing"]++
				result.DriftedDocuments++
				teams[item.TeamID] = struct{}{}
				if age := now.Sub(updatedAt); age > result.OldestDriftAge {
					result.OldestDriftAge = age
				}
				continue
			}
			result.ExpectedDocuments++
			canonicalMatch := searchDocumentMatchesCanonical(item, *expected)
			if canonicalMatch && item.SearchState == string(domain.SearchProjectionCurrent) && vectorCurrent {
				result.CurrentDocuments++
				continue
			}
			class := "canonical_projection_mismatch"
			if canonicalMatch {
				switch {
				case item.SearchState != string(domain.SearchProjectionCurrent):
					class = "state_not_current"
				case !vectorCurrent:
					class = "vector_missing_or_dimension_mismatch"
				}
			}
			classes[class]++
			result.DriftedDocuments++
			teams[item.TeamID] = struct{}{}
			if age := now.Sub(updatedAt); age > result.OldestDriftAge {
				result.OldestDriftAge = age
			}
		}
		if err := addMissingCanonicalSearchStats(ctx, tx, contract, result, classes, teams, now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("search: canonical convergence projection: %w", err)
	}
	result.AffectedTeamCount = int64(len(teams))
	result.DriftClasses = make([]SearchDocumentDriftCount, 0, len(classes))
	for class, count := range classes {
		result.DriftClasses = append(result.DriftClasses, SearchDocumentDriftCount{Class: class, Count: count})
	}
	sort.Slice(result.DriftClasses, func(i, j int) bool { return result.DriftClasses[i].Class < result.DriftClasses[j].Class })
	return result, nil
}

func addMissingCanonicalSearchStats(
	ctx context.Context,
	tx *gorm.DB,
	contract *ActiveSearchContract,
	result *canonicalSearchConvergence,
	classes map[string]int64,
	teams map[string]struct{},
	now time.Time,
) error {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH canonical_sources AS (
			SELECT fragment.team_id, fragment.fragment_id AS source_id,
			       'evidence'::text AS source_kind, fragment.created_at
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
			UNION ALL
			SELECT relationship.team_id, relationship.relationship_id AS source_id,
			       'relationship'::text AS source_kind, relationship.created_at
			FROM relationship_records AS relationship
			JOIN teams AS team
			  ON team.id = relationship.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE relationship.status = 'active'
			  AND relationship.support_count > 0
			  AND relationship.identity_alias_of_relationship_id IS NULL
		)
		SELECT source.team_id::text, count(*),
		       COALESCE(EXTRACT(EPOCH FROM (clock_timestamp() - min(source.created_at))), 0)
		FROM canonical_sources AS source
		WHERE NOT EXISTS (
		          SELECT 1
		          FROM search_documents AS document
		          WHERE document.team_id = source.team_id
		            AND document.source_kind = source.source_kind
		            AND document.source_id = source.source_id
		            AND document.embedding_contract_id = ?::uuid
		      )
		GROUP BY source.team_id
	`, contract.EmbeddingContractID).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var teamID string
		var count int64
		var oldestSeconds float64
		if err := rows.Scan(&teamID, &count, &oldestSeconds); err != nil {
			return err
		}
		result.ExpectedDocuments += count
		result.DriftedDocuments += count
		classes["canonical_document_missing"] += count
		teams[teamID] = struct{}{}
		age := time.Duration(oldestSeconds * float64(time.Second))
		if age > result.OldestDriftAge {
			result.OldestDriftAge = age
		}
	}
	return rows.Err()
}

func (r *SearchRepositoryImpl) latestSearchReconciliationRun(ctx context.Context) (*SearchReconciliationRun, error) {
	var run SearchReconciliationRun
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw(`
			SELECT reconciliation_run_id::text, local_run_date, status,
			       selected_count, embedded_count, updated_count, drifted_count, last_error,
			       started_at, completed_at, updated_at
			FROM search_reconciliation_runs
			ORDER BY updated_at DESC, reconciliation_run_id DESC
			LIMIT 1
		`).Row().Scan(
			&run.RunID, &run.LocalRunDate, &run.Status,
			&run.SelectedCount, &run.EmbeddedCount, &run.UpdatedCount, &run.DriftedCount, &run.LastError,
			&run.StartedAt, &run.CompletedAt, &run.UpdatedAt,
		)
	})
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("search: latest reconciliation run: %w", err)
	}
	return &run, nil
}

func searchTimePointer(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func normalizeSearchConvergenceInput(input SearchConvergenceInput) SearchConvergenceInput {
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	return input
}

func validateSearchConvergenceInput(input SearchConvergenceInput) error {
	if input.EmbeddingContractID != "" {
		if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
			return fmt.Errorf("embedding_contract_id is invalid: %w", err)
		}
	}
	if input.EmbeddingDimensions < 0 {
		return errors.New("embedding_dimensions cannot be negative")
	}
	return nil
}

func refreshRelationshipProjectionGeneration(ctx context.Context, tx *gorm.DB, teamID string, projectionGenerationID string) error {
	return tx.WithContext(ctx).Exec("UPDATE search_projection_generations SET drifted_count = 0 WHERE team_id = ?::uuid AND projection_generation_id = ?::uuid", teamID, projectionGenerationID).Error
}
