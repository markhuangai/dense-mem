-- +goose Up
-- +goose StatementBegin

CREATE EXTENSION IF NOT EXISTS vector;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Intentionally left installed: dropping vector can break existing vector columns.
SELECT 1;

-- +goose StatementEnd
