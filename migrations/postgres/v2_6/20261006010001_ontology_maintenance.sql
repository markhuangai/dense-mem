-- Lock/rewrite impact: adds empty tables/indexes and short trigger-definition locks;
-- no canonical rewrite or bulk backfill. Workers discover existing sources in pages.
-- RLS impact: global accounting is system-only; source coordination is shared-space scoped.
-- Backward compatibility: additive operational state preserves existing readers and writers; maintenance is disabled by default.
-- Rollback: disable admission and drain workers; populated accounting/history requires roll-forward recovery.

-- +goose Up
-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
CREATE SEQUENCE ontology_maintenance_marker_seq;

CREATE TABLE ontology_maintenance_state (
    singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    paused BOOLEAN NOT NULL DEFAULT false,
    turn_sequence BIGINT NOT NULL DEFAULT 0,
    last_successful_progress TIMESTAMPTZ NULL
);
INSERT INTO ontology_maintenance_state(singleton) VALUES(true);

CREATE TABLE ontology_maintenance_windows (
    window_id UUID PRIMARY KEY,
    starts_at TIMESTAMPTZ NOT NULL UNIQUE,
    ends_at TIMESTAMPTZ NOT NULL CHECK (ends_at > starts_at),
    policy JSONB NOT NULL CHECK (jsonb_typeof(policy)='object'),
    charged_input BIGINT NOT NULL DEFAULT 0 CHECK (charged_input >= 0),
    charged_output BIGINT NOT NULL DEFAULT 0 CHECK (charged_output >= 0),
    reported_input BIGINT NOT NULL DEFAULT 0 CHECK (reported_input >= 0),
    reported_output BIGINT NOT NULL DEFAULT 0 CHECK (reported_output >= 0),
    reserved_input BIGINT NOT NULL DEFAULT 0 CHECK (reserved_input >= 0),
    reserved_output BIGINT NOT NULL DEFAULT 0 CHECK (reserved_output >= 0),
    overrun BOOLEAN NOT NULL DEFAULT false
);

CREATE TABLE ontology_maintenance_runs (
    run_id UUID PRIMARY KEY,
    window_id UUID NULL REFERENCES ontology_maintenance_windows(window_id),
    operation_key TEXT NOT NULL UNIQUE CHECK (length(operation_key) BETWEEN 1 AND 128),
    request_hash TEXT NOT NULL,
    command JSONB NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('scheduled','run','retry','pause','resume')),
    status TEXT NOT NULL CHECK (status IN ('pending','running','completed','incomplete','failed')),
    max_batches INTEGER NOT NULL CHECK (max_batches BETWEEN 0 AND 100),
    completed_batches INTEGER NOT NULL DEFAULT 0 CHECK (completed_batches >= 0),
    failure_code TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX ontology_maintenance_runs_pending_idx ON ontology_maintenance_runs(window_id,created_at,run_id) WHERE status IN ('pending','running');

CREATE TABLE ontology_maintenance_teams (
    team_id UUID NOT NULL,
    shared_space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL CHECK (space_generation > 0),
    discovery_kind INTEGER NOT NULL DEFAULT 0 CHECK (discovery_kind BETWEEN 0 AND 4),
    discovery_cursor TEXT NOT NULL DEFAULT '',
    last_turn BIGINT NOT NULL DEFAULT 0,
    lease_token UUID NULL,
    lease_until TIMESTAMPTZ NULL,
    PRIMARY KEY(team_id,shared_space_id,space_generation),
    FOREIGN KEY(team_id,shared_space_id) REFERENCES memory_spaces(team_id,id) ON DELETE CASCADE,
    CHECK (shared_space_id=dense_mem_team_shared_space(team_id))
);

