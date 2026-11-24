-- +goose Up
-- +goose StatementBegin
SET LOCAL lock_timeout='5s';
CREATE TABLE evidence_equivalence_outcomes
    (LIKE historical_organization_assessments INCLUDING CONSTRAINTS INCLUDING DEFAULTS);
ALTER TABLE evidence_equivalence_outcomes
    ADD PRIMARY KEY (team_id,shared_space_id,space_generation,operation_key),
    ADD FOREIGN KEY (team_id,shared_space_id) REFERENCES memory_spaces(team_id,id) ON DELETE RESTRICT,
    ADD CHECK (operation_key LIKE 'equivalence:%');
CREATE INDEX evidence_equivalence_outcomes_input_idx ON evidence_equivalence_outcomes
    (team_id,shared_space_id,space_generation,input_hash,created_at DESC,operation_key);
CREATE INDEX evidence_equivalence_outcomes_assessment_idx ON evidence_equivalence_outcomes
    (team_id,shared_space_id,space_generation,assessment_id);
ALTER TABLE evidence_equivalence_outcomes ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_equivalence_outcomes FORCE ROW LEVEL SECURITY;
CREATE POLICY equivalence_outcome_select ON evidence_equivalence_outcomes FOR SELECT USING (
    current_setting('app.tx_mode',true) IN ('system','migration') OR
    (current_setting('app.tx_mode',true)='team' AND team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid
        AND shared_space_id=dense_mem_team_shared_space(team_id)
        AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id))
);
CREATE POLICY equivalence_outcome_insert ON evidence_equivalence_outcomes FOR INSERT WITH CHECK (
    shared_space_id=dense_mem_team_shared_space(team_id) AND space_generation=dense_mem_active_space_generation(team_id,shared_space_id)
    AND (current_setting('app.tx_mode',true) IN ('system','migration') OR
         (current_setting('app.tx_mode',true)='team' AND team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid))
);
CREATE TRIGGER equivalence_outcome_append_only BEFORE UPDATE OR DELETE ON evidence_equivalence_outcomes
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();
CREATE TRIGGER feature_storage_write_gate BEFORE INSERT OR UPDATE OR DELETE ON evidence_equivalence_outcomes
    FOR EACH ROW EXECUTE FUNCTION dense_mem_feature_storage_write_gate();
-- The previous constraint proved that every existing value is in this superset.
ALTER TABLE topic_maintenance_sources
    DROP CONSTRAINT topic_maintenance_sources_status_check,
    ADD CONSTRAINT topic_maintenance_sources_status_check
        CHECK(status IN ('pending','organized','uncategorized','ambiguous','failed','unavailable')) NOT VALID;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Evidence equivalence outcome storage requires forward recovery'; END $$;
-- +goose StatementEnd
