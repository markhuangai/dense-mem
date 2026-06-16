-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'community_detection_runs'
          AND column_name = 'run_date'
          AND data_type = 'text'
    ) THEN
        ALTER TABLE community_detection_runs
            DROP CONSTRAINT IF EXISTS community_detection_runs_run_date_format;
        ALTER TABLE community_detection_runs
            ALTER COLUMN run_date TYPE DATE USING run_date::DATE;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'community_detection_runs'
          AND column_name = 'run_date'
          AND data_type = 'date'
    ) THEN
        ALTER TABLE community_detection_runs
            ALTER COLUMN run_date TYPE TEXT USING run_date::TEXT;
        ALTER TABLE community_detection_runs
            ADD CONSTRAINT community_detection_runs_run_date_format
            CHECK (run_date ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$');
    END IF;
END $$;
-- +goose StatementEnd
