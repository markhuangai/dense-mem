-- Lock/rewrite impact: empty payload/checkpoint tables and short trigger DDL; no Evidence rewrite.
-- RLS impact: preserve shared-space/generation and draft-owner scope with FORCE RLS.
-- Backfill: the next application-owned migration commits bounded batches and checkpoints.
-- Backward compatibility: normal writers are fenced until verified cutover; old writers stay fenced afterward.
-- Rollback: resume with the matching application; no automatic destructive Down.

-- +goose Up
-- +goose StatementBegin
SET LOCAL app.tx_mode = 'migration';
SET LOCAL lock_timeout = '5s';

CREATE TABLE data_migration_runs (
    migration_version BIGINT PRIMARY KEY,
    executor_version TEXT NOT NULL,
    inventory_fingerprint TEXT NOT NULL,
    schema_fingerprint TEXT NOT NULL,
    source_fingerprint TEXT,
    phase TEXT NOT NULL CHECK (phase IN ('snapshot','copy','ready','complete')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE data_migration_checkpoints (
    migration_version BIGINT NOT NULL REFERENCES data_migration_runs(migration_version),
    phase TEXT NOT NULL CHECK (phase IN ('snapshot','copy','verify')),
    work_unit TEXT NOT NULL,
    cursor JSONB NOT NULL DEFAULT '[]',
    row_count BIGINT NOT NULL DEFAULT 0 CHECK (row_count >= 0),
    batch_count BIGINT NOT NULL DEFAULT 0 CHECK (batch_count >= 0),
    fingerprint TEXT NOT NULL DEFAULT '',
    complete BOOLEAN NOT NULL DEFAULT false,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (migration_version,phase,work_unit)
);
DO $security$
DECLARE relation TEXT;
BEGIN
    FOREACH relation IN ARRAY ARRAY['data_migration_runs','data_migration_checkpoints'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',relation);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',relation);
        EXECUTE format('CREATE POLICY migration_read ON %I FOR SELECT USING (current_setting(''app.tx_mode'',true) IN (''system'',''migration''))',relation);
        EXECUTE format('CREATE POLICY migration_write ON %I FOR ALL USING (current_setting(''app.tx_mode'',true)=''migration'') WITH CHECK (current_setting(''app.tx_mode'',true)=''migration'')',relation);
    END LOOP;
END $security$;

CREATE TABLE ontology_draft_envelopes (
    team_id UUID NOT NULL,
    shared_space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL,
    owner_profile_id UUID NOT NULL,
    draft_id UUID NOT NULL,
    version BIGINT NOT NULL,
    format_version SMALLINT NOT NULL DEFAULT 1 CHECK (format_version=1),
    envelope JSONB NOT NULL CHECK (jsonb_typeof(envelope)='object'),
    edits_shape TEXT NOT NULL CHECK (edits_shape IN ('missing','null','array')),
    changes_shape TEXT NOT NULL CHECK (changes_shape IN ('missing','null','array')),
    edit_count INTEGER NOT NULL CHECK (edit_count BETWEEN 0 AND 64),
    change_count INTEGER NOT NULL CHECK (change_count BETWEEN 0 AND 64),
    PRIMARY KEY (team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version),
    FOREIGN KEY (team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version)
        REFERENCES ontology_draft_revisions(team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version) ON DELETE RESTRICT
);

DO $payloads$
DECLARE feature TEXT; record_kind TEXT; relation TEXT; draft BOOLEAN;
BEGIN
    FOR feature,record_kind IN SELECT * FROM (VALUES ('topic','topic'),('membership','assignment'),('equivalence','evidence_group'),('override','override')) kinds(feature,kind) LOOP
        relation := 'ontology_'||feature||'_revision_payloads';
        EXECUTE format('CREATE TABLE %I (
            team_id UUID NOT NULL,shared_space_id UUID NOT NULL,space_generation BIGINT NOT NULL,
            record_id UUID NOT NULL,version BIGINT NOT NULL,
            format_version SMALLINT NOT NULL DEFAULT 1 CHECK (format_version=1),
            body JSONB NOT NULL CHECK (jsonb_typeof(body)=''object'' AND body->>''kind''=%L AND octet_length(body::text)<=65536),
            PRIMARY KEY(team_id,shared_space_id,space_generation,record_id,version),
            FOREIGN KEY(team_id,shared_space_id,space_generation,record_id,version)
                REFERENCES ontology_record_revisions(team_id,shared_space_id,space_generation,record_id,version) ON DELETE RESTRICT)',relation,record_kind);
        relation := 'ontology_'||feature||'_draft_payloads';
        EXECUTE format('CREATE TABLE %I (
            team_id UUID NOT NULL,shared_space_id UUID NOT NULL,space_generation BIGINT NOT NULL,
            owner_profile_id UUID NOT NULL,draft_id UUID NOT NULL,version BIGINT NOT NULL,
            container TEXT NOT NULL CHECK (container IN (''edit'',''change'')),
            ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 0 AND 63),
            format_version SMALLINT NOT NULL DEFAULT 1 CHECK (format_version=1),
            body JSONB NOT NULL CHECK (jsonb_typeof(body)=''object'' AND octet_length(body::text)<=2097152),
            PRIMARY KEY(team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version,container,ordinal),
            FOREIGN KEY(team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version)
                REFERENCES ontology_draft_revisions(team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version) ON DELETE RESTRICT)',relation);
    END LOOP;
    FOR relation IN SELECT unnest(ARRAY['ontology_draft_envelopes',
        'ontology_topic_revision_payloads','ontology_membership_revision_payloads','ontology_equivalence_revision_payloads','ontology_override_revision_payloads',
        'ontology_topic_draft_payloads','ontology_membership_draft_payloads','ontology_equivalence_draft_payloads','ontology_override_draft_payloads']) LOOP
        draft := relation='ontology_draft_envelopes' OR relation LIKE '%_draft_payloads';
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',relation);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',relation);
        EXECUTE format('CREATE POLICY feature_read ON %I FOR SELECT USING (
            current_setting(''app.tx_mode'',true) IN (''system'',''migration'') OR (
            current_setting(''app.tx_mode'',true) IN (''team'',''profile'')
            AND team_id=NULLIF(current_setting(''app.current_team_id'',true),'''')::uuid
            AND shared_space_id=dense_mem_team_shared_space(team_id)
            AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id)%s))',relation,
            CASE WHEN draft THEN ' AND current_setting(''app.tx_mode'',true)=''profile'' AND owner_profile_id=NULLIF(current_setting(''app.current_profile_id'',true),'''')::uuid' ELSE '' END);
        EXECUTE format('CREATE POLICY feature_insert ON %I FOR INSERT WITH CHECK (
            current_setting(''app.tx_mode'',true)=''migration'' OR (
            shared_space_id=dense_mem_team_shared_space(team_id)
            AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id)
            AND (current_setting(''app.tx_mode'',true)=''system'' OR (
            current_setting(''app.tx_mode'',true) IN (''team'',''profile'')
            AND team_id=NULLIF(current_setting(''app.current_team_id'',true),'''')::uuid%s))))',relation,
            CASE WHEN draft THEN ' AND current_setting(''app.tx_mode'',true)=''profile'' AND owner_profile_id=NULLIF(current_setting(''app.current_profile_id'',true),'''')::uuid' ELSE '' END);
        EXECUTE format('CREATE TRIGGER feature_append_only BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation()',relation);
    END LOOP;
END $payloads$;

CREATE VIEW ontology_revision_payloads WITH (security_invoker=true) AS
    SELECT *, 'topic'::text AS kind FROM ontology_topic_revision_payloads
    UNION ALL SELECT *, 'assignment'::text FROM ontology_membership_revision_payloads
    UNION ALL SELECT *, 'evidence_group'::text FROM ontology_equivalence_revision_payloads
    UNION ALL SELECT *, 'override'::text FROM ontology_override_revision_payloads;
CREATE VIEW ontology_draft_payloads WITH (security_invoker=true) AS
    SELECT *, 'topic'::text AS kind FROM ontology_topic_draft_payloads
    UNION ALL SELECT *, 'assignment'::text FROM ontology_membership_draft_payloads
    UNION ALL SELECT *, 'evidence_group'::text FROM ontology_equivalence_draft_payloads
    UNION ALL SELECT *, 'override'::text FROM ontology_override_draft_payloads;

CREATE FUNCTION dense_mem_feature_payload_table(record_kind TEXT, draft BOOLEAN) RETURNS TEXT
LANGUAGE plpgsql IMMUTABLE STRICT AS $$
DECLARE feature TEXT;
BEGIN
    feature := CASE record_kind WHEN 'topic' THEN 'topic' WHEN 'assignment' THEN 'membership' WHEN 'evidence_group' THEN 'equivalence' WHEN 'override' THEN 'override' END;
    IF feature IS NULL THEN RAISE EXCEPTION 'feature_storage_blocked: unsupported payload kind'; END IF;
    RETURN 'ontology_'||feature||CASE WHEN draft THEN '_draft_payloads' ELSE '_revision_payloads' END;
END $$;

CREATE FUNCTION dense_mem_store_feature_revision(team UUID, space UUID, generation BIGINT, id UUID, revision BIGINT, record_kind TEXT, payload JSONB) RETURNS VOID
LANGUAGE plpgsql AS $$
DECLARE relation TEXT; existing JSONB;
BEGIN
    relation := dense_mem_feature_payload_table(record_kind,false);
    EXECUTE format('INSERT INTO %I(team_id,shared_space_id,space_generation,record_id,version,body) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING',relation)
        USING team,space,generation,id,revision,payload;
    EXECUTE format('SELECT body FROM %I WHERE (team_id,shared_space_id,space_generation,record_id,version)=($1,$2,$3,$4,$5)',relation)
        INTO existing USING team,space,generation,id,revision;
    IF existing IS DISTINCT FROM payload THEN
        RAISE EXCEPTION 'feature_storage_blocked: destination payload mismatch' USING DETAIL=
            'expected='||encode(sha256(convert_to(payload::text,'UTF8')),'hex')||';observed='||COALESCE(encode(sha256(convert_to(existing::text,'UTF8')),'hex'),'missing');
    END IF;
END $$;

CREATE FUNCTION dense_mem_feature_draft_body(team UUID, space UUID, generation BIGINT, owner_id UUID, id UUID, revision BIGINT) RETURNS JSONB
LANGUAGE plpgsql STABLE AS $$
DECLARE stored ontology_draft_envelopes%ROWTYPE; result JSONB; items JSONB; n INTEGER; distinct_n INTEGER;
BEGIN
    SELECT * INTO stored FROM ontology_draft_envelopes WHERE (team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version)=(team,space,generation,owner_id,id,revision);
    IF NOT FOUND THEN RAISE EXCEPTION 'feature_storage_blocked: missing draft envelope'; END IF;
    result := stored.envelope;
    SELECT count(*),count(DISTINCT (container,ordinal)) INTO n,distinct_n FROM ontology_draft_payloads
        WHERE (team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version)=(team,space,generation,owner_id,id,revision);
    IF n<>stored.edit_count+stored.change_count OR n<>distinct_n THEN RAISE EXCEPTION 'feature_storage_blocked: draft payload count mismatch'; END IF;
    IF stored.edits_shape<>'missing' THEN
        SELECT COALESCE(jsonb_agg(body ORDER BY ordinal),'[]'::jsonb) INTO items FROM ontology_draft_payloads
            WHERE (team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version)=(team,space,generation,owner_id,id,revision) AND container='edit';
        result := jsonb_set(result,'{draft,edits}',CASE WHEN stored.edits_shape='null' THEN 'null'::jsonb ELSE items END);
    END IF;
    IF stored.changes_shape<>'missing' THEN
        SELECT COALESCE(jsonb_agg(body ORDER BY ordinal),'[]'::jsonb) INTO items FROM ontology_draft_payloads
            WHERE (team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version)=(team,space,generation,owner_id,id,revision) AND container='change';
        result := jsonb_set(result,'{prepared,changes}',CASE WHEN stored.changes_shape='null' THEN 'null'::jsonb ELSE items END);
    END IF;
    RETURN result;
END $$;

CREATE FUNCTION dense_mem_store_feature_draft(team UUID, space UUID, generation BIGINT, owner_id UUID, id UUID, revision BIGINT, payload JSONB) RETURNS VOID
LANGUAGE plpgsql AS $$
DECLARE edits TEXT; changes TEXT; header JSONB; item RECORD; record_kind TEXT; relation TEXT; existing JSONB;
BEGIN
    edits := COALESCE(jsonb_typeof(payload#>'{draft,edits}'),'missing');
    changes := COALESCE(jsonb_typeof(payload#>'{prepared,changes}'),'missing');
    IF edits NOT IN ('missing','null','array') OR changes NOT IN ('missing','null','array') THEN
        RAISE EXCEPTION 'feature_storage_blocked: unsupported draft shape';
    END IF;
    header := payload #- '{draft,edits}' #- '{prepared,changes}';
    INSERT INTO ontology_draft_envelopes(team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version,envelope,edits_shape,changes_shape,edit_count,change_count)
        VALUES(team,space,generation,owner_id,id,revision,header,edits,changes,
            CASE WHEN edits='array' THEN jsonb_array_length(payload#>'{draft,edits}') ELSE 0 END,
            CASE WHEN changes='array' THEN jsonb_array_length(payload#>'{prepared,changes}') ELSE 0 END) ON CONFLICT DO NOTHING;
    FOR item IN
        SELECT 'edit' AS container,value AS body,ordinality-1 AS ordinal FROM jsonb_array_elements(CASE WHEN edits='array' THEN payload#>'{draft,edits}' ELSE '[]'::jsonb END) WITH ORDINALITY
        UNION ALL SELECT 'change',value,ordinality-1 FROM jsonb_array_elements(CASE WHEN changes='array' THEN payload#>'{prepared,changes}' ELSE '[]'::jsonb END) WITH ORDINALITY
    LOOP
        record_kind := CASE WHEN item.container='change' THEN item.body#>>'{record,kind}'
            WHEN item.body->>'action'='define' THEN item.body->>'kind'
            WHEN item.body->>'action' IN ('classify','group_together','keep_separate','pin_definition','remove_override') THEN 'override' END;
        relation := dense_mem_feature_payload_table(record_kind,true);
        IF relation IS NULL THEN RAISE EXCEPTION 'feature_storage_blocked: draft feature is unavailable'; END IF;
        EXECUTE format('INSERT INTO %I(team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version,container,ordinal,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING',relation)
            USING team,space,generation,owner_id,id,revision,item.container,item.ordinal,item.body;
    END LOOP;
    existing := dense_mem_feature_draft_body(team,space,generation,owner_id,id,revision);
    IF existing IS DISTINCT FROM payload THEN
        RAISE EXCEPTION 'feature_storage_blocked: destination draft mismatch' USING DETAIL=
            'expected='||encode(sha256(convert_to(payload::text,'UTF8')),'hex')||';observed='||COALESCE(encode(sha256(convert_to(existing::text,'UTF8')),'hex'),'missing');
    END IF;
END $$;

CREATE FUNCTION dense_mem_feature_storage_write_gate() RETURNS TRIGGER
LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE saved_mode TEXT; state TEXT; migration_pending BOOLEAN;
BEGIN
    saved_mode := current_setting('app.tx_mode',true);
    IF saved_mode='migration' THEN
        IF TG_OP='DELETE' THEN RETURN OLD; END IF;
        RETURN NEW;
    END IF;
    IF COALESCE(saved_mode,'')='' AND EXISTS(SELECT 1 FROM pg_roles WHERE rolname=session_user AND rolsuper) THEN
        IF TG_OP='DELETE' THEN RETURN OLD; END IF;
        RETURN NEW;
    END IF;
    PERFORM set_config('app.tx_mode','system',true);
    SELECT phase INTO state FROM data_migration_runs WHERE migration_version=20261113010001;
    migration_pending := state IS DISTINCT FROM 'complete';
    PERFORM set_config('app.tx_mode',COALESCE(saved_mode,''),true);
    IF migration_pending OR current_setting('app.storage_layout',true) IS DISTINCT FROM 'feature-storage-v1' THEN
        RAISE EXCEPTION 'feature_storage_blocked: incompatible writer or incomplete migration' USING ERRCODE='55000';
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('app.tx_mode',COALESCE(saved_mode,''),true);
    RAISE;
END $$;
DO $fence$
DECLARE relation RECORD;
BEGIN
    FOR relation IN SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
        WHERE n.nspname='public' AND c.relkind IN ('r','p') AND c.relname NOT IN ('goose_db_version','data_migration_runs','data_migration_checkpoints')
    LOOP
        EXECUTE format('CREATE POLICY feature_storage_inventory_read ON %I FOR SELECT USING (current_setting(''app.tx_mode'',true)=''migration'')',relation.relname);
        EXECUTE format('CREATE TRIGGER feature_storage_write_gate BEFORE INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION dense_mem_feature_storage_write_gate()',relation.relname);
    END LOOP;
END $fence$;

CREATE FUNCTION dense_mem_feature_catalog_fingerprint() RETURNS TEXT LANGUAGE sql STABLE AS $$
    SELECT 'sha256:'||encode(sha256(convert_to(COALESCE(jsonb_agg(jsonb_build_object(
        'name',c.relname,'rls',c.relrowsecurity,'force_rls',c.relforcerowsecurity,
        'columns',(SELECT jsonb_agg(jsonb_build_array(a.attname,format_type(a.atttypid,a.atttypmod),a.attnotnull) ORDER BY a.attnum)
            FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped),
        'constraints',(SELECT jsonb_agg(jsonb_build_array(f.conname,pg_get_constraintdef(f.oid,true),f.convalidated) ORDER BY f.conname)
            FROM pg_constraint f WHERE f.conrelid=c.oid),
        'policies',(SELECT jsonb_agg(jsonb_build_array(p.polname,p.polcmd,p.polpermissive,p.polroles,pg_get_expr(p.polqual,p.polrelid),pg_get_expr(p.polwithcheck,p.polrelid)) ORDER BY p.polname)
            FROM pg_policy p WHERE p.polrelid=c.oid),
        'triggers',(SELECT jsonb_agg(jsonb_build_array(t.tgname,pg_get_triggerdef(t.oid,true),t.tgenabled,pg_get_functiondef(t.tgfoid)) ORDER BY t.tgname)
            FROM pg_trigger t WHERE t.tgrelid=c.oid AND NOT t.tgisinternal
            AND (c.relname NOT LIKE 'ontology_%_payloads' OR t.tgname IN ('feature_append_only','feature_storage_write_gate'))),
        'view',CASE WHEN c.relkind='v' THEN pg_get_viewdef(c.oid,true) END
    ) ORDER BY c.relname)::text,'[]'),'UTF8')),'hex')
    FROM pg_class c WHERE c.relnamespace='public'::regnamespace AND c.relkind IN ('r','p','v') AND c.relname<>'goose_db_version';
$$;
INSERT INTO data_migration_runs(migration_version,executor_version,inventory_fingerprint,schema_fingerprint,phase)
    VALUES(20261113010001,'feature-storage-v1',
        'sha256:'||encode(sha256(convert_to(E'feature-storage-v1\nall public application tables with primary keys; exclude goose_db_version,data_migration_runs,data_migration_checkpoints,ontology_draft_envelopes,ontology_{topic,membership,equivalence,override}_{revision,draft}_payloads','UTF8')),'hex'),
        dense_mem_feature_catalog_fingerprint(),'snapshot');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'resume feature storage migration with a compatible application; recovery rolls forward'; END $$;
-- +goose StatementEnd
