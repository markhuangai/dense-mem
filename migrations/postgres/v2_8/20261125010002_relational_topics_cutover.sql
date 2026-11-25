-- Lock/rewrite impact: validates scoped foreign keys and removes temporary fences.
-- RLS impact: topology and history retain their scoped policies.
-- Recovery: restore the backup before reopening writes or roll forward afterward.

-- +goose Up
-- +goose StatementBegin
SET LOCAL app.tx_mode='migration';
SET LOCAL lock_timeout='5s';

DO $verified$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM data_migration_checkpoints copied
        JOIN data_migration_checkpoints verified USING(migration_version,work_unit)
        WHERE copied.migration_version=20261125010001 AND copied.phase='copy' AND copied.complete
          AND verified.phase='verify' AND verified.complete AND copied.row_count=verified.row_count
          AND copied.fingerprint=verified.fingerprint
          AND copied.row_count=(SELECT count(*) FROM topics)+(SELECT count(*) FROM topic_memberships)+(SELECT count(*) FROM evidence_equivalences)
    ) THEN
        RAISE EXCEPTION 'relational Topics cutover requires matching identities, row counts and fingerprints';
    END IF;
END $verified$;

ALTER TABLE topics ADD CONSTRAINT topics_parent_scope FOREIGN KEY(team_id,shared_space_id,space_generation,parent_topic_id)
    REFERENCES topics(team_id,shared_space_id,space_generation,topic_id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE topic_memberships ADD CONSTRAINT memberships_topic_scope FOREIGN KEY(team_id,shared_space_id,space_generation,topic_id)
    REFERENCES topics(team_id,shared_space_id,space_generation,topic_id) DEFERRABLE INITIALLY DEFERRED;

DROP FUNCTION dense_mem_copy_relational_topics(text);
DROP FUNCTION dense_mem_relational_topic_record(topic_record_heads);
DROP FUNCTION dense_mem_write_relational_topic(text,jsonb);

DO $catalog_reference$
DECLARE reference name;
BEGIN
    SELECT conname INTO STRICT reference FROM pg_constraint
    WHERE conrelid='topic_publications'::regclass AND confrelid='topic_catalog_heads'::regclass AND contype='f';
    EXECUTE format('ALTER TABLE topic_publications DROP CONSTRAINT %I',reference);
END $catalog_reference$;
ALTER TABLE topic_publications ADD CONSTRAINT topic_publication_space
    FOREIGN KEY(team_id,shared_space_id) REFERENCES memory_spaces(team_id,id);

ALTER TABLE remember_invocation_diagnostics ADD COLUMN uses_attempt_capture boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT remember_invocation_attempt_capture CHECK (NOT uses_attempt_capture OR
        (canonical_attempt_id IS NOT NULL AND canonical_attempt_id=invocation_id AND outcome IN ('failed','cancelled')
         AND octet_length(request_bytes)=0 AND octet_length(response_bytes)=0 AND provider_exchanges='[]'::jsonb));

CREATE SEQUENCE topic_publication_revision;
SELECT setval('topic_publication_revision',COALESCE((SELECT max(revision) FROM topic_publications),0)+1,false);

UPDATE data_migration_runs SET phase='complete',source_fingerprint=(SELECT fingerprint FROM data_migration_checkpoints
    WHERE migration_version=20261125010001 AND phase='verify' AND work_unit='current_topic_records'),updated_at=clock_timestamp()
    WHERE migration_version=20261125010001;

DO $unfence$
DECLARE trigger_row record;
BEGIN
    FOR trigger_row IN SELECT t.tgrelid::regclass AS relation,t.tgname AS name FROM pg_trigger t
        WHERE t.tgname IN ('offline_upgrade_fence','feature_storage_write_gate') AND NOT t.tgisinternal
          AND t.tgrelid IN (SELECT oid FROM pg_class WHERE relnamespace='public'::regnamespace)
    LOOP
        EXECUTE format('DROP TRIGGER %I ON %s',trigger_row.name,trigger_row.relation);
    END LOOP;
END $unfence$;
DROP FUNCTION dense_mem_offline_upgrade_fence();
DROP FUNCTION dense_mem_feature_storage_write_gate();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'offline topic upgrade requires forward recovery or backup restoration'; END $$;
-- +goose StatementEnd
