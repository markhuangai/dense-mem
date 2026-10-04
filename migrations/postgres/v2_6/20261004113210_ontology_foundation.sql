-- Lock/rewrite impact: creates empty ontology tables and indexes; no canonical
-- table is locked or rewritten beyond catalog metadata locks.
-- RLS impact: FORCE RLS scopes every row to one team-shared space and generation;
-- system/migration access stays separate from request transactions.
-- Backfill: none; bounded explicit seeding is owned by the ontology adapter.
-- Backward compatibility: existing applications ignore the additive schema.
-- Rollback: local Down refuses populated history; production uses roll-forward.

-- +goose Up
-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);

CREATE TABLE ontology_catalog_heads (
    team_id UUID NOT NULL,
    shared_space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL CHECK (space_generation > 0),
    revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
    PRIMARY KEY (team_id, shared_space_id, space_generation),
    FOREIGN KEY (team_id, shared_space_id) REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT,
    CHECK (shared_space_id = dense_mem_team_shared_space(team_id))
);

CREATE TABLE ontology_publications (
    team_id UUID NOT NULL,
    shared_space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL,
    publication_id UUID NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    operation_key TEXT NOT NULL CHECK (length(operation_key) BETWEEN 1 AND 128),
    request_hash TEXT NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
    origin TEXT NOT NULL CHECK (origin IN ('automatic', 'manager', 'seed', 'rollback')),
    actor_id UUID NULL,
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 512),
    rollback_of UUID NULL,
    result JSONB NOT NULL CHECK (jsonb_typeof(result) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, shared_space_id, space_generation, publication_id),
    UNIQUE (team_id, shared_space_id, space_generation, revision),
    UNIQUE (team_id, shared_space_id, space_generation, operation_key),
    FOREIGN KEY (team_id, shared_space_id, space_generation)
        REFERENCES ontology_catalog_heads(team_id, shared_space_id, space_generation) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, shared_space_id, space_generation, rollback_of)
        REFERENCES ontology_publications(team_id, shared_space_id, space_generation, publication_id) ON DELETE RESTRICT,
    CHECK ((origin IN ('manager', 'rollback')) = (actor_id IS NOT NULL))
);

CREATE TABLE ontology_record_revisions (
    team_id UUID NOT NULL,
    shared_space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL,
    record_id UUID NOT NULL,
    version BIGINT NOT NULL CHECK (version > 0),
    kind TEXT NOT NULL CHECK (kind IN (
        'entity_class', 'predicate_concept', 'topic', 'assignment',
        'evidence_group', 'relationship_group', 'override'
    )),
    publication_id UUID NOT NULL,
    retired BOOLEAN NOT NULL,
    body JSONB NOT NULL CHECK (jsonb_typeof(body) = 'object'),
    fingerprint TEXT NOT NULL CHECK (fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, shared_space_id, space_generation, record_id, version),
    FOREIGN KEY (team_id, shared_space_id, space_generation, publication_id)
        REFERENCES ontology_publications(team_id, shared_space_id, space_generation, publication_id) ON DELETE RESTRICT,
    CHECK (octet_length(body::text) <= 65536)
);

CREATE TABLE ontology_record_heads (
    team_id UUID NOT NULL,
    shared_space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL,
    record_id UUID NOT NULL,
    version BIGINT NOT NULL CHECK (version > 0),
    kind TEXT NOT NULL CHECK (kind IN (
        'entity_class', 'predicate_concept', 'topic', 'assignment',
        'evidence_group', 'relationship_group', 'override'
    )),
    retired BOOLEAN NOT NULL,
    names TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    PRIMARY KEY (team_id, shared_space_id, space_generation, record_id),
    FOREIGN KEY (team_id, shared_space_id, space_generation, record_id, version)
        REFERENCES ontology_record_revisions(team_id, shared_space_id, space_generation, record_id, version) ON DELETE RESTRICT,
    CHECK (cardinality(names) <= 22)
);
CREATE INDEX ontology_heads_page_idx
    ON ontology_record_heads(team_id, shared_space_id, space_generation, kind, record_id);
CREATE INDEX ontology_heads_names_idx ON ontology_record_heads USING GIN (names);

CREATE TABLE ontology_source_dependencies (
    team_id UUID NOT NULL,
    shared_space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL,
    record_id UUID NOT NULL,
    record_version BIGINT NOT NULL,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('entity', 'predicate', 'evidence', 'relationship')),
    source_id TEXT NOT NULL CHECK (length(source_id) BETWEEN 1 AND 128),
    source_version BIGINT NOT NULL CHECK (source_version > 0),
    fingerprint TEXT NOT NULL CHECK (fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    PRIMARY KEY (team_id, shared_space_id, space_generation, record_id, record_version, source_kind, source_id),
    FOREIGN KEY (team_id, shared_space_id, space_generation, record_id, record_version)
        REFERENCES ontology_record_revisions(team_id, shared_space_id, space_generation, record_id, version) ON DELETE RESTRICT
);
CREATE INDEX ontology_sources_lookup_idx
    ON ontology_source_dependencies(team_id, shared_space_id, space_generation, source_kind, source_id);

