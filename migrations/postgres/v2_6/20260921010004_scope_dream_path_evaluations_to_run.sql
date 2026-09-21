-- +goose Up

ALTER TABLE dream_path_evaluations
    DROP CONSTRAINT IF EXISTS dream_path_evaluations_exact_path_unique;

ALTER TABLE dream_path_evaluations
    ADD CONSTRAINT dream_path_evaluations_exact_path_unique
    UNIQUE (team_id, first_relationship_id, first_relationship_version,
            second_relationship_id, second_relationship_version,
            allowed_predicate_fingerprint, run_id);

-- +goose Down

ALTER TABLE dream_path_evaluations
    DROP CONSTRAINT IF EXISTS dream_path_evaluations_exact_path_unique;

ALTER TABLE dream_path_evaluations
    ADD CONSTRAINT dream_path_evaluations_exact_path_unique
    UNIQUE (team_id, first_relationship_id, first_relationship_version,
            second_relationship_id, second_relationship_version,
            allowed_predicate_fingerprint);
