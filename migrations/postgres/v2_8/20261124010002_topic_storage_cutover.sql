-- Lock/rewrite impact: exclusive catalog locks rename relations in place and build maintenance indexes.
-- RLS impact: preserves forced RLS, renames policies, and validates scoped outcome writes.
-- Backfill: verifies completed pages before moving stored settings and closing superseded runs.
-- Backward compatibility: the new application opens listeners after cutover and keeps historical receipts readable.
-- Rollback: resume with the compatible application or restore the verified pre-cutover backup.
-- +goose Up
-- +goose StatementBegin
SET LOCAL lock_timeout='5s';
SELECT set_config('app.tx_mode','migration',true);

DO $verified$
BEGIN
    IF NOT EXISTS(SELECT 1 FROM data_migration_runs WHERE migration_version=20261124010001 AND phase='ready')
        OR 5<>(SELECT count(*) FROM data_migration_checkpoints copied JOIN data_migration_checkpoints verified
             USING(migration_version,work_unit)
             WHERE copied.migration_version=20261124010001 AND copied.phase='copy' AND copied.complete
               AND verified.phase='verify' AND verified.complete AND copied.row_count=verified.row_count
               AND copied.fingerprint=verified.fingerprint AND copied.work_unit IN ('membership_heads','recall_capture','translated_topic_markers','retired_definition_markers','community_work')) THEN
        RAISE EXCEPTION 'topic storage cutover missing matching copy and verification checkpoints';
    END IF;
END $verified$;

CREATE TEMP TABLE topic_object_names(old_name text PRIMARY KEY,new_name text UNIQUE,kind text NOT NULL) ON COMMIT DROP;
INSERT INTO topic_object_names VALUES
 ('ontology_catalog_heads','topic_catalog_heads','TABLE'),
 ('ontology_record_heads','topic_record_heads','TABLE'),
 ('ontology_record_revisions','topic_record_revisions','TABLE'),
 ('ontology_publications','topic_publications','TABLE'),
 ('ontology_source_dependencies','topic_source_dependencies','TABLE'),
 ('ontology_revision_dependencies','topic_revision_dependencies','TABLE'),
 ('ontology_draft_heads','topic_draft_heads','TABLE'),
 ('ontology_draft_revisions','topic_draft_revisions','TABLE'),
 ('ontology_draft_envelopes','topic_draft_envelopes','TABLE'),
 ('ontology_assessments','historical_organization_assessments','TABLE'),
 ('ontology_topic_revision_payloads','topic_revision_payloads','TABLE'),
 ('ontology_topic_draft_payloads','topic_draft_payloads','TABLE'),
 ('ontology_membership_revision_payloads','topic_membership_revision_payloads','TABLE'),
 ('ontology_membership_draft_payloads','topic_membership_draft_payloads','TABLE'),
 ('ontology_equivalence_revision_payloads','evidence_equivalence_revision_payloads','TABLE'),
 ('ontology_equivalence_draft_payloads','evidence_equivalence_draft_payloads','TABLE'),
 ('ontology_override_revision_payloads','historical_topic_override_revision_payloads','TABLE'),
 ('ontology_override_draft_payloads','historical_topic_override_draft_payloads','TABLE'),
 ('ontology_revision_payloads','topic_all_revision_payloads','VIEW'),
 ('ontology_draft_payloads','topic_all_draft_payloads','VIEW'),
 ('ontology_revision_records','topic_revision_records','VIEW'),
 ('ontology_draft_revision_records','topic_draft_revision_records','VIEW'),
 ('ontology_draft_head_records','topic_draft_head_records','VIEW'),
 ('ontology_maintenance_marker_seq','topic_maintenance_marker_seq','SEQUENCE');
INSERT INTO topic_object_names
 SELECT 'ontology_maintenance_'||suffix,'topic_maintenance_'||suffix,'TABLE'
 FROM unnest(ARRAY['state','windows','teams','runs','markers','sources','batches','attempts']) AS names(suffix);