CREATE TABLE ontology_revision_dependencies (
    team_id UUID NOT NULL,
    shared_space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL,
    record_id UUID NOT NULL,
    record_version BIGINT NOT NULL,
    dependency_id UUID NOT NULL,
    dependency_version BIGINT NOT NULL,
    PRIMARY KEY (team_id, shared_space_id, space_generation, record_id, record_version, dependency_id),
    FOREIGN KEY (team_id, shared_space_id, space_generation, record_id, record_version)
        REFERENCES ontology_record_revisions(team_id, shared_space_id, space_generation, record_id, version) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, shared_space_id, space_generation, dependency_id, dependency_version)
        REFERENCES ontology_record_revisions(team_id, shared_space_id, space_generation, record_id, version) ON DELETE RESTRICT,
    CHECK (record_id <> dependency_id)
);
CREATE INDEX ontology_revisions_lookup_idx
    ON ontology_revision_dependencies(team_id, shared_space_id, space_generation, dependency_id);

CREATE FUNCTION dense_mem_guard_ontology_head() RETURNS TRIGGER
LANGUAGE plpgsql AS $guard$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'ontology heads cannot be deleted';
    END IF;
    IF NEW.team_id <> OLD.team_id OR NEW.shared_space_id <> OLD.shared_space_id
       OR NEW.space_generation <> OLD.space_generation THEN
        RAISE EXCEPTION 'ontology head scope is immutable';
    END IF;
    IF TG_TABLE_NAME = 'ontology_catalog_heads' THEN
        IF NEW.revision <> OLD.revision + 1 THEN
            RAISE EXCEPTION 'ontology catalog revision must advance once';
        END IF;
    ELSIF NEW.record_id <> OLD.record_id OR NEW.kind <> OLD.kind
          OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'ontology record identity is immutable and version must advance once';
    END IF;
    RETURN NEW;
END $guard$;
CREATE TRIGGER ontology_catalog_head_guard BEFORE UPDATE OR DELETE ON ontology_catalog_heads
    FOR EACH ROW EXECUTE FUNCTION dense_mem_guard_ontology_head();
CREATE TRIGGER ontology_record_head_guard BEFORE UPDATE OR DELETE ON ontology_record_heads
    FOR EACH ROW EXECUTE FUNCTION dense_mem_guard_ontology_head();

DO $policy$
DECLARE table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'ontology_catalog_heads', 'ontology_publications', 'ontology_record_revisions',
        'ontology_record_heads', 'ontology_source_dependencies', 'ontology_revision_dependencies'
    ] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
        EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (
            current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
            OR (current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                AND team_id = NULLIF(current_setting(''app.current_team_id'', true), '''')::uuid
                AND shared_space_id = dense_mem_team_shared_space(team_id)
                AND space_generation = dense_mem_active_space_generation(team_id, shared_space_id))
        )', table_name || '_select', table_name);
        EXECUTE format('CREATE POLICY %I ON %I FOR INSERT WITH CHECK (
            shared_space_id = dense_mem_team_shared_space(team_id)
            AND space_generation = dense_mem_active_space_generation(team_id, shared_space_id)
            AND (current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                 OR (current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                     AND team_id = NULLIF(current_setting(''app.current_team_id'', true), '''')::uuid))
        )', table_name || '_insert', table_name);
        IF table_name IN ('ontology_catalog_heads', 'ontology_record_heads') THEN
            EXECUTE format('CREATE POLICY %I ON %I FOR UPDATE USING (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                    AND team_id = NULLIF(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND shared_space_id = dense_mem_team_shared_space(team_id)
                    AND space_generation = dense_mem_active_space_generation(team_id, shared_space_id))
            ) WITH CHECK (
                shared_space_id = dense_mem_team_shared_space(team_id)
                AND space_generation = dense_mem_active_space_generation(team_id, shared_space_id)
                AND (current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                     OR team_id = NULLIF(current_setting(''app.current_team_id'', true), '''')::uuid)
            )', table_name || '_update', table_name);
        ELSE
            EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OR DELETE ON %I
                FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation()',
                table_name || '_append_only', table_name);
        END IF;
    END LOOP;
END $policy$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
DO $rollback$
BEGIN
    IF EXISTS (SELECT 1 FROM ontology_publications)
       OR EXISTS (SELECT 1 FROM ontology_record_revisions) THEN
        RAISE EXCEPTION 'cannot roll back populated ontology history; use roll-forward recovery';
    END IF;
END $rollback$;
DROP TABLE ontology_revision_dependencies;
DROP TABLE ontology_source_dependencies;
DROP TABLE ontology_record_heads;
DROP TABLE ontology_record_revisions;
DROP TABLE ontology_publications;
DROP TABLE ontology_catalog_heads;
DROP FUNCTION dense_mem_guard_ontology_head();
-- +goose StatementEnd
