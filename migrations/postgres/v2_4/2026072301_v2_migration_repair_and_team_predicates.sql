-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE v2_migration_runs
    ADD COLUMN IF NOT EXISTS claim_epoch INTEGER NOT NULL DEFAULT 1;

ALTER TABLE placement_runs
    ADD COLUMN IF NOT EXISTS migration_claim_epoch INTEGER NOT NULL DEFAULT 0;

ALTER TABLE knowledge_ingests
    ADD COLUMN IF NOT EXISTS migration_run_id UUID NULL;

DO $$
DECLARE
    conflicts TEXT;
BEGIN
    SELECT string_agg(format('team_id=%s ingest_id=%s runs=%s', team_id, ingest_id, run_count), '; ' ORDER BY team_id, ingest_id)
    INTO conflicts
    FROM (
        SELECT team_id, ingest_id, count(DISTINCT run_id) AS run_count
        FROM v2_migration_corpus_items
        WHERE ingest_id IS NOT NULL
        GROUP BY team_id, ingest_id
        HAVING count(DISTINCT run_id) > 1
    ) AS conflicting_ingests;

    IF conflicts IS NOT NULL THEN
        RAISE EXCEPTION 'cannot backfill knowledge_ingests.migration_run_id: %', conflicts;
    END IF;
END $$;

UPDATE knowledge_ingests AS ingest
SET migration_run_id = mapped.run_id
FROM (
    SELECT DISTINCT team_id, ingest_id, run_id
    FROM v2_migration_corpus_items
    WHERE ingest_id IS NOT NULL
) AS mapped
WHERE ingest.team_id = mapped.team_id
  AND ingest.ingest_id = mapped.ingest_id
  AND ingest.migration_run_id IS NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'knowledge_ingests_migration_run_id_fkey'
          AND conrelid = 'knowledge_ingests'::regclass
    ) THEN
        ALTER TABLE knowledge_ingests
            ADD CONSTRAINT knowledge_ingests_migration_run_id_fkey
            FOREIGN KEY (migration_run_id) REFERENCES v2_migration_runs(run_id)
            ON DELETE RESTRICT NOT VALID;
    END IF;
END $$;

ALTER TABLE knowledge_ingests
    VALIDATE CONSTRAINT knowledge_ingests_migration_run_id_fkey;

CREATE INDEX IF NOT EXISTS knowledge_ingests_migration_run_idx
    ON knowledge_ingests(team_id, migration_run_id)
    WHERE migration_run_id IS NOT NULL;

ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_status_check;
ALTER TABLE placement_runs
    ADD CONSTRAINT placement_runs_status_check
    CHECK (status IN ('queued', 'guarded', 'quarantined', 'processing', 'awaiting_review', 'completed', 'failed')) NOT VALID;
ALTER TABLE placement_runs
    VALIDATE CONSTRAINT placement_runs_status_check;

ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_completion_check;
ALTER TABLE placement_runs
    ADD CONSTRAINT placement_runs_completion_check CHECK (
        (status IN ('awaiting_review', 'completed', 'failed', 'quarantined') AND completed_at IS NOT NULL)
        OR (status NOT IN ('awaiting_review', 'completed', 'failed', 'quarantined'))
    ) NOT VALID;
ALTER TABLE placement_runs
    VALIDATE CONSTRAINT placement_runs_completion_check;

ALTER TABLE placement_items
    DROP CONSTRAINT IF EXISTS placement_items_status_check;
ALTER TABLE placement_items
    ADD CONSTRAINT placement_items_status_check
    CHECK (status IN ('queued', 'processing', 'awaiting_review', 'completed', 'failed', 'quarantined')) NOT VALID;
ALTER TABLE placement_items
    VALIDATE CONSTRAINT placement_items_status_check;

ALTER TABLE placement_items
    DROP CONSTRAINT IF EXISTS placement_items_category_check;
ALTER TABLE placement_items
    ADD CONSTRAINT placement_items_category_check
    CHECK (category IN ('pending', 'fragment_only', 'candidate', 'validated_claim', 'fact', 'quarantined', 'failed')) NOT VALID;
ALTER TABLE placement_items
    VALIDATE CONSTRAINT placement_items_category_check;

ALTER TABLE review_tasks
    ADD COLUMN IF NOT EXISTS dedupe_key TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS review_tasks_open_dedupe_key_unique
    ON review_tasks(team_id, dedupe_key)
    WHERE dedupe_key <> '' AND status IN ('open', 'acknowledged');

CREATE TABLE IF NOT EXISTS team_predicate_definitions (
    team_id UUID NOT NULL,
    predicate_key TEXT NOT NULL,
    version INTEGER NOT NULL,
    aliases TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    allowed_subject_kinds TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    allowed_object_kinds TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    relationship_kind TEXT NOT NULL,
    current_cardinality TEXT NOT NULL,
    lifecycle_state TEXT NOT NULL DEFAULT 'active',
    origin TEXT NOT NULL DEFAULT 'provider_generated',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, predicate_key, version),
    FOREIGN KEY (team_id) REFERENCES semantic_team_refs(team_id) ON DELETE RESTRICT,
    CONSTRAINT team_predicate_definitions_key_nonempty CHECK (btrim(predicate_key) <> ''),
    CONSTRAINT team_predicate_definitions_version_check CHECK (version >= 1),
    CONSTRAINT team_predicate_definitions_relationship_kind_check CHECK (relationship_kind IN ('state', 'event')),
    CONSTRAINT team_predicate_definitions_cardinality_check CHECK (current_cardinality IN ('one', 'many')),
    CONSTRAINT team_predicate_definitions_lifecycle_check CHECK (lifecycle_state IN ('active', 'deprecated', 'retired')),
    CONSTRAINT team_predicate_definitions_origin_nonempty CHECK (btrim(origin) <> ''),
    CONSTRAINT team_predicate_definitions_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS team_predicate_definitions_aliases_idx
    ON team_predicate_definitions USING GIN (aliases);

