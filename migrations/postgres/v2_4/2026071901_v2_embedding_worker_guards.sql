-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DROP INDEX IF EXISTS embedding_jobs_ready_idx;

CREATE INDEX IF NOT EXISTS embedding_jobs_ready_idx
    ON embedding_jobs(team_id, available_at ASC, created_at ASC, embedding_job_id)
    WHERE status = 'queued';

CREATE INDEX IF NOT EXISTS embedding_jobs_contract_status_idx
    ON embedding_jobs(embedding_contract_id, embedding_dimensions, status, updated_at DESC);

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

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DROP INDEX IF EXISTS embedding_jobs_contract_status_idx;
DROP INDEX IF EXISTS embedding_jobs_ready_idx;

CREATE INDEX IF NOT EXISTS embedding_jobs_ready_idx
    ON embedding_jobs(team_id, status, available_at ASC, created_at ASC, embedding_job_id)
    WHERE status IN ('queued', 'failed');

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd
