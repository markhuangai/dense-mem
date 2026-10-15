-- +goose Up
-- +goose StatementBegin
-- Lock/rewrite impact: maintenance-only ALTERs take DDL locks; CHECK validation
-- and the handle index scan whole tables, while ownership/kind updates rewrite
-- affected rows. Lock acquisition is limited to 30s.
-- RLS impact: install team-visible catalog and owner-scoped session/erasure
-- policies with internal system/migration modes; preserve forced RLS.
-- Backfill: require the preflighted ready executor; finalize membership ownership,
-- private kinds, provisioning, queued-erasure provenance and sealed retry tombstones.
-- Backward compatibility: old binaries/workers must stay stopped because binding
-- and owner columns and the credential/profile-private kinds are removed.
-- Rollback: Down raises; before new writes restore the verified stopped-state
-- backup with its matching binary, and after new writes use forward recovery.
SELECT set_config('app.tx_mode','migration',true);
SET LOCAL lock_timeout = '30s';
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM private_memory_migration_state WHERE singleton AND phase='ready') THEN
  RAISE EXCEPTION 'private memory migration requires private-memory-preflight and the fingerprint-bound private-memory-migrate command with all instances stopped';
 END IF;
END $$;

UPDATE memory_spaces space SET owner_membership_id=membership.id
FROM team_memberships membership
WHERE membership.team_id=space.team_id AND space.owner_membership_id IS NULL AND (
 (space.kind='profile_private' AND membership.actor_identity_id=space.owner_profile_id)
 OR (space.kind='credential_private' AND membership.actor_identity_id=(SELECT actor_identity_id FROM credentials WHERE id=space.owner_credential_id AND team_id=space.team_id)));
UPDATE memory_spaces SET kind='private',owner_profile_id=NULL,owner_credential_id=NULL WHERE kind IN ('profile_private','credential_private');

DROP TRIGGER credentials_memory_space_defaults ON credentials;
DROP FUNCTION dense_mem_credentials_space_defaults();
DROP TRIGGER team_memberships_memory_space_defaults ON team_memberships;
DROP FUNCTION dense_mem_initialize_profile_private_space();
DROP FUNCTION dense_mem_ensure_private_space(UUID,TEXT,UUID);
ALTER TABLE credentials DROP COLUMN memory_binding, DROP COLUMN memory_space_id;
ALTER TABLE memory_spaces DROP CONSTRAINT memory_spaces_owner_shape;
ALTER TABLE memory_spaces DROP COLUMN owner_profile_id, DROP COLUMN owner_credential_id;
ALTER TABLE memory_spaces DROP CONSTRAINT memory_spaces_kind_check;
ALTER TABLE memory_spaces ADD CONSTRAINT memory_spaces_kind_check CHECK(kind IN ('private','team_shared'));
ALTER TABLE memory_spaces ADD CONSTRAINT memory_spaces_owner_shape CHECK ((kind='private')=(owner_membership_id IS NOT NULL));
DROP POLICY memory_spaces_select ON memory_spaces;
CREATE POLICY memory_spaces_select ON memory_spaces FOR SELECT USING (current_setting('app.tx_mode',true) IN ('system','migration') OR team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid);

CREATE FUNCTION dense_mem_ensure_private_space(p_team UUID,p_membership UUID) RETURNS UUID LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE previous_mode TEXT; result UUID; membership_active BOOLEAN;
BEGIN
 previous_mode:=current_setting('app.tx_mode',true);
 IF previous_mode NOT IN ('team','profile','system','migration') OR
  (previous_mode IN ('team','profile') AND NULLIF(current_setting('app.current_team_id',true),'')::uuid IS DISTINCT FROM p_team) THEN
  RAISE EXCEPTION 'private space provisioning is outside the authenticated team';
 END IF;
 PERFORM set_config('app.tx_mode','system',true);
 SELECT membership.status='active' AND actor.active INTO membership_active
 FROM team_memberships membership JOIN actor_identities actor ON actor.id=membership.actor_identity_id
 WHERE membership.id=p_membership AND membership.team_id=p_team FOR SHARE OF membership,actor;
 IF membership_active IS DISTINCT FROM true THEN RAISE EXCEPTION 'private space membership is inactive'; END IF;
 SELECT id INTO result FROM memory_spaces WHERE team_id=p_team AND owner_membership_id=p_membership AND kind='private' AND NOT migration_archive AND lifecycle_state <> 'retired';
 IF result IS NULL THEN
  IF EXISTS(SELECT 1 FROM memory_spaces WHERE team_id=p_team AND owner_membership_id=p_membership AND kind='private' AND NOT migration_archive) THEN
   RAISE EXCEPTION 'retired private space cannot be provisioned again';
  END IF;
  INSERT INTO memory_spaces(team_id,kind,owner_membership_id) VALUES(p_team,'private',p_membership)
  ON CONFLICT(team_id,owner_membership_id) WHERE kind='private' AND NOT migration_archive AND lifecycle_state<>'retired' DO UPDATE SET owner_membership_id=EXCLUDED.owner_membership_id RETURNING id INTO result;
 END IF;
 PERFORM set_config('app.tx_mode',COALESCE(previous_mode,''),true);
 RETURN result;
EXCEPTION WHEN OTHERS THEN PERFORM set_config('app.tx_mode',COALESCE(previous_mode,''),true); RAISE;
END $$;

CREATE FUNCTION dense_mem_initialize_membership_private_space() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE previous_mode TEXT;
BEGIN
 IF NEW.status='active' THEN
  previous_mode:=current_setting('app.tx_mode',true);
  PERFORM set_config('app.tx_mode','system',true);
  PERFORM dense_mem_ensure_private_space(NEW.team_id,NEW.id);
  PERFORM set_config('app.tx_mode',COALESCE(previous_mode,''),true);
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER team_memberships_memory_space_defaults AFTER INSERT ON team_memberships FOR EACH ROW EXECUTE FUNCTION dense_mem_initialize_membership_private_space();

-- Provisioning cannot create a replacement for revoked identities or retired spaces.
INSERT INTO memory_spaces(team_id,kind,owner_membership_id)
SELECT membership.team_id,'private',membership.id FROM team_memberships membership JOIN actor_identities actor ON actor.id=membership.actor_identity_id
WHERE membership.status='active' AND actor.active AND NOT EXISTS(SELECT 1 FROM memory_spaces space WHERE space.team_id=membership.team_id AND space.owner_membership_id=membership.id AND space.kind='private' AND NOT migration_archive);

ALTER TABLE private_memory_erasure_operations DROP CONSTRAINT private_memory_erasure_operations_action_check;
ALTER TABLE private_memory_erasure_operations ADD CONSTRAINT private_memory_erasure_operations_action_check CHECK(action IN ('erase_private','erase_profile_private','erase_credential_private','retire_credential','retention_purge'));
ALTER TABLE private_memory_erasure_operations DROP CONSTRAINT private_memory_erasure_operations_space_kind_check;
ALTER TABLE private_memory_erasure_operations ADD CONSTRAINT private_memory_erasure_operations_space_kind_check CHECK(space_kind IN ('private','profile_private','credential_private'));

CREATE OR REPLACE FUNCTION dense_mem_note_private_content() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE previous_mode TEXT;
BEGIN
 previous_mode:=current_setting('app.tx_mode',true);
 PERFORM set_config('app.tx_mode','system',true);
 UPDATE memory_spaces SET private_content_at=GREATEST(COALESCE(private_content_at,NEW.created_at),NEW.created_at),updated_at=GREATEST(updated_at,NEW.created_at) WHERE id=NEW.space_id AND kind='private';
 PERFORM set_config('app.tx_mode',COALESCE(previous_mode,''),true);
 RETURN NEW;
