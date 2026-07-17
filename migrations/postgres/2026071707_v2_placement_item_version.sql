-- +goose Up
ALTER TABLE placement_items
    ADD COLUMN IF NOT EXISTS version INTEGER NOT NULL DEFAULT 1;

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'placement_items_version_check'
    ) THEN
        ALTER TABLE placement_items
            ADD CONSTRAINT placement_items_version_check CHECK (version >= 1);
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE placement_items
    DROP CONSTRAINT IF EXISTS placement_items_version_check;

ALTER TABLE placement_items
    DROP COLUMN IF EXISTS version;
