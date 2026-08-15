-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE TABLE IF NOT EXISTS community_snapshot_runs (
    team_id UUID NOT NULL,
    run_id UUID NOT NULL DEFAULT gen_random_uuid(),
    window_key TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running',
    algorithm_kind TEXT NOT NULL DEFAULT 'connected_components',
    algorithm_version TEXT NOT NULL DEFAULT 'v1',
    profile_version TEXT NOT NULL DEFAULT 'postgres-v2',
    configuration_hash TEXT NOT NULL DEFAULT '',
    source_fingerprint TEXT NOT NULL DEFAULT '',
    source_snapshot JSONB NOT NULL DEFAULT '[]'::jsonb,
    node_count INTEGER NOT NULL DEFAULT 0,
    edge_count INTEGER NOT NULL DEFAULT 0,
    community_count INTEGER NOT NULL DEFAULT 0,
    max_nodes INTEGER NOT NULL DEFAULT 0,
    max_edges INTEGER NOT NULL DEFAULT 0,
    lease_until TIMESTAMPTZ NULL,
    error TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, run_id),
    FOREIGN KEY (team_id) REFERENCES semantic_team_refs(team_id) ON DELETE RESTRICT,
    CONSTRAINT community_snapshot_runs_window_nonempty CHECK (btrim(window_key) <> ''),
    CONSTRAINT community_snapshot_runs_status_check CHECK (status IN (
        'running', 'completed', 'failed', 'skipped', 'too_large', 'cancelled'
    )),
    CONSTRAINT community_snapshot_runs_algorithm_nonempty CHECK (
        btrim(algorithm_kind) <> '' AND btrim(algorithm_version) <> ''
    ),
    CONSTRAINT community_snapshot_runs_counts_check CHECK (
        node_count >= 0 AND edge_count >= 0 AND community_count >= 0
        AND max_nodes >= 0 AND max_edges >= 0
    ),
    CONSTRAINT community_snapshot_runs_snapshot_array_check CHECK (jsonb_typeof(source_snapshot) = 'array'),
    CONSTRAINT community_snapshot_runs_window_unique UNIQUE (team_id, window_key)
);

CREATE INDEX IF NOT EXISTS community_snapshot_runs_status_idx
    ON community_snapshot_runs(team_id, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS community_records (
    team_id UUID NOT NULL,
    community_id UUID NOT NULL,
    run_id UUID NOT NULL,
    ordinal INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'current',
    summary TEXT NOT NULL DEFAULT '',
    summary_version TEXT NOT NULL DEFAULT '',
    member_count INTEGER NOT NULL DEFAULT 0,
    source_count INTEGER NOT NULL DEFAULT 0,
    top_entities TEXT[] NOT NULL DEFAULT ARRAY[]::text[],
    top_predicates TEXT[] NOT NULL DEFAULT ARRAY[]::text[],
    source_fingerprint TEXT NOT NULL DEFAULT '',
    stale_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    superseded_at TIMESTAMPTZ NULL,
    PRIMARY KEY (team_id, community_id),
    FOREIGN KEY (team_id, run_id) REFERENCES community_snapshot_runs(team_id, run_id) ON DELETE RESTRICT,
    CONSTRAINT community_records_ordinal_check CHECK (ordinal >= 0),
    CONSTRAINT community_records_status_check CHECK (status IN ('current', 'stale', 'superseded')),
    CONSTRAINT community_records_counts_check CHECK (member_count >= 0 AND source_count >= 0)
);

CREATE INDEX IF NOT EXISTS community_records_current_idx
    ON community_records(team_id, member_count DESC, community_id)
    WHERE status = 'current';

CREATE INDEX IF NOT EXISTS community_records_run_idx
    ON community_records(team_id, run_id, ordinal ASC);

CREATE TABLE IF NOT EXISTS community_memberships (
    team_id UUID NOT NULL,
    community_id UUID NOT NULL,
    entity_id UUID NOT NULL,
    rank INTEGER NOT NULL,
    membership_score NUMERIC(5,4) NOT NULL DEFAULT 1,
    source_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, community_id, entity_id),
    FOREIGN KEY (team_id, community_id) REFERENCES community_records(team_id, community_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, entity_id) REFERENCES entity_records(team_id, entity_id) ON DELETE RESTRICT,
    CONSTRAINT community_memberships_rank_check CHECK (rank >= 0),
    CONSTRAINT community_memberships_score_check CHECK (membership_score >= 0 AND membership_score <= 1),
    CONSTRAINT community_memberships_source_count_check CHECK (source_count >= 0)
);

CREATE INDEX IF NOT EXISTS community_memberships_entity_idx
    ON community_memberships(team_id, entity_id, community_id);

CREATE TABLE IF NOT EXISTS community_sources (
    team_id UUID NOT NULL,
    community_id UUID NOT NULL,
    relationship_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    relationship_version INTEGER NOT NULL,
    source_rank INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, community_id, relationship_id),
    FOREIGN KEY (team_id, community_id) REFERENCES community_records(team_id, community_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, relationship_id, owner_profile_id)
        REFERENCES relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT,
    CONSTRAINT community_sources_version_check CHECK (relationship_version >= 1),
    CONSTRAINT community_sources_rank_check CHECK (source_rank >= 0)
);

CREATE INDEX IF NOT EXISTS community_sources_relationship_idx
    ON community_sources(team_id, relationship_id, relationship_version);

ALTER TABLE community_snapshot_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE community_snapshot_runs FORCE ROW LEVEL SECURITY;
ALTER TABLE community_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE community_records FORCE ROW LEVEL SECURITY;
ALTER TABLE community_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE community_memberships FORCE ROW LEVEL SECURITY;
ALTER TABLE community_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE community_sources FORCE ROW LEVEL SECURITY;

DO $$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'community_snapshot_runs',
        'community_records',
        'community_memberships',
        'community_sources'
    ]
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
            ) WITH CHECK (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) = ''team''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
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

DROP TABLE IF EXISTS community_sources;
DROP TABLE IF EXISTS community_memberships;
DROP TABLE IF EXISTS community_records;
DROP TABLE IF EXISTS community_snapshot_runs;

-- +goose StatementEnd
