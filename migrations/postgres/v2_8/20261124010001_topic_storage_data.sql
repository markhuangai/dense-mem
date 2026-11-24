-- Registered as a Goose Go migration. Each keyset page commits its checkpoint and data together.
-- +goose Up
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'topic storage requires the application checkpoint executor'; END $$;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'topic storage requires forward recovery'; END $$;
-- +goose StatementEnd
