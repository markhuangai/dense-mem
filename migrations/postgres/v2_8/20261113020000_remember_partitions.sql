-- Lock/rewrite impact: new empty provenance tables; the lifecycle ALTER takes a
-- bounded metadata lock and validates a looser constraint without rewriting rows.
-- No historical Evidence or receipt is backfilled. Old writers must stop before
-- partitioned writes because they do not know the expanded erasure inventory.
-- Rollback is permitted only before new provenance exists.
-- +goose Up
-- +goose StatementBegin
SET LOCAL lock_timeout = '30s';
SELECT set_config('app.tx_mode','migration',true);

CREATE TABLE remember_submitted_items (
 team_id UUID NOT NULL,
 source_item_id UUID NOT NULL,
 ingest_id UUID NOT NULL,
 owner_profile_id UUID NOT NULL,
 space_id UUID NOT NULL,
 space_generation BIGINT NOT NULL CHECK(space_generation>0),
 submitted_index INTEGER NOT NULL CHECK(submitted_index BETWEEN 0 AND 19),
 content TEXT NOT NULL CHECK(char_length(content) BETWEEN 1 AND 16384 AND btrim(content)<>''),
 content_hash TEXT NOT NULL CHECK(content_hash<>''),
 policy_version TEXT NOT NULL CHECK(policy_version='remember.semantic-partition.v1'),
 source_id UUID,
 source_revision_id UUID,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(team_id,source_item_id),
 UNIQUE(team_id,source_item_id,owner_profile_id),
 UNIQUE(team_id,source_item_id,owner_profile_id,space_id,space_generation),
 UNIQUE(team_id,ingest_id,submitted_index),
 FOREIGN KEY(team_id,ingest_id,owner_profile_id) REFERENCES knowledge_ingests(team_id,ingest_id,owner_profile_id) DEFERRABLE INITIALLY IMMEDIATE,
 FOREIGN KEY(team_id,space_id) REFERENCES memory_spaces(team_id,id) DEFERRABLE INITIALLY IMMEDIATE,
 FOREIGN KEY(team_id,source_id,source_revision_id,owner_profile_id) REFERENCES evidence_source_revisions(team_id,source_id,source_revision_id,owner_profile_id) DEFERRABLE INITIALLY IMMEDIATE,
 CHECK((source_id IS NULL)=(source_revision_id IS NULL))
);

CREATE TABLE remember_item_partitions (
 team_id UUID NOT NULL,
 source_item_id UUID NOT NULL,
 owner_profile_id UUID NOT NULL,
 space_id UUID NOT NULL,
 space_generation BIGINT NOT NULL,
 partition_index INTEGER NOT NULL CHECK(partition_index BETWEEN 0 AND 99),
 occurrence_id UUID NOT NULL,
 span_start INTEGER NOT NULL CHECK(span_start>=0),
 span_end INTEGER NOT NULL CHECK(span_end>span_start AND span_end<=16384),
 PRIMARY KEY(team_id,source_item_id,partition_index),
 UNIQUE(team_id,occurrence_id),
 FOREIGN KEY(team_id,source_item_id,owner_profile_id,space_id,space_generation) REFERENCES remember_submitted_items(team_id,source_item_id,owner_profile_id,space_id,space_generation) DEFERRABLE INITIALLY IMMEDIATE,
 FOREIGN KEY(team_id,occurrence_id,owner_profile_id) REFERENCES evidence_occurrences(team_id,occurrence_id,owner_profile_id) DEFERRABLE INITIALLY IMMEDIATE
);

ALTER TABLE evidence_lifecycle_events ADD COLUMN replacement_source_item_id UUID;
ALTER TABLE evidence_lifecycle_events ADD CONSTRAINT evidence_lifecycle_replacement_item_fk
 FOREIGN KEY(team_id,replacement_source_item_id,owner_profile_id) REFERENCES remember_submitted_items(team_id,source_item_id,owner_profile_id) DEFERRABLE INITIALLY IMMEDIATE;
ALTER TABLE evidence_lifecycle_events DROP CONSTRAINT evidence_lifecycle_events_replacement_check;
ALTER TABLE evidence_lifecycle_events ADD CONSTRAINT evidence_lifecycle_events_replacement_check CHECK(
 (action='supersede' AND ((replacement_fragment_id IS NOT NULL AND replacement_source_item_id IS NULL) OR (replacement_fragment_id IS NULL AND replacement_source_item_id IS NOT NULL)))
 OR (action='retract' AND replacement_fragment_id IS NULL AND replacement_source_item_id IS NULL)
);

