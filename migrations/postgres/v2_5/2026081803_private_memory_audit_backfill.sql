-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin

-- The audit association is a resumable follow-up so large audit tables do not
-- hold one migration transaction or row-lock set for the entire upgrade.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);
SELECT set_config('app.private_erasure_space_id', '', true);

DROP POLICY IF EXISTS audit_log_memory_space_backfill_select ON audit_log;
DROP POLICY IF EXISTS audit_log_memory_space_backfill_update ON audit_log;
CREATE POLICY audit_log_memory_space_backfill_select ON audit_log
    FOR SELECT
    USING (current_setting('app.tx_mode', true) = 'migration');
CREATE POLICY audit_log_memory_space_backfill_update ON audit_log
    FOR UPDATE
    USING (current_setting('app.tx_mode', true) = 'migration')
    WITH CHECK (current_setting('app.tx_mode', true) = 'migration');

-- +goose StatementEnd

-- Each batch commits independently. Rows without a surviving credential and
-- exact team match remain unassociated and cannot be scrubbed by erasure.
-- +goose StatementBegin
CREATE OR REPLACE PROCEDURE dense_mem_backfill_private_memory_audit_2026081803()
LANGUAGE plpgsql
AS $procedure$
DECLARE
    updated_rows INTEGER;
BEGIN
    LOOP
        PERFORM set_config('app.tx_mode', 'migration', true);
        PERFORM set_config('app.current_team_id', '', true);
        PERFORM set_config('app.current_profile_id', '', true);
        PERFORM set_config('app.allowed_space_ids', '', true);
        PERFORM set_config('app.private_erasure_space_id', '', true);

        WITH batch AS MATERIALIZED (
            SELECT audit.id, credential.memory_space_id
            FROM audit_log AS audit
            JOIN credentials AS credential
              ON credential.id = CASE
                  WHEN audit.entity_id ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$'
                      THEN audit.entity_id::uuid
                  ELSE NULL
              END
             AND credential.team_id = audit.team_id
            WHERE audit.memory_space_id IS NULL
              AND audit.entity_type = 'api_key'
              AND audit.entity_id ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$'
              AND credential.memory_space_id IS NOT NULL
            ORDER BY audit.id
            LIMIT 500
            FOR UPDATE OF audit
        )
        UPDATE audit_log AS audit
        SET memory_space_id = batch.memory_space_id
        FROM batch
        WHERE audit.id = batch.id
          AND audit.memory_space_id IS NULL;
        GET DIAGNOSTICS updated_rows = ROW_COUNT;
        COMMIT;
        EXIT WHEN updated_rows = 0;
    END LOOP;
END
$procedure$;
-- +goose StatementEnd

CALL dense_mem_backfill_private_memory_audit_2026081803();
DROP PROCEDURE dense_mem_backfill_private_memory_audit_2026081803();

-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);
SELECT set_config('app.private_erasure_space_id', '', true);
DROP POLICY audit_log_memory_space_backfill_update ON audit_log;
DROP POLICY audit_log_memory_space_backfill_select ON audit_log;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $dense_mem_irreversible_private_memory_audit_backfill$
BEGIN
    RAISE EXCEPTION 'private-memory audit association migration is irreversible';
END
$dense_mem_irreversible_private_memory_audit_backfill$;
-- +goose StatementEnd
