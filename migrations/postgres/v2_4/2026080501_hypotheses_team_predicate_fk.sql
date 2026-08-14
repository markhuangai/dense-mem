-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite analysis:
-- - No table rewrite is required.
-- - The orphan preflight and constraint validation scan hypotheses.
-- - Dropping and adding the constraint takes an ACCESS EXCLUSIVE hypotheses
--   table lock that the transactional migration retains through validation;
--   use a maintenance window when Hypothesis history is large.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $dense_mem_hypotheses_team_predicate_preflight$
DECLARE
    missing_count BIGINT;
BEGIN
    SELECT count(*)
    INTO missing_count
    FROM hypotheses AS hypothesis
    LEFT JOIN team_predicate_definitions AS predicate
      ON predicate.team_id = hypothesis.team_id
     AND predicate.predicate_key = hypothesis.predicate_key
     AND predicate.version = hypothesis.predicate_version
    WHERE hypothesis.predicate_key IS NOT NULL
      AND hypothesis.predicate_version IS NOT NULL
      AND predicate.predicate_key IS NULL;

    IF missing_count > 0 THEN
        RAISE EXCEPTION
            'cannot apply 2026080501: % hypothesis rows lack a matching team predicate definition',
            missing_count;
    END IF;
END
$dense_mem_hypotheses_team_predicate_preflight$;

ALTER TABLE hypotheses
    DROP CONSTRAINT IF EXISTS hypotheses_predicate_fk;
ALTER TABLE hypotheses
    ADD CONSTRAINT hypotheses_team_predicate_fk
    FOREIGN KEY (team_id, predicate_key, predicate_version)
    REFERENCES team_predicate_definitions(team_id, predicate_key, version)
    ON DELETE RESTRICT NOT VALID;
ALTER TABLE hypotheses
    VALIDATE CONSTRAINT hypotheses_team_predicate_fk;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $dense_mem_hypotheses_global_predicate_preflight$
DECLARE
    team_only_count BIGINT;
BEGIN
    SELECT count(*)
    INTO team_only_count
    FROM hypotheses AS hypothesis
    LEFT JOIN predicate_definitions AS predicate
      ON predicate.predicate_key = hypothesis.predicate_key
     AND predicate.version = hypothesis.predicate_version
    WHERE hypothesis.predicate_key IS NOT NULL
      AND hypothesis.predicate_version IS NOT NULL
      AND predicate.predicate_key IS NULL;

    IF team_only_count > 0 THEN
        RAISE EXCEPTION
            'cannot roll back 2026080501: % hypothesis rows use team-only predicates',
            team_only_count;
    END IF;
END
$dense_mem_hypotheses_global_predicate_preflight$;

ALTER TABLE hypotheses
    DROP CONSTRAINT IF EXISTS hypotheses_team_predicate_fk;
ALTER TABLE hypotheses
    ADD CONSTRAINT hypotheses_predicate_fk
    FOREIGN KEY (predicate_key, predicate_version)
    REFERENCES predicate_definitions(predicate_key, version)
    ON DELETE RESTRICT NOT VALID;
ALTER TABLE hypotheses
    VALIDATE CONSTRAINT hypotheses_predicate_fk;

-- +goose StatementEnd
