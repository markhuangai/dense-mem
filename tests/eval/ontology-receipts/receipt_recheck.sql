-- +goose Up
-- +goose StatementBegin
SET LOCAL lock_timeout = '5s';

CREATE TABLE ontology_study_receipts (
    team_id uuid NOT NULL,
    shared_space_id uuid NOT NULL,
    space_generation bigint NOT NULL CHECK (space_generation > 0),
    assessment_id uuid NOT NULL,
    operation_key text NOT NULL,
    fully_assigned boolean NOT NULL,
    observed pg_snapshot NOT NULL,
    publication_xid xid8,
    members text[] NOT NULL,
    PRIMARY KEY (team_id, shared_space_id, space_generation, assessment_id),
    FOREIGN KEY (team_id, shared_space_id, space_generation, operation_key)
        REFERENCES ontology_assessments(team_id, shared_space_id, space_generation, operation_key)
);
CREATE TABLE ontology_study_inputs (
    team_id uuid NOT NULL,
    shared_space_id uuid NOT NULL,
    space_generation bigint NOT NULL,
    assessment_id uuid NOT NULL,
    input_key text NOT NULL,
    PRIMARY KEY (team_id, shared_space_id, space_generation, assessment_id, input_key),
    FOREIGN KEY (team_id, shared_space_id, space_generation, assessment_id)
        REFERENCES ontology_study_receipts(team_id, shared_space_id, space_generation, assessment_id)
        ON DELETE CASCADE
);
CREATE INDEX ontology_study_input_reverse_idx ON ontology_study_inputs
    (team_id, shared_space_id, space_generation, input_key, assessment_id);
CREATE TABLE ontology_study_changes (
    team_id uuid NOT NULL,
    shared_space_id uuid NOT NULL,
    space_generation bigint NOT NULL,
    input_key text NOT NULL,
    writer xid8 NOT NULL,
    PRIMARY KEY (team_id, shared_space_id, space_generation, input_key),
    FOREIGN KEY (team_id, shared_space_id) REFERENCES memory_spaces(team_id,id) ON DELETE CASCADE
);
CREATE INDEX ontology_study_source_receipt_idx ON ontology_maintenance_sources
    (team_id, shared_space_id, space_generation, assessment_id)
    WHERE assessment_id IS NOT NULL;

DO $policy$
DECLARE relation text;
        expression text := $scope$
            current_setting('app.tx_mode',true) IN ('system','migration') OR
            (current_setting('app.tx_mode',true) IN ('team','profile')
            AND team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid
            AND (shared_space_id,space_generation)=(SELECT id,generation FROM memory_spaces
                 WHERE team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid
                 AND kind='team_shared' AND lifecycle_state='active'))
        $scope$;
BEGIN
    FOREACH relation IN ARRAY ARRAY['ontology_study_receipts','ontology_study_inputs','ontology_study_changes'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', relation);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', relation);
        EXECUTE format('CREATE POLICY ontology_study_scope ON %I USING (%s) WITH CHECK (%s)', relation, expression, expression);
    END LOOP;
END $policy$;

-- A change committed after the check's snapshot remains visible after its marker has drained.
-- The catalog lock serializes receipt publication with changes to record heads.
CREATE FUNCTION ontology_study_note(p_team uuid, p_space uuid, p_generation bigint, keys text[], canonical boolean)
RETURNS void LANGUAGE plpgsql AS $body$
DECLARE item text;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM memory_spaces WHERE team_id=p_team AND id=p_space
        AND generation=p_generation AND kind='team_shared' AND lifecycle_state='active') THEN
        RETURN;
    END IF;
    FOR item IN SELECT DISTINCT value FROM unnest(keys) AS key(value) WHERE value IS NOT NULL ORDER BY value LOOP
        INSERT INTO ontology_study_changes VALUES (p_team,p_space,p_generation,item,pg_current_xact_id())
        ON CONFLICT (team_id,shared_space_id,space_generation,input_key)
        DO UPDATE SET writer=EXCLUDED.writer WHERE ontology_study_changes.writer<>EXCLUDED.writer;

        IF canonical OR EXISTS (
            SELECT 1 FROM ontology_study_inputs AS input
            JOIN ontology_maintenance_sources AS source USING(team_id,shared_space_id,space_generation,assessment_id)
            WHERE input.team_id=p_team AND input.shared_space_id=p_space AND input.space_generation=p_generation
              AND input.input_key=item AND source.eligible) THEN
            PERFORM dense_mem_enqueue_ontology_marker(p_team,p_space,p_generation,'ontology_study_input',item,'definition',item);
        END IF;
    END LOOP;
END $body$;

CREATE FUNCTION ontology_study_canonical_changed() RETURNS trigger LANGUAGE plpgsql AS $body$
DECLARE old_key text; new_key text; event_team uuid; event_space uuid; event_generation bigint;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        CASE TG_TABLE_NAME
            WHEN 'evidence_fragments' THEN old_key := 'evidence:' || OLD.fragment_id;
            WHEN 'evidence_occurrences' THEN old_key := 'evidence:' || OLD.canonical_fragment_id;
            WHEN 'evidence_lifecycle_events' THEN old_key := 'evidence:' || OLD.target_fragment_id;
            WHEN 'evidence_quarantines' THEN old_key := 'evidence:' || OLD.fragment_id;
            WHEN 'evidence_sources' THEN old_key := 'source:' || OLD.source_id;
            WHEN 'knowledge_ingests' THEN old_key := 'ingest:' || OLD.ingest_id;
        END CASE;
        event_team := OLD.team_id; event_space := OLD.space_id; event_generation := OLD.space_generation;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        CASE TG_TABLE_NAME
            WHEN 'evidence_fragments' THEN new_key := 'evidence:' || NEW.fragment_id;
            WHEN 'evidence_occurrences' THEN new_key := 'evidence:' || NEW.canonical_fragment_id;
            WHEN 'evidence_lifecycle_events' THEN new_key := 'evidence:' || NEW.target_fragment_id;
            WHEN 'evidence_quarantines' THEN new_key := 'evidence:' || NEW.fragment_id;
            WHEN 'evidence_sources' THEN new_key := 'source:' || NEW.source_id;
            WHEN 'knowledge_ingests' THEN new_key := 'ingest:' || NEW.ingest_id;
        END CASE;
        IF TG_OP='UPDATE' AND (event_team,event_space,event_generation)
                IS DISTINCT FROM (NEW.team_id,NEW.space_id,NEW.space_generation) THEN
            PERFORM ontology_study_note(event_team,event_space,event_generation,ARRAY[old_key],true);
            old_key := NULL;
        END IF;
        event_team := NEW.team_id; event_space := NEW.space_id; event_generation := NEW.space_generation;
    END IF;
    PERFORM ontology_study_note(event_team,event_space,event_generation,ARRAY[old_key,new_key],true);
    RETURN NULL;
END $body$;

DO $canonical$
DECLARE relation text;
BEGIN
    FOREACH relation IN ARRAY ARRAY['evidence_fragments','evidence_occurrences','evidence_lifecycle_events',
        'evidence_quarantines','evidence_sources','knowledge_ingests'] LOOP
        EXECUTE format('CREATE TRIGGER ontology_study_canonical AFTER INSERT OR UPDATE OR DELETE ON %I
            FOR EACH ROW EXECUTE FUNCTION ontology_study_canonical_changed()', relation);
    END LOOP;
END $canonical$;

CREATE FUNCTION ontology_study_head_changed() RETURNS trigger LANGUAGE plpgsql AS $body$
DECLARE head ontology_record_heads%ROWTYPE; old_version bigint; new_version bigint; names text[]; keys text[];
BEGIN
    IF TG_OP='UPDATE' AND OLD.version=NEW.version THEN RETURN NULL; END IF;
    IF TG_OP <> 'INSERT' THEN
        head := OLD; old_version := OLD.version; names := OLD.names;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        head := NEW; new_version := NEW.version; names := names || NEW.names;
    END IF;
    SELECT ARRAY(
        SELECT 'record:' || head.record_id
        UNION SELECT 'name:' || name FROM unnest(names) AS name
        UNION SELECT 'association:' || source_kind || ':' || source_id FROM ontology_source_dependencies
            WHERE (team_id,shared_space_id,space_generation,record_id)=
                  (head.team_id,head.shared_space_id,head.space_generation,head.record_id)
            AND record_version IN (old_version,new_version)
        UNION SELECT 'record:' || (revision.body->'override'->>'target_id')
            FROM ontology_revision_records AS revision
            WHERE head.kind='override'
            AND (revision.team_id,revision.shared_space_id,revision.space_generation,revision.record_id)=
                  (head.team_id,head.shared_space_id,head.space_generation,head.record_id)
            AND revision.version IN (old_version,new_version)
    ) INTO keys;
    PERFORM ontology_study_note(head.team_id,head.shared_space_id,head.space_generation,keys,false);
    RETURN NULL;
END $body$;
CREATE TRIGGER ontology_study_head AFTER INSERT OR UPDATE OR DELETE ON ontology_record_heads
    FOR EACH ROW EXECUTE FUNCTION ontology_study_head_changed();

CREATE FUNCTION ontology_study_can_skip(p_team uuid,p_space uuid,p_generation bigint,p_receipt uuid)
RETURNS boolean LANGUAGE sql STABLE AS $body$
SELECT EXISTS (
    SELECT 1 FROM ontology_study_receipts AS receipt
    WHERE (receipt.team_id,receipt.shared_space_id,receipt.space_generation,receipt.assessment_id)=
          (p_team,p_space,p_generation,p_receipt) AND receipt.fully_assigned
    AND NOT EXISTS (
        SELECT 1 FROM ontology_study_inputs AS input
        JOIN ontology_study_changes AS change USING (team_id,shared_space_id,space_generation,input_key)
        WHERE (input.team_id,input.shared_space_id,input.space_generation,input.assessment_id)=
              (p_team,p_space,p_generation,p_receipt)
        AND change.writer IS DISTINCT FROM receipt.publication_xid
        AND NOT pg_visible_in_snapshot(change.writer,receipt.observed))
    AND NOT EXISTS (
        SELECT 1 FROM unnest(receipt.members) AS member(id)
        LEFT JOIN ontology_maintenance_sources AS peer
          ON (peer.team_id,peer.shared_space_id,peer.space_generation,peer.source_kind,peer.source_id)=
             (p_team,p_space,p_generation,'evidence',member.id)
        WHERE peer.status IS DISTINCT FROM 'organized' OR peer.eligible IS DISTINCT FROM true
           OR peer.assessment_id IS DISTINCT FROM p_receipt)
    AND NOT EXISTS (
        SELECT 1 FROM ontology_maintenance_batches AS active
        CROSS JOIN LATERAL jsonb_array_elements(active.sources) AS claimed(source)
        WHERE (active.team_id,active.shared_space_id,active.space_generation)=(p_team,p_space,p_generation)
          AND active.status='running' AND active.lease_until>statement_timestamp()
          AND claimed.source->>'id'=ANY(receipt.members)));
$body$;
SELECT dense_mem_enqueue_ontology_marker(team_id,shared_space_id,space_generation,
    'ontology_study_backfill',team_id::text,'definition',team_id::text)
FROM (SELECT DISTINCT team_id,shared_space_id,space_generation FROM ontology_maintenance_sources
    WHERE eligible AND status IN ('organized','ambiguous') AND assessment_id IS NOT NULL) AS awaiting;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $canonical$
DECLARE relation text;
BEGIN
    FOREACH relation IN ARRAY ARRAY['evidence_fragments','evidence_occurrences','evidence_lifecycle_events',
        'evidence_quarantines','evidence_sources','knowledge_ingests'] LOOP
        EXECUTE format('DROP TRIGGER ontology_study_canonical ON %I', relation);
    END LOOP;
END $canonical$;
DROP TRIGGER ontology_study_head ON ontology_record_heads;
DROP FUNCTION ontology_study_canonical_changed(),ontology_study_head_changed(),
    ontology_study_note(uuid,uuid,bigint,text[],boolean),ontology_study_can_skip(uuid,uuid,bigint,uuid);
DELETE FROM ontology_maintenance_markers WHERE anchor_kind IN ('ontology_study_input','ontology_study_backfill');
DROP INDEX ontology_study_source_receipt_idx;
DROP TABLE ontology_study_inputs,ontology_study_receipts,ontology_study_changes;
-- +goose StatementEnd
