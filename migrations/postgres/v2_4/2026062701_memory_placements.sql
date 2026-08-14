-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE TABLE IF NOT EXISTS memory_placement_runs (
    ingest_id UUID PRIMARY KEY,
    profile_id UUID NOT NULL,
    status TEXT NOT NULL,
    check_after_seconds INTEGER NOT NULL DEFAULT 60,
    status_tool TEXT NOT NULL DEFAULT 'get_memory_placement',
    evidence JSONB NOT NULL DEFAULT '[]'::jsonb,
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ NULL,
    completed_at TIMESTAMPTZ NULL,
    CONSTRAINT memory_placement_runs_status_check
        CHECK (status IN ('queued', 'processing', 'completed', 'failed')),
    CONSTRAINT memory_placement_runs_check_after_seconds_check
        CHECK (check_after_seconds >= 1)
);

CREATE TABLE IF NOT EXISTS memory_placement_items (
    item_id UUID PRIMARY KEY,
    ingest_id UUID NOT NULL REFERENCES memory_placement_runs(ingest_id) ON DELETE CASCADE,
    profile_id UUID NOT NULL,
    evidence_index INTEGER NOT NULL,
    fragment_id TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL DEFAULT 'fragment_only',
    status TEXT NOT NULL DEFAULT 'queued',
    reason TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    claim_id TEXT NOT NULL DEFAULT '',
    fact_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT memory_placement_items_category_check
        CHECK (category IN (
            'fragment_only',
            'candidate_claim',
            'validated_claim',
            'promoted_fact',
            'needs_more_evidence',
            'rejected_false',
            'accepted_promoted',
            'rejected_explained'
        ))
);

CREATE TABLE IF NOT EXISTS memory_dispute_sessions (
    dispute_id UUID PRIMARY KEY,
    profile_id UUID NOT NULL,
    ingest_id UUID NOT NULL REFERENCES memory_placement_runs(ingest_id) ON DELETE CASCADE,
    placement_item_id UUID NULL,
    status TEXT NOT NULL,
    turns JSONB NOT NULL DEFAULT '[]'::jsonb,
    final_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ NULL,
    CONSTRAINT memory_dispute_sessions_status_check
        CHECK (status IN ('open', 'processing', 'accepted_promoted', 'rejected_explained'))
);

CREATE INDEX IF NOT EXISTS idx_memory_placement_runs_status_created
    ON memory_placement_runs(status, created_at ASC);

CREATE INDEX IF NOT EXISTS idx_memory_placement_runs_profile_created
    ON memory_placement_runs(profile_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_memory_placement_items_ingest
    ON memory_placement_items(ingest_id, evidence_index ASC);

CREATE INDEX IF NOT EXISTS idx_memory_dispute_sessions_profile_created
    ON memory_dispute_sessions(profile_id, created_at DESC);

ALTER TABLE memory_placement_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE memory_placement_runs FORCE ROW LEVEL SECURITY;
ALTER TABLE memory_placement_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE memory_placement_items FORCE ROW LEVEL SECURITY;
ALTER TABLE memory_dispute_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE memory_dispute_sessions FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS memory_placement_runs_system_access ON memory_placement_runs;
CREATE POLICY memory_placement_runs_system_access ON memory_placement_runs
    FOR ALL
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

DROP POLICY IF EXISTS memory_placement_items_system_access ON memory_placement_items;
CREATE POLICY memory_placement_items_system_access ON memory_placement_items
    FOR ALL
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

DROP POLICY IF EXISTS memory_dispute_sessions_system_access ON memory_dispute_sessions;
CREATE POLICY memory_dispute_sessions_system_access ON memory_dispute_sessions
    FOR ALL
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

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

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

DROP TABLE IF EXISTS memory_dispute_sessions;
DROP TABLE IF EXISTS memory_placement_items;
DROP TABLE IF EXISTS memory_placement_runs;

-- +goose StatementEnd
