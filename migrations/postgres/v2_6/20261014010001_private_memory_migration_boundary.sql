-- +goose Up
-- +goose StatementBegin
-- Lock/rewrite impact: maintenance-only ALTERs take DDL locks; uniqueness/FK
-- validation and replacement indexes scan affected tables. Added nullable scope
-- columns and the constant archive default do not require a heap rewrite;
-- lock acquisition is limited to 30s.
-- RLS impact: force RLS on migration metadata and add restrictive archive-read
-- policies; non-migration writes stay blocked until maintenance is complete.
-- Backfill: this boundary records pending/ready state; the fingerprint-bound
-- executor moves private rows in bounded transactions before final cutover.
-- Backward compatibility: transitional kinds remain for maintenance input only;
-- old binaries/workers must stay stopped and runtime waits for complete state.
-- Rollback: Down raises; restore the verified stopped-state backup with its
-- matching binary before new writes, otherwise use forward recovery.
SELECT set_config('app.tx_mode', 'migration', true);
SET LOCAL lock_timeout = '30s';

CREATE TABLE private_memory_migration_state (
 singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
 planner_version TEXT NOT NULL,
 source_fingerprint TEXT,
 phase TEXT NOT NULL CHECK (phase IN ('pending','planned','copying','moving','rebuilding','ready','complete')),
 report JSONB NOT NULL DEFAULT '{}',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE private_memory_migration_records (
 record_id UUID PRIMARY KEY,
 team_id UUID NOT NULL REFERENCES teams(id),
 source_table TEXT NOT NULL,
 source_key JSONB NOT NULL CHECK (jsonb_typeof(source_key)='array'),
 source_space_id UUID NOT NULL REFERENCES memory_spaces(id),
 source_generation BIGINT NOT NULL CHECK (source_generation > 0),
 original_membership_ids UUID[] NOT NULL,
 destination_space_id UUID NOT NULL REFERENCES memory_spaces(id),
 destination_generation BIGINT NOT NULL CHECK (destination_generation > 0),
 destination_key JSONB NOT NULL CHECK (jsonb_typeof(destination_key)='array'),
 destination_membership_id UUID REFERENCES team_memberships(id),
 replacements JSONB NOT NULL DEFAULT '{}',
 supporting_copy BOOLEAN NOT NULL,
 sealed BOOLEAN NOT NULL,
 completed BOOLEAN NOT NULL DEFAULT false,
 projection_rebuilt BOOLEAN NOT NULL DEFAULT false,
 purged_at TIMESTAMPTZ,
 UNIQUE(source_table,source_key,destination_space_id)
);
CREATE INDEX private_memory_migration_original_owners ON private_memory_migration_records USING gin(original_membership_ids);
CREATE INDEX private_memory_migration_destination ON private_memory_migration_records(team_id,destination_space_id,source_table);
CREATE INDEX private_memory_migration_remaining ON private_memory_migration_records(supporting_copy,source_table,record_id) WHERE NOT completed;
CREATE TABLE private_memory_migration_constraints (
 table_name TEXT NOT NULL,
 constraint_name TEXT NOT NULL,
 definition TEXT NOT NULL,
 PRIMARY KEY(table_name,constraint_name)
);

DO $rls$
DECLARE relation TEXT;
BEGIN
 FOREACH relation IN ARRAY ARRAY['private_memory_migration_state','private_memory_migration_records','private_memory_migration_constraints'] LOOP
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',relation);
  EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',relation);
  EXECUTE format('CREATE POLICY private_memory_migration_system ON %I USING (current_setting(''app.tx_mode'',true) IN (''system'',''migration'')) WITH CHECK (current_setting(''app.tx_mode'',true) IN (''system'',''migration''))',relation);
 END LOOP;
END $rls$;

ALTER TABLE team_memberships ADD CONSTRAINT team_memberships_team_id_id_unique UNIQUE(team_id,id);
ALTER TABLE private_memory_migration_records ADD CONSTRAINT private_memory_mapping_source_team_fk FOREIGN KEY(team_id,source_space_id) REFERENCES memory_spaces(team_id,id);
ALTER TABLE private_memory_migration_records ADD CONSTRAINT private_memory_mapping_destination_team_fk FOREIGN KEY(team_id,destination_space_id) REFERENCES memory_spaces(team_id,id);
ALTER TABLE private_memory_migration_records ADD CONSTRAINT private_memory_mapping_membership_team_fk FOREIGN KEY(team_id,destination_membership_id) REFERENCES team_memberships(team_id,id);
ALTER TABLE memory_spaces ADD COLUMN owner_membership_id UUID;
ALTER TABLE memory_spaces ADD COLUMN migration_archive BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE memory_spaces ADD CONSTRAINT memory_spaces_membership_fk FOREIGN KEY(team_id,owner_membership_id) REFERENCES team_memberships(team_id,id) ON DELETE RESTRICT;
ALTER TABLE memory_spaces DROP CONSTRAINT memory_spaces_kind_check;
ALTER TABLE memory_spaces ADD CONSTRAINT memory_spaces_kind_check CHECK(kind IN ('team_shared','profile_private','credential_private','private'));
ALTER TABLE memory_spaces DROP CONSTRAINT memory_spaces_owner_shape;
ALTER TABLE memory_spaces ADD CONSTRAINT memory_spaces_owner_shape CHECK (
 (kind='team_shared' AND owner_profile_id IS NULL AND owner_credential_id IS NULL AND owner_membership_id IS NULL)
 OR (kind='profile_private' AND owner_profile_id IS NOT NULL AND owner_credential_id IS NULL)
 OR (kind='credential_private' AND owner_profile_id IS NULL AND owner_credential_id IS NOT NULL)
 OR (kind='private' AND owner_membership_id IS NOT NULL AND owner_profile_id IS NULL AND owner_credential_id IS NULL));
ALTER TABLE memory_spaces ADD CONSTRAINT memory_spaces_archive_sealed CHECK (NOT migration_archive OR lifecycle_state IN ('sealed','retired'));
CREATE UNIQUE INDEX memory_spaces_membership_active_unique ON memory_spaces(team_id,owner_membership_id) WHERE kind='private' AND NOT migration_archive AND lifecycle_state <> 'retired';

DO $inherited_scope$
DECLARE relation TEXT;
BEGIN
 FOREACH relation IN ARRAY ARRAY['remember_attempt_events','remember_attempt_diagnostics','remember_failure_artifacts','semantic_assessments'] LOOP
  EXECUTE format('ALTER TABLE %I ADD COLUMN space_id UUID REFERENCES memory_spaces(id), ADD COLUMN space_generation BIGINT',relation);
  EXECUTE format('CREATE POLICY private_memory_archive_isolation ON %I AS RESTRICTIVE FOR SELECT USING (current_setting(''app.tx_mode'',true) IN (''system'',''migration'') OR space_id IS NULL OR (dense_mem_space_allowed(space_id) AND space_generation=dense_mem_active_space_generation(team_id,space_id)))',relation);
 END LOOP;
END $inherited_scope$;

DROP INDEX evidence_sources_owner_key_unique;
CREATE UNIQUE INDEX evidence_sources_owner_key_unique ON evidence_sources(team_id,space_id,owner_profile_id,source_key);
ALTER TABLE value_records DROP CONSTRAINT value_records_canonical_unique;
ALTER TABLE value_records ADD CONSTRAINT value_records_canonical_unique UNIQUE NULLS NOT DISTINCT(team_id,space_id,value_type,canonical_value,unit,normalization_version);

DO $initial_state$
DECLARE relation RECORD; populated BOOLEAN := false; found_rows BOOLEAN;
BEGIN
 FOR relation IN SELECT table_name,column_name FROM information_schema.columns WHERE table_schema='public' AND (column_name='space_id' OR (table_name='audit_log' AND column_name='memory_space_id')) AND table_name NOT LIKE 'private_memory_%' LOOP
  EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I row JOIN memory_spaces space ON space.id=row.%I WHERE space.kind<>''team_shared'')',relation.table_name,relation.column_name) INTO found_rows;
  populated := populated OR found_rows;
 END LOOP;
 INSERT INTO private_memory_migration_state(singleton,planner_version,phase) VALUES(true,'private-memory.v2.7.0.1',CASE WHEN populated THEN 'pending' ELSE 'ready' END);
