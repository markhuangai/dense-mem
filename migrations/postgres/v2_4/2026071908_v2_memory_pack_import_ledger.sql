-- +goose Up
-- +goose StatementBegin

ALTER TABLE skill_pack_imports
    ADD COLUMN IF NOT EXISTS owner_profile_id UUID NULL,
    ADD COLUMN IF NOT EXISTS ingest_id UUID NULL,
    ADD COLUMN IF NOT EXISTS placement_run_id UUID NULL;

ALTER TABLE skill_pack_imports
    DROP CONSTRAINT IF EXISTS skill_pack_imports_owner_profile_fk,
    DROP CONSTRAINT IF EXISTS skill_pack_imports_ingest_fk,
    DROP CONSTRAINT IF EXISTS skill_pack_imports_placement_run_fk;

ALTER TABLE skill_pack_imports
    ADD CONSTRAINT skill_pack_imports_owner_profile_fk
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES team_profiles(team_id, id) ON DELETE RESTRICT,
    ADD CONSTRAINT skill_pack_imports_ingest_fk
    FOREIGN KEY (team_id, ingest_id) REFERENCES knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT,
    ADD CONSTRAINT skill_pack_imports_placement_run_fk
    FOREIGN KEY (team_id, placement_run_id) REFERENCES placement_runs(team_id, placement_run_id) ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS idx_skill_pack_imports_team_owner_hash
    ON skill_pack_imports(team_id, owner_profile_id, artifact_hash, mode)
    WHERE owner_profile_id IS NOT NULL;

ALTER TABLE skill_pack_import_changes
    DROP CONSTRAINT IF EXISTS skill_pack_import_changes_entity_type_check;

ALTER TABLE skill_pack_import_changes
    ADD CONSTRAINT skill_pack_import_changes_entity_type_check
    CHECK (entity_type IN (
        'fragment',
        'claim',
        'fact',
        'relationship',
        'v2_ingest',
        'v2_placement_item'
    ));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE skill_pack_import_changes
    DROP CONSTRAINT IF EXISTS skill_pack_import_changes_entity_type_check;

ALTER TABLE skill_pack_import_changes
    ADD CONSTRAINT skill_pack_import_changes_entity_type_check
    CHECK (entity_type IN ('fragment', 'claim', 'fact'));

DROP INDEX IF EXISTS idx_skill_pack_imports_team_owner_hash;

ALTER TABLE skill_pack_imports
    DROP CONSTRAINT IF EXISTS skill_pack_imports_placement_run_fk,
    DROP CONSTRAINT IF EXISTS skill_pack_imports_ingest_fk,
    DROP CONSTRAINT IF EXISTS skill_pack_imports_owner_profile_fk;

ALTER TABLE skill_pack_imports
    DROP COLUMN IF EXISTS placement_run_id,
    DROP COLUMN IF EXISTS ingest_id,
    DROP COLUMN IF EXISTS owner_profile_id;

-- +goose StatementEnd
