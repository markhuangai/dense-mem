-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS embedding_contracts (
    embedding_contract_id UUID NOT NULL DEFAULT gen_random_uuid(),
    contract_key TEXT NOT NULL,
    version INTEGER NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    dimensions INTEGER NOT NULL,
    distance_metric TEXT NOT NULL DEFAULT 'cosine',
    vector_normalization TEXT NOT NULL DEFAULT 'provider',
    document_format_version INTEGER NOT NULL DEFAULT 1,
    query_format_version INTEGER NOT NULL DEFAULT 1,
    lifecycle_state TEXT NOT NULL DEFAULT 'active',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (embedding_contract_id),
    UNIQUE (contract_key, version),
    UNIQUE (embedding_contract_id, dimensions),
    CONSTRAINT embedding_contracts_key_nonempty CHECK (btrim(contract_key) <> ''),
    CONSTRAINT embedding_contracts_version_check CHECK (version >= 1),
    CONSTRAINT embedding_contracts_provider_nonempty CHECK (btrim(provider) <> ''),
    CONSTRAINT embedding_contracts_model_nonempty CHECK (btrim(model) <> ''),
    CONSTRAINT embedding_contracts_dimensions_check CHECK (dimensions BETWEEN 1 AND 16000),
    CONSTRAINT embedding_contracts_distance_check CHECK (distance_metric = 'cosine'),
    CONSTRAINT embedding_contracts_normalization_check CHECK (vector_normalization IN ('provider', 'unit', 'none')),
    CONSTRAINT embedding_contracts_format_check CHECK (document_format_version >= 1 AND query_format_version >= 1),
    CONSTRAINT embedding_contracts_lifecycle_check CHECK (lifecycle_state IN ('active', 'deprecated', 'retired')),
    CONSTRAINT embedding_contracts_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

DROP TRIGGER IF EXISTS embedding_contracts_reference_guard ON embedding_contracts;
CREATE TRIGGER embedding_contracts_reference_guard
    BEFORE INSERT OR UPDATE OR DELETE ON embedding_contracts
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_reference_definition_mutation();

CREATE TABLE IF NOT EXISTS search_index_generations (
    search_index_generation_id UUID NOT NULL DEFAULT gen_random_uuid(),
    generation INTEGER NOT NULL,
    embedding_contract_id UUID NOT NULL,
    embedding_dimensions INTEGER NOT NULL,
    ann_strategy TEXT NOT NULL DEFAULT 'exact',
    operator_class TEXT NOT NULL DEFAULT '',
    indexed_expression TEXT NOT NULL DEFAULT '',
    physical_index_name TEXT NOT NULL DEFAULT '',
    hnsw_m INTEGER NOT NULL DEFAULT 16,
    hnsw_ef_construction INTEGER NOT NULL DEFAULT 64,
    query_ef_search INTEGER NOT NULL DEFAULT 40,
    exact_max_rows INTEGER NOT NULL DEFAULT 10000,
    candidate_limit INTEGER NOT NULL DEFAULT 200,
    allow_exact_fallback BOOLEAN NOT NULL DEFAULT false,
    activation_state TEXT NOT NULL DEFAULT 'building',
    failure_reason TEXT NOT NULL DEFAULT '',
    activated_at TIMESTAMPTZ NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (search_index_generation_id),
    UNIQUE (embedding_contract_id, generation),
    UNIQUE (search_index_generation_id, embedding_contract_id),
    FOREIGN KEY (embedding_contract_id, embedding_dimensions)
        REFERENCES embedding_contracts(embedding_contract_id, dimensions) ON DELETE RESTRICT,
    CONSTRAINT search_index_generations_generation_check CHECK (generation >= 1),
    CONSTRAINT search_index_generations_strategy_check CHECK (ann_strategy IN ('exact', 'vector_hnsw', 'halfvec_hnsw')),
    CONSTRAINT search_index_generations_hnsw_positive CHECK (
        hnsw_m > 0
        AND hnsw_ef_construction > 0
        AND query_ef_search > 0
        AND exact_max_rows > 0
        AND candidate_limit > 0
    ),
    CONSTRAINT search_index_generations_state_check CHECK (activation_state IN ('building', 'active', 'failed', 'deprecated', 'retired')),
    CONSTRAINT search_index_generations_active_time_check CHECK (
        (activation_state = 'active' AND activated_at IS NOT NULL)
        OR activation_state <> 'active'
    ),
    CONSTRAINT search_index_generations_hnsw_index_check CHECK (
        ann_strategy = 'exact'
        OR (btrim(physical_index_name) <> '' AND btrim(operator_class) <> '' AND btrim(indexed_expression) <> '')
    ),
    CONSTRAINT search_index_generations_dimension_strategy_check CHECK (
        ann_strategy = 'exact'
        OR (ann_strategy = 'vector_hnsw' AND embedding_dimensions BETWEEN 1 AND 2000)
        OR (ann_strategy = 'halfvec_hnsw' AND embedding_dimensions BETWEEN 1 AND 4000)
    ),
    CONSTRAINT search_index_generations_operator_class_check CHECK (
        ann_strategy = 'exact'
        OR (ann_strategy = 'vector_hnsw' AND operator_class = 'vector_cosine_ops')
        OR (ann_strategy = 'halfvec_hnsw' AND operator_class = 'halfvec_cosine_ops')
    ),
    CONSTRAINT search_index_generations_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

DROP TRIGGER IF EXISTS search_index_generations_reference_guard ON search_index_generations;
CREATE TRIGGER search_index_generations_reference_guard
    BEFORE INSERT OR UPDATE OR DELETE ON search_index_generations
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_reference_definition_mutation();

CREATE TABLE IF NOT EXISTS search_documents (
    team_id UUID NOT NULL,
    search_document_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    source_kind TEXT NOT NULL,
    source_id UUID NOT NULL,
    source_version BIGINT NOT NULL,
    document_version BIGINT NOT NULL DEFAULT 1,
    embedding_contract_id UUID NOT NULL,
    embedding_dimensions INTEGER NOT NULL,
    search_state TEXT NOT NULL DEFAULT 'pending',
    document_text TEXT NOT NULL,
    document_hash TEXT NOT NULL,
    search_tsv TSVECTOR GENERATED ALWAYS AS (to_tsvector('simple', document_text)) STORED,
    embedding vector NULL,
    embedding_updated_at TIMESTAMPTZ NULL,
    embedding_error TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, search_document_id),
    UNIQUE (team_id, source_kind, source_id, embedding_contract_id),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (embedding_contract_id, embedding_dimensions)
        REFERENCES embedding_contracts(embedding_contract_id, dimensions) ON DELETE RESTRICT,
    CONSTRAINT search_documents_source_kind_check CHECK (source_kind IN ('evidence', 'relationship', 'entity')),
    CONSTRAINT search_documents_source_version_check CHECK (source_version >= 1),
    CONSTRAINT search_documents_document_version_check CHECK (document_version >= 1),
    CONSTRAINT search_documents_state_check CHECK (search_state IN ('not_required', 'pending', 'current', 'failed')),
    CONSTRAINT search_documents_text_nonempty CHECK (btrim(document_text) <> ''),
    CONSTRAINT search_documents_hash_nonempty CHECK (btrim(document_hash) <> ''),
    CONSTRAINT search_documents_embedding_dims_check CHECK (
        embedding IS NULL
        OR vector_dims(embedding) = embedding_dimensions
    ),
    CONSTRAINT search_documents_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS search_documents_source_idx
    ON search_documents(team_id, source_kind, source_id, source_version DESC);

CREATE INDEX IF NOT EXISTS search_documents_state_idx
    ON search_documents(team_id, search_state, updated_at DESC, search_document_id);

CREATE INDEX IF NOT EXISTS search_documents_contract_state_idx
    ON search_documents(team_id, embedding_contract_id, search_state, source_kind);

CREATE INDEX IF NOT EXISTS search_documents_fts_idx
    ON search_documents USING GIN (search_tsv);

CREATE TABLE IF NOT EXISTS embedding_jobs (
    team_id UUID NOT NULL,
    embedding_job_id UUID NOT NULL DEFAULT gen_random_uuid(),
    search_document_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    source_kind TEXT NOT NULL,
    source_id UUID NOT NULL,
    source_version BIGINT NOT NULL,
    document_version BIGINT NOT NULL,
    embedding_contract_id UUID NOT NULL,
    embedding_dimensions INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 5,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_until TIMESTAMPTZ NULL,
    worker_id TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ NULL,
    PRIMARY KEY (team_id, embedding_job_id),
    UNIQUE (
        team_id, source_kind, source_id, source_version,
        document_version, embedding_contract_id
    ),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, search_document_id) REFERENCES search_documents(team_id, search_document_id) ON DELETE RESTRICT,
    FOREIGN KEY (embedding_contract_id, embedding_dimensions)
        REFERENCES embedding_contracts(embedding_contract_id, dimensions) ON DELETE RESTRICT,
    CONSTRAINT embedding_jobs_source_kind_check CHECK (source_kind IN ('evidence', 'relationship', 'entity')),
    CONSTRAINT embedding_jobs_source_version_check CHECK (source_version >= 1),
    CONSTRAINT embedding_jobs_document_version_check CHECK (document_version >= 1),
    CONSTRAINT embedding_jobs_status_check CHECK (status IN ('queued', 'processing', 'completed', 'failed', 'stale', 'cancelled')),
    CONSTRAINT embedding_jobs_attempts_check CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts),
    CONSTRAINT embedding_jobs_terminal_time_check CHECK (
        (status IN ('completed', 'failed', 'stale', 'cancelled') AND completed_at IS NOT NULL)
        OR status NOT IN ('completed', 'failed', 'stale', 'cancelled')
    )
);