CREATE TABLE ontology_maintenance_markers (
    team_id UUID NOT NULL,
    shared_space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL CHECK (space_generation > 0),
    anchor_kind TEXT NOT NULL,
    anchor_id TEXT NOT NULL,
    target_kind TEXT NOT NULL,
    target_id TEXT NOT NULL,
    marker_sequence BIGINT NOT NULL DEFAULT nextval('ontology_maintenance_marker_seq'),
    cursor TEXT NOT NULL DEFAULT '',
    first_pending_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(team_id,shared_space_id,space_generation,anchor_kind,anchor_id),
    FOREIGN KEY(team_id,shared_space_id) REFERENCES memory_spaces(team_id,id) ON DELETE CASCADE,
    CHECK (shared_space_id=dense_mem_team_shared_space(team_id))
);
CREATE INDEX ontology_maintenance_markers_pending_idx ON ontology_maintenance_markers(team_id,shared_space_id,space_generation,marker_sequence);

CREATE TABLE ontology_maintenance_sources (
    team_id UUID NOT NULL,
    shared_space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL CHECK (space_generation > 0),
    source_kind TEXT NOT NULL CHECK (source_kind IN ('entity','predicate','evidence','relationship')),
    source_id TEXT NOT NULL CHECK (length(source_id) BETWEEN 1 AND 128),
    source_version BIGINT NOT NULL CHECK (source_version > 0),
    fingerprint TEXT NOT NULL DEFAULT '',
	meaning_key TEXT NOT NULL DEFAULT '',
	search_tsv TSVECTOR NOT NULL DEFAULT ''::tsvector,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    eligible BOOLEAN NOT NULL DEFAULT false,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','organized','ambiguous','failed','budget_deferred','unavailable')),
    reason TEXT NOT NULL DEFAULT '',
    assessment_id UUID NULL,
    last_run_id UUID NULL REFERENCES ontology_maintenance_runs(run_id),
    pending_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(team_id,shared_space_id,space_generation,source_kind,source_id),
    FOREIGN KEY(team_id,shared_space_id) REFERENCES memory_spaces(team_id,id) ON DELETE CASCADE,
    CHECK (shared_space_id=dense_mem_team_shared_space(team_id))
);
CREATE INDEX ontology_maintenance_sources_pending_idx ON ontology_maintenance_sources(team_id,shared_space_id,space_generation,pending_at,source_kind,source_id) WHERE status IN ('pending','budget_deferred');
CREATE INDEX ontology_maintenance_sources_meaning_idx ON ontology_maintenance_sources(team_id,shared_space_id,space_generation,source_kind,meaning_key) WHERE eligible;
CREATE INDEX ontology_maintenance_sources_fts_idx ON ontology_maintenance_sources USING GIN(search_tsv);

CREATE TABLE ontology_maintenance_batches (
    batch_id UUID PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES ontology_maintenance_runs(run_id),
    window_id UUID NOT NULL REFERENCES ontology_maintenance_windows(window_id),
    team_id UUID NOT NULL,
    shared_space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL CHECK (space_generation > 0),
    lease_token UUID NOT NULL,
    lease_until TIMESTAMPTZ NOT NULL,
    sources JSONB NOT NULL CHECK (jsonb_typeof(sources)='array' AND jsonb_array_length(sources) BETWEEN 1 AND 20),
    revisions JSONB NOT NULL CHECK (jsonb_typeof(revisions)='object'),
    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','completed','lost')),
    failure_code TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ NULL,
    FOREIGN KEY(team_id,shared_space_id) REFERENCES memory_spaces(team_id,id),
    CHECK (shared_space_id=dense_mem_team_shared_space(team_id))
);
CREATE UNIQUE INDEX ontology_maintenance_batch_team_idx ON ontology_maintenance_batches(team_id,shared_space_id,space_generation) WHERE status='running';
CREATE INDEX ontology_maintenance_batch_admission_idx ON ontology_maintenance_batches(status,lease_until);

