-- +goose NO TRANSACTION

-- Lock/rewrite impact: replaces one CHECK constraint under a bounded
-- ACCESS EXCLUSIVE lock; validation scans the existing table after the DDL.
-- RLS impact: no policies or visibility paths change.
-- Backfill: none; existing capture-state values remain untouched.
-- Backward compatibility: adds the unavailable state while preserving all
-- existing capture-state values and retained diagnostic rows.
-- Rollback: refuses while unavailable rows exist; the state vocabulary is
-- irreversible once retained diagnostics use it.

-- +goose Up

-- Existing attempt diagnostics use the same protected-capture state vocabulary
-- as invocation diagnostics. This additive constraint update admits an explicit
-- unavailable state when credential protection cannot safely retain a body.
-- Lock impact: replacing the check takes a short ACCESS EXCLUSIVE lock; the
-- validation scan runs after that lock is released.
-- +goose StatementBegin
BEGIN;
SET LOCAL lock_timeout = '30s';
ALTER TABLE remember_attempt_diagnostics
    DROP CONSTRAINT IF EXISTS remember_attempt_diagnostics_capture_state_check,
    ADD CONSTRAINT remember_attempt_diagnostics_capture_state_check CHECK (
        capture_state IN ('captured', 'truncated', 'not_captured', 'hash_only', 'provider_not_called', 'no_response', 'interrupted', 'not_delivered', 'unavailable')
    ) NOT VALID;
COMMIT;
-- +goose StatementEnd

-- +goose StatementBegin
BEGIN;
SET LOCAL lock_timeout = '30s';
ALTER TABLE remember_attempt_diagnostics
    VALIDATE CONSTRAINT remember_attempt_diagnostics_capture_state_check;
COMMIT;
RESET lock_timeout;
-- +goose StatementEnd

-- +goose Down
-- Irreversible while unavailable diagnostics exist because reverting the state
-- vocabulary would reject retained operational history.
-- +goose StatementBegin
BEGIN;
SET LOCAL lock_timeout = '30s';
LOCK TABLE remember_attempt_diagnostics IN ACCESS EXCLUSIVE MODE;
DO $do$
BEGIN
    IF EXISTS (SELECT 1 FROM remember_attempt_diagnostics WHERE capture_state = 'unavailable') THEN
        RAISE EXCEPTION 'cannot remove unavailable capture state while diagnostics exist';
    END IF;
END
$do$;
ALTER TABLE remember_attempt_diagnostics
    DROP CONSTRAINT IF EXISTS remember_attempt_diagnostics_capture_state_check,
    ADD CONSTRAINT remember_attempt_diagnostics_capture_state_check CHECK (
        capture_state IN ('captured', 'truncated', 'not_captured', 'hash_only', 'provider_not_called', 'no_response', 'interrupted', 'not_delivered')
    ) NOT VALID;
COMMIT;
RESET lock_timeout;
-- +goose StatementEnd

-- +goose StatementBegin
BEGIN;
SET LOCAL lock_timeout = '30s';
ALTER TABLE remember_attempt_diagnostics
    VALIDATE CONSTRAINT remember_attempt_diagnostics_capture_state_check;
COMMIT;
RESET lock_timeout;
-- +goose StatementEnd
