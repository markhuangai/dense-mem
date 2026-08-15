-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite analysis:
-- - This migration is for the V2.3 exclusive restart cutover. It intentionally
--   uses normal transactional DDL instead of concurrent index swaps.
-- - Relationship rows are rewritten once to remove tier columns; Relationship
--   search documents are rewritten only for active effectively-supported rows.
-- - Evidence and Entity search documents are not re-rendered and their vectors
--   are not invalidated.
-- - RLS impact: schema changes and backfill run in migration mode; runtime RLS
--   policies continue to enforce team/profile scoping after the transaction.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE search_documents
    ADD COLUMN IF NOT EXISTS projection_format_version INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS projection_generation_id UUID NULL;

ALTER TABLE embedding_jobs
    ADD COLUMN IF NOT EXISTS projection_format_version INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS projection_generation_id UUID NULL;

CREATE TABLE IF NOT EXISTS search_projection_generations (
    team_id UUID NOT NULL,
    projection_generation_id UUID NOT NULL DEFAULT gen_random_uuid(),
    source_kind TEXT NOT NULL,
    generation INTEGER NOT NULL,
    projection_format_version INTEGER NOT NULL,
    state TEXT NOT NULL DEFAULT 'projecting_text',
    eligible_count BIGINT NOT NULL DEFAULT 0,
    projected_count BIGINT NOT NULL DEFAULT 0,
    current_vector_count BIGINT NOT NULL DEFAULT 0,
    failed_job_count BIGINT NOT NULL DEFAULT 0,
    last_projected_source_id UUID NULL,
    last_error TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ NULL,
    activated_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, projection_generation_id),
    UNIQUE (team_id, source_kind, projection_format_version, generation),
    FOREIGN KEY (team_id) REFERENCES semantic_team_refs(team_id) ON DELETE RESTRICT,
    CONSTRAINT search_projection_generations_source_kind_check CHECK (source_kind IN ('relationship', 'evidence', 'entity')),
    CONSTRAINT search_projection_generations_generation_check CHECK (generation >= 1),
    CONSTRAINT search_projection_generations_format_check CHECK (projection_format_version >= 1),
    CONSTRAINT search_projection_generations_state_check CHECK (state IN ('projecting_text', 'embedding', 'current', 'failed')),
    CONSTRAINT search_projection_generations_counts_check CHECK (
        eligible_count >= 0
        AND projected_count >= 0
        AND current_vector_count >= 0
        AND failed_job_count >= 0
    ),
    CONSTRAINT search_projection_generations_current_time_check CHECK (
        (state = 'current' AND activated_at IS NOT NULL)
        OR state <> 'current'
    )
);

ALTER TABLE search_projection_generations ENABLE ROW LEVEL SECURITY;
ALTER TABLE search_projection_generations FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS search_projection_generations_select ON search_projection_generations;
CREATE POLICY search_projection_generations_select ON search_projection_generations
FOR SELECT USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) IN ('team', 'profile')
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
);

DROP POLICY IF EXISTS search_projection_generations_insert ON search_projection_generations;
CREATE POLICY search_projection_generations_insert ON search_projection_generations
FOR INSERT WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'team'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
);

DROP POLICY IF EXISTS search_projection_generations_update ON search_projection_generations;
CREATE POLICY search_projection_generations_update ON search_projection_generations
FOR UPDATE USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'team'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
) WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'team'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS search_projection_generations_current_unique
    ON search_projection_generations(team_id, source_kind, projection_format_version)
    WHERE state = 'current';

