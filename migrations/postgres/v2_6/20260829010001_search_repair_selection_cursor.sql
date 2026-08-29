-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);
SELECT set_config('lock_timeout', '30s', true);

-- The document-fenced selector may over-include source keys before canonical
-- hydration. Persisting its keyset position lets the next bounded run resume
-- without repeatedly rehydrating the same healthy prefix. These nullable
-- columns are additive and do not rewrite existing reconciliation history.
-- Lock/rewrite impact: ADD COLUMN IF NOT EXISTS takes the PostgreSQL table lock
-- needed for catalog changes but does not rewrite existing rows.
-- RLS impact: columns are on a system-owned run table and inherit its existing
-- system/migration access policy; no application visibility policy changes.
-- Backfill: none; existing runs remain cursorless and resume from the source
-- beginning unless a later run records a cursor.
-- Backward compatibility: nullable columns preserve existing run rows and are
-- read only by the document-repair selector.
-- Rollback: the down migration is allowed only before any cursor is written;
-- after use, retain the columns and roll forward to preserve recovery state.
ALTER TABLE embedding_reconciliation_runs
    ADD COLUMN IF NOT EXISTS selection_cursor_observed_at TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS selection_cursor_team_id UUID NULL,
    ADD COLUMN IF NOT EXISTS selection_cursor_source_kind TEXT NULL,
    ADD COLUMN IF NOT EXISTS selection_cursor_source_id UUID NULL,
    ADD COLUMN IF NOT EXISTS selection_cursor_search_document_id UUID NULL;

DO $dense_mem_search_repair_cursor_constraint$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'embedding_reconciliation_runs'::regclass
          AND conname = 'embedding_reconciliation_runs_selection_cursor_shape_check'
    ) THEN
        ALTER TABLE embedding_reconciliation_runs
            ADD CONSTRAINT embedding_reconciliation_runs_selection_cursor_shape_check
            CHECK (
                (selection_cursor_observed_at IS NULL
                    AND selection_cursor_team_id IS NULL
                    AND selection_cursor_source_kind IS NULL
                    AND selection_cursor_source_id IS NULL
                    AND selection_cursor_search_document_id IS NULL)
                OR (
                    selection_cursor_observed_at IS NOT NULL
                    AND selection_cursor_team_id IS NOT NULL
                    AND selection_cursor_source_kind IS NOT NULL
                    AND btrim(selection_cursor_source_kind) <> ''
                    AND selection_cursor_source_id IS NOT NULL
                )
            ) NOT VALID;
    END IF;
END
$dense_mem_search_repair_cursor_constraint$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $dense_mem_search_repair_cursor_down$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM embedding_reconciliation_runs
        WHERE selection_cursor_observed_at IS NOT NULL
           OR selection_cursor_team_id IS NOT NULL
           OR selection_cursor_source_kind IS NOT NULL
           OR selection_cursor_source_id IS NOT NULL
           OR selection_cursor_search_document_id IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'search repair selection cursor migration is irreversible after a cursor is written';
    END IF;
    ALTER TABLE embedding_reconciliation_runs
        DROP CONSTRAINT IF EXISTS embedding_reconciliation_runs_selection_cursor_shape_check,
        DROP COLUMN IF EXISTS selection_cursor_observed_at,
        DROP COLUMN IF EXISTS selection_cursor_team_id,
        DROP COLUMN IF EXISTS selection_cursor_source_kind,
        DROP COLUMN IF EXISTS selection_cursor_source_id,
        DROP COLUMN IF EXISTS selection_cursor_search_document_id;
END
$dense_mem_search_repair_cursor_down$;
-- +goose StatementEnd
