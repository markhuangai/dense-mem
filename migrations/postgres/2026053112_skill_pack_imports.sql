-- +goose Up
-- +goose StatementBegin

CREATE TABLE IF NOT EXISTS skill_pack_imports (
    import_id UUID PRIMARY KEY,
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    artifact_hash CHAR(64) NOT NULL,
    source_url TEXT NULL,
    schema_version TEXT NOT NULL,
    name TEXT NOT NULL,
    mode VARCHAR(16) NOT NULL CHECK (mode IN ('review', 'trusted')),
    status VARCHAR(24) NOT NULL CHECK (status IN ('inspecting', 'needs_review', 'applied', 'rolled_back')),
    item_count INTEGER NOT NULL DEFAULT 0 CHECK (item_count >= 0),
    applied_count INTEGER NOT NULL DEFAULT 0 CHECK (applied_count >= 0),
    skipped_count INTEGER NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),
    summary JSONB NOT NULL DEFAULT '{}'::jsonb,
    retention_expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ NULL,
    rolled_back_at TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS idx_skill_pack_imports_team_created
    ON skill_pack_imports(team_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_skill_pack_imports_retention
    ON skill_pack_imports(retention_expires_at);

CREATE TABLE IF NOT EXISTS skill_pack_import_changes (
    change_id UUID PRIMARY KEY,
    import_id UUID NOT NULL REFERENCES skill_pack_imports(import_id) ON DELETE CASCADE,
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    entity_type VARCHAR(32) NOT NULL CHECK (entity_type IN ('fragment', 'claim', 'fact')),
    entity_id TEXT NOT NULL,
    action VARCHAR(32) NOT NULL CHECK (action IN ('created', 'updated', 'superseded', 'linked')),
    before_state JSONB NOT NULL DEFAULT '{}'::jsonb,
    after_state JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_skill_pack_import_changes_import_created
    ON skill_pack_import_changes(import_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_skill_pack_import_changes_team_entity
    ON skill_pack_import_changes(team_id, entity_type, entity_id);

ALTER TABLE skill_pack_imports ENABLE ROW LEVEL SECURITY;
ALTER TABLE skill_pack_imports FORCE ROW LEVEL SECURITY;
ALTER TABLE skill_pack_import_changes ENABLE ROW LEVEL SECURITY;
ALTER TABLE skill_pack_import_changes FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS skill_pack_imports_system_access ON skill_pack_imports;
CREATE POLICY skill_pack_imports_system_access ON skill_pack_imports
    FOR ALL
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

DROP POLICY IF EXISTS skill_pack_imports_team_access ON skill_pack_imports;
CREATE POLICY skill_pack_imports_team_access ON skill_pack_imports
    FOR ALL
    USING (
        current_setting('app.tx_mode', true) = 'team'
        AND team_id::text = current_setting('app.current_team_id', true)
    )
    WITH CHECK (
        current_setting('app.tx_mode', true) = 'team'
        AND team_id::text = current_setting('app.current_team_id', true)
    );

DROP POLICY IF EXISTS skill_pack_import_changes_system_access ON skill_pack_import_changes;
CREATE POLICY skill_pack_import_changes_system_access ON skill_pack_import_changes
    FOR ALL
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

DROP POLICY IF EXISTS skill_pack_import_changes_team_access ON skill_pack_import_changes;
CREATE POLICY skill_pack_import_changes_team_access ON skill_pack_import_changes
    FOR ALL
    USING (
        current_setting('app.tx_mode', true) = 'team'
        AND team_id::text = current_setting('app.current_team_id', true)
    )
    WITH CHECK (
        current_setting('app.tx_mode', true) = 'team'
        AND team_id::text = current_setting('app.current_team_id', true)
    );

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP POLICY IF EXISTS skill_pack_import_changes_team_access ON skill_pack_import_changes;
DROP POLICY IF EXISTS skill_pack_import_changes_system_access ON skill_pack_import_changes;
DROP POLICY IF EXISTS skill_pack_imports_team_access ON skill_pack_imports;
DROP POLICY IF EXISTS skill_pack_imports_system_access ON skill_pack_imports;
DROP TABLE IF EXISTS skill_pack_import_changes;
DROP TABLE IF EXISTS skill_pack_imports;

-- +goose StatementEnd