EXCEPTION WHEN OTHERS THEN PERFORM set_config('app.tx_mode',COALESCE(previous_mode,''),true); RAISE;
END $$;


CREATE INDEX private_memory_migration_handle_lookup ON private_memory_migration_records(team_id,source_table,(source_key->>1),destination_membership_id) WHERE NOT sealed AND purged_at IS NULL;
CREATE FUNCTION dense_mem_resolve_memory_handle(p_team UUID,p_table TEXT,p_id UUID,p_space UUID DEFAULT NULL) RETURNS UUID
LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE previous_mode TEXT; allowed_spaces UUID[]; owner UUID; destination UUID;
BEGIN
 previous_mode:=current_setting('app.tx_mode',true);
 IF p_table NOT IN ('evidence_fragments','relationship_records','entity_records','value_records') OR
  previous_mode NOT IN ('team','profile') OR
  p_team IS DISTINCT FROM NULLIF(current_setting('app.current_team_id',true),'')::uuid THEN RETURN p_id; END IF;
 owner:=NULLIF(current_setting('app.memory_owner_id',true),'')::uuid;
 allowed_spaces:=string_to_array(NULLIF(current_setting('app.allowed_space_ids',true),''),',')::uuid[];
 PERFORM set_config('app.tx_mode','system',true);
 SELECT (mapping.destination_key->>1)::uuid INTO destination
 FROM private_memory_migration_records mapping
 JOIN memory_spaces space ON space.team_id=mapping.team_id AND space.id=mapping.destination_space_id
  AND space.generation=mapping.destination_generation AND space.lifecycle_state='active' AND space.kind='private'
 JOIN team_memberships membership ON membership.team_id=space.team_id AND membership.id=space.owner_membership_id AND membership.status='active'
 JOIN actor_identities actor ON actor.id=membership.actor_identity_id AND actor.active
 JOIN ownership_aliases alias ON alias.team_id=membership.team_id AND alias.canonical_identity_id=membership.actor_identity_id AND alias.legacy_owner_id=owner
 WHERE mapping.team_id=p_team AND mapping.source_table=p_table AND mapping.source_key->>1=p_id::text
  AND mapping.destination_membership_id=membership.id AND mapping.destination_space_id=ANY(allowed_spaces)
  AND (p_space IS NULL OR p_space=mapping.destination_space_id)
  AND mapping.completed AND NOT mapping.sealed AND mapping.purged_at IS NULL;
 PERFORM set_config('app.tx_mode',COALESCE(previous_mode,''),true);
 RETURN COALESCE(destination,p_id);
EXCEPTION WHEN OTHERS THEN PERFORM set_config('app.tx_mode',COALESCE(previous_mode,''),true); RAISE;
END $$;


CREATE FUNCTION dense_mem_inherited_private_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='semantic_assessments' THEN
  SELECT space_id,space_generation INTO NEW.space_id,NEW.space_generation FROM knowledge_ingests
  WHERE team_id=NEW.team_id AND ingest_id=NEW.attempt_id AND owner_profile_id=NEW.owner_profile_id;
 ELSE
  SELECT space_id,space_generation INTO NEW.space_id,NEW.space_generation FROM remember_attempts
  WHERE team_id=NEW.team_id AND attempt_id=NEW.attempt_id AND owner_profile_id=NEW.owner_profile_id;
 END IF;
 IF NOT FOUND THEN RAISE EXCEPTION 'private history has no authoritative submission'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER remember_attempt_events_inherited_scope BEFORE INSERT ON remember_attempt_events FOR EACH ROW EXECUTE FUNCTION dense_mem_inherited_private_scope();
