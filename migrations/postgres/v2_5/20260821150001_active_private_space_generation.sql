-- Lock/rewrite impact: function creation is metadata-only and does not rewrite
-- memory-owned tables; each call performs a bounded primary-key lookup.
-- RLS impact: the SECURITY DEFINER helper uses a function-local system mode
-- only while reading the requested team/space generation, then restores it.
-- Backfill: none; existing rows already carry space_id and space_generation.
-- Backward compatibility: older binaries do not call this helper; the function
-- is additive and required only after the corresponding application update.
-- Rollback: dropping the helper requires rolling back the application fence
-- callers in the same deployment; no durable rows are changed.

-- Active-generation fences run from background team transactions that cannot
-- expose private memory-space rows through the runtime RLS allow-list.

-- +goose Up
-- +goose StatementBegin

CREATE OR REPLACE FUNCTION dense_mem_active_space_generation(p_team_id UUID, p_space_id UUID)
RETURNS BIGINT
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    previous_mode TEXT;
    result_generation BIGINT;
BEGIN
    previous_mode := current_setting('app.tx_mode', true);
    PERFORM set_config('app.tx_mode', 'system', true);
    SELECT generation
    INTO result_generation
    FROM memory_spaces
    WHERE team_id = p_team_id
      AND id = p_space_id
      AND lifecycle_state = 'active';
    PERFORM set_config('app.tx_mode', COALESCE(previous_mode, ''), true);
    RETURN result_generation;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('app.tx_mode', COALESCE(previous_mode, ''), true);
    RAISE;
END;
$$;

REVOKE EXECUTE ON FUNCTION dense_mem_active_space_generation(UUID, UUID) FROM PUBLIC;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP FUNCTION IF EXISTS dense_mem_active_space_generation(UUID, UUID);

-- +goose StatementEnd
