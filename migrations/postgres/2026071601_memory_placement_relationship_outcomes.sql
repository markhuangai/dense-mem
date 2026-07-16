-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE memory_placement_items
    ADD COLUMN IF NOT EXISTS relationship_outcomes JSONB NOT NULL DEFAULT '[]'::jsonb;

DROP INDEX IF EXISTS idx_memory_placement_items_relationship;

ALTER TABLE memory_placement_items
    DROP COLUMN IF EXISTS relationship_id;

ALTER TABLE memory_placement_items
    DROP CONSTRAINT IF EXISTS memory_placement_items_category_check;

ALTER TABLE memory_placement_items
    ADD CONSTRAINT memory_placement_items_category_check
    CHECK (category IN (
        'fragment_only',
        'candidate_claim',
        'validated_claim',
        'promoted_fact',
        'needs_more_evidence',
        'rejected_false',
        'accepted_promoted',
        'rejected_explained',
        'evidence_processed',
        'evidence_quarantined',
        'processing_failed'
    ));

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE memory_placement_items
    DROP COLUMN IF EXISTS relationship_outcomes;

UPDATE memory_placement_items
SET category = 'fragment_only'
WHERE category IN ('evidence_processed', 'evidence_quarantined', 'processing_failed');

ALTER TABLE memory_placement_items
    DROP CONSTRAINT IF EXISTS memory_placement_items_category_check;

ALTER TABLE memory_placement_items
    ADD CONSTRAINT memory_placement_items_category_check
    CHECK (category IN (
        'fragment_only',
        'candidate_claim',
        'validated_claim',
        'promoted_fact',
        'needs_more_evidence',
        'rejected_false',
        'accepted_promoted',
        'rejected_explained'
    ));

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd
