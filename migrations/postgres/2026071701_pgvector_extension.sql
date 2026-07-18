-- +goose Up
CREATE EXTENSION IF NOT EXISTS vector;

-- +goose Down
-- +goose StatementBegin

-- Intentionally left installed: dropping vector can break existing vector columns.
SELECT 1;

-- +goose StatementEnd