CREATE INDEX IF NOT EXISTS search_projection_generations_state_idx
    ON search_projection_generations(team_id, source_kind, projection_format_version, state);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'search_documents'::regclass
          AND conname = 'search_documents_projection_format_check'
    ) THEN
        ALTER TABLE search_documents
            ADD CONSTRAINT search_documents_projection_format_check
            CHECK (projection_format_version >= 1);
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'embedding_jobs'::regclass
          AND conname = 'embedding_jobs_projection_format_check'
    ) THEN
        ALTER TABLE embedding_jobs
            ADD CONSTRAINT embedding_jobs_projection_format_check
            CHECK (projection_format_version >= 1);
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'search_documents'::regclass
          AND conname = 'search_documents_projection_generation_fk'
    ) THEN
        ALTER TABLE search_documents
            ADD CONSTRAINT search_documents_projection_generation_fk
            FOREIGN KEY (team_id, projection_generation_id)
            REFERENCES search_projection_generations(team_id, projection_generation_id)
            ON DELETE RESTRICT;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'embedding_jobs'::regclass
          AND conname = 'embedding_jobs_projection_generation_fk'
    ) THEN
        ALTER TABLE embedding_jobs
            ADD CONSTRAINT embedding_jobs_projection_generation_fk
            FOREIGN KEY (team_id, projection_generation_id)
            REFERENCES search_projection_generations(team_id, projection_generation_id)
            ON DELETE RESTRICT;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS search_documents_relationship_projection_idx
    ON search_documents(team_id, source_kind, projection_format_version, projection_generation_id, search_state)
    WHERE source_kind = 'relationship';

CREATE INDEX IF NOT EXISTS embedding_jobs_projection_generation_idx
    ON embedding_jobs(team_id, projection_generation_id, status)
    WHERE projection_generation_id IS NOT NULL;

WITH eligible_relationships AS (
    SELECT relationship.team_id,
           count(*) AS eligible_count
    FROM relationship_records AS relationship
    WHERE relationship.identity_alias_of_relationship_id IS NULL
      AND relationship.status = 'active'
      AND relationship.support_count > 0
    GROUP BY relationship.team_id
),
next_generation AS (
    SELECT eligible.team_id,
           COALESCE(max(existing.generation), 0) + 1 AS generation,
           eligible.eligible_count
    FROM eligible_relationships AS eligible
    LEFT JOIN search_projection_generations AS existing
      ON existing.team_id = eligible.team_id
     AND existing.source_kind = 'relationship'
     AND existing.projection_format_version = 2
    GROUP BY eligible.team_id, eligible.eligible_count
)
INSERT INTO search_projection_generations (
    team_id, source_kind, generation, projection_format_version, state,
    eligible_count, projected_count, current_vector_count, failed_job_count
)
SELECT team_id, 'relationship', generation, 2, 'projecting_text',
       eligible_count, 0, 0, 0
FROM next_generation
ON CONFLICT (team_id, source_kind, projection_format_version, generation) DO NOTHING;

