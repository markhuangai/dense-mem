-- Lock/rewrite impact: adds metadata columns and empty scoped tables under the offline upgrade procedure.
-- RLS impact: forces the existing shared-space policy on derived Topic vectors and pair assessments.
-- Backfill: the next migration initializes existing Topics and equivalence state in bounded pages.
-- Backward compatibility: additive metadata leaves historical Evidence and Topic revisions intact.
-- Rollback: restore the verified offline backup or use a forward migration after reopening writes.

-- +goose Up
-- +goose StatementBegin
SET LOCAL app.tx_mode='migration';
SET LOCAL lock_timeout='5s';

ALTER TABLE topics
    ADD COLUMN search_tsv tsvector,
    ADD COLUMN manager_authored boolean NOT NULL DEFAULT false,
    ADD COLUMN material_definition_version bigint NOT NULL DEFAULT 1 CHECK (material_definition_version>0);

CREATE FUNCTION dense_mem_topic_search_vector() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.search_tsv := to_tsvector('simple',concat_ws(' ',NEW.topic_key,NEW.label,NEW.description,array_to_string(NEW.aliases,' ')));
    RETURN NEW;
END $$;
CREATE TRIGGER topic_search_vector BEFORE INSERT OR UPDATE OF topic_key,label,description,aliases ON topics
    FOR EACH ROW EXECUTE FUNCTION dense_mem_topic_search_vector();

CREATE TABLE topic_embeddings (
    team_id uuid NOT NULL,
    shared_space_id uuid NOT NULL,
    space_generation bigint NOT NULL CHECK (space_generation>0),
    topic_id uuid NOT NULL,
    topic_version bigint NOT NULL CHECK (topic_version>0),
    embedding_contract_id uuid NOT NULL,
    embedding_dimensions integer NOT NULL,
    embedding vector NOT NULL,
    consolidation_pending boolean NOT NULL DEFAULT true,
    consolidation_batch_id uuid,
    consolidation_lease_until timestamptz,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (team_id,shared_space_id,space_generation,topic_id,embedding_contract_id),
    FOREIGN KEY (team_id,shared_space_id,space_generation,topic_id)
        REFERENCES topics(team_id,shared_space_id,space_generation,topic_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id,shared_space_id,space_generation,topic_id,topic_version)
        REFERENCES topic_record_revisions(team_id,shared_space_id,space_generation,record_id,version) ON DELETE RESTRICT,
    FOREIGN KEY (embedding_contract_id,embedding_dimensions)
        REFERENCES embedding_contracts(embedding_contract_id,dimensions) ON DELETE RESTRICT,
    CHECK (vector_dims(embedding)=embedding_dimensions),
    CHECK ((consolidation_batch_id IS NULL)=(consolidation_lease_until IS NULL)),
    CHECK (shared_space_id=dense_mem_team_shared_space(team_id))
);

CREATE TABLE topic_consolidation_assessments (
    team_id uuid NOT NULL,
    shared_space_id uuid NOT NULL,
    space_generation bigint NOT NULL CHECK (space_generation>0),
    left_topic_id uuid NOT NULL,
    left_version bigint NOT NULL CHECK (left_version>0),
    right_topic_id uuid NOT NULL,
    right_version bigint NOT NULL CHECK (right_version>0),
    embedding_contract_id uuid NOT NULL REFERENCES embedding_contracts(embedding_contract_id),
    completed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (team_id,shared_space_id,space_generation,left_topic_id,left_version,right_topic_id,right_version,embedding_contract_id),
    FOREIGN KEY (team_id,shared_space_id,space_generation,left_topic_id,left_version)
        REFERENCES topic_record_revisions(team_id,shared_space_id,space_generation,record_id,version) ON DELETE RESTRICT,
    FOREIGN KEY (team_id,shared_space_id,space_generation,right_topic_id,right_version)
        REFERENCES topic_record_revisions(team_id,shared_space_id,space_generation,record_id,version) ON DELETE RESTRICT,
    CHECK (left_topic_id<right_topic_id),
    CHECK (shared_space_id=dense_mem_team_shared_space(team_id))
);

DO $policy$
DECLARE relation text;
BEGIN
    FOREACH relation IN ARRAY ARRAY['topic_embeddings','topic_consolidation_assessments'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',relation);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',relation);
        EXECUTE format('CREATE POLICY topic_derived_scope ON %I FOR ALL USING (
            current_setting(''app.tx_mode'',true) IN (''migration'',''system'') OR
            (current_setting(''app.tx_mode'',true) IN (''team'',''profile'')
             AND team_id=NULLIF(current_setting(''app.current_team_id'',true),'''')::uuid
             AND shared_space_id=dense_mem_team_shared_space(team_id)
             AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id)
             AND (current_setting(''app.tx_mode'',true)=''team'' OR dense_mem_space_allowed(shared_space_id))))',relation);
    END LOOP;
END $policy$;

ALTER TABLE topic_maintenance_batches
    ADD COLUMN batch_kind text NOT NULL DEFAULT 'assignment'
    CHECK (batch_kind IN ('assignment','community','consolidation','equivalence'));
ALTER TABLE topic_maintenance_runs
    DROP CONSTRAINT topic_maintenance_runs_kind_check,
    ADD CONSTRAINT topic_maintenance_runs_kind_check
        CHECK (kind IN ('scheduled','run','retry','reassess','pause','resume'));
ALTER TABLE topic_maintenance_teams
    ADD COLUMN next_stage smallint NOT NULL DEFAULT 0 CHECK (next_stage BETWEEN 0 AND 4);
ALTER TABLE topic_maintenance_community_work
    ADD COLUMN lease_batch_id uuid REFERENCES topic_maintenance_batches(batch_id) ON DELETE SET NULL,
    ADD COLUMN lease_until timestamptz,
    ADD CHECK ((lease_batch_id IS NULL)=(lease_until IS NULL));
ALTER TABLE topic_maintenance_sources
    ADD COLUMN equivalence_status text NOT NULL DEFAULT 'uninitialized'
        CHECK (equivalence_status IN ('uninitialized','pending','completed','failed','unavailable')),
    ADD COLUMN equivalence_fingerprint text NOT NULL DEFAULT '',
    ADD COLUMN equivalence_last_run_id uuid REFERENCES topic_maintenance_runs(run_id),
    ADD COLUMN force_reassessment boolean NOT NULL DEFAULT false;

CREATE OR REPLACE FUNCTION dense_mem_topic_dirty_topic_record_heads() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE material_version bigint;
BEGIN
    IF TG_OP='INSERT' THEN RETURN NEW; END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    IF NEW.kind<>'topic' OR NEW.version=OLD.version THEN RETURN NEW; END IF;
    SELECT material_definition_version INTO material_version FROM topics
      WHERE (team_id,shared_space_id,space_generation,topic_id)=
            (NEW.team_id,NEW.shared_space_id,NEW.space_generation,NEW.record_id);
    IF material_version=NEW.version OR NEW.retired IS DISTINCT FROM OLD.retired THEN
        PERFORM dense_mem_enqueue_topic_marker(NEW.team_id,NEW.shared_space_id,NEW.space_generation,
            'topic_record_heads',NEW.record_id::text,'definition',NEW.record_id::text);
    END IF;
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION dense_mem_schedule_community_placement() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE due timestamptz:=statement_timestamp(); material_version bigint;
BEGIN
    IF TG_OP='UPDATE' AND NOT NEW.retired AND NOT OLD.retired THEN
        SELECT material_definition_version INTO material_version FROM topics
          WHERE (team_id,shared_space_id,space_generation,topic_id)=
                (NEW.team_id,NEW.shared_space_id,NEW.space_generation,NEW.record_id);
        IF material_version<>NEW.version THEN
            UPDATE topic_maintenance_community_work SET topic_version=NEW.version,work_id=gen_random_uuid(),
                lease_batch_id=NULL,lease_until=NULL
            WHERE (team_id,shared_space_id,space_generation,topic_id)=
                  (NEW.team_id,NEW.shared_space_id,NEW.space_generation,NEW.record_id);
            UPDATE topic_maintenance_community_work SET parent_topic_version=NEW.version
            WHERE (team_id,shared_space_id,space_generation,parent_topic_id,parent_topic_version)=
                  (OLD.team_id,OLD.shared_space_id,OLD.space_generation,OLD.record_id,OLD.version);
            RETURN NEW;
        END IF;
    END IF;
    IF NEW.retired THEN
        DELETE FROM topic_maintenance_community_work w WHERE
            (w.team_id,w.shared_space_id,w.space_generation,w.topic_id)=
            (NEW.team_id,NEW.shared_space_id,NEW.space_generation,NEW.record_id);
    ELSE
        INSERT INTO topic_maintenance_community_work(team_id,shared_space_id,space_generation,topic_id,topic_version,ready_at)
            VALUES(NEW.team_id,NEW.shared_space_id,NEW.space_generation,NEW.record_id,NEW.version,due)
            ON CONFLICT(team_id,shared_space_id,space_generation,topic_id) DO UPDATE
            SET topic_version=EXCLUDED.topic_version,work_id=EXCLUDED.work_id,ready_at=EXCLUDED.ready_at,
                lease_batch_id=NULL,lease_until=NULL;
    END IF;
    IF TG_OP='UPDATE' AND (OLD.version,OLD.retired) IS DISTINCT FROM (NEW.version,NEW.retired) THEN
        UPDATE topic_maintenance_community_work w SET work_id=gen_random_uuid(),ready_at=due,lease_batch_id=NULL,lease_until=NULL
        WHERE (w.team_id,w.shared_space_id,w.space_generation,w.parent_topic_id,w.parent_topic_version)=
              (OLD.team_id,OLD.shared_space_id,OLD.space_generation,OLD.record_id,OLD.version);
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Topic hybrid storage requires forward recovery or backup restoration'; END $$;
-- +goose StatementEnd
