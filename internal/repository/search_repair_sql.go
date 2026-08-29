package repository

// PostgreSQL's POSIX space class omits Unicode spaces that Go strings.TrimSpace removes.
const searchRepairUnicodeTrimPattern = `^[[:space:]\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+|[[:space:]\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+$`

const searchRepairDriftCTE = `
	WITH active_contract AS (
		SELECT ?::uuid AS embedding_contract_id, ?::integer AS embedding_dimensions
	), activated_generations AS (
		SELECT DISTINCT ON (team_id) team_id, projection_generation_id, updated_at
		FROM search_projection_generations
		WHERE source_kind = 'relationship' AND projection_format_version = 2
		  AND state = 'current' AND activated_at IS NOT NULL
		ORDER BY team_id, generation DESC, created_at DESC
	), latest_generations AS (
		SELECT DISTINCT ON (team_id) team_id, projection_generation_id, updated_at
		FROM search_projection_generations
		WHERE source_kind = 'relationship' AND projection_format_version = 2
		ORDER BY team_id, generation DESC, created_at DESC
	), foreground_generations AS (
		SELECT COALESCE(active.team_id, latest.team_id) AS team_id,
		       COALESCE(active.projection_generation_id, latest.projection_generation_id) AS projection_generation_id,
		       COALESCE(active.updated_at, latest.updated_at) AS updated_at
		FROM activated_generations AS active
		FULL JOIN latest_generations AS latest ON latest.team_id = active.team_id
	), canonical_sources AS NOT MATERIALIZED (
		SELECT fragment.team_id, fragment.owner_profile_id, 'evidence'::text AS source_kind,
		       fragment.fragment_id AS source_id, 1::bigint AS source_version,
		       1 AS projection_format_version, NULL::uuid AS projection_generation_id,
		       regexp_replace(fragment.content, '` + searchRepairUnicodeTrimPattern + `', '', 'g') AS document_text,
		       encode(digest(regexp_replace(fragment.content, '` + searchRepairUnicodeTrimPattern + `', '', 'g'), 'sha256'), 'hex') AS document_hash,
		       fragment.space_id, COALESCE(fragment.space_generation, 0)::bigint AS space_generation,
		       COALESCE(source.updated_at, fragment.created_at) AS observed_at
		FROM evidence_fragments AS fragment
` + searchRepairEvidenceTerminalPlacementJoinSQL + `
		JOIN teams AS team ON team.id = fragment.team_id AND team.status = 'active' AND team.deleted_at IS NULL
		WHERE COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
		  AND fragment.space_generation = dense_mem_active_space_generation(fragment.team_id, fragment.space_id)
		  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
		  AND NOT EXISTS (SELECT 1 FROM evidence_quarantines q WHERE q.team_id = fragment.team_id AND q.fragment_id = fragment.fragment_id AND q.status = 'active')
		  AND NOT EXISTS (SELECT 1 FROM evidence_lifecycle_events e WHERE e.team_id = fragment.team_id AND e.target_fragment_id = fragment.fragment_id)
		UNION ALL
		SELECT relationship.team_id, relationship.owner_profile_id, 'relationship'::text,
		       relationship.relationship_id, relationship.version::bigint, 2,
		       foreground.projection_generation_id,
		       concat_ws(E'\n',
		         'relationship',
		         'subject: ' || COALESCE(NULLIF(btrim(subject_name.display_name), ''), relationship.subject_entity_id::text),
		         'predicate: ' || replace(relationship.predicate_key, '_', ' '),
		         'object: ' || COALESCE(NULLIF(btrim(object_name.display_name), ''), NULLIF(btrim(value_record.display), ''), NULLIF(btrim(value_record.canonical_value), ''), relationship.object_entity_id::text, relationship.object_value_id::text),
		         CASE relationship.polarity WHEN '-' THEN 'polarity: negative' ELSE 'polarity: positive' END,
		         CASE WHEN btrim(relationship.scope_key) = '' THEN NULL ELSE 'scope: ' || btrim(relationship.scope_key) END,
	         CASE WHEN relationship.valid_from IS NULL THEN NULL ELSE 'valid_from: ' || regexp_replace(to_char(relationship.valid_from AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '\.{0,1}0+Z$', 'Z') END,
	         CASE WHEN relationship.valid_to IS NULL THEN NULL ELSE 'valid_to: ' || regexp_replace(to_char(relationship.valid_to AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '\.{0,1}0+Z$', 'Z') END
		       ) AS document_text,
		       encode(digest(concat_ws(E'\n',
		         'relationship',
		         'subject: ' || COALESCE(NULLIF(btrim(subject_name.display_name), ''), relationship.subject_entity_id::text),
		         'predicate: ' || replace(relationship.predicate_key, '_', ' '),
		         'object: ' || COALESCE(NULLIF(btrim(object_name.display_name), ''), NULLIF(btrim(value_record.display), ''), NULLIF(btrim(value_record.canonical_value), ''), relationship.object_entity_id::text, relationship.object_value_id::text),
		         CASE relationship.polarity WHEN '-' THEN 'polarity: negative' ELSE 'polarity: positive' END,
		         CASE WHEN btrim(relationship.scope_key) = '' THEN NULL ELSE 'scope: ' || btrim(relationship.scope_key) END,
	         CASE WHEN relationship.valid_from IS NULL THEN NULL ELSE 'valid_from: ' || regexp_replace(to_char(relationship.valid_from AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '\.{0,1}0+Z$', 'Z') END,
	         CASE WHEN relationship.valid_to IS NULL THEN NULL ELSE 'valid_to: ' || regexp_replace(to_char(relationship.valid_to AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '\.{0,1}0+Z$', 'Z') END
		       ), 'sha256'), 'hex') AS document_hash,
		       relationship.space_id, COALESCE(relationship.space_generation, 0)::bigint,
		       GREATEST(relationship.updated_at, COALESCE(foreground.updated_at, relationship.updated_at))
		FROM relationship_records AS relationship
		JOIN teams AS team ON team.id = relationship.team_id AND team.status = 'active' AND team.deleted_at IS NULL
		LEFT JOIN foreground_generations AS foreground ON foreground.team_id = relationship.team_id
		LEFT JOIN entity_names AS subject_name ON subject_name.team_id = relationship.team_id AND subject_name.entity_id = relationship.subject_entity_id AND subject_name.name_kind = 'canonical' AND subject_name.valid_to IS NULL
		LEFT JOIN entity_names AS object_name ON object_name.team_id = relationship.team_id AND object_name.entity_id = relationship.object_entity_id AND object_name.name_kind = 'canonical' AND object_name.valid_to IS NULL
		LEFT JOIN value_records AS value_record ON value_record.team_id = relationship.team_id AND value_record.value_id = relationship.object_value_id
		WHERE relationship.status = 'active' AND relationship.support_count > 0 AND relationship.identity_alias_of_relationship_id IS NULL
		  AND relationship.space_generation = dense_mem_active_space_generation(relationship.team_id, relationship.space_id)
	), drift (
		team_id, search_document_id, owner_profile_id, source_kind, source_id,
		source_version, projection_format_version, projection_generation_id,
		document_version, embedding_contract_id, embedding_dimensions, space_id,
		space_generation, document_text, document_hash, stored_document_hash,
		retired, observed_at
	) AS (
		SELECT source.team_id::text, COALESCE(document.search_document_id::text, ''), source.owner_profile_id::text,
		       source.source_kind, source.source_id::text, source.source_version, source.projection_format_version,
		       COALESCE(source.projection_generation_id::text, ''), COALESCE(document.document_version, 1),
		       contract.embedding_contract_id::text, contract.embedding_dimensions,
		       COALESCE(source.space_id::text, ''), source.space_generation, source.document_text, source.document_hash,
		       COALESCE(document.document_hash, ''), false AS retired, COALESCE(document.updated_at, source.observed_at)
		FROM canonical_sources AS source
		CROSS JOIN active_contract AS contract
		LEFT JOIN search_documents AS document ON document.team_id = source.team_id AND document.source_kind = source.source_kind AND document.source_id = source.source_id AND document.embedding_contract_id = contract.embedding_contract_id
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
		   OR document.search_state <> 'current' OR document.embedding IS NULL OR vector_dims(document.embedding) <> document.embedding_dimensions
		UNION ALL
		SELECT document.team_id::text, document.search_document_id::text, document.owner_profile_id::text,
		       document.source_kind, document.source_id::text, document.source_version, document.projection_format_version,
		       COALESCE(document.projection_generation_id::text, ''), document.document_version,
		       document.embedding_contract_id::text, document.embedding_dimensions, COALESCE(document.space_id::text, ''),
		       COALESCE(document.space_generation, 0), document.document_text, document.document_hash, document.document_hash,
		       true, document.updated_at
		FROM search_documents AS document
		JOIN teams AS team ON team.id = document.team_id AND team.status = 'active' AND team.deleted_at IS NULL
		CROSS JOIN active_contract AS contract
		WHERE document.embedding_contract_id = contract.embedding_contract_id AND document.embedding_dimensions = contract.embedding_dimensions
		  AND document.source_kind IN ('evidence', 'relationship') AND document.search_state <> 'not_required'
		  AND document.space_generation = dense_mem_active_space_generation(document.team_id, document.space_id)
		  AND NOT EXISTS (
			SELECT 1
			FROM evidence_quarantines AS quarantine
			WHERE document.source_kind = 'evidence'
			  AND quarantine.team_id = document.team_id
			  AND quarantine.fragment_id = document.source_id
			  AND quarantine.status = 'active'
		  )
		  AND NOT EXISTS (SELECT 1 FROM canonical_sources source WHERE source.team_id = document.team_id AND source.source_kind = document.source_kind AND source.source_id = document.source_id)
		UNION ALL
		SELECT document.team_id::text, document.search_document_id::text, document.owner_profile_id::text,
		       document.source_kind, document.source_id::text, document.source_version, document.projection_format_version,
		       COALESCE(document.projection_generation_id::text, ''), document.document_version,
		       document.embedding_contract_id::text, document.embedding_dimensions, COALESCE(document.space_id::text, ''),
		       COALESCE(document.space_generation, 0), document.document_text, document.document_hash, document.document_hash,
		       false, document.updated_at
		FROM search_documents AS document
		JOIN teams AS team ON team.id = document.team_id AND team.status = 'active' AND team.deleted_at IS NULL
		CROSS JOIN active_contract AS contract
		WHERE document.embedding_contract_id = contract.embedding_contract_id AND document.embedding_dimensions = contract.embedding_dimensions
		  AND document.source_kind NOT IN ('evidence', 'relationship') AND document.search_state <> 'not_required'
		  AND document.space_generation = dense_mem_active_space_generation(document.team_id, document.space_id)
		  AND (document.search_state <> 'current' OR document.embedding IS NULL OR vector_dims(document.embedding) <> document.embedding_dimensions)
	)
`

