-- Lock/rewrite impact: creates empty draft tables and indexes; no canonical rewrite.
-- RLS impact: shared generation plus authenticated draft-owner scope; no private sources.
-- Backfill: none. Existing applications ignore this additive schema.
-- Backward compatibility: existing readers and writers do not access draft tables.
-- Rollback: refuses populated history; production recovery rolls forward.

-- +goose Up
-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);

CREATE TABLE ontology_draft_heads (
    team_id UUID NOT NULL,
    shared_space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL CHECK (space_generation > 0),
    owner_profile_id UUID NOT NULL,
    draft_id UUID NOT NULL,
    version BIGINT NOT NULL CHECK (version > 0),
    state TEXT NOT NULL CHECK (state IN ('open','published','discarded')),
    body JSONB NOT NULL CHECK (jsonb_typeof(body) = 'object' AND octet_length(body::text) <= 2097152),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id,shared_space_id,space_generation,owner_profile_id,draft_id),
    FOREIGN KEY (team_id,shared_space_id) REFERENCES memory_spaces(team_id,id) ON DELETE RESTRICT,
    CHECK (shared_space_id = dense_mem_team_shared_space(team_id))
);
CREATE INDEX ontology_drafts_page_idx ON ontology_draft_heads
    (team_id,shared_space_id,space_generation,owner_profile_id,draft_id);

CREATE TABLE ontology_draft_revisions (
    team_id UUID NOT NULL,
    shared_space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL,
    owner_profile_id UUID NOT NULL,
    draft_id UUID NOT NULL,
    version BIGINT NOT NULL CHECK (version > 0),
    operation_key TEXT NOT NULL CHECK (length(operation_key) BETWEEN 1 AND 128),
    request_hash TEXT NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
    body JSONB NOT NULL CHECK (jsonb_typeof(body) = 'object' AND octet_length(body::text) <= 2097152),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version),
    UNIQUE (team_id,shared_space_id,space_generation,owner_profile_id,operation_key),
    FOREIGN KEY (team_id,shared_space_id,space_generation,owner_profile_id,draft_id)
        REFERENCES ontology_draft_heads(team_id,shared_space_id,space_generation,owner_profile_id,draft_id) ON DELETE RESTRICT
);
ALTER TABLE ontology_draft_heads ADD CONSTRAINT ontology_draft_head_revision_fk
    FOREIGN KEY (team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version)
    REFERENCES ontology_draft_revisions(team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version)
    DEFERRABLE INITIALLY DEFERRED;

CREATE FUNCTION dense_mem_guard_ontology_draft() RETURNS TRIGGER
LANGUAGE plpgsql AS $guard$
BEGIN
    IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'ontology drafts cannot be deleted'; END IF;
    IF NEW.team_id <> OLD.team_id OR NEW.shared_space_id <> OLD.shared_space_id
       OR NEW.space_generation <> OLD.space_generation OR NEW.owner_profile_id <> OLD.owner_profile_id
       OR NEW.draft_id <> OLD.draft_id OR NEW.version <> OLD.version + 1 OR OLD.state <> 'open' THEN
        RAISE EXCEPTION 'ontology draft identity is immutable and only open drafts can advance';
    END IF;
    RETURN NEW;
END $guard$;
CREATE TRIGGER ontology_draft_head_guard BEFORE UPDATE OR DELETE ON ontology_draft_heads
    FOR EACH ROW EXECUTE FUNCTION dense_mem_guard_ontology_draft();
CREATE TRIGGER ontology_draft_revision_guard BEFORE UPDATE OR DELETE ON ontology_draft_revisions
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

DO $policy$
DECLARE table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY['ontology_draft_heads','ontology_draft_revisions'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',table_name);
        EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (
            current_setting(''app.tx_mode'',true) IN (''system'',''migration'') OR (
            current_setting(''app.tx_mode'',true)=''profile''
            AND team_id=NULLIF(current_setting(''app.current_team_id'',true),'''')::uuid
            AND owner_profile_id=NULLIF(current_setting(''app.current_profile_id'',true),'''')::uuid
            AND shared_space_id=dense_mem_team_shared_space(team_id)
            AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id)))',table_name||'_select',table_name);
        EXECUTE format('CREATE POLICY %I ON %I FOR INSERT WITH CHECK (
            shared_space_id=dense_mem_team_shared_space(team_id)
            AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id)
            AND (current_setting(''app.tx_mode'',true) IN (''system'',''migration'') OR (
            current_setting(''app.tx_mode'',true)=''profile''
            AND team_id=NULLIF(current_setting(''app.current_team_id'',true),'''')::uuid
            AND owner_profile_id=NULLIF(current_setting(''app.current_profile_id'',true),'''')::uuid)))',table_name||'_insert',table_name);
    END LOOP;
END $policy$;
CREATE POLICY ontology_drafts_update ON ontology_draft_heads FOR UPDATE USING (
    current_setting('app.tx_mode',true) IN ('system','migration') OR (
    current_setting('app.tx_mode',true)='profile'
    AND team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid
    AND owner_profile_id=NULLIF(current_setting('app.current_profile_id',true),'')::uuid
    AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id))) WITH CHECK (
    shared_space_id=dense_mem_team_shared_space(team_id)
    AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id)
    AND (current_setting('app.tx_mode',true) IN ('system','migration') OR (
    current_setting('app.tx_mode',true)='profile'
    AND team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid
    AND owner_profile_id=NULLIF(current_setting('app.current_profile_id',true),'')::uuid)));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT set_config('app.tx_mode','migration',true);
DO $rollback$
BEGIN
    IF EXISTS (SELECT 1 FROM ontology_draft_heads) OR EXISTS (SELECT 1 FROM ontology_draft_revisions) THEN
        RAISE EXCEPTION 'cannot roll back populated ontology draft history; use roll-forward recovery';
    END IF;
END $rollback$;
ALTER TABLE ontology_draft_heads DROP CONSTRAINT ontology_draft_head_revision_fk;
DROP TABLE ontology_draft_revisions,ontology_draft_heads;
DROP FUNCTION dense_mem_guard_ontology_draft();
-- +goose StatementEnd
