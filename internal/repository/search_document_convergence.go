package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const searchDocumentConvergenceQueryTimeout = 2 * time.Second

type searchDocumentConvergenceStats struct {
	expected      int64
	current       int64
	drifted       int64
	affectedTeams int64
	oldestDrift   time.Duration
	classes       []SearchDocumentDriftCount
}

const searchDocumentConvergenceStatsSQL = searchRepairDriftCTE + `
	SELECT
		(SELECT count(*) FROM canonical_sources) AS expected_documents,
		(SELECT count(*)
		 FROM canonical_sources AS source
		 CROSS JOIN active_contract AS contract
		 JOIN search_documents AS document
		   ON document.team_id = source.team_id
		  AND document.source_kind = source.source_kind
		  AND document.source_id = source.source_id
		  AND document.embedding_contract_id = contract.embedding_contract_id
		 WHERE document.owner_profile_id IS NOT DISTINCT FROM source.owner_profile_id
		   AND document.embedding_dimensions = contract.embedding_dimensions
		   AND document.source_version = source.source_version
		   AND document.projection_format_version = source.projection_format_version
		   AND (
				document.projection_generation_id IS NOT DISTINCT FROM source.projection_generation_id
				OR (
					source.source_kind = 'relationship'
					AND source.projection_generation_id IS NOT NULL
					AND document.projection_generation_id IS NULL
					AND COALESCE(document.metadata->>'` + relationshipForegroundRecallGenerationMetadataKey + `', '') = source.projection_generation_id::text
				)
			)
		   AND document.document_text = source.document_text
		   AND document.document_hash = source.document_hash
		   AND document.space_id IS NOT DISTINCT FROM source.space_id
		   AND COALESCE(document.space_generation, 0) = source.space_generation
		   AND document.search_state = 'current'
		   AND document.embedding IS NOT NULL
		   AND vector_dims(document.embedding) = document.embedding_dimensions
		) AS current_documents,
		(SELECT count(*) FROM drift) AS drifted_documents,
		(SELECT count(DISTINCT team_id) FROM drift) AS affected_team_count,
		COALESCE((SELECT EXTRACT(EPOCH FROM (clock_timestamp() - min(observed_at))) FROM drift), 0) AS oldest_drift_age_seconds
`

const searchDocumentConvergenceHealthSQL = searchRepairDriftCTE + `
	SELECT EXISTS (SELECT 1 FROM drift LIMIT 1)
`

const searchDocumentConvergenceClassesSQL = searchRepairDriftCTE + `
	SELECT class, count(*)
	FROM (
		SELECT CASE
			WHEN retired THEN 'retired_document'
			WHEN search_document_id = '' THEN 'missing_document'
			WHEN stored_document_hash IS DISTINCT FROM document_hash THEN 'content_mismatch'
			ELSE 'document_fence_or_vector'
		END AS class
		FROM drift
	) AS classified
	GROUP BY class
	ORDER BY class
`

func (r *SearchRepositoryImpl) searchDocumentConvergence(ctx context.Context, contract *ActiveSearchContract) (searchDocumentConvergenceStats, error) {
	if contract == nil {
		return searchDocumentConvergenceStats{}, fmt.Errorf("active search contract is required")
	}
	stats := searchDocumentConvergenceStats{classes: []SearchDocumentDriftCount{}}
	err := r.withSystemReadOnlyRepeatableTx(ctx, func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Exec("SET LOCAL statement_timeout = '2s'").Error; err != nil {
			return err
		}
		var oldestSeconds float64
		if err := tx.WithContext(ctx).Raw(
			searchDocumentConvergenceStatsSQL, contract.EmbeddingContractID, contract.EmbeddingDimensions,
		).Row().Scan(&stats.expected, &stats.current, &stats.drifted, &stats.affectedTeams, &oldestSeconds); err != nil {
			return err
		}
		stats.oldestDrift = time.Duration(oldestSeconds * float64(time.Second))
		rows, err := tx.WithContext(ctx).Raw(
			searchDocumentConvergenceClassesSQL, contract.EmbeddingContractID, contract.EmbeddingDimensions,
		).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item SearchDocumentDriftCount
			if err := rows.Scan(&item.Class, &item.Count); err != nil {
				return err
			}
			stats.classes = append(stats.classes, item)
		}
		return rows.Err()
	})
	if err != nil {
		return searchDocumentConvergenceStats{}, fmt.Errorf("search: document convergence: %w", err)
	}
	return stats, nil
}

func (r *SearchRepositoryImpl) searchDocumentConvergenceAttentionRequired(ctx context.Context, contract *ActiveSearchContract) (bool, error) {
	if contract == nil {
		return false, fmt.Errorf("active search contract is required")
	}
	var attentionRequired bool
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Exec("SET LOCAL statement_timeout = '2s'").Error; err != nil {
			return err
		}
		return tx.WithContext(ctx).Raw(
			searchDocumentConvergenceHealthSQL, contract.EmbeddingContractID, contract.EmbeddingDimensions,
		).Scan(&attentionRequired).Error
	})
	if err != nil {
		return false, err
	}
	return attentionRequired, nil
}
