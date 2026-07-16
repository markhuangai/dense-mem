-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS semantic_team_refs (
    team_id UUID PRIMARY KEY REFERENCES teams(id) ON DELETE CASCADE,
    name TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS semantic_profile_refs (
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    profile_id UUID NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, profile_id)
);

CREATE TABLE IF NOT EXISTS semantic_evidence_fragments (
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    fragment_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    content TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT '',
    source_doc_id TEXT NOT NULL DEFAULT '',
    source_group TEXT NOT NULL DEFAULT '',
    source_type TEXT NOT NULL DEFAULT 'conversation',
    authority TEXT NOT NULL DEFAULT 'primary',
    labels TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    content_hash TEXT NOT NULL,
    idempotency_key TEXT NOT NULL DEFAULT '',
    search_state TEXT NOT NULL DEFAULT 'pending',
    search_document_version BIGINT NOT NULL DEFAULT 1,
    embedding_contract_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, fragment_id),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT semantic_evidence_content_nonempty CHECK (btrim(content) <> ''),
    CONSTRAINT semantic_evidence_source_type_check CHECK (source_type IN ('conversation', 'document', 'observation', 'manual')),
    CONSTRAINT semantic_evidence_search_state_check CHECK (search_state IN ('not_required', 'pending', 'current', 'failed'))
);

CREATE UNIQUE INDEX IF NOT EXISTS semantic_evidence_idempotency_unique
    ON semantic_evidence_fragments(team_id, idempotency_key)
    WHERE idempotency_key <> '';

CREATE INDEX IF NOT EXISTS semantic_evidence_source_doc_idx
    ON semantic_evidence_fragments(team_id, source_doc_id)
    WHERE source_doc_id <> '';

CREATE INDEX IF NOT EXISTS semantic_evidence_content_fts_idx
    ON semantic_evidence_fragments
    USING gin (to_tsvector('english', content));

CREATE TABLE IF NOT EXISTS semantic_entities (
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    entity_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    kind TEXT NOT NULL DEFAULT 'unknown',
    canonical_name TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'active',
    version BIGINT NOT NULL DEFAULT 1,
    search_state TEXT NOT NULL DEFAULT 'pending',
    search_document_version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, entity_id),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT semantic_entities_name_nonempty CHECK (btrim(canonical_name) <> ''),
    CONSTRAINT semantic_entities_kind_check CHECK (kind IN ('unknown', 'person', 'organization', 'project', 'product', 'place', 'document', 'concept')),
    CONSTRAINT semantic_entities_status_check CHECK (status IN ('active', 'merged', 'retracted')),
    CONSTRAINT semantic_entities_search_state_check CHECK (search_state IN ('not_required', 'pending', 'current', 'failed'))
);

CREATE UNIQUE INDEX IF NOT EXISTS semantic_entities_name_unique
    ON semantic_entities(team_id, lower(canonical_name), kind)
    WHERE status = 'active';

CREATE TABLE IF NOT EXISTS semantic_entity_names (
    team_id UUID NOT NULL,
    entity_id UUID NOT NULL,
    name_id UUID NOT NULL DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    source_fragment_id UUID NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, name_id),
    FOREIGN KEY (team_id, entity_id) REFERENCES semantic_entities(team_id, entity_id) ON DELETE CASCADE,
    FOREIGN KEY (team_id, source_fragment_id) REFERENCES semantic_evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
    CONSTRAINT semantic_entity_names_nonempty CHECK (btrim(name) <> '')
);

CREATE UNIQUE INDEX IF NOT EXISTS semantic_entity_names_unique
    ON semantic_entity_names(team_id, entity_id, lower(name));

CREATE TABLE IF NOT EXISTS semantic_values (
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    value_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    value_type TEXT NOT NULL DEFAULT 'string',
    canonical_value TEXT NOT NULL,
    display_value TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'active',
    version BIGINT NOT NULL DEFAULT 1,
    search_state TEXT NOT NULL DEFAULT 'pending',
    search_document_version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, value_id),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT semantic_values_type_check CHECK (value_type IN ('string', 'number', 'boolean', 'date', 'date_time')),
    CONSTRAINT semantic_values_canonical_nonempty CHECK (btrim(canonical_value) <> ''),
    CONSTRAINT semantic_values_display_nonempty CHECK (btrim(display_value) <> ''),
    CONSTRAINT semantic_values_status_check CHECK (status IN ('active', 'retracted')),
    CONSTRAINT semantic_values_search_state_check CHECK (search_state IN ('not_required', 'pending', 'current', 'failed'))
);

CREATE UNIQUE INDEX IF NOT EXISTS semantic_values_identity_unique
    ON semantic_values(team_id, value_type, canonical_value)
    WHERE status = 'active';

CREATE TABLE IF NOT EXISTS semantic_relationship_records (
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    relationship_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    subject_entity_id UUID NOT NULL,
    predicate TEXT NOT NULL,
    polarity TEXT NOT NULL DEFAULT '+',
    object_entity_id UUID NULL,
    object_value_id UUID NULL,
    tier TEXT NOT NULL DEFAULT 'candidate',
    status TEXT NOT NULL DEFAULT 'pending_evidence',
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    support_count INTEGER NOT NULL DEFAULT 0,
    source_group_count INTEGER NOT NULL DEFAULT 0,
    semantic_group_key TEXT NOT NULL DEFAULT '',
    version BIGINT NOT NULL DEFAULT 1,
    search_state TEXT NOT NULL DEFAULT 'pending',
    search_document_version BIGINT NOT NULL DEFAULT 1,
    embedding_contract_id TEXT NOT NULL DEFAULT '',
    valid_from TIMESTAMPTZ NULL,
    valid_to TIMESTAMPTZ NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    recorded_to TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, relationship_id),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, subject_entity_id) REFERENCES semantic_entities(team_id, entity_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, object_entity_id) REFERENCES semantic_entities(team_id, entity_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, object_value_id) REFERENCES semantic_values(team_id, value_id) ON DELETE RESTRICT,
    CONSTRAINT semantic_relationship_predicate_nonempty CHECK (btrim(predicate) <> ''),
    CONSTRAINT semantic_relationship_polarity_check CHECK (polarity IN ('+', '-')),
    CONSTRAINT semantic_relationship_object_exactly_one CHECK ((object_entity_id IS NULL) <> (object_value_id IS NULL)),
    CONSTRAINT semantic_relationship_tier_check CHECK (tier IN ('candidate', 'validated_claim', 'fact')),
    CONSTRAINT semantic_relationship_status_check CHECK (status IN ('pending_evidence', 'active', 'needs_review', 'quarantined', 'disputed', 'rejected', 'retracted', 'superseded')),
    CONSTRAINT semantic_relationship_tier_status_check CHECK (
        (tier = 'candidate' AND status <> 'active') OR
        (tier IN ('validated_claim', 'fact') AND status = 'active')
    ),
    CONSTRAINT semantic_relationship_confidence_check CHECK (confidence >= 0 AND confidence <= 1),
    CONSTRAINT semantic_relationship_valid_range_check CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to > valid_from),
    CONSTRAINT semantic_relationship_search_state_check CHECK (search_state IN ('not_required', 'pending', 'current', 'failed'))
);

CREATE INDEX IF NOT EXISTS semantic_relationship_lookup_idx
    ON semantic_relationship_records(team_id, predicate, tier, status, updated_at DESC);

CREATE INDEX IF NOT EXISTS semantic_relationship_subject_adjacency_idx
    ON semantic_relationship_records(team_id, subject_entity_id, updated_at DESC, relationship_id)
    WHERE status = 'active' AND tier IN ('validated_claim', 'fact');

CREATE INDEX IF NOT EXISTS semantic_relationship_object_adjacency_idx
    ON semantic_relationship_records(team_id, object_entity_id, updated_at DESC, relationship_id)
    WHERE status = 'active' AND tier IN ('validated_claim', 'fact') AND object_entity_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS semantic_relationship_object_value_adjacency_idx
    ON semantic_relationship_records(team_id, object_value_id, updated_at DESC, relationship_id)
    WHERE status = 'active' AND tier IN ('validated_claim', 'fact') AND object_value_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS semantic_relationship_group_idx
    ON semantic_relationship_records(team_id, semantic_group_key, updated_at DESC, relationship_id)
    WHERE status = 'active' AND tier IN ('validated_claim', 'fact');

CREATE UNIQUE INDEX IF NOT EXISTS semantic_relationship_identity_unique
    ON semantic_relationship_records(team_id, owner_profile_id, subject_entity_id, predicate, polarity,
        COALESCE(object_entity_id, '00000000-0000-0000-0000-000000000000'::uuid),
        COALESCE(object_value_id, '00000000-0000-0000-0000-000000000000'::uuid));

CREATE TABLE IF NOT EXISTS semantic_relationship_supports (
    team_id UUID NOT NULL,
    relationship_id UUID NOT NULL,
    fragment_id UUID NOT NULL,
    evidence_index INTEGER NOT NULL DEFAULT 0,
    span_start INTEGER NOT NULL DEFAULT 0,
    span_end INTEGER NOT NULL DEFAULT 0,
    source_group TEXT NOT NULL DEFAULT '',
    quote TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, relationship_id, fragment_id, span_start, span_end),
    FOREIGN KEY (team_id, relationship_id) REFERENCES semantic_relationship_records(team_id, relationship_id) ON DELETE CASCADE,
    FOREIGN KEY (team_id, fragment_id) REFERENCES semantic_evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
    CONSTRAINT semantic_relationship_support_span_check CHECK (span_start >= 0 AND span_end >= span_start)
);

CREATE INDEX IF NOT EXISTS semantic_relationship_supports_evidence_idx
    ON semantic_relationship_supports(team_id, fragment_id, relationship_id);

CREATE TABLE IF NOT EXISTS semantic_relationship_observations (
    team_id UUID NOT NULL,
    observation_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    fragment_id UUID NOT NULL,
    subject_text TEXT NOT NULL,
    predicate_text TEXT NOT NULL,
    object_text TEXT NOT NULL,
    evidence_index INTEGER NOT NULL DEFAULT 0,
    span_start INTEGER NOT NULL DEFAULT 0,
    span_end INTEGER NOT NULL DEFAULT 0,
    extraction_model TEXT NOT NULL DEFAULT '',
    extraction_raw JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, observation_id),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, fragment_id) REFERENCES semantic_evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
    CONSTRAINT semantic_observation_predicate_nonempty CHECK (btrim(predicate_text) <> ''),
    CONSTRAINT semantic_observation_span_check CHECK (span_start >= 0 AND span_end >= span_start)
);

CREATE TABLE IF NOT EXISTS semantic_verification_events (
    team_id UUID NOT NULL,
    verification_event_id UUID NOT NULL DEFAULT gen_random_uuid(),
    relationship_id UUID NULL,
    observation_id UUID NULL,
    verifier_model TEXT NOT NULL DEFAULT '',
    evidence_verdict TEXT NOT NULL,
    knowledge_alignment TEXT NOT NULL DEFAULT '',
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    rationale TEXT NOT NULL DEFAULT '',
    raw_response JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, verification_event_id),
    FOREIGN KEY (team_id, relationship_id) REFERENCES semantic_relationship_records(team_id, relationship_id) ON DELETE CASCADE,
    FOREIGN KEY (team_id, observation_id) REFERENCES semantic_relationship_observations(team_id, observation_id) ON DELETE RESTRICT,
    CONSTRAINT semantic_verification_verdict_check CHECK (evidence_verdict IN ('entailed', 'contradicted', 'insufficient')),
    CONSTRAINT semantic_verification_confidence_check CHECK (confidence >= 0 AND confidence <= 1)
);

CREATE TABLE IF NOT EXISTS semantic_relationship_events (
    team_id UUID NOT NULL,
    event_id UUID NOT NULL DEFAULT gen_random_uuid(),
    relationship_id UUID NOT NULL,
    event_type TEXT NOT NULL,
    from_tier TEXT NOT NULL DEFAULT '',
    to_tier TEXT NOT NULL DEFAULT '',
    from_status TEXT NOT NULL DEFAULT '',
    to_status TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    actor_profile_id UUID NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, event_id),
    FOREIGN KEY (team_id, relationship_id) REFERENCES semantic_relationship_records(team_id, relationship_id) ON DELETE CASCADE,
    FOREIGN KEY (team_id, actor_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT semantic_relationship_event_type_nonempty CHECK (btrim(event_type) <> '')
);

