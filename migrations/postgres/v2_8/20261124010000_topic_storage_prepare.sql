-- Lock/rewrite impact: maintenance admission locks and short catalog locks add nullable columns and empty outcome tables.
-- RLS impact: sets up forced RLS on new tables and migration-only access for the copy.
-- Backfill: reconciles expired maintenance batches. The next migration owns the paged copy.
-- Backward compatibility: drains old writers and fences writes until the new layout is published.
-- Rollback: resume the fenced migration or restore the verified pre-cutover backup.
-- +goose Up
-- +goose StatementBegin
SET LOCAL lock_timeout = '5s';
SELECT set_config('app.tx_mode','migration',true);

LOCK TABLE ontology_maintenance_state, ontology_maintenance_batches, ontology_maintenance_teams,
    ontology_maintenance_attempts IN SHARE ROW EXCLUSIVE MODE;
DO $drain$
BEGIN
    IF EXISTS (SELECT 1 FROM ontology_maintenance_batches WHERE status='running' AND lease_until>clock_timestamp())
        OR EXISTS (SELECT 1 FROM ontology_maintenance_teams WHERE lease_until>clock_timestamp())
        OR EXISTS (SELECT 1 FROM ontology_maintenance_attempts WHERE reconciled_at IS NULL) THEN
        RAISE EXCEPTION 'topic storage upgrade requires drained workers and reconciled provider usage' USING ERRCODE='55000';
    END IF;
    UPDATE ontology_maintenance_batches SET status='lost',failure_code='placement_policy_replaced',completed_at=clock_timestamp()
        WHERE status='running' AND lease_until<=clock_timestamp();
END $drain$;

ALTER TABLE ontology_record_heads ADD COLUMN evidence_id uuid, ADD COLUMN topic_id uuid;
ALTER TABLE ontology_record_heads ADD CONSTRAINT topic_membership_pair_required
    CHECK((kind='assignment')=(evidence_id IS NOT NULL) AND (kind='assignment')=(topic_id IS NOT NULL)) NOT VALID;
CREATE POLICY topic_storage_head_backfill ON ontology_record_heads FOR UPDATE
    USING (current_setting('app.tx_mode',true)='migration') WITH CHECK (current_setting('app.tx_mode',true)='migration');
CREATE POLICY topic_storage_feedback_copy ON recall_feedback_events FOR ALL
    USING (current_setting('app.tx_mode',true)='migration') WITH CHECK (current_setting('app.tx_mode',true)='migration');

CREATE TABLE historical_topic_marker_dispositions (
    team_id uuid NOT NULL,
    shared_space_id uuid NOT NULL,
    space_generation bigint NOT NULL,
    anchor_id text NOT NULL,
    reason text NOT NULL CHECK(reason IN ('translated_active_topic','retired_vocabulary')),
    original_marker jsonb NOT NULL CHECK(jsonb_typeof(original_marker)='object'),
    PRIMARY KEY(team_id,shared_space_id,space_generation,anchor_id)
);
ALTER TABLE historical_topic_marker_dispositions ENABLE ROW LEVEL SECURITY;
ALTER TABLE historical_topic_marker_dispositions FORCE ROW LEVEL SECURITY;
CREATE POLICY marker_history_read ON historical_topic_marker_dispositions FOR SELECT
    USING(current_setting('app.tx_mode',true) IN ('system','migration'));
CREATE POLICY marker_history_copy ON historical_topic_marker_dispositions FOR INSERT
    WITH CHECK(current_setting('app.tx_mode',true)='migration');

