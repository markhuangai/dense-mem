-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE TABLE IF NOT EXISTS recall_feedback_events (
    recall_id TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    feedback_at TIMESTAMPTZ NULL,
    team_id UUID NULL,
    profile_id UUID NULL,
    key_id UUID NULL,
    auth_method TEXT NOT NULL DEFAULT '',
    tool_name TEXT NOT NULL DEFAULT 'recall_memory',
    query TEXT NOT NULL DEFAULT '',
    tool_args JSONB NOT NULL DEFAULT '{}'::jsonb,
    result_refs JSONB NOT NULL DEFAULT '[]'::jsonb,
    result_count INTEGER NOT NULL DEFAULT 0,
    snapshot_state TEXT NOT NULL DEFAULT 'captured',
    used BOOLEAN NULL,
    answer_supported BOOLEAN NULL,
    quality TEXT NOT NULL DEFAULT '',
    missing_context BOOLEAN NULL,
    irrelevant BOOLEAN NULL,
    CONSTRAINT recall_feedback_events_result_count_check
        CHECK (result_count >= 0),
    CONSTRAINT recall_feedback_events_snapshot_state_check
        CHECK (snapshot_state IN ('captured', 'feedback_only')),
    CONSTRAINT recall_feedback_events_quality_check
        CHECK (quality IN ('', 'high', 'medium', 'low'))
);

CREATE INDEX IF NOT EXISTS idx_recall_feedback_events_created_at
    ON recall_feedback_events(created_at DESC, recall_id DESC);

CREATE INDEX IF NOT EXISTS idx_recall_feedback_events_feedback_at
    ON recall_feedback_events(feedback_at DESC)
    WHERE feedback_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_recall_feedback_events_team_created_at
    ON recall_feedback_events(team_id, created_at DESC)
    WHERE team_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_recall_feedback_events_profile_created_at
    ON recall_feedback_events(profile_id, created_at DESC)
    WHERE profile_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_recall_feedback_events_quality_created_at
    ON recall_feedback_events(quality, created_at DESC)
    WHERE quality <> '';

CREATE INDEX IF NOT EXISTS idx_recall_feedback_events_negative_flags
    ON recall_feedback_events(missing_context, irrelevant, created_at DESC)
    WHERE missing_context IS TRUE OR irrelevant IS TRUE;

ALTER TABLE recall_feedback_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE recall_feedback_events FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS recall_feedback_events_system_access ON recall_feedback_events;
CREATE POLICY recall_feedback_events_system_access ON recall_feedback_events
    FOR ALL
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

INSERT INTO app_config (key, value)
VALUES ('RECALL_FEEDBACK_RETENTION_DAYS', '30')
ON CONFLICT (key) DO NOTHING;

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

DELETE FROM app_config
WHERE key = 'RECALL_FEEDBACK_RETENTION_DAYS';

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

DROP TABLE IF EXISTS recall_feedback_events;

-- +goose StatementEnd