ALTER TABLE team_predicate_definitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE team_predicate_definitions FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS team_predicate_definitions_select ON team_predicate_definitions;
CREATE POLICY team_predicate_definitions_select ON team_predicate_definitions
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) IN ('team', 'profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

DROP POLICY IF EXISTS team_predicate_definitions_insert ON team_predicate_definitions;
CREATE POLICY team_predicate_definitions_insert ON team_predicate_definitions
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) IN ('team', 'profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

DROP TRIGGER IF EXISTS team_predicate_definitions_reference_guard ON team_predicate_definitions;
CREATE TRIGGER team_predicate_definitions_reference_guard
    BEFORE UPDATE OR DELETE ON team_predicate_definitions
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_reference_definition_mutation();

INSERT INTO team_predicate_definitions (
    team_id, predicate_key, version, aliases, allowed_subject_kinds,
    allowed_object_kinds, relationship_kind, current_cardinality,
    lifecycle_state, origin, metadata, created_at
)
SELECT team.team_id, predicate.predicate_key, predicate.version, predicate.aliases,
       predicate.allowed_subject_kinds, predicate.allowed_object_kinds,
       predicate.relationship_kind, predicate.current_cardinality,
       predicate.lifecycle_state, 'built_in',
       predicate.metadata || jsonb_build_object('source', 'predicate_definitions'),
       predicate.created_at
FROM semantic_team_refs AS team
CROSS JOIN predicate_definitions AS predicate
ON CONFLICT (team_id, predicate_key, version) DO NOTHING;

ALTER TABLE relationship_records
    DROP CONSTRAINT IF EXISTS relationship_records_predicate_key_predicate_version_fkey;
ALTER TABLE relationship_records
    DROP CONSTRAINT IF EXISTS relationship_records_team_predicate_fkey;
ALTER TABLE relationship_records
    ADD CONSTRAINT relationship_records_team_predicate_fkey
    FOREIGN KEY (team_id, predicate_key, predicate_version)
    REFERENCES team_predicate_definitions(team_id, predicate_key, version)
    ON DELETE RESTRICT NOT VALID;
ALTER TABLE relationship_records
    VALIDATE CONSTRAINT relationship_records_team_predicate_fkey;

ALTER TABLE relationship_observations
    DROP CONSTRAINT IF EXISTS relationship_observations_predicate_key_predicate_version_fkey;
ALTER TABLE relationship_observations
    DROP CONSTRAINT IF EXISTS relationship_observations_team_predicate_fkey;
ALTER TABLE relationship_observations
    ADD CONSTRAINT relationship_observations_team_predicate_fkey
    FOREIGN KEY (team_id, predicate_key, predicate_version)
    REFERENCES team_predicate_definitions(team_id, predicate_key, version)
    ON DELETE RESTRICT NOT VALID;
ALTER TABLE relationship_observations
    VALIDATE CONSTRAINT relationship_observations_team_predicate_fkey;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE relationship_observations
    DROP CONSTRAINT IF EXISTS relationship_observations_team_predicate_fkey;
ALTER TABLE relationship_observations
    ADD CONSTRAINT relationship_observations_predicate_key_predicate_version_fkey
    FOREIGN KEY (predicate_key, predicate_version) REFERENCES predicate_definitions(predicate_key, version)
    ON DELETE RESTRICT NOT VALID;
ALTER TABLE relationship_observations
    VALIDATE CONSTRAINT relationship_observations_predicate_key_predicate_version_fkey;

ALTER TABLE relationship_records
    DROP CONSTRAINT IF EXISTS relationship_records_team_predicate_fkey;
ALTER TABLE relationship_records
    ADD CONSTRAINT relationship_records_predicate_key_predicate_version_fkey
    FOREIGN KEY (predicate_key, predicate_version) REFERENCES predicate_definitions(predicate_key, version)
    ON DELETE RESTRICT NOT VALID;
ALTER TABLE relationship_records
    VALIDATE CONSTRAINT relationship_records_predicate_key_predicate_version_fkey;

DROP TRIGGER IF EXISTS team_predicate_definitions_reference_guard ON team_predicate_definitions;
DROP POLICY IF EXISTS team_predicate_definitions_insert ON team_predicate_definitions;
DROP POLICY IF EXISTS team_predicate_definitions_select ON team_predicate_definitions;
DROP TABLE IF EXISTS team_predicate_definitions;

DROP INDEX IF EXISTS review_tasks_open_dedupe_key_unique;
ALTER TABLE review_tasks
    DROP COLUMN IF EXISTS dedupe_key;

ALTER TABLE placement_items
    DROP CONSTRAINT IF EXISTS placement_items_status_check;
ALTER TABLE placement_items
    ADD CONSTRAINT placement_items_status_check
    CHECK (status IN ('queued', 'processing', 'completed', 'failed', 'quarantined')) NOT VALID;
ALTER TABLE placement_items
    VALIDATE CONSTRAINT placement_items_status_check;

ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_completion_check;
ALTER TABLE placement_runs
    ADD CONSTRAINT placement_runs_completion_check CHECK (
        (status IN ('completed', 'failed', 'quarantined') AND completed_at IS NOT NULL)
        OR (status NOT IN ('completed', 'failed', 'quarantined'))
    ) NOT VALID;
ALTER TABLE placement_runs
    VALIDATE CONSTRAINT placement_runs_completion_check;

ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_status_check;
ALTER TABLE placement_runs
    ADD CONSTRAINT placement_runs_status_check
    CHECK (status IN ('queued', 'guarded', 'quarantined', 'processing', 'completed', 'failed')) NOT VALID;
ALTER TABLE placement_runs
    VALIDATE CONSTRAINT placement_runs_status_check;

DROP INDEX IF EXISTS knowledge_ingests_migration_run_idx;
ALTER TABLE knowledge_ingests
    DROP CONSTRAINT IF EXISTS knowledge_ingests_migration_run_id_fkey;
ALTER TABLE knowledge_ingests
    DROP COLUMN IF EXISTS migration_run_id;

ALTER TABLE placement_runs
    DROP COLUMN IF EXISTS migration_claim_epoch;

ALTER TABLE v2_migration_runs
    DROP COLUMN IF EXISTS claim_epoch;

-- +goose StatementEnd
