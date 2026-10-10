-- Lock/rewrite impact: brief ALTER/trigger locks bounded to five seconds; status validation scans history and the replacement index builds concurrently.
-- RLS impact: derived state remains forced and scoped; SELECT exposes only the nonsecret ontology mode flag, with no configuration write permission.
-- Backfill: none; workers capture derived topic dependencies in bounded pages.
-- Backward compatibility: additive projections retain legacy history and ontology-disabled publication.
-- Rollback: populated projection history refuses Down; recover forward. Empty installations may revert; interrupted concurrent indexes require cleanup before retry.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
BEGIN;
SELECT set_config('app.tx_mode','migration',true);
SELECT set_config('lock_timeout','5s',true);

SELECT set_config('app.tx_mode','system',true);
INSERT INTO app_config(key,value) VALUES('ONTOLOGY_MAINTENANCE_ENABLED','false') ON CONFLICT(key) DO NOTHING;
SELECT set_config('app.tx_mode','migration',true);
DROP POLICY IF EXISTS community_mode_read ON app_config;
CREATE POLICY community_mode_read ON app_config FOR SELECT
    USING (key='ONTOLOGY_MAINTENANCE_ENABLED' AND current_setting('app.tx_mode',true) IN ('team','profile'));
-- Row locks require UPDATE visibility; WITH CHECK(false) rejects configuration writes.
DROP POLICY IF EXISTS community_mode_lock ON app_config;
CREATE POLICY community_mode_lock ON app_config FOR UPDATE
    USING (key='ONTOLOGY_MAINTENANCE_ENABLED' AND current_setting('app.tx_mode',true) IN ('team','profile'))
    WITH CHECK(false);

ALTER TABLE community_records ADD COLUMN IF NOT EXISTS topic_id UUID;
ALTER TABLE community_records DROP CONSTRAINT IF EXISTS community_records_status_check;
ALTER TABLE community_records ADD CONSTRAINT community_records_status_check
    CHECK(status IN ('building','current','stale','superseded')) NOT VALID;
ALTER TABLE community_records VALIDATE CONSTRAINT community_records_status_check;

CREATE TABLE IF NOT EXISTS community_topic_versions (
    team_id UUID NOT NULL,
    space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL CHECK(space_generation>0),
    dependency_key TEXT NOT NULL,
    version BIGINT NOT NULL CHECK(version>=0),
    PRIMARY KEY(team_id,space_id,space_generation,dependency_key),
    FOREIGN KEY(team_id,space_id) REFERENCES memory_spaces(team_id,id) ON DELETE RESTRICT,
    CHECK(space_id=dense_mem_team_shared_space(team_id))
);
CREATE TABLE IF NOT EXISTS community_topic_dependencies (
    team_id UUID NOT NULL,
    space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL CHECK(space_generation>0),
    community_id UUID NOT NULL,
    dependency_key TEXT NOT NULL,
    version BIGINT NOT NULL CHECK(version>=0),
    fingerprint TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(team_id,community_id,dependency_key),
    FOREIGN KEY(team_id,community_id) REFERENCES community_records(team_id,community_id) ON DELETE RESTRICT,
    FOREIGN KEY(team_id,space_id) REFERENCES memory_spaces(team_id,id) ON DELETE RESTRICT,
    CHECK(space_id=dense_mem_team_shared_space(team_id))
);
CREATE TABLE IF NOT EXISTS community_topic_work (
    team_id UUID NOT NULL,
    space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL CHECK(space_generation>0),
    topic_id UUID NOT NULL,
    topic_version BIGINT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','building','current','empty','failed','retired')),
    community_id UUID,
    run_id UUID,
    assignment_cursor TEXT NOT NULL DEFAULT '',
    relationship_cursor TEXT NOT NULL DEFAULT '',
    lease_token UUID,
    lease_until TIMESTAMPTZ,
    last_turn TIMESTAMPTZ NOT NULL DEFAULT '-infinity',
    failure_code TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(team_id,space_id,space_generation,topic_id),
    FOREIGN KEY(team_id,space_id,space_generation,topic_id)
        REFERENCES ontology_record_heads(team_id,shared_space_id,space_generation,record_id) ON DELETE RESTRICT,
    FOREIGN KEY(team_id,community_id) REFERENCES community_records(team_id,community_id) ON DELETE RESTRICT,
    FOREIGN KEY(team_id,run_id) REFERENCES community_snapshot_runs(team_id,run_id) ON DELETE RESTRICT,
    CHECK(space_id=dense_mem_team_shared_space(team_id))
);
CREATE TABLE IF NOT EXISTS community_topic_discovery (
    singleton BOOLEAN PRIMARY KEY CHECK(singleton),
    after_team TEXT NOT NULL DEFAULT '',
    after_topic TEXT NOT NULL DEFAULT ''
);
INSERT INTO community_topic_discovery(singleton) VALUES(true) ON CONFLICT DO NOTHING;
CREATE INDEX IF NOT EXISTS community_topic_work_turn_idx ON community_topic_work(last_turn,team_id,topic_id);
CREATE INDEX IF NOT EXISTS community_topic_dependencies_lookup_idx
    ON community_topic_dependencies(team_id,space_id,space_generation,dependency_key,community_id);

