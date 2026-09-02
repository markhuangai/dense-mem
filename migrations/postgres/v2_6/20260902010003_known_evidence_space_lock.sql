-- Lock/rewrite impact: function creation is metadata-only and does not rewrite
-- memory-owned tables; each call performs one bounded primary-key lookup.
-- RLS impact: the SECURITY DEFINER helper uses the existing function-local
-- system mode only while locking the requested space row, then restores the
-- caller's transaction mode before returning.
-- Backfill: none; existing memory-space rows are already authoritative.
-- Backward compatibility: older binaries do not call this additive helper;
-- the application update must be deployed with this migration.
-- Rollback: drop the helper only after rolling back its application caller.

-- +goose Up
-- +goose StatementBegin

CREATE OR REPLACE FUNCTION dense_mem_lock_memory_space(p_team_id UUID, p_space_id UUID)
RETURNS BOOLEAN
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    previous_mode TEXT;
    locked_space_id UUID;
BEGIN
    previous_mode := current_setting('app.tx_mode', true);
    PERFORM set_config('app.tx_mode', 'system', true);
    SELECT id
    INTO locked_space_id
    FROM memory_spaces
    WHERE team_id = p_team_id
      AND id = p_space_id
    FOR SHARE NOWAIT;
    PERFORM set_config('app.tx_mode', COALESCE(previous_mode, ''), true);
    RETURN locked_space_id IS NOT NULL;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('app.tx_mode', COALESCE(previous_mode, ''), true);
    RAISE;
END;
$$;

REVOKE EXECUTE ON FUNCTION dense_mem_lock_memory_space(UUID, UUID) FROM PUBLIC;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP FUNCTION IF EXISTS dense_mem_lock_memory_space(UUID, UUID);

-- +goose StatementEnd
