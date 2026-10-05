-- Lock/rewrite impact: creates one empty receipt table and indexes; canonical tables are unchanged.
-- RLS impact: shared-only scope, active generation checks, and FORCE RLS match the ontology foundation.
-- Backfill: none; assessments append receipts on demand.
-- Backward compatibility: existing applications ignore the additive receipt table.
-- Rollback: local Down refuses populated receipts; production recovery rolls forward.

-- +goose Up
-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
CREATE TABLE ontology_assessments (
    team_id UUID NOT NULL,
    shared_space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL CHECK (space_generation > 0),
    operation_key TEXT NOT NULL CHECK (length(operation_key) BETWEEN 1 AND 128),
    assessment_id UUID NOT NULL,
    input_hash TEXT NOT NULL CHECK (input_hash ~ '^sha256:[0-9a-f]{64}$'),
    batch_hash TEXT NOT NULL CHECK (batch_hash ~ '^sha256:[0-9a-f]{64}$'),
    body JSONB NOT NULL CHECK (jsonb_typeof(body)='object' AND octet_length(body::text)<=1048576),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, shared_space_id, space_generation, operation_key),
    FOREIGN KEY (team_id, shared_space_id) REFERENCES memory_spaces(team_id,id) ON DELETE RESTRICT,
    CHECK (shared_space_id=dense_mem_team_shared_space(team_id))
);
CREATE INDEX ontology_assessments_input_idx
    ON ontology_assessments(team_id,shared_space_id,space_generation,input_hash,created_at DESC,operation_key);
CREATE INDEX ontology_assessments_id_idx
    ON ontology_assessments(team_id,shared_space_id,space_generation,assessment_id);
ALTER TABLE ontology_assessments ENABLE ROW LEVEL SECURITY;
ALTER TABLE ontology_assessments FORCE ROW LEVEL SECURITY;
CREATE POLICY ontology_assessments_select ON ontology_assessments FOR SELECT USING (
    current_setting('app.tx_mode',true) IN ('system','migration')
    OR (current_setting('app.tx_mode',true) IN ('team','profile')
        AND team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid
        AND shared_space_id=dense_mem_team_shared_space(team_id)
        AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id))
);
CREATE POLICY ontology_assessments_insert ON ontology_assessments FOR INSERT WITH CHECK (
    shared_space_id=dense_mem_team_shared_space(team_id)
    AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id)
    AND (current_setting('app.tx_mode',true) IN ('system','migration')
        OR (current_setting('app.tx_mode',true) IN ('team','profile')
            AND team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid))
);
CREATE TRIGGER ontology_assessments_append_only BEFORE UPDATE OR DELETE ON ontology_assessments
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
DO $rollback$
BEGIN
    IF EXISTS (SELECT 1 FROM ontology_assessments) THEN
        RAISE EXCEPTION 'cannot roll back populated ontology assessments; use roll-forward recovery';
    END IF;
END $rollback$;
DROP TABLE ontology_assessments;
-- +goose StatementEnd
