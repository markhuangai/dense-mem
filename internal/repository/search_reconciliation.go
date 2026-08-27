package repository

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// CheckSearchConvergence is the startup health probe for the active search
// contract. It checks canonical documents directly; no queue or worker state
// participates in readiness.
func (r *SearchRepositoryImpl) CheckSearchConvergence(ctx context.Context) error {
	contract, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return err
	}
	var attentionRequired bool
	err = r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw(searchConvergenceHealthSQL,
			contract.EmbeddingContractID, contract.EmbeddingDimensions,
		).Scan(&attentionRequired).Error
	})
	if err != nil {
		return fmt.Errorf("search: convergence health: %w", err)
	}
	if attentionRequired {
		return ErrSearchConvergenceAttentionRequired
	}
	return nil
}

const searchConvergenceHealthSQL = `
	WITH health_contract AS (
		SELECT ?::uuid AS embedding_contract_id, ?::integer AS embedding_dimensions
	), activated_generation AS (
		SELECT DISTINCT ON (generation.team_id)
		       generation.team_id, generation.projection_generation_id
		FROM search_projection_generations AS generation
		WHERE generation.source_kind = 'relationship'
		  AND generation.projection_format_version = 2
		  AND generation.state = 'current'
		  AND generation.activated_at IS NOT NULL
		ORDER BY generation.team_id, generation.generation DESC, generation.created_at DESC
	), latest_generation AS (
		SELECT DISTINCT ON (generation.team_id)
		       generation.team_id, generation.projection_generation_id
		FROM search_projection_generations AS generation
		WHERE generation.source_kind = 'relationship'
		  AND generation.projection_format_version = 2
		ORDER BY generation.team_id, generation.generation DESC, generation.created_at DESC
	), foreground_generation AS (
		SELECT COALESCE(activated.team_id, latest.team_id) AS team_id,
		       COALESCE(activated.projection_generation_id, latest.projection_generation_id) AS projection_generation_id
		FROM activated_generation AS activated
		FULL JOIN latest_generation AS latest ON latest.team_id = activated.team_id
	), canonical_sources AS NOT MATERIALIZED (
		SELECT fragment.team_id, fragment.owner_profile_id,
		       'evidence'::text AS source_kind, fragment.fragment_id AS source_id,
		       1::bigint AS source_version, 1 AS projection_format_version,
		       NULL::uuid AS projection_generation_id,
		       btrim(fragment.content) AS document_text,
		       encode(digest(btrim(fragment.content), 'sha256'), 'hex') AS document_hash,
		       fragment.space_id, COALESCE(fragment.space_generation, 0)::bigint AS space_generation
		FROM evidence_fragments AS fragment
		JOIN knowledge_ingests AS ingest
		  ON ingest.team_id = fragment.team_id
		 AND ingest.ingest_id = fragment.ingest_id
		 AND ingest.status = 'completed'
		JOIN teams AS team
		  ON team.id = fragment.team_id
		 AND team.status = 'active'
		 AND team.deleted_at IS NULL
		WHERE COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
		  AND NOT EXISTS (
		      SELECT 1
		      FROM evidence_quarantines AS quarantine
		      WHERE quarantine.team_id = fragment.team_id
		        AND quarantine.fragment_id = fragment.fragment_id
		        AND quarantine.status = 'active'
		  )
		  AND NOT EXISTS (
		      SELECT 1
		      FROM evidence_lifecycle_events AS lifecycle
		      WHERE lifecycle.team_id = fragment.team_id
		        AND lifecycle.target_fragment_id = fragment.fragment_id
		  )
		UNION ALL
		SELECT relationship.team_id, relationship.owner_profile_id,
		       'relationship'::text AS source_kind, relationship.relationship_id AS source_id,
		       relationship.version::bigint AS source_version, 2 AS projection_format_version,
		       foreground.projection_generation_id,
		       rendered.document_text,
		       encode(digest(rendered.document_text, 'sha256'), 'hex') AS document_hash,
		       relationship.space_id, COALESCE(relationship.space_generation, 0)::bigint AS space_generation
		FROM relationship_records AS relationship
		JOIN teams AS team
		  ON team.id = relationship.team_id
		 AND team.status = 'active'
		 AND team.deleted_at IS NULL
		LEFT JOIN foreground_generation AS foreground ON foreground.team_id = relationship.team_id
		LEFT JOIN entity_names AS subject_name
		  ON subject_name.team_id = relationship.team_id
		 AND subject_name.entity_id = relationship.subject_entity_id
		 AND subject_name.name_kind = 'canonical'
		 AND subject_name.valid_to IS NULL
		LEFT JOIN entity_names AS object_name
		  ON object_name.team_id = relationship.team_id
		 AND object_name.entity_id = relationship.object_entity_id
		 AND object_name.name_kind = 'canonical'
		 AND object_name.valid_to IS NULL
		LEFT JOIN value_records AS value_record
		  ON value_record.team_id = relationship.team_id
		 AND value_record.value_id = relationship.object_value_id
		CROSS JOIN LATERAL (
			SELECT concat_ws(E'\n',
				'relationship',
				'subject: ' || COALESCE(NULLIF(btrim(subject_name.display_name), ''), relationship.subject_entity_id::text),
				'predicate: ' || replace(relationship.predicate_key, '_', ' '),
				'object: ' || COALESCE(
					NULLIF(btrim(object_name.display_name), ''),
					NULLIF(btrim(value_record.display), ''),
					NULLIF(btrim(value_record.canonical_value), ''),
					relationship.object_entity_id::text,
					relationship.object_value_id::text
				),
				CASE relationship.polarity WHEN '-' THEN 'polarity: negative' ELSE 'polarity: positive' END,
				CASE WHEN btrim(relationship.scope_key) = '' THEN NULL ELSE 'scope: ' || btrim(relationship.scope_key) END,
				CASE WHEN relationship.valid_from IS NULL THEN NULL ELSE 'valid_from: ' || regexp_replace(
					to_char(relationship.valid_from AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '\.?0+Z$', 'Z'
				) END,
				CASE WHEN relationship.valid_to IS NULL THEN NULL ELSE 'valid_to: ' || regexp_replace(
					to_char(relationship.valid_to AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '\.?0+Z$', 'Z'
				) END
			) AS document_text
		) AS rendered
		WHERE relationship.status = 'active'
		  AND relationship.support_count > 0
		  AND relationship.identity_alias_of_relationship_id IS NULL
	)
	SELECT EXISTS (
		SELECT 1
		FROM canonical_sources AS source
		CROSS JOIN health_contract AS contract
		LEFT JOIN search_documents AS document
		  ON document.team_id = source.team_id
		 AND document.source_kind = source.source_kind
		 AND document.source_id = source.source_id
		 AND document.embedding_contract_id = contract.embedding_contract_id
		WHERE document.search_document_id IS NULL
		   OR document.owner_profile_id IS DISTINCT FROM source.owner_profile_id
		   OR document.embedding_dimensions <> contract.embedding_dimensions
		   OR document.source_version <> source.source_version
		   OR document.projection_format_version <> source.projection_format_version
		   OR document.projection_generation_id IS DISTINCT FROM source.projection_generation_id
		   OR document.document_text <> source.document_text
		   OR document.document_hash <> source.document_hash
		   OR document.space_id IS DISTINCT FROM source.space_id
		   OR COALESCE(document.space_generation, 0) <> source.space_generation
		   OR document.search_state <> 'current'
		   OR document.embedding IS NULL
		   OR vector_dims(document.embedding) <> document.embedding_dimensions
		LIMIT 1
	) OR EXISTS (
		SELECT 1
		FROM search_documents AS document
		JOIN teams AS team
		  ON team.id = document.team_id
		 AND team.status = 'active'
		 AND team.deleted_at IS NULL
		CROSS JOIN health_contract AS contract
		WHERE document.embedding_contract_id = contract.embedding_contract_id
		  AND document.embedding_dimensions = contract.embedding_dimensions
		  AND document.source_kind IN ('evidence', 'relationship')
		  AND document.search_state <> 'not_required'
		  AND NOT EXISTS (
		      SELECT 1
		      FROM canonical_sources AS source
		      WHERE source.team_id = document.team_id
		        AND source.source_kind = document.source_kind
		        AND source.source_id = document.source_id
		  )
		LIMIT 1
	) OR EXISTS (
		SELECT 1
		FROM search_documents AS document
		JOIN teams AS team
		  ON team.id = document.team_id
		 AND team.status = 'active'
		 AND team.deleted_at IS NULL
		CROSS JOIN health_contract AS contract
		WHERE document.embedding_contract_id = contract.embedding_contract_id
		  AND document.embedding_dimensions = contract.embedding_dimensions
		  AND document.source_kind NOT IN ('evidence', 'relationship')
		  AND document.search_state <> 'not_required'
		  AND (
		      document.search_state <> 'current'
		      OR document.embedding IS NULL
		      OR vector_dims(document.embedding) <> document.embedding_dimensions
		  )
		LIMIT 1
	)
`
