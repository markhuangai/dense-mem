-- +goose Up
CREATE TABLE IF NOT EXISTS community_detection_runs (
    profile_id TEXT NOT NULL,
    run_date TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (profile_id, run_date),
    CONSTRAINT community_detection_runs_run_date_format CHECK (run_date ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$')
);

CREATE INDEX IF NOT EXISTS community_detection_runs_run_date_idx
    ON community_detection_runs (run_date);

-- +goose Down
DROP TABLE IF EXISTS community_detection_runs;