CREATE TABLE ontology_maintenance_attempts (
    batch_id UUID NOT NULL REFERENCES ontology_maintenance_batches(batch_id),
    assessment_id UUID NOT NULL,
    attempt INTEGER NOT NULL CHECK (attempt BETWEEN 1 AND 3),
    window_id UUID NOT NULL REFERENCES ontology_maintenance_windows(window_id),
    reserved_input BIGINT NOT NULL CHECK (reserved_input > 0),
    reserved_output BIGINT NOT NULL CHECK (reserved_output > 0),
    reported_input BIGINT NULL CHECK (reported_input >= 0),
    reported_output BIGINT NULL CHECK (reported_output >= 0),
    reported_total BIGINT NULL CHECK (reported_total >= 0),
    admitted_at TIMESTAMPTZ NOT NULL,
    reconciled_at TIMESTAMPTZ NULL,
    PRIMARY KEY(batch_id,assessment_id,attempt)
);

CREATE FUNCTION dense_mem_guard_ontology_window() RETURNS TRIGGER LANGUAGE plpgsql AS $body$
BEGIN
    IF TG_OP='DELETE' OR NEW.window_id<>OLD.window_id OR NEW.starts_at<>OLD.starts_at
       OR NEW.ends_at<>OLD.ends_at OR NEW.policy<>OLD.policy THEN
        RAISE EXCEPTION 'ontology window policy and identity are immutable';
    END IF;
    RETURN NEW;
END $body$;
CREATE TRIGGER ontology_maintenance_window_guard BEFORE UPDATE OR DELETE ON ontology_maintenance_windows
    FOR EACH ROW EXECUTE FUNCTION dense_mem_guard_ontology_window();

CREATE FUNCTION dense_mem_guard_ontology_attempt() RETURNS TRIGGER LANGUAGE plpgsql AS $body$
BEGIN
    IF TG_OP='DELETE' OR OLD.reconciled_at IS NOT NULL OR NEW.batch_id<>OLD.batch_id
       OR NEW.assessment_id<>OLD.assessment_id OR NEW.attempt<>OLD.attempt OR NEW.window_id<>OLD.window_id
       OR NEW.reserved_input<>OLD.reserved_input OR NEW.reserved_output<>OLD.reserved_output
       OR NEW.admitted_at<>OLD.admitted_at OR NEW.reconciled_at IS NULL THEN
        RAISE EXCEPTION 'ontology reservations are immutable and reconcile once';
    END IF;
    RETURN NEW;
END $body$;
CREATE TRIGGER ontology_maintenance_attempt_guard BEFORE UPDATE OR DELETE ON ontology_maintenance_attempts
    FOR EACH ROW EXECUTE FUNCTION dense_mem_guard_ontology_attempt();