DO $rls$
DECLARE target TEXT; scope TEXT;
BEGIN
    FOREACH target IN ARRAY ARRAY['community_topic_versions','community_topic_dependencies','community_topic_work'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',target);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',target);
        scope:=$scope$ current_setting('app.tx_mode',true) IN ('system','migration') OR (
            current_setting('app.tx_mode',true) IN ('team','profile')
            AND team_id=nullif(current_setting('app.current_team_id',true),'')::uuid
            AND space_id=dense_mem_team_shared_space(team_id)
            AND space_generation=dense_mem_team_shared_generation(team_id)) $scope$;
        EXECUTE format('DROP POLICY IF EXISTS community_topic_scope ON %I',target);
        EXECUTE format('CREATE POLICY community_topic_scope ON %I FOR ALL USING (%s) WITH CHECK (%s)',target,scope,scope);
    END LOOP;
END $rls$;
ALTER TABLE community_topic_discovery ENABLE ROW LEVEL SECURITY;
ALTER TABLE community_topic_discovery FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS community_topic_system ON community_topic_discovery;
CREATE POLICY community_topic_system ON community_topic_discovery FOR ALL
    USING(current_setting('app.tx_mode',true) IN ('system','migration'))
    WITH CHECK(current_setting('app.tx_mode',true) IN ('system','migration'));

CREATE OR REPLACE FUNCTION dense_mem_touch_community_topic_dependency(p_team UUID,p_space UUID,p_generation BIGINT,p_kind TEXT,p_id TEXT)
RETURNS VOID LANGUAGE plpgsql AS $body$
DECLARE shared_id UUID; active_generation BIGINT;
BEGIN
    SELECT id,generation INTO shared_id,active_generation FROM memory_spaces
        WHERE team_id=p_team AND kind='team_shared' AND lifecycle_state='active'
        AND (p_space IS NULL OR id=p_space) AND (p_generation IS NULL OR generation=p_generation);
    IF shared_id IS NULL THEN RETURN; END IF;
    INSERT INTO community_topic_versions(team_id,space_id,space_generation,dependency_key,version)
        VALUES(p_team,shared_id,active_generation,p_kind || ':' || p_id,1)
        ON CONFLICT(team_id,space_id,space_generation,dependency_key)
        DO UPDATE SET version=community_topic_versions.version+1;
END $body$;