CREATE FUNCTION dense_mem_validate_remember_reconstruction() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE original remember_submitted_items; part RECORD; expected_start INTEGER:=0; expected_index INTEGER:=0;
BEGIN
 SELECT * INTO original FROM remember_submitted_items WHERE team_id=NEW.team_id AND source_item_id=NEW.source_item_id;
 IF NOT FOUND THEN RETURN NULL; END IF;
 FOR part IN SELECT mapping.*, occurrence.content, occurrence.space_id AS occurrence_space, occurrence.space_generation AS occurrence_generation,
   occurrence.source_id AS occurrence_source, occurrence.source_revision_id AS occurrence_revision
   FROM remember_item_partitions mapping JOIN evidence_occurrences occurrence ON occurrence.team_id=mapping.team_id AND occurrence.occurrence_id=mapping.occurrence_id AND occurrence.owner_profile_id=mapping.owner_profile_id
   WHERE mapping.team_id=original.team_id AND mapping.source_item_id=original.source_item_id ORDER BY mapping.partition_index
 LOOP
   IF part.partition_index<>expected_index OR part.span_start<>expected_start OR part.occurrence_space<>original.space_id OR part.occurrence_generation<>original.space_generation
      OR part.occurrence_source IS DISTINCT FROM original.source_id OR part.occurrence_revision IS DISTINCT FROM original.source_revision_id
      OR substring(original.content FROM part.span_start+1 FOR part.span_end-part.span_start)<>part.content
   THEN RAISE EXCEPTION 'Remember partitions do not reconstruct their immutable source' USING ERRCODE='23514'; END IF;
   expected_start:=part.span_end; expected_index:=expected_index+1;
 END LOOP;
 IF expected_start<>char_length(original.content) OR expected_index=0 THEN RAISE EXCEPTION 'Remember partition coverage is incomplete' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;

DO $$ DECLARE table_name TEXT; BEGIN
 FOR table_name IN SELECT unnest(ARRAY['remember_submitted_items','remember_item_partitions']) LOOP
   EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',table_name);
   EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',table_name);
   EXECUTE format($policy$CREATE POLICY %I ON %I FOR SELECT USING (
     current_setting('app.tx_mode',true) IN ('system','migration') OR
     (current_setting('app.tx_mode',true) IN ('team','profile') AND team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid
      AND dense_mem_space_allowed(space_id) AND space_generation=dense_mem_active_space_generation(team_id,space_id)))$policy$,table_name||'_select',table_name);
   EXECUTE format($policy$CREATE POLICY %I ON %I FOR INSERT WITH CHECK (
     space_generation=dense_mem_active_space_generation(team_id,space_id) AND
     (current_setting('app.tx_mode',true) IN ('system','migration') OR
      (current_setting('app.tx_mode',true)='profile' AND team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid
       AND owner_profile_id=NULLIF(current_setting('app.current_profile_id',true),'')::uuid AND dense_mem_space_allowed(space_id))))$policy$,table_name||'_insert',table_name);
   EXECUTE format($policy$CREATE POLICY %I ON %I FOR DELETE USING (current_setting('app.tx_mode',true)='system' AND space_id=NULLIF(current_setting('app.private_erasure_space_id',true),'')::uuid)$policy$,table_name||'_delete',table_name);
   EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION prevent_evidence_occurrence_mutation()',table_name||'_append_only',table_name);
   EXECUTE format('CREATE CONSTRAINT TRIGGER %I AFTER INSERT ON %I DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION dense_mem_validate_remember_reconstruction()',table_name||'_reconstruction',table_name);
 END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT set_config('app.tx_mode','migration',true);
DO $$ BEGIN IF EXISTS(SELECT 1 FROM remember_submitted_items) THEN RAISE EXCEPTION 'cannot roll back populated Remember provenance; roll forward'; END IF; END $$;
ALTER TABLE evidence_lifecycle_events DROP CONSTRAINT evidence_lifecycle_events_replacement_check;
ALTER TABLE evidence_lifecycle_events ADD CONSTRAINT evidence_lifecycle_events_replacement_check CHECK((action='supersede' AND replacement_fragment_id IS NOT NULL) OR (action='retract' AND replacement_fragment_id IS NULL));
ALTER TABLE evidence_lifecycle_events DROP COLUMN replacement_source_item_id;
DROP TABLE remember_item_partitions;
DROP TABLE remember_submitted_items;
DROP FUNCTION dense_mem_validate_remember_reconstruction();
-- +goose StatementEnd
