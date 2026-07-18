-- +goose Up
-- +goose StatementBegin

ALTER TABLE v2_migration_runs
    ADD COLUMN IF NOT EXISTS gate_report_hash TEXT NOT NULL DEFAULT '';

ALTER TABLE v2_migration_gate_results
    ADD COLUMN IF NOT EXISTS gate_version TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_v2_compatibility_markers_single_compatible_cutover
    ON v2_compatibility_markers(marker_kind)
    WHERE marker_kind = 'v2_cutover'
      AND status = 'compatible';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_v2_compatibility_markers_single_compatible_cutover;

ALTER TABLE v2_migration_gate_results
    DROP COLUMN IF EXISTS gate_version;

ALTER TABLE v2_migration_runs
    DROP COLUMN IF EXISTS gate_report_hash;

-- +goose StatementEnd