CREATE TRIGGER remember_attempt_diagnostics_inherited_scope BEFORE INSERT ON remember_attempt_diagnostics FOR EACH ROW EXECUTE FUNCTION dense_mem_inherited_private_scope();
CREATE TRIGGER remember_failure_artifacts_inherited_scope BEFORE INSERT ON remember_failure_artifacts FOR EACH ROW EXECUTE FUNCTION dense_mem_inherited_private_scope();
CREATE TRIGGER semantic_assessments_inherited_scope BEFORE INSERT ON semantic_assessments FOR EACH ROW EXECUTE FUNCTION dense_mem_inherited_private_scope();

ALTER TABLE private_memory_erasure_operations ADD COLUMN legacy_source_space_id UUID REFERENCES memory_spaces(id);
UPDATE private_memory_erasure_operations SET legacy_source_space_id=space_id,space_kind='private'
WHERE space_id IS NOT NULL AND status IN ('queued','processing','failed');

CREATE TABLE private_memory_erased_operations (
 team_id UUID NOT NULL REFERENCES teams(id),
 owner_profile_id UUID NOT NULL,
 operation_kind TEXT NOT NULL CHECK(operation_kind IN ('remember','session')),
 key_hash TEXT NOT NULL,
 erased_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(team_id,owner_profile_id,operation_kind,key_hash)
);
ALTER TABLE private_memory_erased_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE private_memory_erased_operations FORCE ROW LEVEL SECURITY;
CREATE POLICY private_memory_erased_operations_system ON private_memory_erased_operations USING(current_setting('app.tx_mode',true) IN ('system','migration')) WITH CHECK(current_setting('app.tx_mode',true) IN ('system','migration'));
CREATE POLICY private_memory_erased_operations_owner ON private_memory_erased_operations FOR SELECT USING(team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid AND owner_profile_id=NULLIF(current_setting('app.current_profile_id',true),'')::uuid);

INSERT INTO private_memory_erased_operations(team_id,owner_profile_id,operation_kind,key_hash)
SELECT intake.team_id,intake.owner_profile_id,'session',encode(sha256(convert_to(intake.idempotency_key,'UTF8')),'hex')
FROM session_submissions intake JOIN private_memory_migration_records mapping
 ON mapping.source_table='session_submissions' AND mapping.destination_key=jsonb_build_array(intake.team_id,intake.space_id,intake.submission_id)
JOIN memory_spaces source ON source.id=mapping.source_space_id AND source.lifecycle_state<>'active'
WHERE mapping.sealed AND mapping.completed ON CONFLICT DO NOTHING;
INSERT INTO private_memory_erased_operations(team_id,owner_profile_id,operation_kind,key_hash)
SELECT attempt.team_id,attempt.owner_profile_id,'remember',encode(sha256(convert_to(attempt.idempotency_key,'UTF8')),'hex')
FROM remember_attempts attempt JOIN private_memory_migration_records mapping
 ON mapping.source_table='remember_attempts' AND mapping.destination_key=jsonb_build_array(attempt.team_id,attempt.attempt_id)
JOIN memory_spaces source ON source.id=mapping.source_space_id AND source.lifecycle_state<>'active'
WHERE mapping.sealed AND mapping.completed AND attempt.idempotency_key<>'' ON CONFLICT DO NOTHING;

CREATE FUNCTION dense_mem_private_lineage_spaces(p_space UUID,p_legacy UUID DEFAULT NULL)
RETURNS TABLE(space_id UUID,legacy_source BOOLEAN) LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE previous_mode TEXT;
BEGIN
 previous_mode:=current_setting('app.tx_mode',true);
 PERFORM set_config('app.tx_mode','system',true);
 RETURN QUERY WITH target AS (SELECT id,team_id,CASE WHEN migration_archive THEN NULL ELSE owner_membership_id END owner_membership_id FROM memory_spaces WHERE id=p_space AND kind='private'),
 roots AS (SELECT mapping.* FROM private_memory_migration_records mapping,target
  WHERE mapping.team_id=target.team_id AND mapping.purged_at IS NULL AND (mapping.destination_space_id=target.id OR mapping.original_membership_ids @> ARRAY[target.owner_membership_id] OR mapping.source_space_id=p_legacy)),
 records AS (SELECT mapping.* FROM private_memory_migration_records mapping JOIN roots ON mapping.team_id=roots.team_id AND mapping.source_table=roots.source_table AND mapping.source_key=roots.source_key AND mapping.source_space_id=roots.source_space_id AND mapping.source_generation=roots.source_generation WHERE mapping.purged_at IS NULL)
 SELECT p_space,false UNION SELECT destination_space_id,false FROM records UNION SELECT source_space_id,true FROM records
 UNION SELECT owner_space.id,false FROM memory_spaces owner_space JOIN roots
  ON owner_space.team_id=roots.team_id AND owner_space.owner_membership_id=ANY(roots.original_membership_ids)
  WHERE owner_space.kind='private' AND NOT owner_space.migration_archive;
 PERFORM set_config('app.tx_mode',COALESCE(previous_mode,''),true);
EXCEPTION WHEN OTHERS THEN PERFORM set_config('app.tx_mode',COALESCE(previous_mode,''),true); RAISE;
END $$;
CREATE FUNCTION dense_mem_private_space_held(p_space UUID,p_legacy UUID DEFAULT NULL) RETURNS BOOLEAN LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE previous_mode TEXT; held BOOLEAN;
BEGIN
 previous_mode:=current_setting('app.tx_mode',true);
 PERFORM set_config('app.tx_mode','system',true);
 SELECT EXISTS(SELECT 1 FROM private_memory_legal_holds hold
 JOIN dense_mem_private_lineage_spaces(p_space,p_legacy) lineage ON lineage.space_id=hold.space_id
 WHERE hold.released_at IS NULL) INTO held;
 PERFORM set_config('app.tx_mode',COALESCE(previous_mode,''),true);
 RETURN held;
EXCEPTION WHEN OTHERS THEN PERFORM set_config('app.tx_mode',COALESCE(previous_mode,''),true); RAISE;
END $$;

CREATE OR REPLACE FUNCTION prevent_remember_attempt_diagnostics_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND current_setting('app.tx_mode',true)='system'
  AND NEW.space_id=NULLIF(current_setting('app.remember_attempt_diagnostic_retention_space_id',true),'')::uuid
  AND (to_jsonb(NEW)-ARRAY['retained_by_legal_hold'])=(to_jsonb(OLD)-ARRAY['retained_by_legal_hold'])
  AND NEW.retained_by_legal_hold=EXISTS(SELECT 1 FROM private_memory_legal_holds WHERE space_id=NEW.space_id AND released_at IS NULL) THEN RETURN NEW; END IF;
 IF TG_OP='DELETE' AND current_setting('app.tx_mode',true)='system'
  AND ((current_setting('app.remember_attempt_diagnostic_purge',true)='true' AND OLD.expires_at<=clock_timestamp())
   OR OLD.space_id=NULLIF(current_setting('app.private_erasure_space_id',true),'')::uuid) THEN RETURN OLD; END IF;
 RAISE EXCEPTION '% is append-only: % operations are not allowed',TG_TABLE_NAME,TG_OP;
END $$;

-- Lock the complete lineage before the live check, including diagnostic TTL purges.
CREATE FUNCTION dense_mem_guard_private_hold() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.space_id IS NOT NULL AND current_setting('app.tx_mode',true)<>'migration' THEN
  PERFORM 1 FROM memory_spaces WHERE id IN (SELECT space_id FROM dense_mem_private_lineage_spaces(OLD.space_id)) ORDER BY id FOR KEY SHARE;
  IF dense_mem_private_space_held(OLD.space_id) THEN RAISE EXCEPTION 'private memory legal hold blocks deletion'; END IF;
 END IF;
 RETURN OLD;
END $$;

CREATE FUNCTION dense_mem_private_generation_rebase(old_row JSONB,new_row JSONB) RETURNS BOOLEAN LANGUAGE sql STABLE AS $$
 SELECT current_setting('app.tx_mode',true)='system'
 AND NULLIF(current_setting('app.private_erasure_rebase_space_id',true),'')::uuid=(old_row->>'space_id')::uuid
 -- PostgreSQL computes generated search_tsv after BEFORE triggers; document_text remains immutable here.
 AND (old_row-ARRAY['space_generation','search_tsv'])=(new_row-ARRAY['space_generation','search_tsv'])
 AND (new_row->>'space_generation')::bigint=(old_row->>'space_generation')::bigint+1
 AND EXISTS(SELECT 1 FROM memory_spaces space WHERE space.id=(new_row->>'space_id')::uuid AND space.team_id=(new_row->>'team_id')::uuid AND space.kind='private' AND space.lifecycle_state='active' AND space.generation=(new_row->>'space_generation')::bigint)
 AND EXISTS(SELECT 1 FROM private_memory_erasure_operations WHERE id=NULLIF(current_setting('app.private_erasure_operation_id',true),'')::uuid AND status='processing' AND team_id=(old_row->>'team_id')::uuid)
 AND EXISTS(SELECT 1 FROM private_memory_migration_state WHERE singleton AND phase='complete');
$$;

-- A purge fences in-flight writes in destinations that held supporting copies;
-- the only permitted history update changes their placement generation.
DO $generation_guards$
DECLARE routine RECORD; definition TEXT;
BEGIN
 FOR routine IN SELECT DISTINCT routine_row.oid FROM pg_trigger trigger_row
  JOIN pg_proc routine_row ON routine_row.oid=trigger_row.tgfoid
  JOIN pg_class relation ON relation.oid=trigger_row.tgrelid
  JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
  JOIN pg_language language ON language.oid=routine_row.prolang
  WHERE namespace.nspname='public' AND NOT trigger_row.tgisinternal AND language.lanname='plpgsql'
   AND EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name=relation.relname AND column_name='space_generation')
   AND EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name=relation.relname AND column_name='space_id') LOOP
  definition:=pg_get_functiondef(routine.oid);
  definition:=regexp_replace(definition,'\mBEGIN\M',E'BEGIN\n IF TG_OP = ''UPDATE'' AND current_setting(''app.tx_mode'',true) = ''system'' THEN\n  IF dense_mem_private_generation_rebase(to_jsonb(OLD),to_jsonb(NEW)) THEN RETURN NEW; END IF;\n END IF;');
  EXECUTE definition;
 END LOOP;
END $generation_guards$;

DO $held_deletes$
DECLARE relation RECORD;
BEGIN
 FOR relation IN SELECT table_name FROM information_schema.columns WHERE table_schema='public' AND column_name='space_id' AND table_name NOT LIKE 'private_memory_%' LOOP
  EXECUTE format('CREATE TRIGGER private_memory_held_deletion BEFORE DELETE ON %I FOR EACH ROW EXECUTE FUNCTION dense_mem_guard_private_hold()',relation.table_name);
 END LOOP;
END $held_deletes$;

DO $session_private_policies$
DECLARE relation TEXT;
BEGIN
 FOREACH relation IN ARRAY ARRAY['session_submissions','session_events','session_extraction_checkpoints','session_submission_receipts','session_submission_diagnostics'] LOOP
  EXECUTE format('DROP POLICY %I ON %I',relation||'_insert',relation);
  EXECUTE format($policy$
   CREATE POLICY %I ON %I FOR INSERT WITH CHECK (
    space_generation=dense_mem_active_space_generation(team_id,space_id)
    AND EXISTS(SELECT 1 FROM memory_spaces space WHERE space.team_id=%I.team_id AND space.id=%I.space_id AND space.kind='private')
    AND (current_setting('app.tx_mode',true) IN ('system','migration') OR
     (current_setting('app.tx_mode',true)='profile' AND team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid
      AND owner_profile_id=NULLIF(current_setting('app.current_profile_id',true),'')::uuid AND dense_mem_space_allowed(space_id)))
   )
  $policy$,relation||'_insert',relation,relation,relation);
 END LOOP;
