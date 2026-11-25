-- Lock/rewrite impact: bounded acquisition of writer-conflicting locks, no heap rewrite.
-- RLS impact: new forced policies use the existing authenticated shared-space context.
-- Backfill: the next migration copies and verifies ordered pages under this write fence.
-- Recovery: keep services offline and resume or restore the verified backup.

-- +goose Up
-- +goose StatementBegin
SET LOCAL app.tx_mode='migration';
SET LOCAL lock_timeout='5s';

CREATE FUNCTION dense_mem_offline_upgrade_fence() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF current_setting('app.tx_mode',true) IS DISTINCT FROM 'migration' THEN
        RAISE EXCEPTION 'offline_upgrade_blocked: migration has not reached cutover' USING ERRCODE='55000';
    END IF;
    RETURN NULL;
END $$;

DO $fence$
DECLARE relation regclass;
BEGIN
    FOR relation IN SELECT c.oid::regclass FROM pg_class c
        WHERE c.relnamespace='public'::regnamespace AND c.relkind IN ('r','p')
          AND c.relname<>'goose_db_version' AND NOT c.relispartition
          AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass
                AND d.objid=c.oid AND d.deptype='e')
        ORDER BY c.oid
    LOOP
        EXECUTE format('LOCK TABLE %s IN SHARE ROW EXCLUSIVE MODE NOWAIT',relation);
        EXECUTE format('CREATE TRIGGER offline_upgrade_fence BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON %s
            FOR EACH STATEMENT EXECUTE FUNCTION dense_mem_offline_upgrade_fence()',relation);
    END LOOP;
END $fence$;

CREATE TABLE topics (
    team_id uuid NOT NULL,
    shared_space_id uuid NOT NULL,
    space_generation bigint NOT NULL CHECK (space_generation>0),
    topic_id uuid NOT NULL,
    definition_version bigint NOT NULL CHECK (definition_version>0),
    version bigint NOT NULL CHECK (version>=definition_version),
    retired boolean NOT NULL,
    topic_key text NOT NULL,
    label text NOT NULL,
    description text NOT NULL,
    aliases text[] NOT NULL,
    parent_topic_id uuid,
    placement text NOT NULL CHECK (placement IN ('declared','placed','standalone')),
    PRIMARY KEY (team_id,shared_space_id,space_generation,topic_id),
    FOREIGN KEY (team_id,shared_space_id) REFERENCES memory_spaces(team_id,id),
    FOREIGN KEY (team_id,shared_space_id,space_generation,topic_id,definition_version)
        REFERENCES topic_record_revisions(team_id,shared_space_id,space_generation,record_id,version),
    CHECK (topic_id<>parent_topic_id)
);
CREATE INDEX topics_parent_idx ON topics(team_id,shared_space_id,space_generation,parent_topic_id)
    WHERE NOT retired AND parent_topic_id IS NOT NULL;

CREATE TABLE topic_memberships (
    team_id uuid NOT NULL,
    shared_space_id uuid NOT NULL,
    space_generation bigint NOT NULL CHECK (space_generation>0),
    record_id uuid NOT NULL,
    version bigint NOT NULL CHECK (version>0),
    retired boolean NOT NULL,
    evidence_id uuid NOT NULL,
    topic_id uuid NOT NULL,
    topic_version bigint NOT NULL CHECK (topic_version>=0),
    source_fingerprint text NOT NULL,
    PRIMARY KEY (team_id,shared_space_id,space_generation,record_id),
    FOREIGN KEY (team_id,shared_space_id) REFERENCES memory_spaces(team_id,id),
    FOREIGN KEY (team_id,shared_space_id,space_generation,record_id,version)
        REFERENCES topic_record_revisions(team_id,shared_space_id,space_generation,record_id,version)
);
CREATE UNIQUE INDEX topic_memberships_active_idx ON topic_memberships(team_id,shared_space_id,space_generation,evidence_id,topic_id)
    WHERE NOT retired;
CREATE INDEX topic_memberships_topic_idx ON topic_memberships(team_id,shared_space_id,space_generation,topic_id,evidence_id)
    WHERE NOT retired;

CREATE TABLE evidence_equivalences (
    team_id uuid NOT NULL,
    shared_space_id uuid NOT NULL,
    space_generation bigint NOT NULL CHECK (space_generation>0),
    record_id uuid NOT NULL,
    version bigint NOT NULL CHECK (version>0),
    retired boolean NOT NULL,
    members uuid[] NOT NULL CHECK (cardinality(members)>=2),
    PRIMARY KEY (team_id,shared_space_id,space_generation,record_id),
    FOREIGN KEY (team_id,shared_space_id) REFERENCES memory_spaces(team_id,id),
    FOREIGN KEY (team_id,shared_space_id,space_generation,record_id,version)
        REFERENCES topic_record_revisions(team_id,shared_space_id,space_generation,record_id,version)
);
CREATE INDEX evidence_equivalences_members_idx ON evidence_equivalences USING gin(members) WHERE NOT retired;

DO $policy$
DECLARE relation text;
DECLARE scoped text := 'team_id=NULLIF(current_setting(''app.current_team_id'',true),'''')::uuid
    AND shared_space_id=dense_mem_team_shared_space(team_id)
    AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id)
    AND (current_setting(''app.tx_mode'',true)=''team'' OR dense_mem_space_allowed(shared_space_id))';
BEGIN
    FOREACH relation IN ARRAY ARRAY['topics','topic_memberships','evidence_equivalences'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',relation);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',relation);
        EXECUTE format('CREATE POLICY topic_relational_scope ON %I FOR ALL USING (
            current_setting(''app.tx_mode'',true) IN (''migration'',''system'') OR (%s))',relation,scoped);
        EXECUTE format('CREATE TRIGGER offline_upgrade_fence BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON %I
            FOR EACH STATEMENT EXECUTE FUNCTION dense_mem_offline_upgrade_fence()',relation);
    END LOOP;
END $policy$;

INSERT INTO data_migration_runs(migration_version,executor_version,inventory_fingerprint,schema_fingerprint,phase)
VALUES(20261125010001,'relational-topics-v1',encode(sha256('topics/memberships/equivalences'::bytea),'hex'),
    encode(sha256('scoped-current-records-v1'::bytea),'hex'),'copy');
INSERT INTO data_migration_checkpoints(migration_version,phase,work_unit)
VALUES(20261125010001,'copy','current_topic_records'),(20261125010001,'verify','current_topic_records');

CREATE FUNCTION dense_mem_relational_topic_record(head topic_record_heads) RETURNS jsonb
LANGUAGE plpgsql STABLE AS $$
DECLARE body jsonb;
DECLARE value jsonb;
DECLARE selected community_placement_outcomes;
BEGIN
    SELECT revision.body INTO STRICT body FROM topic_revision_records revision WHERE
        (revision.team_id,revision.shared_space_id,revision.space_generation,revision.record_id,revision.version)=
        (head.team_id,head.shared_space_id,head.space_generation,head.record_id,head.version);
    CASE head.kind
    WHEN 'topic' THEN
        IF NOT head.retired THEN
            SELECT outcome.* INTO selected FROM community_placement_outcomes outcome
            WHERE (outcome.team_id,outcome.shared_space_id,outcome.space_generation,outcome.topic_id,outcome.topic_version)=
                (head.team_id,head.shared_space_id,head.space_generation,head.record_id,head.version)
              AND (outcome.status='standalone' OR (outcome.status='placed' AND EXISTS (
                  SELECT 1 FROM topic_record_heads parent
                  WHERE (parent.team_id,parent.shared_space_id,parent.space_generation,parent.record_id,parent.version)=
                    (outcome.team_id,outcome.shared_space_id,outcome.space_generation,outcome.parent_topic_id,outcome.parent_topic_version)
                    AND parent.kind='topic' AND NOT parent.retired)))
            ORDER BY outcome.created_at DESC,outcome.outcome_id LIMIT 1;
        END IF;
        value := jsonb_build_object(
            'topic_key',body#>>'{definition,key}', 'label',body#>>'{definition,label}',
            'description',body#>>'{definition,description}', 'aliases',COALESCE(NULLIF(body#>'{definition,aliases}','null'::jsonb),'[]'::jsonb),
            'parent_topic_id', CASE WHEN selected.status IS NOT NULL THEN selected.parent_topic_id::text
                ELSE NULLIF(body#>>'{definition,parent_id}','') END,
            'placement',COALESCE(selected.status,'declared'));
    WHEN 'assignment' THEN
        value := jsonb_build_object('evidence_id',head.evidence_id,'topic_id',head.topic_id,
            'topic_version',COALESCE((body#>>'{dependencies,0,version}')::bigint,0),
            'source_fingerprint',COALESCE(body#>>'{sources,0,fingerprint}',''));
    WHEN 'evidence_group' THEN
        value := jsonb_build_object('members',(SELECT jsonb_agg(member->>'id' ORDER BY member->>'id')
            FROM jsonb_array_elements(body#>'{group,members}') AS member));
    ELSE
        RAISE EXCEPTION 'unsupported relational record kind %',head.kind;
    END CASE;
    RETURN value || jsonb_build_object('team_id',head.team_id,'shared_space_id',head.shared_space_id,
        'space_generation',head.space_generation,'record_id',head.record_id,'version',head.version,'retired',head.retired);
END $$;

CREATE FUNCTION dense_mem_write_relational_topic(kind text, body jsonb) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    CASE kind
    WHEN 'topic' THEN
        INSERT INTO topics(team_id,shared_space_id,space_generation,topic_id,definition_version,
            version,retired,topic_key,label,description,aliases,parent_topic_id,placement)
        SELECT b.team_id,b.shared_space_id,b.space_generation,b.record_id,b.version,b.version,b.retired,
            b.topic_key,b.label,b.description,b.aliases,b.parent_topic_id,b.placement
        FROM jsonb_to_record(body) b(team_id uuid,shared_space_id uuid,space_generation bigint,record_id uuid,
            version bigint,retired boolean,topic_key text,label text,description text,aliases text[],parent_topic_id uuid,placement text)
        ON CONFLICT(team_id,shared_space_id,space_generation,topic_id) DO UPDATE SET
            definition_version=excluded.definition_version,version=topics.version+1,retired=excluded.retired,
            topic_key=excluded.topic_key,label=excluded.label,description=excluded.description,aliases=excluded.aliases,
            parent_topic_id=excluded.parent_topic_id,placement=excluded.placement;
    WHEN 'assignment' THEN
        INSERT INTO topic_memberships
        SELECT * FROM jsonb_populate_record(NULL::topic_memberships,body)
        ON CONFLICT(team_id,shared_space_id,space_generation,record_id) DO UPDATE SET
            version=excluded.version,retired=excluded.retired,evidence_id=excluded.evidence_id,topic_id=excluded.topic_id,
            topic_version=excluded.topic_version,source_fingerprint=excluded.source_fingerprint;
    WHEN 'evidence_group' THEN
        INSERT INTO evidence_equivalences
        SELECT * FROM jsonb_populate_record(NULL::evidence_equivalences,body)
        ON CONFLICT(team_id,shared_space_id,space_generation,record_id) DO UPDATE SET
            version=excluded.version,retired=excluded.retired,members=excluded.members;
    END CASE;
END $$;

CREATE FUNCTION dense_mem_copy_relational_topics(work_phase text) RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE checkpoint data_migration_checkpoints;
DECLARE head topic_record_heads;
DECLARE expected jsonb;
DECLARE actual jsonb;
DECLARE rows_done bigint:=0;
BEGIN
    SELECT * INTO STRICT checkpoint FROM data_migration_checkpoints
      WHERE migration_version=20261125010001 AND phase=work_phase AND work_unit='current_topic_records' FOR UPDATE;
    IF checkpoint.complete THEN RETURN 0; END IF;
    FOR head IN SELECT h.* FROM topic_record_heads h
        WHERE h.kind IN ('topic','assignment','evidence_group')
          AND (h.team_id,h.shared_space_id,h.space_generation,h.record_id) >
              (COALESCE((checkpoint.cursor->>0)::uuid,'00000000-0000-0000-0000-000000000000'::uuid),
               COALESCE((checkpoint.cursor->>1)::uuid,'00000000-0000-0000-0000-000000000000'::uuid),
               COALESCE((checkpoint.cursor->>2)::bigint,0),
               COALESCE((checkpoint.cursor->>3)::uuid,'00000000-0000-0000-0000-000000000000'::uuid))
        ORDER BY h.team_id,h.shared_space_id,h.space_generation,h.record_id LIMIT 100
    LOOP
        expected:=dense_mem_relational_topic_record(head);
        IF work_phase='copy' THEN
            PERFORM dense_mem_write_relational_topic(head.kind,expected);
        ELSE
            CASE head.kind
            WHEN 'topic' THEN
                SELECT (to_jsonb(t)-'topic_id'-'definition_version') || jsonb_build_object('record_id',t.topic_id)
                    INTO actual FROM topics t WHERE (t.team_id,t.shared_space_id,t.space_generation,t.topic_id)=
                    (head.team_id,head.shared_space_id,head.space_generation,head.record_id);
            WHEN 'assignment' THEN
                SELECT to_jsonb(m) INTO actual FROM topic_memberships m WHERE (m.team_id,m.shared_space_id,m.space_generation,m.record_id)=
                    (head.team_id,head.shared_space_id,head.space_generation,head.record_id);
            WHEN 'evidence_group' THEN
                SELECT to_jsonb(e) INTO actual FROM evidence_equivalences e WHERE (e.team_id,e.shared_space_id,e.space_generation,e.record_id)=
                    (head.team_id,head.shared_space_id,head.space_generation,head.record_id);
            END CASE;
            IF actual IS DISTINCT FROM expected THEN
                RAISE EXCEPTION 'relational Topics fingerprint mismatch for record % version %',head.record_id,head.version;
            END IF;
        END IF;
        checkpoint.cursor:=jsonb_build_array(head.team_id,head.shared_space_id,head.space_generation,head.record_id);
        checkpoint.fingerprint:=encode(sha256(convert_to(checkpoint.fingerprint||head.kind||expected::text,'UTF8')),'hex');
        rows_done:=rows_done+1;
    END LOOP;
    UPDATE data_migration_checkpoints SET cursor=checkpoint.cursor,row_count=checkpoint.row_count+rows_done,
        batch_count=checkpoint.batch_count+1,fingerprint=checkpoint.fingerprint,complete=rows_done<100,updated_at=clock_timestamp()
    WHERE migration_version=checkpoint.migration_version AND phase=work_phase AND work_unit=checkpoint.work_unit;
    RETURN rows_done;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'offline topic upgrade requires forward recovery or backup restoration'; END $$;
-- +goose StatementEnd
