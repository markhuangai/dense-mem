-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

-- Replacing these constraints takes a brief ACCESS EXCLUSIVE lock and validates
-- generation metadata only; it does not rewrite search documents or embeddings.
ALTER TABLE search_index_generations
    DROP CONSTRAINT search_index_generations_strategy_check,
    DROP CONSTRAINT search_index_generations_dimension_strategy_check,
    DROP CONSTRAINT search_index_generations_operator_class_check;

ALTER TABLE search_index_generations
    ADD CONSTRAINT search_index_generations_strategy_check CHECK (
        ann_strategy IN ('exact', 'vector_hnsw', 'halfvec_hnsw', 'binary_hnsw')
    ),
    ADD CONSTRAINT search_index_generations_dimension_strategy_check CHECK (
        ann_strategy = 'exact'
        OR (ann_strategy = 'vector_hnsw' AND embedding_dimensions BETWEEN 1 AND 2000)
        OR (ann_strategy = 'halfvec_hnsw' AND embedding_dimensions BETWEEN 1 AND 4000)
        OR (ann_strategy = 'binary_hnsw' AND embedding_dimensions BETWEEN 1 AND 16000)
    ),
    ADD CONSTRAINT search_index_generations_operator_class_check CHECK (
        ann_strategy = 'exact'
        OR (ann_strategy = 'vector_hnsw' AND operator_class = 'vector_cosine_ops')
        OR (ann_strategy = 'halfvec_hnsw' AND operator_class = 'halfvec_cosine_ops')
        OR (ann_strategy = 'binary_hnsw' AND operator_class = 'bit_hamming_ops')
    );

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM search_index_generations
        WHERE ann_strategy = 'binary_hnsw'
    ) THEN
        RAISE EXCEPTION 'cannot roll back binary HNSW support while binary_hnsw generations exist';
    END IF;
END $$;

ALTER TABLE search_index_generations
    DROP CONSTRAINT search_index_generations_strategy_check,
    DROP CONSTRAINT search_index_generations_dimension_strategy_check,
    DROP CONSTRAINT search_index_generations_operator_class_check;

ALTER TABLE search_index_generations
    ADD CONSTRAINT search_index_generations_strategy_check CHECK (
        ann_strategy IN ('exact', 'vector_hnsw', 'halfvec_hnsw')
    ),
    ADD CONSTRAINT search_index_generations_dimension_strategy_check CHECK (
        ann_strategy = 'exact'
        OR (ann_strategy = 'vector_hnsw' AND embedding_dimensions BETWEEN 1 AND 2000)
        OR (ann_strategy = 'halfvec_hnsw' AND embedding_dimensions BETWEEN 1 AND 4000)
    ),
    ADD CONSTRAINT search_index_generations_operator_class_check CHECK (
        ann_strategy = 'exact'
        OR (ann_strategy = 'vector_hnsw' AND operator_class = 'vector_cosine_ops')
        OR (ann_strategy = 'halfvec_hnsw' AND operator_class = 'halfvec_cosine_ops')
    );

-- +goose StatementEnd