// searchRepairCandidateSQL returns source/document keys without loading
// canonical text. The caller applies a bounded keyset page, hydrates that page,
// and persists its cursor before the next run. The full drift CTE remains the
// authoritative source for aggregate convergence counts.
const searchRepairCandidateSQL = `
	WITH active_contract AS (
		SELECT ?::uuid AS embedding_contract_id, ?::integer AS embedding_dimensions
	), activated_generations AS (
		SELECT DISTINCT ON (team_id) team_id, projection_generation_id, updated_at
		FROM search_projection_generations
		WHERE source_kind = 'relationship' AND projection_format_version = 2
		  AND state = 'current' AND activated_at IS NOT NULL
		ORDER BY team_id, generation DESC, created_at DESC
	), latest_generations AS (
		SELECT DISTINCT ON (team_id) team_id, projection_generation_id, updated_at
		FROM search_projection_generations
		WHERE source_kind = 'relationship' AND projection_format_version = 2
		ORDER BY team_id, generation DESC, created_at DESC
	), foreground_generations AS (
		SELECT COALESCE(active.team_id, latest.team_id) AS team_id,
		       COALESCE(active.projection_generation_id, latest.projection_generation_id) AS projection_generation_id,
		       COALESCE(active.updated_at, latest.updated_at) AS updated_at
		FROM activated_generations AS active
		FULL JOIN latest_generations AS latest ON latest.team_id = active.team_id
	), source_keys AS NOT MATERIALIZED (
		SELECT fragment.team_id, fragment.owner_profile_id, 'evidence'::text AS source_kind,
		       fragment.fragment_id AS source_id, 1::bigint AS source_version,
		       1 AS projection_format_version, NULL::uuid AS projection_generation_id,
		       fragment.space_id, COALESCE(fragment.space_generation, 0)::bigint AS space_generation,
		       COALESCE(source.updated_at, fragment.created_at) AS observed_at
		FROM evidence_fragments AS fragment
` + searchRepairEvidenceTerminalPlacementJoinSQL + `
		JOIN teams AS team ON team.id = fragment.team_id AND team.status = 'active' AND team.deleted_at IS NULL
		WHERE COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
		  AND fragment.space_generation = dense_mem_active_space_generation(fragment.team_id, fragment.space_id)
		  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
		  AND NOT EXISTS (SELECT 1 FROM evidence_quarantines AS quarantine WHERE quarantine.team_id = fragment.team_id AND quarantine.fragment_id = fragment.fragment_id AND quarantine.status = 'active')
		  AND NOT EXISTS (SELECT 1 FROM evidence_lifecycle_events AS lifecycle WHERE lifecycle.team_id = fragment.team_id AND lifecycle.target_fragment_id = fragment.fragment_id)
		UNION ALL
		SELECT relationship.team_id, relationship.owner_profile_id, 'relationship'::text,
		       relationship.relationship_id, relationship.version::bigint, 2,
		       foreground.projection_generation_id, relationship.space_id,
		       COALESCE(relationship.space_generation, 0)::bigint,
		       GREATEST(relationship.updated_at, COALESCE(foreground.updated_at, relationship.updated_at))
		FROM relationship_records AS relationship
		JOIN teams AS team ON team.id = relationship.team_id AND team.status = 'active' AND team.deleted_at IS NULL
		LEFT JOIN foreground_generations AS foreground ON foreground.team_id = relationship.team_id
		WHERE relationship.status = 'active' AND relationship.support_count > 0
		  AND relationship.identity_alias_of_relationship_id IS NULL
		  AND relationship.space_generation = dense_mem_active_space_generation(relationship.team_id, relationship.space_id)
	), candidate_keys AS (
	SELECT source.team_id::text AS team_id, COALESCE(document.search_document_id::text, '') AS search_document_id,
	       source.owner_profile_id::text AS owner_profile_id, source.source_kind AS source_kind,
	       source.source_id::text AS source_id, source.source_version AS source_version,
	       source.projection_format_version AS projection_format_version,
	       COALESCE(source.projection_generation_id::text, '') AS projection_generation_id,
	       COALESCE(document.document_version, 1) AS document_version,
	       contract.embedding_contract_id::text AS embedding_contract_id,
	       contract.embedding_dimensions AS embedding_dimensions,
	       COALESCE(source.space_id::text, '') AS space_id, source.space_generation AS space_generation,
	       false AS retired,
	       CASE WHEN source.source_kind = 'relationship' THEN source.observed_at
            ELSE COALESCE(document.updated_at, source.observed_at)
       END AS observed_at
	FROM source_keys AS source
	CROSS JOIN active_contract AS contract
	LEFT JOIN search_documents AS document
	  ON document.team_id = source.team_id
	 AND document.source_kind = source.source_kind
	 AND document.source_id = source.source_id
	 AND document.embedding_contract_id = contract.embedding_contract_id
	UNION ALL
	SELECT document.team_id::text, document.search_document_id::text, document.owner_profile_id::text,
	       document.source_kind, document.source_id::text, document.source_version,
	       document.projection_format_version, COALESCE(document.projection_generation_id::text, ''),
	       document.document_version, document.embedding_contract_id::text, document.embedding_dimensions,
	       COALESCE(document.space_id::text, ''), COALESCE(document.space_generation, 0),
	       true AS retired, document.updated_at
	FROM search_documents AS document
	JOIN teams AS team ON team.id = document.team_id AND team.status = 'active' AND team.deleted_at IS NULL
	CROSS JOIN active_contract AS contract
	WHERE document.embedding_contract_id = contract.embedding_contract_id
	  AND document.embedding_dimensions = contract.embedding_dimensions
	  AND document.source_kind IN ('evidence', 'relationship')
	  AND document.search_state <> 'not_required'
	  AND document.space_generation = dense_mem_active_space_generation(document.team_id, document.space_id)
	  AND NOT EXISTS (
		SELECT 1
		FROM evidence_quarantines AS quarantine
		WHERE document.source_kind = 'evidence'
		  AND quarantine.team_id = document.team_id
		  AND quarantine.fragment_id = document.source_id
		  AND quarantine.status = 'active'
	  )
	  AND NOT EXISTS (
		SELECT 1
		FROM source_keys AS source
		WHERE source.team_id = document.team_id
		  AND source.source_kind = document.source_kind
		  AND source.source_id = document.source_id
	  )
	UNION ALL
	SELECT document.team_id::text, document.search_document_id::text, document.owner_profile_id::text,
	       document.source_kind, document.source_id::text, document.source_version,
	       document.projection_format_version, COALESCE(document.projection_generation_id::text, ''),
	       document.document_version, document.embedding_contract_id::text, document.embedding_dimensions,
	       COALESCE(document.space_id::text, ''), COALESCE(document.space_generation, 0),
	       false AS retired, document.updated_at
	FROM search_documents AS document
	JOIN teams AS team ON team.id = document.team_id AND team.status = 'active' AND team.deleted_at IS NULL
	CROSS JOIN active_contract AS contract
	WHERE document.embedding_contract_id = contract.embedding_contract_id
	  AND document.embedding_dimensions = contract.embedding_dimensions
	  AND document.source_kind NOT IN ('evidence', 'relationship')
	  AND document.search_state <> 'not_required'
	  AND document.space_generation = dense_mem_active_space_generation(document.team_id, document.space_id)
	  AND (document.search_state <> 'current' OR document.embedding IS NULL OR vector_dims(document.embedding) <> document.embedding_dimensions)
	)
	SELECT team_id, search_document_id, owner_profile_id, source_kind, source_id, source_version,
	       projection_format_version, projection_generation_id, document_version, embedding_contract_id,
	       embedding_dimensions, space_id, space_generation, retired, observed_at
	FROM candidate_keys
`

