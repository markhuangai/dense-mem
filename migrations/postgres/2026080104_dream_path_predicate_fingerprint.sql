-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite analysis:
-- - The path-evaluation table is append-only and was introduced immediately
--   before this migration. Existing rows receive a legacy fingerprint so they
--   do not suppress an assessment under a newly explicit predicate allowlist.
-- - Replacing the exact-path unique constraint preserves every historical row
--   and allows one bounded reassessment per changed predicate allowlist.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE dream_path_evaluations
    ADD COLUMN IF NOT EXISTS allowed_predicate_fingerprint TEXT NOT NULL DEFAULT 'legacy';

ALTER TABLE dream_path_evaluations
    ALTER COLUMN allowed_predicate_fingerprint DROP DEFAULT;

ALTER TABLE dream_path_evaluations
    DROP CONSTRAINT IF EXISTS dream_path_evaluations_exact_path_unique;

ALTER TABLE dream_path_evaluations
    ADD CONSTRAINT dream_path_evaluations_exact_path_unique
    UNIQUE (team_id, first_relationship_id, first_relationship_version,
            second_relationship_id, second_relationship_version,
            allowed_predicate_fingerprint);

ALTER TABLE dream_path_evaluations
    ADD CONSTRAINT dream_path_evaluations_predicate_fingerprint_nonempty
    CHECK (btrim(allowed_predicate_fingerprint) <> '');

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $$
BEGIN
    RAISE EXCEPTION '2026080104_dream_path_predicate_fingerprint is irreversible';
END $$;

-- +goose StatementEnd