CREATE INDEX IF NOT EXISTS semantic_relationship_events_relationship_idx
    ON semantic_relationship_events(team_id, relationship_id, occurred_at ASC, event_id ASC);

CREATE TABLE IF NOT EXISTS semantic_search_documents (
    team_id UUID NOT NULL,
    search_document_id UUID NOT NULL DEFAULT gen_random_uuid(),
    source_type TEXT NOT NULL,
    source_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    document_text TEXT NOT NULL,
    source_version BIGINT NOT NULL DEFAULT 1,
    document_version BIGINT NOT NULL DEFAULT 1,
    embedding vector(3072),
    embedding_model TEXT NOT NULL DEFAULT '',
    embedding_contract_id TEXT NOT NULL DEFAULT '',
    search_state TEXT NOT NULL DEFAULT 'pending',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, search_document_id),
    UNIQUE (team_id, source_type, source_id, document_version),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT semantic_search_source_type_check CHECK (source_type IN ('evidence', 'relationship', 'entity', 'value')),
    CONSTRAINT semantic_search_text_nonempty CHECK (btrim(document_text) <> ''),
    CONSTRAINT semantic_search_state_check CHECK (search_state IN ('not_required', 'pending', 'current', 'failed')),
    CONSTRAINT semantic_search_embedding_dims_check CHECK (embedding IS NULL OR vector_dims(embedding) = 3072)
);

CREATE INDEX IF NOT EXISTS semantic_search_documents_fts_idx
    ON semantic_search_documents
    USING gin (to_tsvector('english', document_text));

CREATE INDEX IF NOT EXISTS semantic_search_documents_source_idx
    ON semantic_search_documents(team_id, source_type, source_id);

CREATE INDEX IF NOT EXISTS semantic_search_documents_vector_filter_idx
    ON semantic_search_documents(team_id, source_type, search_state, source_id)
    WHERE embedding IS NOT NULL;

CREATE INDEX IF NOT EXISTS semantic_search_documents_evidence_hnsw_idx
    ON semantic_search_documents
    USING hnsw ((embedding::halfvec(3072)) halfvec_cosine_ops)
    WHERE embedding IS NOT NULL AND search_state = 'current' AND source_type = 'evidence';

CREATE INDEX IF NOT EXISTS semantic_search_documents_relationship_hnsw_idx
    ON semantic_search_documents
    USING hnsw ((embedding::halfvec(3072)) halfvec_cosine_ops)
    WHERE embedding IS NOT NULL AND search_state = 'current' AND source_type = 'relationship';

CREATE INDEX IF NOT EXISTS semantic_search_documents_entity_hnsw_idx
    ON semantic_search_documents
    USING hnsw ((embedding::halfvec(3072)) halfvec_cosine_ops)
    WHERE embedding IS NOT NULL AND search_state = 'current' AND source_type = 'entity';

CREATE INDEX IF NOT EXISTS semantic_search_documents_value_hnsw_idx
    ON semantic_search_documents
    USING hnsw ((embedding::halfvec(3072)) halfvec_cosine_ops)
    WHERE embedding IS NOT NULL AND search_state = 'current' AND source_type = 'value';

CREATE TABLE IF NOT EXISTS semantic_embedding_jobs (
    team_id UUID NOT NULL,
    job_id UUID NOT NULL DEFAULT gen_random_uuid(),
    search_document_id UUID NOT NULL,
    source_type TEXT NOT NULL,
    source_id UUID NOT NULL,
    document_version BIGINT NOT NULL,
    embedding_contract_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'queued',
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_until TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ NULL,
    PRIMARY KEY (team_id, job_id),
    FOREIGN KEY (team_id, search_document_id) REFERENCES semantic_search_documents(team_id, search_document_id) ON DELETE CASCADE,
    CONSTRAINT semantic_embedding_job_status_check CHECK (status IN ('queued', 'processing', 'completed', 'failed')),
    CONSTRAINT semantic_embedding_job_attempts_check CHECK (attempts >= 0)
);

CREATE INDEX IF NOT EXISTS semantic_embedding_jobs_claim_idx
    ON semantic_embedding_jobs(status, available_at, created_at);