ALTER TABLE topic_object_names ADD COLUMN original_oid oid;
UPDATE topic_object_names SET original_oid=('public.'||old_name)::regclass;

-- References stored as PL/pgSQL source text and relation-derived constraint names do not follow a relation rename.
CREATE FUNCTION pg_temp.topic_name(value text) RETURNS text LANGUAGE plpgsql AS $names$
DECLARE mapping record;
BEGIN
    FOR mapping IN SELECT * FROM topic_object_names ORDER BY length(old_name) DESC,old_name LOOP
        value:=replace(value,mapping.old_name,mapping.new_name);
    END LOOP;
    RETURN replace(value,'ontology_','topic_');
END $names$;
CREATE FUNCTION pg_temp.topic_identifier(value text) RETURNS text LANGUAGE plpgsql AS $names$
DECLARE changed text:=pg_temp.topic_name(value);
BEGIN
    IF octet_length(changed)>63 THEN RETURN left(changed,54)||'_'||left(md5(value),8); END IF;
    RETURN changed;
END $names$;

DO $rename$
DECLARE obj record; signature text; definition text;
BEGIN
    FOR obj IN SELECT * FROM topic_object_names ORDER BY old_name LOOP
        EXECUTE format('ALTER %s public.%I RENAME TO %I',obj.kind,obj.old_name,obj.new_name);
    END LOOP;
    FOR obj IN SELECT p.oid,p.proname,pg_get_function_identity_arguments(p.oid) args
        FROM pg_proc p WHERE p.pronamespace='public'::regnamespace
        AND p.proname=ANY(ARRAY[
            'dense_mem_enqueue_ontology_marker','dense_mem_guard_ontology_head','dense_mem_guard_ontology_draft',
            'dense_mem_guard_ontology_window','dense_mem_guard_ontology_attempt','dense_mem_guard_ontology_allowance',
            'dense_mem_reject_ontology_allowance','dense_mem_insert_ontology_revision','dense_mem_persist_ontology_draft',
            'dense_mem_ontology_dirty_evidence_fragments','dense_mem_ontology_dirty_knowledge_ingests',
            'dense_mem_ontology_dirty_evidence_sources','dense_mem_ontology_dirty_evidence_occurrences',
            'dense_mem_ontology_dirty_evidence_quarantines','dense_mem_ontology_dirty_evidence_lifecycle_events',
            'dense_mem_ontology_dirty_memory_spaces','dense_mem_ontology_dirty_teams','dense_mem_ontology_dirty_ontology_record_heads',
            'dense_mem_feature_draft_body','dense_mem_store_feature_draft','dense_mem_check_feature_payload'])
        ORDER BY p.proname
    LOOP
        IF obj.proname<>pg_temp.topic_name(obj.proname) THEN
            signature:=format('%I.%I(%s)','public',obj.proname,obj.args);
            EXECUTE format('ALTER FUNCTION %s RENAME TO %I',signature,pg_temp.topic_identifier(obj.proname));
        END IF;
        definition:=pg_temp.topic_name(pg_get_functiondef(obj.oid));
        EXECUTE definition;
    END LOOP;
    FOR obj IN SELECT conrelid::regclass AS rel,conname FROM pg_constraint
        WHERE connamespace='public'::regnamespace AND conrelid<>0 AND conname LIKE 'ontology\_%' ESCAPE '\' LOOP
        EXECUTE format('ALTER TABLE %s RENAME CONSTRAINT %I TO %I',obj.rel,obj.conname,pg_temp.topic_identifier(obj.conname));
    END LOOP;
    FOR obj IN SELECT relname FROM pg_class WHERE relnamespace='public'::regnamespace AND relkind='i'
        AND relname LIKE 'ontology\_%' ESCAPE '\' LOOP
        EXECUTE format('ALTER INDEX public.%I RENAME TO %I',obj.relname,pg_temp.topic_identifier(obj.relname));
    END LOOP;
    FOR obj IN SELECT tgrelid::regclass AS rel,tgname FROM pg_trigger JOIN pg_class c ON c.oid=tgrelid
        WHERE relnamespace='public'::regnamespace AND NOT tgisinternal AND tgname LIKE '%ontology\_%' ESCAPE '\' LOOP
        EXECUTE format('ALTER TRIGGER %I ON %s RENAME TO %I',obj.tgname,obj.rel,pg_temp.topic_identifier(obj.tgname));
    END LOOP;
    FOR obj IN SELECT polrelid::regclass AS rel,polname FROM pg_policy JOIN pg_class c ON c.oid=polrelid
        WHERE relnamespace='public'::regnamespace AND polname LIKE 'ontology\_%' ESCAPE '\' LOOP
        EXECUTE format('ALTER POLICY %I ON %s RENAME TO %I',obj.polname,obj.rel,pg_temp.topic_identifier(obj.polname));
    END LOOP;
END $rename$;

CREATE OR REPLACE FUNCTION dense_mem_feature_payload_table(record_kind text,draft boolean) RETURNS text
LANGUAGE plpgsql IMMUTABLE STRICT AS $feature$
DECLARE prefix text;
BEGIN
    prefix:=CASE record_kind WHEN 'topic' THEN 'topic' WHEN 'assignment' THEN 'topic_membership'
        WHEN 'evidence_group' THEN 'evidence_equivalence' WHEN 'override' THEN 'historical_topic_override' END;
    IF prefix IS NULL THEN RAISE EXCEPTION 'unsupported topic payload kind'; END IF;
    RETURN prefix || CASE WHEN draft THEN '_draft_payloads' ELSE '_revision_payloads' END;
END $feature$;

CREATE FUNCTION dense_mem_validate_topic_outcome() RETURNS trigger LANGUAGE plpgsql AS $scope$
DECLARE topic_ids uuid[]; evidence_scope boolean;
BEGIN
    IF TG_TABLE_NAME='topic_assignment_outcomes' THEN
        SELECT EXISTS(SELECT 1 FROM evidence_fragments
               WHERE team_id=NEW.team_id AND fragment_id=NEW.evidence_id
                 AND space_id=NEW.shared_space_id AND space_generation=NEW.space_generation)
            INTO evidence_scope;
        IF NOT evidence_scope THEN RAISE EXCEPTION 'assignment Evidence is outside its publication scope' USING ERRCODE='23514'; END IF;
        topic_ids:=NEW.topic_ids;
    ELSE
        topic_ids:=array_remove(ARRAY[NEW.topic_id,NEW.parent_topic_id],NULL);
    END IF;
    IF cardinality(topic_ids)<>(SELECT count(*) FROM topic_record_heads h
        WHERE h.team_id=NEW.team_id AND h.shared_space_id=NEW.shared_space_id
        AND h.space_generation=NEW.space_generation AND h.kind='topic' AND NOT h.retired AND h.record_id=ANY(topic_ids))
    THEN RAISE EXCEPTION 'outcome requires distinct Topics in its active scope' USING ERRCODE='23514'; END IF;
    RETURN NEW;
END $scope$;
CREATE TRIGGER topic_assignment_scope BEFORE INSERT ON topic_assignment_outcomes FOR EACH ROW EXECUTE FUNCTION dense_mem_validate_topic_outcome();
CREATE TRIGGER community_placement_scope BEFORE INSERT ON community_placement_outcomes FOR EACH ROW EXECUTE FUNCTION dense_mem_validate_topic_outcome();