-- Each writer changes one source counter; dependency expansion remains outside canonical transactions.
DO $triggers$
DECLARE spec RECORD; fn TEXT; space_expr TEXT; gen_expr TEXT; key_expr TEXT; body TEXT;
BEGIN
    FOR spec IN SELECT * FROM (VALUES
        ('entity_records','entity','entity_id','team_id','space_id','space_generation','version,status,entity_kind,identity_context'),
        ('entity_names','entity','entity_id','team_id','space_id','space_generation','entity_id,owner_profile_id,display_name,name_kind,locale,valid_to'),
        ('value_records','value','value_id','team_id','space_id','space_generation','value_type,canonical_value,unit,display,metadata'),
        ('evidence_fragments','evidence','fragment_id','team_id','space_id','space_generation','content_hash,content,authority,labels,metadata,source_id,source_revision_id'),
        ('knowledge_ingests','ingest','ingest_id','team_id','space_id','space_generation','status,source_summary,metadata'),
        ('evidence_sources','source','source_id','team_id','space_id','space_generation','current_revision_id'),
        ('evidence_occurrences','evidence','canonical_fragment_id','team_id','space_id','space_generation','canonical_fragment_id,owner_profile_id,content_hash'),
        ('evidence_quarantines','evidence','fragment_id','team_id','space_id','space_generation','status,fragment_id'),
        ('evidence_lifecycle_events','evidence','target_fragment_id','team_id','space_id','space_generation','target_fragment_id'),
        ('relationship_records','relationship','relationship_id','team_id','space_id','space_generation','version,status,support_count,identity_alias_of_relationship_id,subject_entity_id,predicate_key,predicate_version,object_entity_id,object_value_id,relationship_kind,current_cardinality,polarity,scope_key,valid_from,valid_to,metadata'),
        ('relationship_evidence_supports','relationship','relationship_id','team_id','space_id','space_generation','relationship_id,fragment_id,owner_profile_id,source_id,source_revision_id'),
        ('relationship_support_decision_events','relationship','relationship_id','team_id','space_id','space_generation','relationship_id,decision'),
        ('team_predicate_definitions','predicate','predicate_key','team_id','','','version,aliases,allowed_subject_kinds,allowed_object_kinds,relationship_kind,current_cardinality,lifecycle_state,metadata'),
        ('ontology_record_heads','definition','record_id','team_id','shared_space_id','space_generation','version,retired')
    ) AS specs(tbl,kind,target_column,team_column,space_column,generation_column,changes) LOOP
        fn:='dense_mem_community_dirty_' || spec.tbl;
        space_expr:=CASE WHEN spec.space_column='' THEN 'NULL::uuid' ELSE 'row_value.'||quote_ident(spec.space_column) END;
        gen_expr:=CASE WHEN spec.generation_column='' THEN 'NULL::bigint' ELSE 'row_value.'||quote_ident(spec.generation_column) END;
        key_expr:=format('ROW(row_value.%I,%s,%s,row_value.%I)',spec.team_column,space_expr,gen_expr,spec.target_column);
        body:=format('DECLARE row_value %I%%ROWTYPE; BEGIN
            IF TG_OP=''UPDATE'' AND ROW(%s) IS NOT DISTINCT FROM ROW(%s) THEN RETURN NEW; END IF;
            IF TG_OP IN (''DELETE'',''UPDATE'') THEN
                row_value:=OLD;
                PERFORM dense_mem_touch_community_topic_dependency(row_value.%I,%s,%s,%L,row_value.%I::text);
                IF TG_OP=''UPDATE'' AND %s IS NOT DISTINCT FROM %s THEN RETURN NEW; END IF;
            END IF;
            IF TG_OP IN (''INSERT'',''UPDATE'') THEN
                row_value:=NEW;
                PERFORM dense_mem_touch_community_topic_dependency(row_value.%I,%s,%s,%L,row_value.%I::text);
            END IF;
            RETURN row_value; END',spec.tbl,
            (SELECT string_agg('NEW.'||quote_ident(c),',') FROM unnest(string_to_array(spec.changes,',')) AS c),
            (SELECT string_agg('OLD.'||quote_ident(c),',') FROM unnest(string_to_array(spec.changes,',')) AS c),
            spec.team_column,space_expr,gen_expr,spec.kind,spec.target_column,
            replace(key_expr,'row_value.','OLD.'),replace(key_expr,'row_value.','NEW.'),
            spec.team_column,space_expr,gen_expr,spec.kind,spec.target_column);
        EXECUTE format('CREATE OR REPLACE FUNCTION %I() RETURNS TRIGGER LANGUAGE plpgsql AS %L',fn,body);
        EXECUTE format('DROP TRIGGER IF EXISTS community_topic_dirty ON %I',spec.tbl);
        EXECUTE format('CREATE TRIGGER community_topic_dirty AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION %I()',spec.tbl,fn);
    END LOOP;
END $triggers$;

CREATE OR REPLACE FUNCTION dense_mem_community_predicate_membership_dirty() RETURNS TRIGGER LANGUAGE plpgsql AS $body$
DECLARE changed JSONB; rows JSONB;
BEGIN
    IF TG_OP='UPDATE' AND ROW(NEW.predicate_key,NEW.predicate_version,NEW.status,NEW.support_count,NEW.identity_alias_of_relationship_id)
        IS NOT DISTINCT FROM ROW(OLD.predicate_key,OLD.predicate_version,OLD.status,OLD.support_count,OLD.identity_alias_of_relationship_id)
        THEN RETURN NEW; END IF;
    rows:=CASE TG_OP WHEN 'INSERT' THEN jsonb_build_array(to_jsonb(NEW))
        WHEN 'DELETE' THEN jsonb_build_array(to_jsonb(OLD)) ELSE jsonb_build_array(to_jsonb(OLD),to_jsonb(NEW)) END;
    FOR changed IN SELECT DISTINCT value FROM jsonb_array_elements(rows) LOOP
        PERFORM dense_mem_touch_community_topic_dependency((changed->>'team_id')::uuid,
            (changed->>'space_id')::uuid,(changed->>'space_generation')::bigint,'predicate-membership',
            (changed->>'predicate_key') || '@' || (changed->>'predicate_version'));
    END LOOP;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $body$;
DROP TRIGGER IF EXISTS community_predicate_membership_dirty ON relationship_records;
CREATE TRIGGER community_predicate_membership_dirty AFTER INSERT OR UPDATE OR DELETE ON relationship_records
    FOR EACH ROW EXECUTE FUNCTION dense_mem_community_predicate_membership_dirty();

CREATE OR REPLACE FUNCTION dense_mem_community_topic_assignment_dirty() RETURNS TRIGGER LANGUAGE plpgsql AS $body$
DECLARE row_value ontology_record_heads%ROWTYPE; definition_id TEXT;
BEGIN
    IF TG_OP='UPDATE' AND NEW.version=OLD.version THEN RETURN NEW; END IF;
    IF TG_OP IN ('DELETE','UPDATE') AND OLD.kind='assignment' THEN
        row_value:=OLD;
        SELECT body->'assignment'->>'definition_id' INTO definition_id FROM ontology_record_revisions
            WHERE team_id=row_value.team_id AND shared_space_id=row_value.shared_space_id
            AND space_generation=row_value.space_generation AND record_id=row_value.record_id AND version=row_value.version;
        PERFORM dense_mem_touch_community_topic_dependency(row_value.team_id,row_value.shared_space_id,row_value.space_generation,'topic',definition_id);
    END IF;
    IF TG_OP IN ('INSERT','UPDATE') AND NEW.kind='assignment' THEN
        row_value:=NEW;
        SELECT body->'assignment'->>'definition_id' INTO definition_id FROM ontology_record_revisions
            WHERE team_id=row_value.team_id AND shared_space_id=row_value.shared_space_id
            AND space_generation=row_value.space_generation AND record_id=row_value.record_id AND version=row_value.version;
        PERFORM dense_mem_touch_community_topic_dependency(row_value.team_id,row_value.shared_space_id,row_value.space_generation,'topic',definition_id);
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $body$;
DROP TRIGGER IF EXISTS community_topic_assignment_dirty ON ontology_record_heads;
CREATE TRIGGER community_topic_assignment_dirty AFTER INSERT OR UPDATE OR DELETE ON ontology_record_heads
    FOR EACH ROW EXECUTE FUNCTION dense_mem_community_topic_assignment_dirty();

