CREATE OR REPLACE VIEW ontology_revision_records WITH (security_invoker=true) AS SELECT * FROM ontology_record_revisions;
DO $$ BEGIN
    IF to_regclass('public.ontology_draft_heads') IS NOT NULL THEN
        EXECUTE 'CREATE OR REPLACE VIEW ontology_draft_head_records WITH (security_invoker=true) AS SELECT * FROM ontology_draft_heads';
        EXECUTE 'CREATE OR REPLACE VIEW ontology_draft_revision_records WITH (security_invoker=true) AS SELECT * FROM ontology_draft_revisions';
    END IF;
END $$;

CREATE OR REPLACE FUNCTION dense_mem_insert_ontology_revision(team UUID, space UUID, generation BIGINT, publication UUID, payload JSONB) RETURNS VOID
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO ontology_record_revisions(team_id,shared_space_id,space_generation,record_id,version,kind,publication_id,retired,body,fingerprint)
        VALUES(team,space,generation,(payload->>'id')::uuid,(payload->>'version')::bigint,payload->>'kind',publication,(payload->>'retired')::boolean,payload,payload->>'fingerprint');
END $$;

CREATE OR REPLACE FUNCTION dense_mem_persist_ontology_draft(team UUID, space UUID, generation BIGINT, owner_id UUID, operation TEXT, request_fingerprint TEXT, previous BIGINT, payload JSONB) RETURNS BOOLEAN
LANGUAGE plpgsql AS $$
DECLARE id UUID; revision BIGINT; draft_state TEXT; updated TIMESTAMPTZ; changed INTEGER;
BEGIN
    id := (payload#>>'{draft,id}')::uuid;
    revision := (payload#>>'{draft,version}')::bigint;
    draft_state := payload#>>'{draft,state}';
    updated := (payload#>>'{draft,updated_at}')::timestamptz;
    IF previous=0 THEN
        INSERT INTO ontology_draft_heads(team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version,state,body,updated_at)
            VALUES(team,space,generation,owner_id,id,revision,draft_state,payload,updated);
    ELSE
        UPDATE ontology_draft_heads SET version=revision,state=draft_state,body=payload,updated_at=updated
            WHERE (team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version)=(team,space,generation,owner_id,id,previous) AND state='open';
        GET DIAGNOSTICS changed=ROW_COUNT;
        IF changed<>1 THEN RETURN false; END IF;
    END IF;
    INSERT INTO ontology_draft_revisions(team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version,operation_key,request_hash,body)
        VALUES(team,space,generation,owner_id,id,revision,operation,request_fingerprint,payload);
    RETURN true;
END $$;
