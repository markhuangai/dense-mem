-- +goose Up
-- +goose StatementBegin

-- Create trigger function to prevent UPDATE and DELETE on audit_log
-- This enforces append-only semantics for the audit log table
CREATE OR REPLACE FUNCTION prevent_audit_log_mutation()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'audit_log is append-only: UPDATE operations are not allowed';
    ELSIF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'audit_log is append-only: DELETE operations are not allowed';
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

-- Create the trigger that fires before UPDATE or DELETE on audit_log
DROP TRIGGER IF EXISTS audit_log_append_only ON audit_log;
CREATE TRIGGER audit_log_append_only
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW
    EXECUTE FUNCTION prevent_audit_log_mutation();

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin

-- Drop the trigger
DROP TRIGGER IF EXISTS audit_log_append_only ON audit_log;

-- Drop the trigger function
DROP FUNCTION IF EXISTS prevent_audit_log_mutation();

-- +goose StatementEnd
