-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE recall_feedback_events
    ADD COLUMN IF NOT EXISTS contract_version TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS ranking_profile_version TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS embedding_contract_version TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS search_index_profile_version TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS search_state TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS degradation JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS snapshot_metadata JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE recall_feedback_events
    DROP CONSTRAINT IF EXISTS recall_feedback_events_degradation_object_check,
    ADD CONSTRAINT recall_feedback_events_degradation_object_check
        CHECK (jsonb_typeof(degradation) = 'object'),
    DROP CONSTRAINT IF EXISTS recall_feedback_events_snapshot_metadata_object_check,
    ADD CONSTRAINT recall_feedback_events_snapshot_metadata_object_check
        CHECK (jsonb_typeof(snapshot_metadata) = 'object');

CREATE INDEX IF NOT EXISTS idx_recall_feedback_events_contract_created_at
    ON recall_feedback_events(contract_version, created_at DESC)
    WHERE contract_version <> '';

CREATE INDEX IF NOT EXISTS idx_recall_feedback_events_search_state_created_at
    ON recall_feedback_events(search_state, created_at DESC)
    WHERE search_state <> '';

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

DROP INDEX IF EXISTS idx_recall_feedback_events_search_state_created_at;
DROP INDEX IF EXISTS idx_recall_feedback_events_contract_created_at;

ALTER TABLE recall_feedback_events
    DROP CONSTRAINT IF EXISTS recall_feedback_events_snapshot_metadata_object_check,
    DROP CONSTRAINT IF EXISTS recall_feedback_events_degradation_object_check,
    DROP COLUMN IF EXISTS snapshot_metadata,
    DROP COLUMN IF EXISTS degradation,
    DROP COLUMN IF EXISTS search_state,
    DROP COLUMN IF EXISTS search_index_profile_version,
    DROP COLUMN IF EXISTS embedding_contract_version,
    DROP COLUMN IF EXISTS ranking_profile_version,
    DROP COLUMN IF EXISTS contract_version;

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd
