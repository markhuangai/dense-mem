-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE OR REPLACE FUNCTION community_record_search_vector(
    record_summary TEXT,
    record_top_entities TEXT[],
    record_top_predicates TEXT[]
)
RETURNS TSVECTOR
LANGUAGE SQL
IMMUTABLE
PARALLEL SAFE
AS $$
    SELECT to_tsvector(
        'simple'::regconfig,
        COALESCE(record_summary, '') || ' ' ||
        COALESCE(array_to_string(record_top_entities, ' '), '') || ' ' ||
        COALESCE(array_to_string(record_top_predicates, ' '), '')
    )
$$;

CREATE INDEX IF NOT EXISTS community_records_current_fts_idx
    ON community_records USING GIN (
        community_record_search_vector(summary, top_entities, top_predicates)
    )
    WHERE status = 'current';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DROP INDEX IF EXISTS community_records_current_fts_idx;
DROP FUNCTION IF EXISTS community_record_search_vector(TEXT, TEXT[], TEXT[]);

-- +goose StatementEnd
