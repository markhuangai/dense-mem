-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

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

ALTER TABLE semantic_relationship_records
    ADD COLUMN IF NOT EXISTS object_value_id UUID NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'semantic_relationship_records_team_id_object_value_id_fkey'
          AND conrelid = 'semantic_relationship_records'::regclass
    ) THEN
        ALTER TABLE semantic_relationship_records
            ADD CONSTRAINT semantic_relationship_records_team_id_object_value_id_fkey
            FOREIGN KEY (team_id, object_value_id) REFERENCES semantic_values(team_id, value_id) ON DELETE RESTRICT;
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_name = 'semantic_relationship_records'
          AND column_name = 'object_value'
    ) THEN
        EXECUTE $sql$
            INSERT INTO semantic_values (
                team_id,
                value_id,
                owner_profile_id,
                value_type,
                canonical_value,
                display_value,
                metadata,
                status,
                version,
                search_state,
                search_document_version,
                created_at,
                updated_at
            )
            SELECT
                team_id,
                gen_random_uuid(),
                min(owner_profile_id::text)::uuid,
                'string',
                lower(btrim(object_value)),
                min(btrim(object_value)),
                jsonb_strip_nulls(jsonb_build_object('legacy_object_kind', min(nullif(object_kind, '')))),
                'active',
                1,
                'pending',
                1,
                min(created_at),
                max(updated_at)
            FROM semantic_relationship_records
            WHERE object_entity_id IS NULL
              AND btrim(object_value) <> ''
            GROUP BY team_id, lower(btrim(object_value))
            ON CONFLICT (team_id, value_type, canonical_value) WHERE status = 'active' DO NOTHING
        $sql$;

        EXECUTE $sql$
            UPDATE semantic_relationship_records r
            SET object_value_id = v.value_id
            FROM semantic_values v
            WHERE r.object_entity_id IS NULL
              AND r.object_value_id IS NULL
              AND btrim(r.object_value) <> ''
              AND v.team_id = r.team_id
              AND v.value_type = 'string'
              AND v.status = 'active'
              AND v.canonical_value = lower(btrim(r.object_value))
        $sql$;
    END IF;
END $$;

ALTER TABLE semantic_search_documents
    DROP CONSTRAINT IF EXISTS semantic_search_source_type_check;

ALTER TABLE semantic_search_documents
    ADD CONSTRAINT semantic_search_source_type_check
    CHECK (source_type IN ('evidence', 'relationship', 'entity', 'value'));

DROP INDEX IF EXISTS semantic_relationship_identity_unique;

CREATE INDEX IF NOT EXISTS semantic_relationship_object_value_adjacency_idx
    ON semantic_relationship_records(team_id, object_value_id, updated_at DESC, relationship_id)
    WHERE status = 'active' AND tier IN ('validated_claim', 'fact') AND object_value_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS semantic_relationship_identity_unique
    ON semantic_relationship_records(team_id, owner_profile_id, subject_entity_id, predicate, polarity,
        COALESCE(object_entity_id, '00000000-0000-0000-0000-000000000000'::uuid),
        COALESCE(object_value_id, '00000000-0000-0000-0000-000000000000'::uuid));

DROP VIEW IF EXISTS semantic_edges;

CREATE VIEW semantic_edges AS
SELECT
    r.team_id,
    r.relationship_id,
    r.owner_profile_id,
    r.subject_entity_id,
    subject.canonical_name AS subject_name,
    subject.kind AS subject_kind,
    r.predicate,
    r.polarity,
    r.object_entity_id,
    object.canonical_name AS object_name,
    object.kind AS object_kind,
    r.object_value_id,
    v.display_value AS object_value,
    v.value_type AS object_value_type,
    r.tier,
    r.status,
    r.confidence,
    r.support_count,
    r.source_group_count,
    r.valid_from,
    r.valid_to,
    r.recorded_at,
    r.updated_at
FROM semantic_relationship_records r
JOIN semantic_entities subject
  ON subject.team_id = r.team_id AND subject.entity_id = r.subject_entity_id
LEFT JOIN semantic_entities object
  ON object.team_id = r.team_id AND object.entity_id = r.object_entity_id
LEFT JOIN semantic_values v
  ON v.team_id = r.team_id AND v.value_id = r.object_value_id
WHERE r.status = 'active'
  AND r.tier IN ('validated_claim', 'fact');

ALTER TABLE semantic_relationship_records
    DROP CONSTRAINT IF EXISTS semantic_relationship_object_present;

ALTER TABLE semantic_relationship_records
    DROP CONSTRAINT IF EXISTS semantic_relationship_object_exactly_one;

ALTER TABLE semantic_relationship_records
    ADD CONSTRAINT semantic_relationship_object_exactly_one
    CHECK ((object_entity_id IS NULL) <> (object_value_id IS NULL));

ALTER TABLE semantic_relationship_records
    DROP COLUMN IF EXISTS object_value;

ALTER TABLE semantic_relationship_records
    DROP COLUMN IF EXISTS object_kind;

DO $$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'semantic_values'
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

ALTER TABLE semantic_relationship_records
    ADD COLUMN IF NOT EXISTS object_value TEXT NOT NULL DEFAULT '';

ALTER TABLE semantic_relationship_records
    ADD COLUMN IF NOT EXISTS object_kind TEXT NOT NULL DEFAULT '';

UPDATE semantic_relationship_records r
SET object_value = v.display_value,
    object_kind = v.value_type
FROM semantic_values v
WHERE r.object_value_id = v.value_id
  AND r.team_id = v.team_id
  AND r.object_entity_id IS NULL;

ALTER TABLE semantic_relationship_records
    DROP CONSTRAINT IF EXISTS semantic_relationship_object_exactly_one;

ALTER TABLE semantic_relationship_records
    ADD CONSTRAINT semantic_relationship_object_present
    CHECK (object_entity_id IS NOT NULL OR btrim(object_value) <> '');

DROP INDEX IF EXISTS semantic_relationship_identity_unique;

CREATE UNIQUE INDEX IF NOT EXISTS semantic_relationship_identity_unique
    ON semantic_relationship_records(team_id, owner_profile_id, subject_entity_id, predicate, polarity,
        COALESCE(object_entity_id, '00000000-0000-0000-0000-000000000000'::uuid),
        object_value);

DROP INDEX IF EXISTS semantic_relationship_object_value_adjacency_idx;

ALTER TABLE semantic_relationship_records
    DROP CONSTRAINT IF EXISTS semantic_relationship_records_team_id_object_value_id_fkey;

ALTER TABLE semantic_relationship_records
    DROP COLUMN IF EXISTS object_value_id;

ALTER TABLE semantic_search_documents
    DROP CONSTRAINT IF EXISTS semantic_search_source_type_check;

ALTER TABLE semantic_search_documents
    ADD CONSTRAINT semantic_search_source_type_check
    CHECK (source_type IN ('evidence', 'relationship', 'entity'));

-- +goose StatementEnd