CREATE OR REPLACE FUNCTION dense_mem_community_evidence_membership_dirty() RETURNS TRIGGER LANGUAGE plpgsql AS $body$
DECLARE changed JSONB; rows JSONB; fragment UUID;
BEGIN
    rows:=CASE TG_OP WHEN 'INSERT' THEN jsonb_build_array(to_jsonb(NEW))
        WHEN 'DELETE' THEN jsonb_build_array(to_jsonb(OLD)) ELSE jsonb_build_array(to_jsonb(OLD),to_jsonb(NEW)) END;
    FOR changed IN SELECT DISTINCT value FROM jsonb_array_elements(rows) LOOP
        IF TG_TABLE_NAME='relationship_evidence_supports' THEN
            fragment:=(changed->>'fragment_id')::uuid;
        ELSE
            SELECT fragment_id INTO fragment FROM relationship_evidence_supports
                WHERE team_id=(changed->>'team_id')::uuid AND support_id=(changed->>'support_id')::uuid;
        END IF;
        IF fragment IS NOT NULL THEN
            PERFORM dense_mem_touch_community_topic_dependency((changed->>'team_id')::uuid,
                (changed->>'space_id')::uuid,(changed->>'space_generation')::bigint,'evidence',fragment::text);
        END IF;
    END LOOP;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $body$;
DROP TRIGGER IF EXISTS community_evidence_membership_dirty ON relationship_evidence_supports;
CREATE TRIGGER community_evidence_membership_dirty AFTER INSERT OR UPDATE OR DELETE ON relationship_evidence_supports
    FOR EACH ROW EXECUTE FUNCTION dense_mem_community_evidence_membership_dirty();
DROP TRIGGER IF EXISTS community_evidence_membership_dirty ON relationship_support_decision_events;
CREATE TRIGGER community_evidence_membership_dirty AFTER INSERT OR UPDATE OR DELETE ON relationship_support_decision_events
    FOR EACH ROW EXECUTE FUNCTION dense_mem_community_evidence_membership_dirty();

CREATE OR REPLACE FUNCTION dense_mem_community_topic_override_dirty() RETURNS TRIGGER LANGUAGE plpgsql AS $body$
DECLARE changed JSONB; rows JSONB; revision JSONB; member JSONB; target TEXT;
BEGIN
    rows:=CASE TG_OP WHEN 'INSERT' THEN jsonb_build_array(to_jsonb(NEW))
        WHEN 'DELETE' THEN jsonb_build_array(to_jsonb(OLD)) ELSE jsonb_build_array(to_jsonb(OLD),to_jsonb(NEW)) END;
    FOR changed IN SELECT DISTINCT value FROM jsonb_array_elements(rows) LOOP
        IF changed->>'kind'<>'override' THEN CONTINUE; END IF;
        SELECT body INTO revision FROM ontology_record_revisions WHERE team_id=(changed->>'team_id')::uuid
            AND shared_space_id=(changed->>'shared_space_id')::uuid AND space_generation=(changed->>'space_generation')::bigint
            AND record_id=(changed->>'record_id')::uuid AND version=(changed->>'version')::bigint;
        IF revision->'override'->>'action' IN ('pin_definition','set_classification') THEN
            target:=revision->'override'->>'target_id';
            IF target IS NOT NULL THEN
                PERFORM dense_mem_touch_community_topic_dependency((changed->>'team_id')::uuid,
                    (changed->>'shared_space_id')::uuid,(changed->>'space_generation')::bigint,'definition',target);
            END IF;
        END IF;
        IF revision->'override'->>'action'='set_classification' THEN
            FOR member IN SELECT value FROM jsonb_array_elements(COALESCE(revision->'override'->'members','[]'::jsonb)) LOOP
                PERFORM dense_mem_touch_community_topic_dependency((changed->>'team_id')::uuid,
                    (changed->>'shared_space_id')::uuid,(changed->>'space_generation')::bigint,'classification',
                    (member->>'kind') || ':' || (member->>'id'));
            END LOOP;
        END IF;
    END LOOP;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $body$;
DROP TRIGGER IF EXISTS community_topic_override_dirty ON ontology_record_heads;
CREATE TRIGGER community_topic_override_dirty AFTER INSERT OR UPDATE OR DELETE ON ontology_record_heads
    FOR EACH ROW EXECUTE FUNCTION dense_mem_community_topic_override_dirty();