CREATE INDEX IF NOT EXISTS semantic_embedding_jobs_expired_lease_idx
    ON semantic_embedding_jobs(lease_until, created_at)
    WHERE status = 'processing';

CREATE UNIQUE INDEX IF NOT EXISTS semantic_embedding_jobs_active_unique
    ON semantic_embedding_jobs(team_id, search_document_id, document_version)
    WHERE status IN ('queued', 'processing');

CREATE TABLE IF NOT EXISTS semantic_hypotheses (
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    hypothesis_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    text TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'proposed',
    source_refs JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, hypothesis_id),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT semantic_hypotheses_text_nonempty CHECK (btrim(text) <> ''),
    CONSTRAINT semantic_hypotheses_status_check CHECK (status IN ('proposed', 'submitted', 'rejected', 'stale', 'reinforced'))
);

CREATE OR REPLACE VIEW semantic_edges AS
SELECT
    r.team_id,
    r.relationship_id,
    r.subject_entity_id,
    s.canonical_name AS subject_name,
    r.predicate,
    r.polarity,
    r.object_entity_id,
    o.canonical_name AS object_name,
    r.object_value_id,
    v.display_value AS object_value,
    v.value_type AS object_value_type,
    r.tier,
    r.status,
    r.confidence,
    r.updated_at
FROM semantic_relationship_records r
JOIN semantic_entities s
  ON s.team_id = r.team_id AND s.entity_id = r.subject_entity_id
LEFT JOIN semantic_entities o
  ON o.team_id = r.team_id AND o.entity_id = r.object_entity_id
LEFT JOIN semantic_values v
  ON v.team_id = r.team_id AND v.value_id = r.object_value_id
WHERE r.status = 'active'
  AND r.tier IN ('validated_claim', 'fact');

CREATE OR REPLACE FUNCTION prevent_semantic_append_only_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is append-only: % operations are not allowed', TG_TABLE_NAME, TG_OP;
END;
$$;

DROP TRIGGER IF EXISTS semantic_relationship_events_append_only ON semantic_relationship_events;
CREATE TRIGGER semantic_relationship_events_append_only
    BEFORE UPDATE OR DELETE ON semantic_relationship_events
    FOR EACH ROW
    EXECUTE FUNCTION prevent_semantic_append_only_mutation();

DO $$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'semantic_team_refs',
        'semantic_profile_refs',
        'semantic_evidence_fragments',
        'semantic_entities',
        'semantic_entity_names',
        'semantic_values',
        'semantic_relationship_records',
        'semantic_relationship_supports',
        'semantic_relationship_observations',
        'semantic_verification_events',
        'semantic_relationship_events',
        'semantic_search_documents',
        'semantic_embedding_jobs',
        'semantic_hypotheses'
    ]
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_team_access', table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR ALL USING (
                current_setting(''app.tx_mode'', true) = ''system''
                OR team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
            ) WITH CHECK (
                current_setting(''app.tx_mode'', true) = ''system''
                OR team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
            )',
            table_name || '_team_access',
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

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DROP VIEW IF EXISTS semantic_edges;
DROP TRIGGER IF EXISTS semantic_relationship_events_append_only ON semantic_relationship_events;
DROP FUNCTION IF EXISTS prevent_semantic_append_only_mutation();
DROP TABLE IF EXISTS semantic_hypotheses;
DROP TABLE IF EXISTS semantic_embedding_jobs;
DROP TABLE IF EXISTS semantic_search_documents;
DROP TABLE IF EXISTS semantic_relationship_events;
DROP TABLE IF EXISTS semantic_verification_events;
DROP TABLE IF EXISTS semantic_relationship_observations;
DROP TABLE IF EXISTS semantic_relationship_supports;
DROP TABLE IF EXISTS semantic_relationship_records;
DROP TABLE IF EXISTS semantic_values;
DROP TABLE IF EXISTS semantic_entity_names;
DROP TABLE IF EXISTS semantic_entities;
DROP TABLE IF EXISTS semantic_evidence_fragments;
DROP TABLE IF EXISTS semantic_profile_refs;
DROP TABLE IF EXISTS semantic_team_refs;

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd
