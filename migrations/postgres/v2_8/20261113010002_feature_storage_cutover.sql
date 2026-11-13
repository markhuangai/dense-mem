-- Lock/rewrite impact: metadata-only column removal and view/constraint DDL after bounded verification.
-- RLS impact: original metadata policies remain; new views use invoker rights.
-- Backfill: none in the cutover transaction. The application revalidates before entering it.
-- Backward compatibility: retain the compatible application; older binaries are fenced.
-- Rollback: roll forward with the retained compatible application.

-- +goose Up
-- +goose StatementBegin
SET LOCAL app.tx_mode='migration';
SET LOCAL lock_timeout='5s';
DO $$ BEGIN
    IF NOT EXISTS(SELECT 1 FROM data_migration_runs WHERE migration_version=20261113010001 AND phase='ready' AND source_fingerprint IS NOT NULL) THEN
        RAISE EXCEPTION 'feature_storage_blocked: verified checkpoints are required';
    END IF;
END $$;

DROP VIEW IF EXISTS ontology_revision_records;
DROP VIEW IF EXISTS ontology_draft_revision_records;
DROP VIEW IF EXISTS ontology_draft_head_records;
ALTER TABLE ontology_record_revisions DROP COLUMN body;
ALTER TABLE ontology_draft_heads DROP COLUMN body;
ALTER TABLE ontology_draft_revisions DROP COLUMN body;

CREATE VIEW ontology_revision_records WITH (security_invoker=true) AS
    SELECT revision.team_id,revision.shared_space_id,revision.space_generation,revision.record_id,revision.version,
        revision.kind,revision.publication_id,revision.retired,payload.body,revision.fingerprint,revision.created_at
    FROM ontology_record_revisions revision JOIN ontology_revision_payloads payload
        USING(team_id,shared_space_id,space_generation,record_id,version,kind);
CREATE VIEW ontology_draft_revision_records WITH (security_invoker=true) AS
    SELECT revision.team_id,revision.shared_space_id,revision.space_generation,revision.owner_profile_id,revision.draft_id,revision.version,
        revision.operation_key,revision.request_hash,
        dense_mem_feature_draft_body(revision.team_id,revision.shared_space_id,revision.space_generation,revision.owner_profile_id,revision.draft_id,revision.version) AS body,
        revision.created_at
    FROM ontology_draft_revisions revision;
CREATE VIEW ontology_draft_head_records WITH (security_invoker=true) AS
    SELECT head.team_id,head.shared_space_id,head.space_generation,head.owner_profile_id,head.draft_id,head.version,head.state,
        dense_mem_feature_draft_body(head.team_id,head.shared_space_id,head.space_generation,head.owner_profile_id,head.draft_id,head.version) AS body,
        head.updated_at
    FROM ontology_draft_heads head;

CREATE OR REPLACE FUNCTION dense_mem_insert_ontology_revision(team UUID, space UUID, generation BIGINT, publication UUID, payload JSONB) RETURNS VOID
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO ontology_record_revisions(team_id,shared_space_id,space_generation,record_id,version,kind,publication_id,retired,fingerprint)
        VALUES(team,space,generation,(payload->>'id')::uuid,(payload->>'version')::bigint,payload->>'kind',publication,(payload->>'retired')::boolean,payload->>'fingerprint');
    PERFORM dense_mem_store_feature_revision(team,space,generation,(payload->>'id')::uuid,(payload->>'version')::bigint,payload->>'kind',payload);
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
        INSERT INTO ontology_draft_heads(team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version,state,updated_at)
            VALUES(team,space,generation,owner_id,id,revision,draft_state,updated);
    ELSE
        UPDATE ontology_draft_heads SET version=revision,state=draft_state,updated_at=updated
            WHERE (team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version)=(team,space,generation,owner_id,id,previous) AND state='open';
        GET DIAGNOSTICS changed=ROW_COUNT;
        IF changed<>1 THEN RETURN false; END IF;
    END IF;
    INSERT INTO ontology_draft_revisions(team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version,operation_key,request_hash)
        VALUES(team,space,generation,owner_id,id,revision,operation,request_fingerprint);
    PERFORM dense_mem_store_feature_draft(team,space,generation,owner_id,id,revision,payload);
    RETURN true;
