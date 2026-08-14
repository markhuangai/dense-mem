-- +goose Up
CREATE TABLE IF NOT EXISTS community_detection_runs (
    profile_id TEXT NOT NULL,
    run_date DATE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (profile_id, run_date)
);

CREATE INDEX IF NOT EXISTS community_detection_runs_run_date_idx
    ON community_detection_runs (run_date);

-- +goose Down
DROP TABLE IF EXISTS community_detection_runs;