CREATE INDEX IF NOT EXISTS embedding_jobs_ready_idx
    ON embedding_jobs(team_id, status, available_at ASC, created_at ASC, embedding_job_id)
    WHERE status IN ('queued', 'failed');

CREATE INDEX IF NOT EXISTS embedding_jobs_lease_idx
    ON embedding_jobs(team_id, lease_until ASC)
    WHERE status = 'processing';

ALTER TABLE search_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE search_documents FORCE ROW LEVEL SECURITY;
ALTER TABLE embedding_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE embedding_jobs FORCE ROW LEVEL SECURITY;

DO $$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY['search_documents', 'embedding_jobs']
    LOOP
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR SELECT USING (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
            )',
            table_name || '_select',
            table_name
        );
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR INSERT WITH CHECK (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) = ''team''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
                OR (
                    current_setting(''app.tx_mode'', true) = ''profile''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND owner_profile_id = nullif(current_setting(''app.current_profile_id'', true), '''')::uuid
                )
            )',
            table_name || '_insert',
            table_name
        );
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR UPDATE USING (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) = ''team''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
                OR (
                    current_setting(''app.tx_mode'', true) = ''profile''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND owner_profile_id = nullif(current_setting(''app.current_profile_id'', true), '''')::uuid
                )
            ) WITH CHECK (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) = ''team''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
                OR (
                    current_setting(''app.tx_mode'', true) = ''profile''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND owner_profile_id = nullif(current_setting(''app.current_profile_id'', true), '''')::uuid
                )
            )',
            table_name || '_update',
            table_name
        );
    END LOOP;
END $$;

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

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

DROP TABLE IF EXISTS embedding_jobs;
DROP TABLE IF EXISTS search_documents;
DROP TABLE IF EXISTS search_index_generations;
DROP TABLE IF EXISTS embedding_contracts;

-- +goose StatementEnd