const searchRepairDriftCountSQL = searchRepairDriftCTE + `
SELECT count(*)
FROM drift
`

// searchRepairReservationProbeSQL only checks for potentially eligible source
// or document keys. It is intentionally conservative: the bounded selection
// pass remains authoritative for deciding whether a repair is needed.
const searchRepairReservationProbeSQL = `
	SELECT EXISTS (
		SELECT 1
		FROM (
			SELECT fragment.team_id
			FROM evidence_fragments AS fragment
` + searchRepairEvidenceTerminalPlacementJoinSQL + `
			JOIN teams AS team ON team.id = fragment.team_id AND team.status = 'active' AND team.deleted_at IS NULL
			WHERE COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
			  AND fragment.space_generation = dense_mem_active_space_generation(fragment.team_id, fragment.space_id)
			  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
			  AND NOT EXISTS (SELECT 1 FROM evidence_quarantines AS quarantine WHERE quarantine.team_id = fragment.team_id AND quarantine.fragment_id = fragment.fragment_id AND quarantine.status = 'active')
			  AND NOT EXISTS (SELECT 1 FROM evidence_lifecycle_events AS lifecycle WHERE lifecycle.team_id = fragment.team_id AND lifecycle.target_fragment_id = fragment.fragment_id)
			LIMIT 1
		) AS evidence_keys
	)
	OR EXISTS (
		SELECT 1
		FROM relationship_records AS relationship
		JOIN teams AS team ON team.id = relationship.team_id AND team.status = 'active' AND team.deleted_at IS NULL
		WHERE relationship.status = 'active'
		  AND relationship.support_count > 0
		  AND relationship.identity_alias_of_relationship_id IS NULL
		  AND relationship.space_generation = dense_mem_active_space_generation(relationship.team_id, relationship.space_id)
		LIMIT 1
	)
	OR EXISTS (
		SELECT 1
		FROM search_documents AS document
		JOIN teams AS team ON team.id = document.team_id AND team.status = 'active' AND team.deleted_at IS NULL
		WHERE document.embedding_contract_id = ?::uuid
		  AND document.embedding_dimensions = ?
		  AND document.search_state <> 'not_required'
		  AND document.space_generation = dense_mem_active_space_generation(document.team_id, document.space_id)
		LIMIT 1
	)
`
