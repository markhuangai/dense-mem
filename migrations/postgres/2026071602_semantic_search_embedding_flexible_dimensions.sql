-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DROP INDEX IF EXISTS semantic_search_documents_evidence_hnsw_idx;
DROP INDEX IF EXISTS semantic_search_documents_relationship_hnsw_idx;
DROP INDEX IF EXISTS semantic_search_documents_entity_hnsw_idx;
DROP INDEX IF EXISTS semantic_search_documents_value_hnsw_idx;

ALTER TABLE semantic_search_documents
    DROP CONSTRAINT IF EXISTS semantic_search_embedding_dims_check;

ALTER TABLE semantic_search_documents
    ALTER COLUMN embedding TYPE vector USING embedding::vector;

ALTER TABLE semantic_search_documents
    ADD CONSTRAINT semantic_search_embedding_dims_check
    CHECK (embedding IS NULL OR vector_dims(embedding) BETWEEN 1 AND 16000);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM semantic_search_documents
        WHERE embedding IS NOT NULL AND vector_dims(embedding) <> 3072
    ) THEN
        RAISE EXCEPTION 'cannot restore semantic_search_documents.embedding to vector(3072) while non-3072 embeddings exist';
    END IF;
END $$;

ALTER TABLE semantic_search_documents
    DROP CONSTRAINT IF EXISTS semantic_search_embedding_dims_check;

ALTER TABLE semantic_search_documents
    ALTER COLUMN embedding TYPE vector(3072) USING embedding::vector(3072);

ALTER TABLE semantic_search_documents
    ADD CONSTRAINT semantic_search_embedding_dims_check
    CHECK (embedding IS NULL OR vector_dims(embedding) = 3072);

CREATE INDEX IF NOT EXISTS semantic_search_documents_evidence_hnsw_idx
    ON semantic_search_documents
    USING hnsw ((embedding::halfvec(3072)) halfvec_cosine_ops)
    WHERE embedding IS NOT NULL AND search_state = 'current' AND source_type = 'evidence';

CREATE INDEX IF NOT EXISTS semantic_search_documents_relationship_hnsw_idx
    ON semantic_search_documents
    USING hnsw ((embedding::halfvec(3072)) halfvec_cosine_ops)
    WHERE embedding IS NOT NULL AND search_state = 'current' AND source_type = 'relationship';

CREATE INDEX IF NOT EXISTS semantic_search_documents_entity_hnsw_idx
    ON semantic_search_documents
    USING hnsw ((embedding::halfvec(3072)) halfvec_cosine_ops)
    WHERE embedding IS NOT NULL AND search_state = 'current' AND source_type = 'entity';

CREATE INDEX IF NOT EXISTS semantic_search_documents_value_hnsw_idx
    ON semantic_search_documents
    USING hnsw ((embedding::halfvec(3072)) halfvec_cosine_ops)
    WHERE embedding IS NOT NULL AND search_state = 'current' AND source_type = 'value';

-- +goose StatementEnd