END $session_private_policies$;

CREATE FUNCTION dense_mem_guard_private_generation_rebase() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF current_setting('app.tx_mode',true)='system'
  AND OLD.space_id=NULLIF(current_setting('app.private_erasure_rebase_space_id',true),'')::uuid
  AND NOT dense_mem_private_generation_rebase(to_jsonb(OLD),to_jsonb(NEW)) THEN
  RAISE EXCEPTION 'private erasure rebase can change only the placement generation';
 END IF;
 RETURN NEW;
END $$;
DO $private_placement_policies$
DECLARE relation RECORD;
BEGIN
 FOR relation IN SELECT placement.table_name FROM information_schema.columns placement
  WHERE placement.table_schema='public' AND placement.column_name='space_generation' AND placement.table_name NOT LIKE 'private_memory_%'
   AND EXISTS(SELECT 1 FROM information_schema.columns scope WHERE scope.table_schema=placement.table_schema AND scope.table_name=placement.table_name AND scope.column_name='space_id') LOOP
  EXECUTE format($policy$CREATE POLICY private_memory_placement_erasure ON %I FOR DELETE USING (
   current_setting('app.tx_mode',true)='system' AND space_id=NULLIF(current_setting('app.private_erasure_space_id',true),'')::uuid)
  $policy$,relation.table_name);
  EXECUTE format($policy$CREATE POLICY private_memory_generation_rebase ON %I FOR UPDATE USING (
   current_setting('app.tx_mode',true)='system' AND space_id=NULLIF(current_setting('app.private_erasure_rebase_space_id',true),'')::uuid
   AND EXISTS(SELECT 1 FROM private_memory_erasure_operations operation WHERE operation.id=NULLIF(current_setting('app.private_erasure_operation_id',true),'')::uuid AND operation.status='processing' AND operation.team_id=%I.team_id)
  ) WITH CHECK (
   current_setting('app.tx_mode',true)='system' AND space_id=NULLIF(current_setting('app.private_erasure_rebase_space_id',true),'')::uuid
   AND space_generation=dense_mem_active_space_generation(team_id,space_id))
  $policy$,relation.table_name,relation.table_name);
  EXECUTE format('CREATE TRIGGER private_memory_generation_rebase_guard BEFORE UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION dense_mem_guard_private_generation_rebase()',relation.table_name);
 END LOOP;
END $private_placement_policies$;

CREATE FUNCTION dense_mem_sso_memory_grants(p_team UUID,p_identity UUID) RETURNS TEXT[]
LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE previous_mode TEXT; provider UUID; subject_name TEXT; member UUID; maximum TEXT[]; group_name TEXT; current_group TEXT;
 connector UUID; groups TEXT[]; allowed TEXT[] := ARRAY[]::text[]; mapped RECORD;