DO $rls$
DECLARE table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY['ontology_maintenance_state','ontology_maintenance_windows','ontology_maintenance_runs','ontology_maintenance_attempts'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',table_name);
        EXECUTE format('CREATE POLICY ontology_system ON %I FOR ALL USING (current_setting(''app.tx_mode'',true) IN (''system'',''migration'')) WITH CHECK (current_setting(''app.tx_mode'',true) IN (''system'',''migration''))',table_name);
    END LOOP;
    FOREACH table_name IN ARRAY ARRAY['ontology_maintenance_teams','ontology_maintenance_markers','ontology_maintenance_sources','ontology_maintenance_batches'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',table_name);
        EXECUTE format('CREATE POLICY ontology_shared ON %I FOR ALL USING (
            current_setting(''app.tx_mode'',true) IN (''system'',''migration'') OR
            (current_setting(''app.tx_mode'',true) IN (''team'',''profile'') AND team_id=NULLIF(current_setting(''app.current_team_id'',true),'''')::uuid
             AND shared_space_id=dense_mem_team_shared_space(team_id) AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id)))
            WITH CHECK (current_setting(''app.tx_mode'',true) IN (''system'',''migration'') OR
            (current_setting(''app.tx_mode'',true) IN (''team'',''profile'') AND team_id=NULLIF(current_setting(''app.current_team_id'',true),'''')::uuid
             AND shared_space_id=dense_mem_team_shared_space(team_id) AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id)))',table_name);
    END LOOP;
END $rls$;

CREATE FUNCTION dense_mem_enqueue_ontology_marker(p_team UUID,p_space UUID,p_generation BIGINT,p_anchor TEXT,p_id TEXT,p_kind TEXT,p_target TEXT)
RETURNS VOID LANGUAGE plpgsql AS $body$
DECLARE shared_id UUID; active_generation BIGINT;
BEGIN
    SELECT id INTO shared_id FROM memory_spaces WHERE team_id=p_team AND kind='team_shared';
    IF shared_id IS NULL THEN RETURN; END IF;
    IF p_space IS NOT NULL AND p_space<>shared_id THEN RETURN; END IF;
    SELECT space.generation INTO active_generation FROM memory_spaces AS space
        JOIN teams AS team ON team.id=space.team_id AND team.status='active' AND team.deleted_at IS NULL
        WHERE space.team_id=p_team AND space.id=shared_id AND space.lifecycle_state='active';
    IF active_generation IS NULL OR (p_generation IS NOT NULL AND p_generation<>active_generation) THEN RETURN; END IF;
    INSERT INTO ontology_maintenance_markers(team_id,shared_space_id,space_generation,anchor_kind,anchor_id,target_kind,target_id)
        VALUES(p_team,shared_id,active_generation,p_anchor,p_id,p_kind,p_target)
        ON CONFLICT(team_id,shared_space_id,space_generation,anchor_kind,anchor_id) DO UPDATE
        SET marker_sequence=nextval('ontology_maintenance_marker_seq'),cursor='',target_kind=EXCLUDED.target_kind,target_id=EXCLUDED.target_id;
END $body$;

-- Source triggers are generated with static field access so large evidence text is never converted to JSON.
DO $triggers$
DECLARE spec RECORD; fn TEXT; team_expr TEXT; space_expr TEXT; gen_expr TEXT; body TEXT;
BEGIN
    FOR spec IN SELECT * FROM (VALUES
        ('entity_records','entity_id','entity','entity_id','team_id','space_id','space_generation','version,status,entity_kind,identity_context'),
        ('entity_names','entity_name_id','entity','entity_id','team_id','space_id','space_generation','entity_id,owner_profile_id,display_name,name_kind,locale,valid_to'),
        ('evidence_fragments','fragment_id','evidence','fragment_id','team_id','space_id','space_generation','content_hash,content,authority,labels,metadata,source_revision_id'),
        ('knowledge_ingests','ingest_id','ingest','ingest_id','team_id','space_id','space_generation','status,source_summary,metadata'),
        ('evidence_sources','source_id','source','source_id','team_id','space_id','space_generation','current_revision_id'),
        ('evidence_occurrences','occurrence_id','evidence','canonical_fragment_id','team_id','space_id','space_generation','canonical_fragment_id,owner_profile_id,content_hash'),
        ('evidence_quarantines','quarantine_id','evidence','fragment_id','team_id','space_id','space_generation','status,fragment_id'),
        ('evidence_lifecycle_events','lifecycle_event_id','evidence','target_fragment_id','team_id','space_id','space_generation','target_fragment_id'),
        ('relationship_records','relationship_id','relationship','relationship_id','team_id','space_id','space_generation','version,status,support_count,identity_alias_of_relationship_id,subject_entity_id,predicate_key,predicate_version,object_entity_id,object_value_id,relationship_kind,current_cardinality,polarity,scope_key,valid_from,valid_to,metadata'),
        ('relationship_evidence_supports','support_id','relationship','relationship_id','team_id','space_id','space_generation','relationship_id,fragment_id,owner_profile_id,source_revision_id'),
        ('relationship_support_decision_events','support_decision_id','relationship','relationship_id','team_id','space_id','space_generation','relationship_id,decision'),
        ('team_predicate_definitions','predicate_key','predicate','predicate_key','team_id','','','version,aliases,allowed_subject_kinds,allowed_object_kinds,relationship_kind,current_cardinality,lifecycle_state,metadata'),
        ('memory_spaces','id','space','id','team_id','id','generation','generation,lifecycle_state'),
        ('teams','id','team','id','id','','','status,deleted_at'),
        ('ontology_record_heads','record_id','definition','record_id','team_id','shared_space_id','space_generation','version')
    ) AS specs(tbl,pk,target_kind,target_column,team_column,space_column,generation_column,changes) LOOP
        fn:='dense_mem_ontology_dirty_'||spec.tbl;
        space_expr:=CASE WHEN spec.space_column='' THEN 'NULL::uuid' ELSE 'row_value.'||quote_ident(spec.space_column) END;
        gen_expr:=CASE WHEN spec.generation_column='' THEN 'NULL::bigint' ELSE 'row_value.'||quote_ident(spec.generation_column) END;
        body:=format('DECLARE row_value %I%%ROWTYPE; BEGIN
            IF TG_OP=''UPDATE'' AND ROW(%s) IS NOT DISTINCT FROM ROW(%s) THEN RETURN NEW; END IF;
            IF TG_OP=''DELETE'' THEN row_value:=OLD; ELSE row_value:=NEW; END IF;
            %s
            PERFORM dense_mem_enqueue_ontology_marker(row_value.%I,%s,%s,%L,row_value.%I::text,%L,row_value.%I::text);
            RETURN row_value; END',spec.tbl,
            (SELECT string_agg('NEW.'||quote_ident(c),',') FROM unnest(string_to_array(spec.changes,',')) AS c),
            (SELECT string_agg('OLD.'||quote_ident(c),',') FROM unnest(string_to_array(spec.changes,',')) AS c),
            CASE WHEN spec.tbl='ontology_record_heads' THEN 'IF row_value.kind NOT IN (''entity_class'',''predicate_concept'',''topic'',''override'') THEN RETURN row_value; END IF;' WHEN spec.tbl IN ('teams','memory_spaces') THEN 'IF TG_OP=''DELETE'' THEN RETURN row_value; END IF;' ELSE '' END,
            spec.team_column,space_expr,gen_expr,spec.tbl,spec.pk,spec.target_kind,spec.target_column);
        EXECUTE format('CREATE FUNCTION %I() RETURNS TRIGGER LANGUAGE plpgsql AS %L',fn,body);
        EXECUTE format('CREATE TRIGGER ontology_maintenance_dirty AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION %I()',spec.tbl,fn);
    END LOOP;
END $triggers$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
DO $rollback$
DECLARE target TEXT;
BEGIN
    IF EXISTS(SELECT 1 FROM ontology_maintenance_windows) OR EXISTS(SELECT 1 FROM ontology_maintenance_runs)
       OR EXISTS(SELECT 1 FROM ontology_maintenance_sources) OR EXISTS(SELECT 1 FROM ontology_maintenance_markers) THEN
        RAISE EXCEPTION 'cannot roll back populated ontology maintenance; use roll-forward recovery';
    END IF;
    FOREACH target IN ARRAY ARRAY['entity_records','entity_names','evidence_fragments','knowledge_ingests','evidence_sources','evidence_occurrences','evidence_quarantines','evidence_lifecycle_events','relationship_records','relationship_evidence_supports','relationship_support_decision_events','team_predicate_definitions','memory_spaces','teams','ontology_record_heads'] LOOP
        EXECUTE format('DROP TRIGGER ontology_maintenance_dirty ON %I',target);
        EXECUTE format('DROP FUNCTION %I()','dense_mem_ontology_dirty_'||target);
    END LOOP;
END $rollback$;
DROP FUNCTION dense_mem_enqueue_ontology_marker(UUID,UUID,BIGINT,TEXT,TEXT,TEXT,TEXT);
DROP TABLE ontology_maintenance_attempts,ontology_maintenance_batches,ontology_maintenance_sources,ontology_maintenance_markers,ontology_maintenance_teams,ontology_maintenance_runs,ontology_maintenance_windows,ontology_maintenance_state;
DROP FUNCTION dense_mem_guard_ontology_attempt(),dense_mem_guard_ontology_window();
DROP SEQUENCE ontology_maintenance_marker_seq;
-- +goose StatementEnd
