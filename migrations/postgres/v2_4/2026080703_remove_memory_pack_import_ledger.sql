-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

-- Memory packs are export-only. Import history and change records were never
-- part of the durable semantic authority and are removed as one irreversible
-- boundary after the public import verbs are gone.
DROP TABLE IF EXISTS skill_pack_import_changes;
DROP TABLE IF EXISTS skill_pack_imports;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $dense_mem_irreversible_pack_import_ledger$
BEGIN
    RAISE EXCEPTION 'irreversible migration: memory-pack import ledger was removed';
END
$dense_mem_irreversible_pack_import_ledger$;
-- +goose StatementEnd
