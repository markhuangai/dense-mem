-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: the idempotency mapping is a small additive control
-- table. Backfilling copies only operation keys and does not rewrite semantic
-- rows. The primary key serializes every canonical and alias scope.
-- RLS impact: the mapping is system-only, like the erasure operation table.
-- Backward compatibility: existing operation keys are backfilled before new
-- binaries can use retirement aliases. Down migration is safe only while no
-- alias rows remain, because removing a mapping would remove replayability.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);
SELECT set_config('app.private_erasure_space_id', '', true);

CREATE TABLE IF NOT EXISTS private_memory_erasure_idempotency_keys (
    idempotency_scope_hash TEXT PRIMARY KEY CHECK (length(idempotency_scope_hash) = 64),
    request_hash TEXT NOT NULL CHECK (length(request_hash) = 64),
    operation_id UUID NOT NULL REFERENCES private_memory_erasure_operations(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS private_memory_erasure_idempotency_operation_idx
    ON private_memory_erasure_idempotency_keys(operation_id);

INSERT INTO private_memory_erasure_idempotency_keys (
    idempotency_scope_hash, request_hash, operation_id
)
SELECT idempotency_scope_hash, request_hash, id
FROM private_memory_erasure_operations
ON CONFLICT (idempotency_scope_hash) DO NOTHING;

ALTER TABLE private_memory_erasure_idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE private_memory_erasure_idempotency_keys FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS private_memory_erasure_idempotency_keys_system_access
    ON private_memory_erasure_idempotency_keys;
CREATE POLICY private_memory_erasure_idempotency_keys_system_access
    ON private_memory_erasure_idempotency_keys
    FOR ALL
    USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);
SELECT set_config('app.private_erasure_space_id', '', true);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM private_memory_erasure_idempotency_keys AS mapping
        JOIN private_memory_erasure_operations AS operation
          ON operation.id = mapping.operation_id
        WHERE mapping.idempotency_scope_hash <> operation.idempotency_scope_hash
    ) THEN
        RAISE EXCEPTION 'refusing idempotency mapping rollback while retirement aliases exist';
    END IF;
END $$;

DROP TABLE IF EXISTS private_memory_erasure_idempotency_keys;

-- +goose StatementEnd