BEGIN
 previous_mode:=current_setting('app.tx_mode',true);
 IF previous_mode NOT IN ('team','profile','system','migration') OR
  (previous_mode IN ('team','profile') AND p_team IS DISTINCT FROM NULLIF(current_setting('app.current_team_id',true),'')::uuid) THEN RETURN allowed; END IF;
 PERFORM set_config('app.tx_mode','system',true);
 SELECT identity.provider_id,identity.subject INTO provider,subject_name
 FROM sso_identities identity JOIN sso_providers authority ON authority.id=identity.provider_id
 WHERE identity.id=p_identity AND identity.active AND authority.enabled AND authority.retired_at IS NULL
 FOR SHARE OF authority,identity;
 IF provider IS NOT NULL THEN
  PERFORM 1 FROM sso_directory_connectors WHERE provider_id=provider ORDER BY id FOR SHARE;
  SELECT id INTO connector FROM sso_directory_connectors WHERE provider_id=provider AND status='active';
  IF connector IS NULL THEN
   SELECT cache.groups INTO groups FROM sso_entitlement_cache cache
   WHERE cache.provider_id=provider AND cache.subject=subject_name AND cache.status='active' AND cache.expires_at>clock_timestamp() FOR SHARE;
   FOR mapped IN SELECT mapping.scopes,mapping.role FROM sso_group_mappings mapping
    WHERE mapping.provider_id=provider AND mapping.team_id=p_team AND mapping.group_id=ANY(groups)
     AND mapping.enabled AND mapping.retired_at IS NULL ORDER BY mapping.id FOR SHARE LOOP
    allowed:=allowed || CASE WHEN mapped.role='manager' THEN ARRAY['read','write'] ELSE COALESCE(NULLIF(mapped.scopes,ARRAY[]::text[]),ARRAY['read']) END;
   END LOOP;
  ELSE
   SELECT sso_group_id INTO group_name FROM team_memberships WHERE team_id=p_team AND actor_identity_id=p_identity;
   FOR mapped IN SELECT mapping.scopes,mapping.role FROM sso_group_mappings mapping
    JOIN sso_directory_groups directory_group ON directory_group.connector_id=connector AND directory_group.active
     AND mapping.group_id=CASE WHEN directory_group.external_id<>'' THEN directory_group.external_id ELSE directory_group.id::text END
    JOIN sso_directory_group_memberships edge ON edge.connector_id=connector AND edge.group_id=directory_group.id
    JOIN sso_directory_users directory_user ON directory_user.connector_id=connector AND directory_user.id=edge.user_id AND directory_user.active AND directory_user.identity_id=p_identity
    WHERE mapping.provider_id=provider AND mapping.team_id=p_team AND mapping.group_id=group_name AND mapping.enabled AND mapping.retired_at IS NULL
    ORDER BY mapping.id,directory_group.id,directory_user.id FOR SHARE OF mapping,directory_group,edge,directory_user LOOP
    allowed:=allowed || CASE WHEN mapped.role='manager' THEN ARRAY['read','write'] ELSE COALESCE(NULLIF(mapped.scopes,ARRAY[]::text[]),ARRAY['read']) END;
   END LOOP;
  END IF;
  PERFORM 1 FROM actor_identities WHERE id=p_identity AND active FOR SHARE;
  IF NOT FOUND THEN allowed:=ARRAY[]::text[]; END IF;
  SELECT membership.id,membership.maximum_grants,membership.sso_group_id INTO member,maximum,current_group
  FROM team_memberships membership WHERE membership.team_id=p_team AND membership.actor_identity_id=p_identity
   AND membership.status='active' AND membership.sso_entitlement_status='active' AND membership.sso_provider_id=provider FOR SHARE;
  IF member IS NULL OR (connector IS NOT NULL AND current_group IS DISTINCT FROM group_name) THEN allowed:=ARRAY[]::text[];
  ELSE
   SELECT COALESCE(array_agg(grant_name),ARRAY[]::text[]) INTO allowed FROM (
    SELECT grant_name FROM membership_grants WHERE membership_id=member AND grant_name=ANY(maximum) AND grant_name=ANY(allowed) FOR SHARE
   ) live_grants;
  END IF;
 END IF;
 PERFORM set_config('app.tx_mode',COALESCE(previous_mode,''),true);
 RETURN allowed;
EXCEPTION WHEN OTHERS THEN PERFORM set_config('app.tx_mode',COALESCE(previous_mode,''),true); RAISE;
END $$;

UPDATE private_memory_migration_state SET phase='complete',updated_at=now() WHERE singleton;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'membership private memory cannot be downgraded; use forward recovery or the verified stopped-state backup'; END $$;
-- +goose StatementEnd
