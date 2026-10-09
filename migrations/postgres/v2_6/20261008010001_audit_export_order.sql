-- +goose Up
-- Lock/rewrite impact: a brief ACCESS EXCLUSIVE lock adds a constant missing value without rewriting historical payloads.
-- RLS impact: unchanged; only the control-admin application uses system-scoped export reads.
-- Backfill: existing rows retain zero; new rows receive the inserting transaction ID.
-- Backward compatibility: existing writers omit the additive field and retain their original behavior.
-- Rollback: disable the feature; retain the additive column and insertion trigger to preserve cursor history.
SET LOCAL lock_timeout = '5s';
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS insertion_xid xid8 NOT NULL DEFAULT '0'::xid8;
ALTER TABLE audit_log ALTER COLUMN insertion_xid SET DEFAULT pg_current_xact_id();
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION assign_audit_insertion_xid() RETURNS trigger AS $$
BEGIN
    NEW.insertion_xid := pg_current_xact_id();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE OR REPLACE TRIGGER audit_log_insertion_xid BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION assign_audit_insertion_xid();

-- +goose Down
SELECT 1;