END $initial_state$;

CREATE FUNCTION dense_mem_private_migration_write_gate() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE previous_mode TEXT; migration_phase TEXT;
BEGIN
 previous_mode:=current_setting('app.tx_mode',true);
 PERFORM set_config('app.tx_mode','system',true);
 SELECT phase INTO migration_phase FROM private_memory_migration_state WHERE singleton;
 PERFORM set_config('app.tx_mode',COALESCE(previous_mode,''),true);
 IF migration_phase <> 'complete' AND previous_mode IS DISTINCT FROM 'migration' THEN
  RAISE EXCEPTION 'private memory maintenance is incomplete';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
DO $gates$
DECLARE relation RECORD;
BEGIN
 FOR relation IN SELECT DISTINCT table_name FROM information_schema.columns WHERE table_schema='public' AND
  ((column_name='space_id' AND table_name NOT LIKE 'private_memory_%') OR table_name IN ('audit_log','credentials','team_memberships','membership_grants','actor_identities','ownership_aliases','memory_spaces')) LOOP
  EXECUTE format('CREATE TRIGGER private_memory_maintenance_gate BEFORE INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION dense_mem_private_migration_write_gate()',relation.table_name);
 END LOOP;
END $gates$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'restore the verified pre-migration backup with instances stopped'; END $$;
-- +goose StatementEnd
