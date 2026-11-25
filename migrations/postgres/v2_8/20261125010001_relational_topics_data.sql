-- This version is executed by Goose through relationalTopicDataMigration.
-- It commits bounded copy and verification pages using durable checkpoints.

-- +goose Up
SELECT 'relational_topics_v1'::text;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'offline topic upgrade requires forward recovery or backup restoration'; END $$;
-- +goose StatementEnd