CREATE TABLE recall_outcomes (
    recall_id text PRIMARY KEY REFERENCES recall_feedback_events(recall_id) ON DELETE CASCADE,
    result_refs jsonb NOT NULL CHECK(jsonb_typeof(result_refs)='array'),
    method text NOT NULL CHECK(length(method) BETWEEN 1 AND 128)
);
CREATE TABLE recall_rerank_outcomes (
    recall_id text PRIMARY KEY REFERENCES recall_outcomes(recall_id) ON DELETE CASCADE,
    decision_stages jsonb NOT NULL CHECK(jsonb_typeof(decision_stages) IN ('array','null')),
    method text NOT NULL CHECK(length(method) BETWEEN 1 AND 128)
);
CREATE TABLE topic_assignment_outcomes (
    team_id uuid NOT NULL,
    shared_space_id uuid NOT NULL,
    space_generation bigint NOT NULL CHECK(space_generation>0),
    outcome_id uuid NOT NULL DEFAULT gen_random_uuid(),
    operation_id uuid,
    evidence_id uuid NOT NULL,
    input_hash text NOT NULL CHECK(length(input_hash)>0),
    method text NOT NULL CHECK(length(method) BETWEEN 1 AND 128),
    status text NOT NULL CHECK(status IN ('assigned','uncategorized','ambiguous','failed')),
    topic_ids uuid[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (team_id,shared_space_id,space_generation,outcome_id),
    FOREIGN KEY (team_id,shared_space_id) REFERENCES memory_spaces(team_id,id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id,evidence_id) REFERENCES evidence_fragments(team_id,fragment_id) ON DELETE CASCADE,
    CHECK(shared_space_id=dense_mem_team_shared_space(team_id)),
    CHECK((status='assigned')=(cardinality(topic_ids)>0))
);
CREATE INDEX topic_assignment_evidence_idx ON topic_assignment_outcomes
    (team_id,shared_space_id,space_generation,evidence_id,created_at DESC,outcome_id);
CREATE INDEX topic_assignment_operation_idx ON topic_assignment_outcomes(team_id,shared_space_id,space_generation,operation_id) WHERE operation_id IS NOT NULL;
CREATE TABLE community_placement_outcomes (
    team_id uuid NOT NULL,
    shared_space_id uuid NOT NULL,
    space_generation bigint NOT NULL CHECK(space_generation>0),
    outcome_id uuid NOT NULL DEFAULT gen_random_uuid(),
    operation_id uuid,
    topic_id uuid NOT NULL,
    topic_version bigint NOT NULL CHECK(topic_version>0),
    input_hash text NOT NULL CHECK(length(input_hash)>0),
    method text NOT NULL CHECK(length(method) BETWEEN 1 AND 128),
    status text NOT NULL CHECK(status IN ('placed','standalone','ambiguous','failed')),
    parent_topic_id uuid,
    parent_topic_version bigint CHECK(parent_topic_version>0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (team_id,shared_space_id,space_generation,outcome_id),
    FOREIGN KEY (team_id,shared_space_id,space_generation,topic_id,topic_version)
        REFERENCES ontology_record_revisions(team_id,shared_space_id,space_generation,record_id,version) ON DELETE CASCADE,
    FOREIGN KEY (team_id,shared_space_id,space_generation,parent_topic_id,parent_topic_version)
        REFERENCES ontology_record_revisions(team_id,shared_space_id,space_generation,record_id,version) ON DELETE CASCADE,
    CHECK(shared_space_id=dense_mem_team_shared_space(team_id)),
    CHECK((status='placed')=(parent_topic_id IS NOT NULL) AND (parent_topic_id IS NULL)=(parent_topic_version IS NULL)),
    CHECK(topic_id<>parent_topic_id)
);
CREATE INDEX community_placement_topic_idx ON community_placement_outcomes
    (team_id,shared_space_id,space_generation,topic_id,topic_version,created_at DESC,outcome_id);

CREATE INDEX community_placement_operation_idx ON community_placement_outcomes(team_id,shared_space_id,space_generation,operation_id) WHERE operation_id IS NOT NULL;

CREATE TABLE topic_maintenance_community_work (
    team_id uuid NOT NULL,
    shared_space_id uuid NOT NULL,
    space_generation bigint NOT NULL CHECK(space_generation>0),
    topic_id uuid NOT NULL,
    topic_version bigint NOT NULL CHECK(topic_version>0),
    work_id uuid NOT NULL DEFAULT gen_random_uuid(),
    ready_at timestamptz,
    parent_topic_id uuid,
    parent_topic_version bigint CHECK(parent_topic_version>0),
    PRIMARY KEY(team_id,shared_space_id,space_generation,topic_id),
    FOREIGN KEY(team_id,shared_space_id,space_generation,topic_id,topic_version)
        REFERENCES ontology_record_revisions(team_id,shared_space_id,space_generation,record_id,version) ON DELETE CASCADE,
    FOREIGN KEY(team_id,shared_space_id,space_generation,parent_topic_id,parent_topic_version)
        REFERENCES ontology_record_revisions(team_id,shared_space_id,space_generation,record_id,version) ON DELETE CASCADE,
    CHECK(shared_space_id=dense_mem_team_shared_space(team_id)),
    CHECK((parent_topic_id IS NULL)=(parent_topic_version IS NULL)),
    CHECK(topic_id<>parent_topic_id)
);
CREATE INDEX topic_maintenance_community_ready_idx ON topic_maintenance_community_work
    (team_id,shared_space_id,space_generation,ready_at,topic_id) WHERE ready_at IS NOT NULL;
CREATE INDEX topic_maintenance_community_parent_idx ON topic_maintenance_community_work
    (team_id,shared_space_id,space_generation,parent_topic_id,parent_topic_version) WHERE parent_topic_id IS NOT NULL;
ALTER TABLE topic_maintenance_community_work ENABLE ROW LEVEL SECURITY;
ALTER TABLE topic_maintenance_community_work FORCE ROW LEVEL SECURITY;
CREATE POLICY community_work_access ON topic_maintenance_community_work FOR ALL USING (
    current_setting('app.tx_mode',true) IN ('system','migration') OR
    (current_setting('app.tx_mode',true) IN ('team','profile')
        AND team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid
        AND shared_space_id=dense_mem_team_shared_space(team_id)
        AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id)
        AND (current_setting('app.tx_mode',true)='team' OR dense_mem_space_allowed(shared_space_id)))
);
CREATE TRIGGER feature_storage_write_gate BEFORE INSERT OR UPDATE OR DELETE ON topic_maintenance_community_work
    FOR EACH ROW EXECUTE FUNCTION dense_mem_feature_storage_write_gate();

CREATE OR REPLACE FUNCTION dense_mem_guard_ontology_head() RETURNS trigger LANGUAGE plpgsql AS $guard$
BEGIN
    IF TG_OP='DELETE' THEN RAISE EXCEPTION 'organization heads cannot be deleted'; END IF;
    IF NEW.team_id<>OLD.team_id OR NEW.shared_space_id<>OLD.shared_space_id OR NEW.space_generation<>OLD.space_generation THEN
        RAISE EXCEPTION 'organization head scope is immutable';
    END IF;
    IF TG_TABLE_NAME='ontology_catalog_heads' THEN
        IF NEW.revision<>OLD.revision+1 THEN RAISE EXCEPTION 'catalog revision must advance once'; END IF;
    ELSIF current_setting('app.tx_mode',true)='migration' AND
        (to_jsonb(NEW)-'evidence_id'-'topic_id')=(to_jsonb(OLD)-'evidence_id'-'topic_id') THEN
        RETURN NEW;
    ELSIF NEW.record_id<>OLD.record_id OR NEW.kind<>OLD.kind OR NEW.version<>OLD.version+1 THEN
        RAISE EXCEPTION 'record identity is immutable and version must advance once';
    END IF;
    RETURN NEW;
END $guard$;

DO $security$
DECLARE relation text;
BEGIN
    FOREACH relation IN ARRAY ARRAY['topic_assignment_outcomes','community_placement_outcomes','recall_outcomes','recall_rerank_outcomes'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',relation);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',relation);
        IF relation IN ('recall_outcomes','recall_rerank_outcomes') THEN
            EXECUTE format('CREATE POLICY capture_access ON %I FOR ALL USING (current_setting(''app.tx_mode'',true) IN (''system'',''migration'')) WITH CHECK (current_setting(''app.tx_mode'',true) IN (''system'',''migration''))',relation);
        ELSE
            EXECUTE format('CREATE POLICY outcome_read ON %I FOR SELECT USING (
                current_setting(''app.tx_mode'',true) IN (''system'',''migration'') OR
                (current_setting(''app.tx_mode'',true) IN (''team'',''profile'')
                    AND team_id=NULLIF(current_setting(''app.current_team_id'',true),'''')::uuid
                    AND dense_mem_space_allowed(shared_space_id)
                    AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id)))',relation);
            EXECUTE format('CREATE POLICY outcome_write ON %I FOR INSERT WITH CHECK (
                shared_space_id=dense_mem_team_shared_space(team_id)
                AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id)
                AND (current_setting(''app.tx_mode'',true) IN (''system'',''migration'') OR
                    (current_setting(''app.tx_mode'',true)=''profile''
                    AND team_id=NULLIF(current_setting(''app.current_team_id'',true),'''')::uuid
                    AND dense_mem_space_allowed(shared_space_id))))',relation);
            EXECUTE format('CREATE TRIGGER outcome_append_only BEFORE UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation()',relation);
        END IF;
        EXECUTE format('CREATE TRIGGER feature_storage_write_gate BEFORE INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION dense_mem_feature_storage_write_gate()',relation);
    END LOOP;
END $security$;

-- Keep the old feature-layout check valid throughout upgrades from earlier releases.
CREATE OR REPLACE FUNCTION dense_mem_feature_storage_write_gate() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $gate$
DECLARE mode text:=current_setting('app.tx_mode',true); ready boolean;
BEGIN
    IF mode IS DISTINCT FROM 'migration' AND
       NOT (COALESCE(mode,'')='' AND EXISTS(SELECT 1 FROM pg_roles WHERE rolname=session_user AND rolsuper)) THEN
        PERFORM set_config('app.tx_mode','system',true);
        SELECT EXISTS(SELECT 1 FROM data_migration_runs WHERE migration_version=20261124010001 AND phase='complete') INTO ready;
        PERFORM set_config('app.tx_mode',COALESCE(mode,''),true);
        IF NOT ready OR current_setting('app.storage_layout',true) IS DISTINCT FROM 'feature-storage-v1'
                     OR current_setting('app.topic_layout',true) IS DISTINCT FROM 'topic-storage-v1' THEN
            RAISE EXCEPTION 'topic_storage_blocked: incompatible writer or incomplete migration' USING ERRCODE='55000';
        END IF;
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('app.tx_mode',COALESCE(mode,''),true);
    RAISE;
END $gate$;
INSERT INTO data_migration_runs(migration_version,executor_version,inventory_fingerprint,schema_fingerprint,phase)
VALUES(20261124010001,'topic-storage-v1',
    encode(sha256(convert_to('topic-storage-v1:membership-heads/recall-feedback/topic-marker-dispositions/community-work','UTF8')),'hex'),
    dense_mem_feature_catalog_fingerprint(),'copy');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'topic storage requires forward recovery'; END $$;
-- +goose StatementEnd