CREATE FUNCTION dense_mem_project_membership_pair() RETURNS trigger LANGUAGE plpgsql AS $pair$
BEGIN
    IF NEW.kind='assignment' THEN
        SELECT (body#>>'{assignment,source,id}')::uuid,(body#>>'{assignment,definition_id}')::uuid
          INTO STRICT NEW.evidence_id,NEW.topic_id FROM topic_membership_revision_payloads
          WHERE (team_id,shared_space_id,space_generation,record_id,version)=
                (NEW.team_id,NEW.shared_space_id,NEW.space_generation,NEW.record_id,NEW.version);
    ELSE
        NEW.evidence_id:=NULL; NEW.topic_id:=NULL;
    END IF;
    RETURN NEW;
END $pair$;
CREATE TRIGGER topic_membership_pair BEFORE INSERT OR UPDATE ON topic_record_heads
    FOR EACH ROW EXECUTE FUNCTION dense_mem_project_membership_pair();
DROP POLICY topic_storage_head_backfill ON topic_record_heads;
-- NULL marks a pre-cutover source awaiting its first incremental assignment.
ALTER TABLE topic_maintenance_sources ADD COLUMN last_assignment_input_hash text;
ALTER TABLE topic_maintenance_sources ALTER COLUMN last_assignment_input_hash SET DEFAULT '';
ALTER TABLE topic_maintenance_teams ADD COLUMN next_discovery_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX topic_maintenance_due_idx ON topic_maintenance_teams(next_discovery_at,last_turn,team_id);
ALTER TABLE topic_maintenance_batches ADD COLUMN community_topic_id uuid;
CREATE UNIQUE INDEX topic_maintenance_live_community_idx ON topic_maintenance_batches
    (team_id,shared_space_id,space_generation,community_topic_id)
    WHERE status='running' AND community_topic_id IS NOT NULL;

CREATE FUNCTION dense_mem_schedule_community_placement() RETURNS trigger LANGUAGE plpgsql AS $work$
DECLARE due timestamptz:=statement_timestamp();
BEGIN
    IF NEW.retired THEN
        DELETE FROM topic_maintenance_community_work w WHERE
            (w.team_id,w.shared_space_id,w.space_generation,w.topic_id)=
            (NEW.team_id,NEW.shared_space_id,NEW.space_generation,NEW.record_id);
    ELSE
        INSERT INTO topic_maintenance_community_work(team_id,shared_space_id,space_generation,topic_id,topic_version,ready_at)
            VALUES(NEW.team_id,NEW.shared_space_id,NEW.space_generation,NEW.record_id,NEW.version,due)
            ON CONFLICT(team_id,shared_space_id,space_generation,topic_id) DO UPDATE
            SET topic_version=EXCLUDED.topic_version,work_id=EXCLUDED.work_id,ready_at=EXCLUDED.ready_at;
    END IF;
    IF TG_OP='UPDATE' AND (OLD.version,OLD.retired) IS DISTINCT FROM (NEW.version,NEW.retired) THEN
        UPDATE topic_maintenance_community_work w SET work_id=gen_random_uuid(),ready_at=due
        WHERE (w.team_id,w.shared_space_id,w.space_generation,w.parent_topic_id,w.parent_topic_version)=
              (OLD.team_id,OLD.shared_space_id,OLD.space_generation,OLD.record_id,OLD.version);
    END IF;
    RETURN NEW;
END $work$;
CREATE TRIGGER topic_schedule_community AFTER INSERT OR UPDATE ON topic_record_heads
    FOR EACH ROW WHEN (NEW.kind='topic') EXECUTE FUNCTION dense_mem_schedule_community_placement();


-- A historical draft may still be inspected. Only new active feature payloads can be published.
DROP POLICY feature_insert ON historical_topic_override_revision_payloads;
DROP POLICY feature_insert ON historical_topic_override_draft_payloads;

-- Migrate the eight live key values with their original timestamps.
CREATE POLICY topic_storage_settings_write ON app_config FOR ALL USING (current_setting('app.tx_mode',true)='migration')
    WITH CHECK (current_setting('app.tx_mode',true)='migration');
UPDATE app_config SET key=replace(key,'ONTOLOGY_MAINTENANCE_','TOPIC_MAINTENANCE_')
    WHERE key=ANY(ARRAY['ONTOLOGY_MAINTENANCE_ENABLED','ONTOLOGY_MAINTENANCE_CADENCE_HOURS',
        'ONTOLOGY_MAINTENANCE_START_TIME_LOCAL','ONTOLOGY_MAINTENANCE_MODEL','ONTOLOGY_MAINTENANCE_MAX_CONCURRENCY',
        'ONTOLOGY_MAINTENANCE_TEAM_MAX_CONCURRENCY','ONTOLOGY_MAINTENANCE_TEAM_OVERRIDES_JSON','ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS']);
DROP POLICY topic_storage_settings_write ON app_config;

UPDATE topic_maintenance_runs SET status='incomplete',failure_code='placement_policy_replaced',updated_at=clock_timestamp()
    WHERE kind IN ('scheduled','run','retry') AND status IN ('pending','running');
ALTER TABLE recall_feedback_events DROP COLUMN result_refs;
DROP POLICY topic_storage_feedback_copy ON recall_feedback_events;
ALTER TABLE recall_feedback_events ADD CONSTRAINT feedback_metadata_has_no_rerank_result CHECK (NOT snapshot_metadata ? 'decision_stages');

DO $catalog$
BEGIN
    IF EXISTS(SELECT 1 FROM topic_object_names names LEFT JOIN pg_class c
            ON c.relnamespace='public'::regnamespace AND c.relname=names.new_name
        WHERE c.oid IS DISTINCT FROM names.original_oid OR
            (names.kind='TABLE' AND (NOT c.relrowsecurity OR NOT c.relforcerowsecurity)) OR
            (names.kind='VIEW' AND NOT COALESCE('security_invoker=true'=ANY(c.reloptions),false)))
        OR NOT EXISTS(SELECT 1 FROM pg_index WHERE indexrelid='topic_active_membership_pair_idx'::regclass
            AND indrelid='topic_record_heads'::regclass AND indisunique AND indisvalid)
        OR NOT EXISTS(SELECT 1 FROM pg_index WHERE indexrelid='topic_membership_peers_idx'::regclass
            AND indrelid='topic_record_heads'::regclass AND indisvalid) THEN
        RAISE EXCEPTION 'topic storage physical identity, membership index, or isolation verification failed';
    END IF;
END $catalog$;

UPDATE data_migration_runs SET phase='complete',source_fingerprint=(
    SELECT encode(sha256(convert_to(string_agg(work_unit||':'||row_count::text||':'||fingerprint,',' ORDER BY work_unit),'UTF8')),'hex')
    FROM data_migration_checkpoints WHERE migration_version=20261124010001 AND phase='verify' AND complete),
    updated_at=clock_timestamp() WHERE migration_version=20261124010001;
CREATE POLICY topic_storage_marker_insert ON v2_compatibility_markers FOR INSERT WITH CHECK (
    current_setting('app.tx_mode',true)='migration' AND marker_kind='topic_storage' AND version='topic-storage-v1' AND status='compatible');
INSERT INTO v2_compatibility_markers(marker_kind,version,status,metadata)
 SELECT 'topic_storage','topic-storage-v1','compatible',jsonb_build_object(
   'source_fingerprint',source_fingerprint,'transaction_outcome','committed','converted_units_verified',true,
   'renamed_relations',(SELECT count(*) FROM topic_object_names),
    'marker_dispositions',(SELECT jsonb_object_agg(work_unit,jsonb_build_object('rows',row_count,'fingerprint',fingerprint))
     FROM data_migration_checkpoints WHERE migration_version=20261124010001 AND phase='verify' AND work_unit IN ('translated_topic_markers','retired_definition_markers')))
 FROM data_migration_runs WHERE migration_version=20261124010001 AND phase='complete';
DROP POLICY topic_storage_marker_insert ON v2_compatibility_markers;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'topic storage cutover requires a compatible reader and forward recovery'; END $$;
-- +goose StatementEnd
