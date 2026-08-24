-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: replacing the immutable shape function changes only
-- function metadata; it does not lock or rewrite submission result rows.
-- RLS impact: no policy or visibility change; result ownership remains enforced
-- by the existing submission_relationship_results policies.
-- Backfill: none; historical rows already satisfy the prior, narrower shape.
-- Backward compatibility: older v2.6 binaries continue writing the two prior
-- reasons, while updated binaries may additionally record security quarantine.
-- Rollback: allowed only before any security_quarantine result is committed;
-- afterward the append-only disposition history is an irreversible boundary.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE OR REPLACE FUNCTION submission_relationship_result_shape_valid(
    result_disposition TEXT,
    result_reason TEXT,
    result_splits JSONB
)
RETURNS BOOLEAN AS $$
DECLARE
    split JSONB;
    split_count INTEGER;
    distinct_count INTEGER;
    minimum_index INTEGER;
    maximum_index INTEGER;
BEGIN
    IF jsonb_typeof(result_splits) IS DISTINCT FROM 'array' THEN
        RETURN false;
    END IF;
    split_count := jsonb_array_length(result_splits);
    IF result_disposition = 'stored' THEN
        IF result_reason <> '' OR split_count < 1 THEN
            RETURN false;
        END IF;
    ELSIF result_disposition = 'not_stored' THEN
        RETURN result_reason IN (
            'not_supported_by_evidence',
            'stale_input',
            'security_quarantine'
        ) AND split_count = 0;
    ELSE
        RETURN false;
    END IF;

    FOR split IN SELECT value FROM jsonb_array_elements(result_splits) LOOP
        IF jsonb_typeof(split) IS DISTINCT FROM 'object'
           OR (split - ARRAY['split_index', 'relationship_id', 'relationship_version', 'status']::TEXT[]) <> '{}'::jsonb
           OR NOT (split ?& ARRAY['split_index', 'relationship_id', 'relationship_version', 'status'])
           OR jsonb_typeof(split -> 'split_index') IS DISTINCT FROM 'number'
           OR (split ->> 'split_index') !~ '^(0|[1-9][0-9]*)$'
           OR jsonb_typeof(split -> 'relationship_id') IS DISTINCT FROM 'string'
           OR (split ->> 'relationship_id') !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
           OR jsonb_typeof(split -> 'relationship_version') IS DISTINCT FROM 'number'
           OR (split ->> 'relationship_version') !~ '^[1-9][0-9]*$'
           OR jsonb_typeof(split -> 'status') IS DISTINCT FROM 'string'
           OR split ->> 'status' <> 'active' THEN
            RETURN false;
        END IF;
    END LOOP;

    SELECT COUNT(DISTINCT (value ->> 'split_index')::INTEGER),
           MIN((value ->> 'split_index')::INTEGER),
           MAX((value ->> 'split_index')::INTEGER)
    INTO distinct_count, minimum_index, maximum_index
    FROM jsonb_array_elements(result_splits);
    RETURN distinct_count = split_count AND minimum_index = 0 AND maximum_index = split_count - 1;
END;
$$ LANGUAGE plpgsql IMMUTABLE;

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
        FROM submission_relationship_results
        WHERE disposition = 'not_stored'
          AND reason = 'security_quarantine'
    ) THEN
        RAISE EXCEPTION 'cannot roll back 20260823010002: security quarantine Relationship results exist';
    END IF;
END $$;

CREATE OR REPLACE FUNCTION submission_relationship_result_shape_valid(
    result_disposition TEXT,
    result_reason TEXT,
    result_splits JSONB
)
RETURNS BOOLEAN AS $$
DECLARE
    split JSONB;
    split_count INTEGER;
    distinct_count INTEGER;
    minimum_index INTEGER;
    maximum_index INTEGER;
BEGIN
    IF jsonb_typeof(result_splits) IS DISTINCT FROM 'array' THEN
        RETURN false;
    END IF;
    split_count := jsonb_array_length(result_splits);
    IF result_disposition = 'stored' THEN
        IF result_reason <> '' OR split_count < 1 THEN
            RETURN false;
        END IF;
    ELSIF result_disposition = 'not_stored' THEN
        RETURN result_reason IN ('not_supported_by_evidence', 'stale_input') AND split_count = 0;
    ELSE
        RETURN false;
    END IF;

    FOR split IN SELECT value FROM jsonb_array_elements(result_splits) LOOP
        IF jsonb_typeof(split) IS DISTINCT FROM 'object'
           OR (split - ARRAY['split_index', 'relationship_id', 'relationship_version', 'status']::TEXT[]) <> '{}'::jsonb
           OR NOT (split ?& ARRAY['split_index', 'relationship_id', 'relationship_version', 'status'])
           OR jsonb_typeof(split -> 'split_index') IS DISTINCT FROM 'number'
           OR (split ->> 'split_index') !~ '^(0|[1-9][0-9]*)$'
           OR jsonb_typeof(split -> 'relationship_id') IS DISTINCT FROM 'string'
           OR (split ->> 'relationship_id') !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
           OR jsonb_typeof(split -> 'relationship_version') IS DISTINCT FROM 'number'
           OR (split ->> 'relationship_version') !~ '^[1-9][0-9]*$'
           OR jsonb_typeof(split -> 'status') IS DISTINCT FROM 'string'
           OR split ->> 'status' <> 'active' THEN
            RETURN false;
        END IF;
    END LOOP;

    SELECT COUNT(DISTINCT (value ->> 'split_index')::INTEGER),
           MIN((value ->> 'split_index')::INTEGER),
           MAX((value ->> 'split_index')::INTEGER)
    INTO distinct_count, minimum_index, maximum_index
    FROM jsonb_array_elements(result_splits);
    RETURN distinct_count = split_count AND minimum_index = 0 AND maximum_index = split_count - 1;
END;
$$ LANGUAGE plpgsql IMMUTABLE;

-- +goose StatementEnd