WITH active_generation AS (
    SELECT DISTINCT ON (team_id)
           team_id,
           projection_generation_id
    FROM search_projection_generations
    WHERE source_kind = 'relationship'
      AND projection_format_version = 2
      AND state = 'projecting_text'
    ORDER BY team_id, generation DESC
),
rendered AS (
    SELECT relationship.team_id,
           relationship.owner_profile_id,
           relationship.relationship_id,
           relationship.version AS source_version,
           active_generation.projection_generation_id,
           concat_ws(E'\n',
               'relationship',
               'subject: ' || COALESCE(NULLIF(subject_name.display_name, ''), relationship.subject_entity_id::text),
               'predicate: ' || replace(relationship.predicate_key, '_', ' '),
               'object: ' || COALESCE(NULLIF(object_name.display_name, ''), NULLIF(value_record.display, ''), value_record.canonical_value, relationship.object_entity_id::text, relationship.object_value_id::text),
               CASE relationship.polarity
                   WHEN '-' THEN 'polarity: negative'
                   ELSE 'polarity: positive'
               END,
               CASE
                   WHEN relationship.scope_key IS NULL OR btrim(relationship.scope_key) = '' THEN NULL
                   ELSE 'scope: ' || relationship.scope_key
               END,
               CASE
                   WHEN relationship.valid_from IS NULL THEN NULL
                   ELSE 'valid_from: ' || regexp_replace(
                       to_char(relationship.valid_from AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
                       '\.?0+Z$',
                       'Z'
                   )
               END,
               CASE
                   WHEN relationship.valid_to IS NULL THEN NULL
                   ELSE 'valid_to: ' || regexp_replace(
                       to_char(relationship.valid_to AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
                       '\.?0+Z$',
                       'Z'
                   )
               END
           ) AS document_text
    FROM relationship_records AS relationship
    JOIN active_generation
      ON active_generation.team_id = relationship.team_id
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
    WHERE relationship.identity_alias_of_relationship_id IS NULL
      AND relationship.status = 'active'
      AND relationship.support_count > 0
),
active_contract AS (
    SELECT contract.embedding_contract_id,
           contract.dimensions AS embedding_dimensions
    FROM search_index_generations AS generation
    JOIN embedding_contracts AS contract
      ON contract.embedding_contract_id = generation.embedding_contract_id
     AND contract.dimensions = generation.embedding_dimensions
    WHERE generation.activation_state = 'active'
      AND contract.lifecycle_state = 'active'
      AND contract.distance_metric = 'cosine'
    ORDER BY contract.version DESC, generation.generation DESC, generation.created_at DESC
    LIMIT 1
),
upserted AS (
    INSERT INTO search_documents (
        team_id, owner_profile_id, source_kind, source_id, source_version,
        projection_format_version, projection_generation_id, document_version,
        embedding_contract_id, embedding_dimensions, search_state,
        document_text, document_hash, metadata
    )
    SELECT rendered.team_id,
           rendered.owner_profile_id,
           'relationship',
           rendered.relationship_id,
           rendered.source_version,
           2,
           rendered.projection_generation_id,
           1,
           active_contract.embedding_contract_id,
           active_contract.embedding_dimensions,
           'pending',
           rendered.document_text,
           encode(digest(rendered.document_text, 'sha256'), 'hex'),
           jsonb_build_object('projection_format_version', 2, 'projection_migration', '2026072701')
    FROM rendered
    CROSS JOIN active_contract
    ON CONFLICT (team_id, source_kind, source_id, embedding_contract_id)
    DO UPDATE SET
        owner_profile_id = EXCLUDED.owner_profile_id,
        source_version = EXCLUDED.source_version,
        projection_format_version = EXCLUDED.projection_format_version,
        projection_generation_id = EXCLUDED.projection_generation_id,
        document_version = CASE
            WHEN search_documents.document_hash = EXCLUDED.document_hash
             AND search_documents.projection_format_version = EXCLUDED.projection_format_version
            THEN search_documents.document_version
            ELSE search_documents.document_version + 1
        END,
        search_state = CASE
            WHEN search_documents.document_hash = EXCLUDED.document_hash
             AND search_documents.projection_format_version = EXCLUDED.projection_format_version
             AND search_documents.search_state = 'current'
            THEN 'current'
            ELSE 'pending'
        END,
        document_text = EXCLUDED.document_text,
        document_hash = EXCLUDED.document_hash,
        embedding = CASE
            WHEN search_documents.document_hash = EXCLUDED.document_hash
             AND search_documents.projection_format_version = EXCLUDED.projection_format_version
            THEN search_documents.embedding
            ELSE NULL
        END,
        embedding_updated_at = CASE
            WHEN search_documents.document_hash = EXCLUDED.document_hash
             AND search_documents.projection_format_version = EXCLUDED.projection_format_version
            THEN search_documents.embedding_updated_at
            ELSE NULL
        END,
        embedding_error = CASE
            WHEN search_documents.document_hash = EXCLUDED.document_hash
             AND search_documents.projection_format_version = EXCLUDED.projection_format_version
            THEN search_documents.embedding_error
            ELSE ''
        END,
        metadata = search_documents.metadata || EXCLUDED.metadata,
        updated_at = now()
    RETURNING team_id, search_document_id, owner_profile_id, source_kind, source_id,
              source_version, projection_format_version, projection_generation_id,
              document_version, embedding_contract_id, embedding_dimensions, search_state
),
queued_jobs AS (
    INSERT INTO embedding_jobs (
        team_id, search_document_id, owner_profile_id, source_kind, source_id,
        source_version, projection_format_version, projection_generation_id,
        document_version, embedding_contract_id, embedding_dimensions, max_attempts
    )
    SELECT team_id, search_document_id, owner_profile_id, source_kind, source_id,
           source_version, projection_format_version, projection_generation_id,
           document_version, embedding_contract_id, embedding_dimensions, 20
    FROM upserted
    WHERE search_state = 'pending'
    ON CONFLICT (
        team_id, source_kind, source_id, source_version,
        document_version, embedding_contract_id
    ) DO NOTHING
    RETURNING team_id, projection_generation_id
),
projected_counts AS (
    SELECT generation.team_id,
           generation.projection_generation_id,
           count(upserted.search_document_id) AS projected_count,
           count(upserted.search_document_id) FILTER (WHERE upserted.search_state = 'current') AS current_vector_count,
           max(upserted.source_id::text)::uuid AS last_projected_source_id
    FROM search_projection_generations AS generation
    LEFT JOIN upserted
      ON upserted.team_id = generation.team_id
     AND upserted.source_kind = 'relationship'
     AND upserted.projection_format_version = 2
     AND upserted.projection_generation_id = generation.projection_generation_id
    WHERE generation.source_kind = 'relationship'
      AND generation.projection_format_version = 2
      AND generation.state = 'projecting_text'
    GROUP BY generation.team_id, generation.projection_generation_id
)
UPDATE search_projection_generations AS generation
SET state = CASE
        WHEN generation.eligible_count = 0
          OR (
              generation.eligible_count = projected_counts.projected_count
              AND projected_counts.projected_count = projected_counts.current_vector_count
          )
            THEN 'current'
        ELSE 'embedding'
    END,
    projected_count = projected_counts.projected_count,
    current_vector_count = projected_counts.current_vector_count,
    last_projected_source_id = projected_counts.last_projected_source_id,
    completed_at = now(),
    activated_at = CASE
        WHEN generation.eligible_count = 0
          OR (
              generation.eligible_count = projected_counts.projected_count
              AND projected_counts.projected_count = projected_counts.current_vector_count
          )
            THEN now()
        ELSE NULL
    END,
    updated_at = now()
FROM projected_counts
WHERE generation.team_id = projected_counts.team_id
  AND generation.projection_generation_id = projected_counts.projection_generation_id;

UPDATE search_documents
SET projection_format_version = 2,
    projection_generation_id = NULL,
    search_state = 'not_required',
    embedding = NULL,
    embedding_updated_at = NULL,
    embedding_error = '',
    updated_at = now()
WHERE source_kind = 'relationship'
  AND NOT EXISTS (
      SELECT 1
      FROM relationship_records AS relationship
      WHERE relationship.team_id = search_documents.team_id
        AND relationship.relationship_id = search_documents.source_id
        AND relationship.identity_alias_of_relationship_id IS NULL
        AND relationship.status = 'active'
        AND relationship.support_count > 0
  );

UPDATE search_documents
SET projection_format_version = 1
WHERE source_kind <> 'relationship'
  AND projection_format_version <> 1;

UPDATE embedding_jobs AS job
SET projection_format_version = document.projection_format_version,
    projection_generation_id = document.projection_generation_id,
    updated_at = now()
FROM search_documents AS document
WHERE job.team_id = document.team_id
  AND job.search_document_id = document.search_document_id
  AND (
      job.projection_format_version IS DISTINCT FROM document.projection_format_version
      OR job.projection_generation_id IS DISTINCT FROM document.projection_generation_id
  );

DROP VIEW IF EXISTS semantic_edges;

DROP INDEX IF EXISTS relationship_records_active_one_current_unique;
DROP INDEX IF EXISTS relationship_records_active_subject_idx;
DROP INDEX IF EXISTS relationship_records_active_object_entity_idx;
DROP INDEX IF EXISTS relationship_records_active_object_value_idx;
DROP INDEX IF EXISTS relationship_records_active_one_current_canonical_unique;

ALTER TABLE relationship_transition_events
    DROP CONSTRAINT IF EXISTS relationship_transitions_tier_check,
    DROP COLUMN IF EXISTS from_tier,
    DROP COLUMN IF EXISTS to_tier;

ALTER TABLE relationship_records
    DROP CONSTRAINT IF EXISTS relationship_records_active_tier_check,
    DROP CONSTRAINT IF EXISTS relationship_records_tier_check,
    DROP COLUMN IF EXISTS tier;

DO $$
DECLARE
    duplicate_count BIGINT;
BEGIN
    SELECT count(*)
    INTO duplicate_count
    FROM (
        SELECT team_id,
               owner_profile_id,
               subject_entity_id,
               predicate_key,
               polarity,
               valid_from,
               scope_key
        FROM relationship_records
        WHERE identity_alias_of_relationship_id IS NULL
          AND current_cardinality = 'one'
          AND status = 'active'
          AND support_count > 0
        GROUP BY team_id,
                 owner_profile_id,
                 subject_entity_id,
                 predicate_key,
                 polarity,
                 valid_from,
                 scope_key
        HAVING count(*) > 1
    ) AS duplicates;

    IF duplicate_count > 0 THEN
        RAISE EXCEPTION 'cannot create relationship_records_active_one_current_canonical_unique: % duplicate active supported one-cardinality canonical groups exist', duplicate_count;
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS relationship_records_active_one_current_canonical_unique
    ON relationship_records (
        team_id, owner_profile_id, subject_entity_id, predicate_key,
        polarity, valid_from, scope_key
    )
    NULLS NOT DISTINCT
    WHERE identity_alias_of_relationship_id IS NULL
      AND current_cardinality = 'one'
      AND status = 'active'
      AND support_count > 0;

CREATE INDEX IF NOT EXISTS relationship_records_active_supported_team_idx
    ON relationship_records(team_id)
    WHERE identity_alias_of_relationship_id IS NULL
      AND status = 'active'
      AND support_count > 0;

CREATE INDEX IF NOT EXISTS relationship_records_active_subject_idx
    ON relationship_records(team_id, subject_entity_id, predicate_key)
    WHERE identity_alias_of_relationship_id IS NULL
      AND status = 'active'
      AND support_count > 0;

CREATE INDEX IF NOT EXISTS relationship_records_active_object_entity_idx
    ON relationship_records(team_id, object_entity_id, predicate_key)
    WHERE identity_alias_of_relationship_id IS NULL
      AND status = 'active'
      AND support_count > 0
      AND object_entity_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS relationship_records_active_object_value_idx
    ON relationship_records(team_id, object_value_id, predicate_key)
    WHERE identity_alias_of_relationship_id IS NULL
      AND status = 'active'
      AND support_count > 0
      AND object_value_id IS NOT NULL;

CREATE VIEW semantic_edges
WITH (security_invoker = true) AS
SELECT relationship_id,
       team_id,
       owner_profile_id,
       semantic_group_key,
       subject_entity_id,
       predicate_key,
       predicate_version,
       object_entity_id,
       object_value_id,
       relationship_kind,
       current_cardinality,
       polarity,
       scope_key,
       valid_from,
       valid_to,
       support_count,
       source_group_count,
       version
FROM relationship_records
WHERE identity_alias_of_relationship_id IS NULL
  AND status = 'active'
  AND support_count > 0;

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $$
BEGIN
    RAISE EXCEPTION 'irreversible migration: V2.3 removes relationship tier history and reprojects relationship search documents';
END $$;

-- +goose StatementEnd