CREATE OR REPLACE FUNCTION dense_mem_community_topic_current(p_team UUID,p_community UUID) RETURNS BOOLEAN
LANGUAGE SQL STABLE AS $body$
    SELECT EXISTS(SELECT 1 FROM community_topic_dependencies AS dependency
        JOIN community_records AS record ON record.team_id=dependency.team_id AND record.community_id=dependency.community_id
        WHERE dependency.team_id=p_team AND dependency.community_id=p_community
        AND dependency.dependency_key='topic:' || record.topic_id::text)
    AND NOT EXISTS(SELECT 1 FROM community_topic_dependencies AS dependency
        LEFT JOIN community_topic_versions AS live USING(team_id,space_id,space_generation,dependency_key)
        WHERE dependency.team_id=p_team AND dependency.community_id=p_community
        AND dependency.version<>COALESCE(live.version,0))
$body$;
COMMIT;
-- +goose StatementEnd

-- The scoped index replaces a stronger legacy index without rewriting history or holding a write lock during its scan.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS community_records_scoped_logical_unique
    ON community_records(team_id,space_id,space_generation,logical_community_id) WHERE status='current';
DROP INDEX CONCURRENTLY IF EXISTS community_records_current_logical_unique;

-- +goose Down
-- +goose StatementBegin
DO $boundary$
BEGIN
    PERFORM set_config('app.tx_mode','migration',true);
    IF EXISTS(SELECT 1 FROM community_records WHERE topic_id IS NOT NULL) THEN
        RAISE EXCEPTION 'ontology community projection history is populated; recover by rolling forward';
    END IF;
END $boundary$;
-- +goose StatementEnd
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS community_records_current_logical_unique
    ON community_records(team_id,logical_community_id) WHERE status='current';
DROP INDEX CONCURRENTLY IF EXISTS community_records_scoped_logical_unique;
-- +goose StatementBegin
BEGIN;
SELECT set_config('app.tx_mode','migration',true);
DO $drop$
DECLARE target TEXT;
BEGIN
    FOREACH target IN ARRAY ARRAY['entity_records','entity_names','value_records','evidence_fragments','knowledge_ingests','evidence_sources','evidence_occurrences','evidence_quarantines','evidence_lifecycle_events','relationship_records','relationship_evidence_supports','relationship_support_decision_events','team_predicate_definitions','ontology_record_heads'] LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS community_topic_dirty ON %I',target);
        EXECUTE format('DROP FUNCTION IF EXISTS %I()','dense_mem_community_dirty_' || target);
    END LOOP;
END $drop$;
DROP TRIGGER community_predicate_membership_dirty ON relationship_records;
DROP FUNCTION dense_mem_community_predicate_membership_dirty();
DROP TRIGGER IF EXISTS community_topic_assignment_dirty ON ontology_record_heads;
DROP FUNCTION dense_mem_community_topic_assignment_dirty();
DROP TRIGGER community_topic_override_dirty ON ontology_record_heads;
DROP FUNCTION dense_mem_community_topic_override_dirty();
DROP TRIGGER community_evidence_membership_dirty ON relationship_evidence_supports;
DROP TRIGGER community_evidence_membership_dirty ON relationship_support_decision_events;
DROP FUNCTION dense_mem_community_evidence_membership_dirty();
DROP POLICY community_mode_read ON app_config;
DROP POLICY community_mode_lock ON app_config;
DROP FUNCTION dense_mem_community_topic_current(UUID,UUID);
DROP FUNCTION dense_mem_touch_community_topic_dependency(UUID,UUID,BIGINT,TEXT,TEXT);
DROP TABLE community_topic_discovery,community_topic_work,community_topic_dependencies,community_topic_versions;
ALTER TABLE community_records DROP COLUMN topic_id;
ALTER TABLE community_records DROP CONSTRAINT community_records_status_check;
ALTER TABLE community_records ADD CONSTRAINT community_records_status_check CHECK(status IN ('current','stale','superseded'));
COMMIT;
-- +goose StatementEnd
