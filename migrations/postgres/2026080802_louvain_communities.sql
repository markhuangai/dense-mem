-- +goose NO TRANSACTION
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

-- The nullable column is populated here in committed batches; the ordered
-- 0803 follow-up enforces NOT NULL after this repair completes.

ALTER TABLE community_sources
    ADD COLUMN IF NOT EXISTS semantic_group_key TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS source_state_hash TEXT NOT NULL DEFAULT '';

-- The source metadata backfill runs below; the ordered 0803 follow-up adds the
-- lookup indexes after the derived values are complete.

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

-- The procedure commits each bounded batch so a large legacy snapshot does not
-- hold row locks or one transaction-wide snapshot until the migration ends.
-- +goose StatementBegin
CREATE OR REPLACE PROCEDURE dense_mem_backfill_community_metadata_2026080802()
LANGUAGE plpgsql
AS $procedure$
DECLARE
    updated_rows INTEGER;
BEGIN
    PERFORM set_config('app.tx_mode', 'migration', true);
    PERFORM set_config('app.current_team_id', '', true);
    PERFORM set_config('app.current_profile_id', '', true);

    LOOP
        WITH batch AS (
            SELECT record.ctid
            FROM community_records AS record
            WHERE record.logical_community_id IS NULL
            LIMIT 1000
        )
        UPDATE community_records AS record
        SET logical_community_id = record.community_id
        FROM batch
        WHERE record.ctid = batch.ctid;
        GET DIAGNOSTICS updated_rows = ROW_COUNT;
        COMMIT;
        EXIT WHEN updated_rows = 0;
        PERFORM set_config('app.tx_mode', 'migration', true);
        PERFORM set_config('app.current_team_id', '', true);
        PERFORM set_config('app.current_profile_id', '', true);
    END LOOP;

    PERFORM set_config('app.tx_mode', 'migration', true);
    PERFORM set_config('app.current_team_id', '', true);
    PERFORM set_config('app.current_profile_id', '', true);

    LOOP
        WITH latest_support AS (
            SELECT DISTINCT ON (team_id, support_id)
                   team_id, support_id, decision
            FROM relationship_support_decision_events
            ORDER BY team_id, support_id, created_at DESC, support_decision_id DESC
        ), effective_support AS (
            SELECT support.team_id,
                   support.relationship_id,
                   array_agg(DISTINCT support.fragment_id::text ORDER BY support.fragment_id::text) AS evidence_ids
            FROM relationship_evidence_supports AS support
            JOIN latest_support AS latest
              ON latest.team_id = support.team_id
             AND latest.support_id = support.support_id
             AND latest.decision IN ('grant', 'reinstate')
            LEFT JOIN evidence_quarantines AS quarantine
              ON quarantine.team_id = support.team_id
             AND quarantine.fragment_id = support.fragment_id
             AND quarantine.status = 'active'
            LEFT JOIN evidence_sources AS evidence_source
              ON evidence_source.team_id = support.team_id
             AND evidence_source.source_id = support.source_id
            LEFT JOIN evidence_lifecycle_events AS lifecycle
              ON lifecycle.team_id = support.team_id
             AND lifecycle.target_fragment_id = support.fragment_id
            WHERE quarantine.quarantine_id IS NULL
              AND lifecycle.lifecycle_event_id IS NULL
              AND (support.source_id IS NULL OR evidence_source.current_revision_id = support.source_revision_id)
            GROUP BY support.team_id, support.relationship_id
        ), batch AS (
            SELECT source.ctid,
                   relationship.semantic_group_key,
                   'sha256:' || encode(
                       digest(
                           '[' ||
                           to_json(relationship.relationship_id::text)::text || ',' ||
                           relationship.version::text || ',' ||
                           to_json(COALESCE(relationship.semantic_group_key, ''))::text || ',' ||
                           COALESCE(array_to_json(effective_support.evidence_ids)::text, '[]') || ',' ||
                           to_json(COALESCE(relationship.object_entity_id::text, ''))::text || ',' ||
                           to_json(COALESCE(relationship.object_value_id::text, ''))::text ||
                           ']',
                           'sha256'
                       ),
                       'hex'
                   ) AS source_state_hash
            FROM community_sources AS source
            JOIN relationship_records AS relationship
              ON relationship.team_id = source.team_id
             AND relationship.relationship_id = source.relationship_id
             AND relationship.version = source.relationship_version
            LEFT JOIN effective_support
              ON effective_support.team_id = relationship.team_id
             AND effective_support.relationship_id = relationship.relationship_id
            WHERE source.semantic_group_key = ''
               OR source.source_state_hash = ''
            LIMIT 1000
        )
        UPDATE community_sources AS source
        SET semantic_group_key = batch.semantic_group_key,
            source_state_hash = batch.source_state_hash
        FROM batch
        WHERE source.ctid = batch.ctid;
        GET DIAGNOSTICS updated_rows = ROW_COUNT;
        COMMIT;
        EXIT WHEN updated_rows = 0;
        PERFORM set_config('app.tx_mode', 'migration', true);
        PERFORM set_config('app.current_team_id', '', true);
        PERFORM set_config('app.current_profile_id', '', true);
    END LOOP;
END
$procedure$;
-- +goose StatementEnd

CALL dense_mem_backfill_community_metadata_2026080802();
DROP PROCEDURE dense_mem_backfill_community_metadata_2026080802();

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE community_snapshot_runs
    ALTER COLUMN algorithm_kind SET DEFAULT 'connected_components',
    ALTER COLUMN algorithm_version SET DEFAULT 'v1';

DROP TABLE IF EXISTS community_summary_attempts;
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
