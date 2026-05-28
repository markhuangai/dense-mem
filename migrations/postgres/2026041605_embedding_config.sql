-- +goose Up
-- +goose StatementBegin

-- Embedding config table: singleton row storing the active embedding model configuration.
-- This ensures consistency between configured model and stored vectors.
CREATE TABLE IF NOT EXISTS embedding_config (
    id SMALLINT PRIMARY KEY DEFAULT 1,
    model VARCHAR(255) NOT NULL,
    dimensions INT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT embedding_config_singleton CHECK (id = 1)
);

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS embedding_config;

-- +goose StatementEnd