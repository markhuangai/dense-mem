-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE community_snapshot_runs
    ALTER COLUMN algorithm_kind SET DEFAULT 'louvain',
    ALTER COLUMN algorithm_version SET DEFAULT 'v2';

ALTER TABLE community_records
    ADD COLUMN IF NOT EXISTS logical_community_id UUID;

-- Backfill in bounded batches so a large deployed snapshot does not hold one
-- unbounded update transaction. UUID logical IDs preserve the legacy version ID
-- until a subsequent Louvain run establishes stable lineage.
DO $$
DECLARE
    updated_rows INTEGER;
BEGIN
    LOOP
        WITH batch AS (
            SELECT ctid
            FROM community_records
            WHERE logical_community_id IS NULL
            LIMIT 1000
        )
        UPDATE community_records AS record
        SET logical_community_id = record.community_id
        FROM batch
        WHERE record.ctid = batch.ctid;
        GET DIAGNOSTICS updated_rows = ROW_COUNT;
        EXIT WHEN updated_rows = 0;
    END LOOP;
END $$;

ALTER TABLE community_records
    ALTER COLUMN logical_community_id SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS community_records_current_logical_unique
    ON community_records(team_id, logical_community_id)
    WHERE status = 'current';

ALTER TABLE community_sources
    ADD COLUMN IF NOT EXISTS semantic_group_key TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS source_state_hash TEXT NOT NULL DEFAULT '';

-- digest() is supplied by pgcrypto in the core schema migration; this migration
-- intentionally does not create or remove extensions.
DO $$
DECLARE
    updated_rows INTEGER;
BEGIN
    LOOP
        WITH batch AS (
            SELECT source.ctid,
                   relationship.semantic_group_key,
                   encode(
                       digest(
                           concat_ws(':', relationship.relationship_id::text, relationship.version::text,
                                     relationship.status, relationship.support_count::text,
                                     relationship.updated_at::text),
                           'sha256'
                       ),
                       'hex'
                   ) AS source_state_hash
            FROM community_sources AS source
            JOIN relationship_records AS relationship
              ON relationship.team_id = source.team_id
             AND relationship.relationship_id = source.relationship_id
            WHERE (source.semantic_group_key = '' AND relationship.semantic_group_key <> '')
               OR source.source_state_hash = ''
            LIMIT 1000
        )
        UPDATE community_sources AS source
        SET semantic_group_key = batch.semantic_group_key,
            source_state_hash = batch.source_state_hash
        FROM batch
        WHERE source.ctid = batch.ctid;
        GET DIAGNOSTICS updated_rows = ROW_COUNT;
        EXIT WHEN updated_rows = 0;
    END LOOP;
END $$;

CREATE INDEX IF NOT EXISTS community_sources_group_idx
    ON community_sources(team_id, semantic_group_key, community_id);

ALTER TABLE community_records
    ADD COLUMN IF NOT EXISTS summary_input_hash TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS summary_provider_model TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS summary_prompt_hash TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS summary_response_hash TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS summary_generated_at TIMESTAMPTZ NULL;

CREATE TABLE IF NOT EXISTS community_summary_attempts (
    team_id UUID NOT NULL,
    attempt_id UUID NOT NULL DEFAULT gen_random_uuid(),
    run_id UUID NOT NULL,
    community_id UUID NULL,
    attempt INTEGER NOT NULL,
    provider_model TEXT NOT NULL DEFAULT '',
    prompt_hash TEXT NOT NULL DEFAULT '',
    response_hash TEXT NOT NULL DEFAULT '',
    input_hash TEXT NOT NULL DEFAULT '',
    admitted_relationship_ids UUID[] NOT NULL DEFAULT ARRAY[]::uuid[],
    admitted_evidence_ids UUID[] NOT NULL DEFAULT ARRAY[]::uuid[],
    admitted_support_quotes JSONB NOT NULL DEFAULT '[]'::jsonb,
    response_summary TEXT NOT NULL DEFAULT '',
    valid BOOLEAN NOT NULL DEFAULT false,
    error_code TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, attempt_id),
    FOREIGN KEY (team_id, run_id) REFERENCES community_snapshot_runs(team_id, run_id) ON DELETE RESTRICT,
    CONSTRAINT community_summary_attempts_attempt_check CHECK (attempt BETWEEN 1 AND 3),
    CONSTRAINT community_summary_attempts_quotes_array_check CHECK (jsonb_typeof(admitted_support_quotes) = 'array')
);

CREATE INDEX IF NOT EXISTS community_summary_attempts_lookup_idx
    ON community_summary_attempts(team_id, community_id, created_at DESC);

ALTER TABLE community_summary_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE community_summary_attempts FORCE ROW LEVEL SECURITY;

CREATE POLICY community_summary_attempts_select ON community_summary_attempts FOR SELECT USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) IN ('team', 'profile')
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
);
CREATE POLICY community_summary_attempts_insert ON community_summary_attempts FOR INSERT WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'team'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE community_snapshot_runs
    ALTER COLUMN algorithm_kind SET DEFAULT 'connected_components',
    ALTER COLUMN algorithm_version SET DEFAULT 'v1';

DROP TABLE IF EXISTS community_summary_attempts;
DROP INDEX IF EXISTS community_sources_group_idx;
DROP INDEX IF EXISTS community_records_current_logical_unique;
ALTER TABLE community_sources
    DROP COLUMN IF EXISTS source_state_hash,
    DROP COLUMN IF EXISTS semantic_group_key;
ALTER TABLE community_records
    DROP COLUMN IF EXISTS summary_generated_at,
    DROP COLUMN IF EXISTS summary_response_hash,
    DROP COLUMN IF EXISTS summary_prompt_hash,
    DROP COLUMN IF EXISTS summary_provider_model,
    DROP COLUMN IF EXISTS summary_input_hash,
    DROP COLUMN IF EXISTS logical_community_id;

-- +goose StatementEnd
