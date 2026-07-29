-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite analysis:
-- - DROP COLUMN takes an ACCESS EXCLUSIVE lock on placement_assessments; apply
--   during the normal migration window after active placement workers stop.
-- - The removed value is non-authoritative audit metadata. No semantic state,
--   search document, vector, support, or embedding data is rewritten.
-- - RLS policies are unchanged because none refer to prompt_revision.
-- - Down restores only a legacy non-empty marker; exact removed labels cannot
--   be reconstructed and are intentionally not used for semantic decisions.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE placement_assessments
    DROP COLUMN IF EXISTS prompt_revision;

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE placement_assessments
    ADD COLUMN IF NOT EXISTS prompt_revision TEXT NOT NULL DEFAULT 'legacy';

ALTER TABLE placement_assessments
    ALTER COLUMN prompt_revision DROP DEFAULT;

ALTER TABLE placement_assessments
    DROP CONSTRAINT IF EXISTS placement_assessments_prompt_nonempty;
ALTER TABLE placement_assessments
    ADD CONSTRAINT placement_assessments_prompt_nonempty
    CHECK (btrim(prompt_revision) <> '');

-- +goose StatementEnd
