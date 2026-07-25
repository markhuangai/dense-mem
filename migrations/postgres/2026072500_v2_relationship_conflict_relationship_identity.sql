-- +goose NO TRANSACTION
-- +goose Up

-- Lock/rewrite analysis:
-- - The duplicate precheck scans relationship_records but does not rewrite it.
-- - Replacement unique indexes are built CONCURRENTLY before the constraint/index swap, so normal writes are not blocked for the full build.
-- - ALTER TABLE DROP/ADD CONSTRAINT and DROP INDEX CONCURRENTLY still take short catalog locks; deploy with the default lock timeout budget and retry if busy.
-- - RLS impact: the duplicate precheck runs with migration tx mode inside its DO block; index builds and constraint swaps do not expose rows.

-- +goose StatementBegin
DO $$
DECLARE
    duplicate_identities TEXT;
BEGIN
    PERFORM set_config('app.tx_mode', 'migration', true);
    PERFORM set_config('app.current_team_id', '', true);
    PERFORM set_config('app.current_profile_id', '', true);

    SELECT string_agg(
        format(
            'team_id=%s owner_profile_id=%s subject=%s predicate=%s object_entity=%s object_value=%s polarity=%s valid_from=%s scope=%s rows=%s',
            team_id,
            owner_profile_id,
            subject_entity_id,
            predicate_key,
            COALESCE(object_entity_id::text, ''),
            COALESCE(object_value_id::text, ''),
            polarity,
            COALESCE(valid_from::text, ''),
            COALESCE(scope_key, ''),
            row_count
        ),
        '; '
        ORDER BY team_id, owner_profile_id, subject_entity_id, predicate_key
    )
    INTO duplicate_identities
    FROM (
        SELECT team_id,
               owner_profile_id,
               subject_entity_id,
               predicate_key,
               object_entity_id,
               object_value_id,
               polarity,
               valid_from,
               scope_key,
               count(*) AS row_count
        FROM relationship_records
        GROUP BY team_id, owner_profile_id, subject_entity_id, predicate_key,
                 object_entity_id, object_value_id, polarity, valid_from, scope_key
        HAVING count(*) > 1
    ) AS duplicates;

    IF duplicate_identities IS NOT NULL THEN
        RAISE EXCEPTION 'cannot remove relationship_records.valid_to from identity: %', duplicate_identities;
    END IF;
END $$;
-- +goose StatementEnd

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS relationship_records_identity_unique_without_valid_to_idx
    ON relationship_records (
        team_id, owner_profile_id, subject_entity_id, predicate_key,
        object_entity_id, object_value_id, polarity, valid_from, scope_key
    )
    NULLS NOT DISTINCT;

ALTER TABLE relationship_records
    DROP CONSTRAINT IF EXISTS relationship_records_identity_unique;
ALTER TABLE relationship_records
    ADD CONSTRAINT relationship_records_identity_unique
    UNIQUE USING INDEX relationship_records_identity_unique_without_valid_to_idx;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS relationship_records_active_one_current_unique_new
    ON relationship_records (
        team_id, owner_profile_id, subject_entity_id, predicate_key,
        polarity, valid_from, scope_key
    )
    NULLS NOT DISTINCT
    WHERE current_cardinality = 'one'
      AND status = 'active'
      AND tier IN ('validated_claim', 'fact');
DROP INDEX CONCURRENTLY IF EXISTS relationship_records_active_one_current_unique;
ALTER INDEX relationship_records_active_one_current_unique_new
    RENAME TO relationship_records_active_one_current_unique;

-- +goose Down

-- +goose StatementBegin
DO $$
DECLARE
    duplicate_identities TEXT;
BEGIN
    PERFORM set_config('app.tx_mode', 'migration', true);
    PERFORM set_config('app.current_team_id', '', true);
    PERFORM set_config('app.current_profile_id', '', true);

    SELECT string_agg(
        format(
            'team_id=%s owner_profile_id=%s subject=%s predicate=%s object_entity=%s object_value=%s polarity=%s valid_from=%s valid_to=%s scope=%s rows=%s',
            team_id,
            owner_profile_id,
            subject_entity_id,
            predicate_key,
            COALESCE(object_entity_id::text, ''),
            COALESCE(object_value_id::text, ''),
            polarity,
            COALESCE(valid_from::text, ''),
            COALESCE(valid_to::text, ''),
            COALESCE(scope_key, ''),
            row_count
        ),
        '; '
        ORDER BY team_id, owner_profile_id, subject_entity_id, predicate_key
    )
    INTO duplicate_identities
    FROM (
        SELECT team_id,
               owner_profile_id,
               subject_entity_id,
               predicate_key,
               object_entity_id,
               object_value_id,
               polarity,
               valid_from,
               valid_to,
               scope_key,
               count(*) AS row_count
        FROM relationship_records
        GROUP BY team_id, owner_profile_id, subject_entity_id, predicate_key,
                 object_entity_id, object_value_id, polarity, valid_from, valid_to, scope_key
        HAVING count(*) > 1
    ) AS duplicates;

    IF duplicate_identities IS NOT NULL THEN
        RAISE EXCEPTION 'cannot restore relationship_records.valid_to identity: %', duplicate_identities;
    END IF;
END $$;
-- +goose StatementEnd

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS relationship_records_identity_unique_with_valid_to_idx
    ON relationship_records (
        team_id, owner_profile_id, subject_entity_id, predicate_key,
        object_entity_id, object_value_id, polarity, valid_from, valid_to, scope_key
    )
    NULLS NOT DISTINCT;

ALTER TABLE relationship_records
    DROP CONSTRAINT IF EXISTS relationship_records_identity_unique;
ALTER TABLE relationship_records
    ADD CONSTRAINT relationship_records_identity_unique
    UNIQUE USING INDEX relationship_records_identity_unique_with_valid_to_idx;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS relationship_records_active_one_current_unique_with_valid_to_new
    ON relationship_records (
        team_id, owner_profile_id, subject_entity_id, predicate_key,
        polarity, valid_from, valid_to, scope_key
    )
    NULLS NOT DISTINCT
    WHERE current_cardinality = 'one'
      AND status = 'active'
      AND tier IN ('validated_claim', 'fact');
DROP INDEX CONCURRENTLY IF EXISTS relationship_records_active_one_current_unique;
ALTER INDEX relationship_records_active_one_current_unique_with_valid_to_new
    RENAME TO relationship_records_active_one_current_unique;
