-- +goose Up
-- +goose StatementBegin

DO $$
BEGIN
	IF EXISTS (
		SELECT 1
		FROM pg_available_extensions
		WHERE name = 'vector'
	) THEN
		CREATE EXTENSION IF NOT EXISTS vector;
	END IF;
EXCEPTION
	WHEN insufficient_privilege THEN
		RAISE NOTICE 'pgvector extension is available but current role cannot install it; V2 readiness will report missing pgvector when required';
END $$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Intentionally left installed: dropping vector can break existing vector columns.
SELECT 1;

-- +goose StatementEnd
