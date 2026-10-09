-- +goose NO TRANSACTION
-- +goose Up
-- Lock/rewrite impact: concurrent indexes avoid blocking normal audit writes and do not rewrite payloads.
-- RLS impact: unchanged; indexes do not alter visibility or mutation authority.
-- Backfill: PostgreSQL builds index entries from existing rows; no application backfill.
-- Backward compatibility: existing writers and readers retain their original behavior.
-- Rollback: remove only export indexes, preserving the additive field and all audit history.
SET lock_timeout = '30s';
DROP INDEX CONCURRENTLY IF EXISTS audit_export_instance_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS audit_export_team_invalid_idx;
-- +goose StatementBegin
DO $$
DECLARE candidate record;
BEGIN
    FOR candidate IN SELECT c.relname FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
        JOIN pg_namespace n ON n.oid=c.relnamespace
        WHERE n.nspname='public' AND NOT i.indisvalid AND c.relname IN ('audit_export_instance_idx','audit_export_team_idx')
    LOOP
        EXECUTE format('ALTER INDEX %I RENAME TO %I', candidate.relname, replace(candidate.relname,'_idx','_invalid_idx'));
    END LOOP;
END $$;
-- +goose StatementEnd
DROP INDEX CONCURRENTLY IF EXISTS audit_export_instance_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS audit_export_team_invalid_idx;
CREATE INDEX CONCURRENTLY IF NOT EXISTS audit_export_instance_idx ON audit_log(insertion_xid,timestamp,id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS audit_export_team_idx ON audit_log(team_id,insertion_xid,timestamp,id);
RESET lock_timeout;

-- +goose Down
SET lock_timeout = '30s';
DROP INDEX CONCURRENTLY IF EXISTS audit_export_instance_idx;
DROP INDEX CONCURRENTLY IF EXISTS audit_export_team_idx;
DROP INDEX CONCURRENTLY IF EXISTS audit_export_instance_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS audit_export_team_invalid_idx;
RESET lock_timeout;
