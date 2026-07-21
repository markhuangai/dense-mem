-- +goose Up
-- +goose StatementBegin

CREATE TABLE IF NOT EXISTS v2_migration_runs (
    run_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    migration_contract_version TEXT NOT NULL,
    corpus_version TEXT NOT NULL DEFAULT '',
    source_kind TEXT NOT NULL DEFAULT 'neo4j',
    state TEXT NOT NULL,
    phase TEXT NOT NULL DEFAULT '',
    required BOOLEAN NOT NULL DEFAULT true,
    preflight_approved BOOLEAN NOT NULL DEFAULT false,
    backup_reference TEXT NOT NULL DEFAULT '',
    preflight_checks JSONB NOT NULL DEFAULT '{}'::jsonb,
    corpus_watermark TEXT NOT NULL DEFAULT '',
    corpus_hash TEXT NOT NULL DEFAULT '',
    total_items INTEGER NOT NULL DEFAULT 0 CHECK (total_items >= 0),
    completed_items INTEGER NOT NULL DEFAULT 0 CHECK (completed_items >= 0),
    failed_items INTEGER NOT NULL DEFAULT 0 CHECK (failed_items >= 0),
    excluded_items INTEGER NOT NULL DEFAULT 0 CHECK (excluded_items >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    retryable BOOLEAN NOT NULL DEFAULT true,
    lease_owner TEXT NOT NULL DEFAULT '',
    checkpoint_key TEXT NOT NULL DEFAULT '',
    checkpoint_value JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at TIMESTAMPTZ NULL,
    completed_at TIMESTAMPTZ NULL,
    cutover_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT v2_migration_runs_state_check CHECK (state IN (
        'required',
        'preflight',
        'ready',
        'running',
        'paused_retryable',
        'failed',
        'verifying',
        'ready_to_cutover',
        'cut_over',
        'incompatible'
    )),
    CONSTRAINT v2_migration_runs_json_check CHECK (
        jsonb_typeof(preflight_checks) = 'object'
        AND jsonb_typeof(checkpoint_value) = 'object'
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_v2_migration_runs_single_active
    ON v2_migration_runs ((true))
    WHERE state IN (
        'required',
        'preflight',
        'ready',
        'running',
        'paused_retryable',
        'verifying',
        'ready_to_cutover'
    );

CREATE INDEX IF NOT EXISTS idx_v2_migration_runs_state_updated
    ON v2_migration_runs(state, updated_at DESC);

CREATE TABLE IF NOT EXISTS v2_migration_corpus_items (
    item_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id UUID NOT NULL REFERENCES v2_migration_runs(run_id) ON DELETE CASCADE,
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
    owner_profile_id UUID NULL,
    source_kind TEXT NOT NULL DEFAULT 'neo4j',
    source_id TEXT NOT NULL,
    source_hash TEXT NOT NULL DEFAULT '',
    item_kind TEXT NOT NULL,
    outcome TEXT NOT NULL DEFAULT 'pending',
    ingest_id UUID NULL,
    placement_item_id UUID NULL,
    exclusion_reason TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (run_id, source_kind, source_id),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES team_profiles(team_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, ingest_id) REFERENCES knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, placement_item_id) REFERENCES placement_items(team_id, placement_item_id) ON DELETE RESTRICT,
    CONSTRAINT v2_migration_corpus_items_outcome_check CHECK (outcome IN (
        'pending',
        'accepted',
        'needs_review',
        'rejected',
        'quarantined',
        'failed',
        'excluded'
    )),
    CONSTRAINT v2_migration_corpus_items_metadata_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_v2_migration_corpus_run_outcome
    ON v2_migration_corpus_items(run_id, outcome, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_v2_migration_corpus_team_owner
    ON v2_migration_corpus_items(run_id, team_id, owner_profile_id, outcome);

CREATE TABLE IF NOT EXISTS v2_migration_source_maps (
    map_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id UUID NOT NULL REFERENCES v2_migration_runs(run_id) ON DELETE CASCADE,
    source_kind TEXT NOT NULL DEFAULT 'neo4j',
    source_id TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (run_id, source_kind, source_id, target_type, target_id),
    CONSTRAINT v2_migration_source_maps_metadata_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE TABLE IF NOT EXISTS v2_migration_checkpoints (
    run_id UUID NOT NULL REFERENCES v2_migration_runs(run_id) ON DELETE CASCADE,
    checkpoint_key TEXT NOT NULL,
    checkpoint_value JSONB NOT NULL DEFAULT '{}'::jsonb,
    lease_owner TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, checkpoint_key),
    CONSTRAINT v2_migration_checkpoints_json_check CHECK (jsonb_typeof(checkpoint_value) = 'object')
);

CREATE TABLE IF NOT EXISTS v2_migration_errors (
    error_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id UUID NOT NULL REFERENCES v2_migration_runs(run_id) ON DELETE CASCADE,
    source_kind TEXT NOT NULL DEFAULT '',
    source_id TEXT NOT NULL DEFAULT '',
    phase TEXT NOT NULL,
    error_code TEXT NOT NULL,
    message TEXT NOT NULL,
    retryable BOOLEAN NOT NULL DEFAULT true,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT v2_migration_errors_metadata_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_v2_migration_errors_run_phase
    ON v2_migration_errors(run_id, phase, created_at DESC);

CREATE TABLE IF NOT EXISTS v2_migration_exclusions (
    exclusion_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id UUID NOT NULL REFERENCES v2_migration_runs(run_id) ON DELETE CASCADE,
    source_kind TEXT NOT NULL DEFAULT 'neo4j',
    source_id TEXT NOT NULL,
    reason TEXT NOT NULL,
    blocks_cutover BOOLEAN NOT NULL DEFAULT true,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (run_id, source_kind, source_id),
    CONSTRAINT v2_migration_exclusions_metadata_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE TABLE IF NOT EXISTS v2_migration_gate_results (
    gate_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id UUID NOT NULL REFERENCES v2_migration_runs(run_id) ON DELETE CASCADE,
    gate_name TEXT NOT NULL,
    outcome TEXT NOT NULL,
    evidence_ref TEXT NOT NULL DEFAULT '',
    evidence_hash TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (run_id, gate_name),
    CONSTRAINT v2_migration_gate_results_outcome_check CHECK (outcome IN ('pass', 'fail', 'warning')),
    CONSTRAINT v2_migration_gate_results_metadata_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE TABLE IF NOT EXISTS v2_migration_operator_actions (
    action_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id UUID NULL REFERENCES v2_migration_runs(run_id) ON DELETE SET NULL,
    action TEXT NOT NULL,
    actor TEXT NOT NULL,
    remote_ip TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT v2_migration_operator_actions_metadata_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_v2_migration_operator_actions_run_created
    ON v2_migration_operator_actions(run_id, created_at DESC);

CREATE TABLE IF NOT EXISTS v2_compatibility_markers (
    marker_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    marker_kind TEXT NOT NULL,
    version TEXT NOT NULL,
    status TEXT NOT NULL,
    run_id UUID NULL REFERENCES v2_migration_runs(run_id) ON DELETE RESTRICT,
    corpus_hash TEXT NOT NULL DEFAULT '',
    gate_report_hash TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (marker_kind, version),
    CONSTRAINT v2_compatibility_markers_status_check CHECK (status IN ('compatible', 'incompatible', 'corrupt')),
    CONSTRAINT v2_compatibility_markers_metadata_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_v2_compatibility_markers_kind_created
    ON v2_compatibility_markers(marker_kind, created_at DESC);

ALTER TABLE v2_migration_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE v2_migration_runs FORCE ROW LEVEL SECURITY;
ALTER TABLE v2_migration_corpus_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE v2_migration_corpus_items FORCE ROW LEVEL SECURITY;
ALTER TABLE v2_migration_source_maps ENABLE ROW LEVEL SECURITY;
ALTER TABLE v2_migration_source_maps FORCE ROW LEVEL SECURITY;
ALTER TABLE v2_migration_checkpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE v2_migration_checkpoints FORCE ROW LEVEL SECURITY;
ALTER TABLE v2_migration_errors ENABLE ROW LEVEL SECURITY;
ALTER TABLE v2_migration_errors FORCE ROW LEVEL SECURITY;
ALTER TABLE v2_migration_exclusions ENABLE ROW LEVEL SECURITY;
ALTER TABLE v2_migration_exclusions FORCE ROW LEVEL SECURITY;
ALTER TABLE v2_migration_gate_results ENABLE ROW LEVEL SECURITY;
ALTER TABLE v2_migration_gate_results FORCE ROW LEVEL SECURITY;
ALTER TABLE v2_migration_operator_actions ENABLE ROW LEVEL SECURITY;
ALTER TABLE v2_migration_operator_actions FORCE ROW LEVEL SECURITY;
ALTER TABLE v2_compatibility_markers ENABLE ROW LEVEL SECURITY;
ALTER TABLE v2_compatibility_markers FORCE ROW LEVEL SECURITY;

CREATE POLICY v2_migration_runs_control_access ON v2_migration_runs
    FOR ALL
    USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));

CREATE POLICY v2_migration_corpus_items_control_access ON v2_migration_corpus_items
    FOR ALL
    USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));

CREATE POLICY v2_migration_source_maps_control_access ON v2_migration_source_maps
    FOR ALL
    USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));

CREATE POLICY v2_migration_checkpoints_control_access ON v2_migration_checkpoints
    FOR ALL
    USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));

CREATE POLICY v2_migration_errors_control_access ON v2_migration_errors
    FOR ALL
    USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));

CREATE POLICY v2_migration_exclusions_control_access ON v2_migration_exclusions
    FOR ALL
    USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));

CREATE POLICY v2_migration_gate_results_control_access ON v2_migration_gate_results
    FOR ALL
    USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));

CREATE POLICY v2_migration_operator_actions_control_access ON v2_migration_operator_actions
    FOR ALL
    USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));

CREATE POLICY v2_compatibility_markers_control_access ON v2_compatibility_markers
    FOR ALL
    USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS v2_compatibility_markers;
DROP TABLE IF EXISTS v2_migration_operator_actions;
DROP TABLE IF EXISTS v2_migration_gate_results;
DROP TABLE IF EXISTS v2_migration_exclusions;
DROP TABLE IF EXISTS v2_migration_errors;
DROP TABLE IF EXISTS v2_migration_checkpoints;
DROP TABLE IF EXISTS v2_migration_source_maps;
DROP TABLE IF EXISTS v2_migration_corpus_items;
DROP TABLE IF EXISTS v2_migration_runs;

-- +goose StatementEnd