END $$;

CREATE FUNCTION dense_mem_check_feature_payload() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE expected TEXT; n INTEGER;
BEGIN
    SELECT kind INTO expected FROM ontology_record_revisions WHERE (team_id,shared_space_id,space_generation,record_id,version)=(NEW.team_id,NEW.shared_space_id,NEW.space_generation,NEW.record_id,NEW.version);
    SELECT count(*) INTO n FROM ontology_revision_payloads WHERE (team_id,shared_space_id,space_generation,record_id,version)=(NEW.team_id,NEW.shared_space_id,NEW.space_generation,NEW.record_id,NEW.version)
        AND kind=expected AND body->>'id'=NEW.record_id::text AND (body->>'version')::bigint=NEW.version;
    IF n<>1 OR (SELECT count(*) FROM ontology_revision_payloads WHERE (team_id,shared_space_id,space_generation,record_id,version)=(NEW.team_id,NEW.shared_space_id,NEW.space_generation,NEW.record_id,NEW.version))<>1 THEN
        RAISE EXCEPTION 'feature_storage_blocked: missing or mismatched revision payload';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER feature_payload_required AFTER INSERT ON ontology_record_revisions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION dense_mem_check_feature_payload();
DO $constraints$
DECLARE relation TEXT;
BEGIN
    FOREACH relation IN ARRAY ARRAY['ontology_topic_revision_payloads','ontology_membership_revision_payloads','ontology_equivalence_revision_payloads','ontology_override_revision_payloads'] LOOP
        EXECUTE format('CREATE CONSTRAINT TRIGGER feature_payload_consistent AFTER INSERT ON %I DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION dense_mem_check_feature_payload()',relation);
    END LOOP;
END $constraints$;

CREATE FUNCTION dense_mem_check_feature_draft() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    PERFORM dense_mem_feature_draft_body(NEW.team_id,NEW.shared_space_id,NEW.space_generation,NEW.owner_profile_id,NEW.draft_id,NEW.version);
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER feature_draft_required AFTER INSERT ON ontology_draft_revisions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION dense_mem_check_feature_draft();

DO $inventory_cleanup$
DECLARE relation RECORD;
BEGIN
    FOR relation IN SELECT c.relname FROM pg_class c JOIN pg_policy p ON p.polrelid=c.oid
        WHERE c.relnamespace='public'::regnamespace AND p.polname='feature_storage_inventory_read'
    LOOP
        EXECUTE format('DROP POLICY feature_storage_inventory_read ON %I',relation.relname);
    END LOOP;
    FOR relation IN SELECT c.relname FROM pg_class c JOIN pg_trigger t ON t.tgrelid=c.oid
        WHERE c.relnamespace='public'::regnamespace AND t.tgname='feature_storage_write_gate'
        AND c.relname=ANY(ARRAY['v2_migration_runs','v2_migration_corpus_items','v2_migration_source_maps',
            'v2_migration_checkpoints','v2_migration_errors','v2_migration_exclusions',
            'v2_migration_gate_results','v2_migration_operator_actions'])
    LOOP
        EXECUTE format('DROP TRIGGER feature_storage_write_gate ON %I',relation.relname);
    END LOOP;
END $inventory_cleanup$;

CREATE POLICY feature_storage_marker_insert ON v2_compatibility_markers FOR INSERT WITH CHECK (
    current_setting('app.tx_mode',true)='migration' AND marker_kind='feature_storage'
    AND version='feature-storage-v1' AND status='compatible');
INSERT INTO v2_compatibility_markers(marker_kind,version,status,metadata)
    SELECT 'feature_storage','feature-storage-v1','compatible',jsonb_build_object('migration_version',migration_version,'source_fingerprint',source_fingerprint,'inventory_fingerprint',inventory_fingerprint,'retained_fingerprints_verified',true,'transaction_outcome','committed')
    FROM data_migration_runs WHERE migration_version=20261113010001;
UPDATE data_migration_runs SET phase='complete',updated_at=clock_timestamp() WHERE migration_version=20261113010001;
DROP POLICY feature_storage_marker_insert ON v2_compatibility_markers;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'feature storage cutover requires compatible readers and roll-forward recovery'; END $$;
-- +goose StatementEnd
