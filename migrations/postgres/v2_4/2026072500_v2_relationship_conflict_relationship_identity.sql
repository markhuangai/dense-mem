-- +goose NO TRANSACTION
-- +goose Up

-- Lock/rewrite analysis:
-- - ADD COLUMN is metadata-only because the nullable column has no default.
-- - Collision reconciliation updates only non-canonical legacy rows, appends one transition per alias, and opens review for active aliases.
-- - Replacement unique indexes are built CONCURRENTLY before the old constraint/index is removed.
-- - The constraint/index swaps and view replacement take short catalog locks; retry the migration if concurrent writes create a new collision during the index build.
-- - RLS impact: reconciliation runs in migration mode; the new column does not change row visibility or mutation ownership.
-- - Rollback boundary: rollback is blocked after any legacy alias is recorded because restoring its prior lifecycle state would rewrite reviewed history.

ALTER TABLE relationship_records
    ADD COLUMN IF NOT EXISTS identity_alias_of_relationship_id UUID NULL;

-- +goose StatementBegin
DO $$
BEGIN
    PERFORM set_config('app.tx_mode', 'migration', true);
    PERFORM set_config('app.current_team_id', '', true);
    PERFORM set_config('app.current_profile_id', '', true);

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'relationship_records'::regclass
          AND conname = 'relationship_records_identity_alias_not_self_check'
    ) THEN
        ALTER TABLE relationship_records
            ADD CONSTRAINT relationship_records_identity_alias_not_self_check
            CHECK (
                identity_alias_of_relationship_id IS NULL
                OR identity_alias_of_relationship_id <> relationship_id
            ) NOT VALID;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'relationship_records'::regclass
          AND conname = 'relationship_records_identity_alias_owner_fk'
    ) THEN
        ALTER TABLE relationship_records
            ADD CONSTRAINT relationship_records_identity_alias_owner_fk
            FOREIGN KEY (team_id, identity_alias_of_relationship_id, owner_profile_id)
            REFERENCES relationship_records(team_id, relationship_id, owner_profile_id)
            ON DELETE RESTRICT
            NOT VALID;
    END IF;
END $$;

ALTER TABLE relationship_records
    VALIDATE CONSTRAINT relationship_records_identity_alias_not_self_check;
ALTER TABLE relationship_records
    VALIDATE CONSTRAINT relationship_records_identity_alias_owner_fk;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    PERFORM set_config('app.tx_mode', 'migration', true);
    PERFORM set_config('app.current_team_id', '', true);
    PERFORM set_config('app.current_profile_id', '', true);

    WITH ranked AS (
        SELECT relationship.team_id,
               relationship.relationship_id,
               first_value(relationship.relationship_id) OVER identity_window
                   AS canonical_relationship_id,
               row_number() OVER identity_window AS identity_rank
        FROM relationship_records AS relationship
        WHERE relationship.identity_alias_of_relationship_id IS NULL
        WINDOW identity_window AS (
            PARTITION BY relationship.team_id,
                         relationship.owner_profile_id,
                         relationship.subject_entity_id,
                         relationship.predicate_key,
                         relationship.object_entity_id,
                         relationship.object_value_id,
                         relationship.polarity,
                         relationship.valid_from,
                         relationship.scope_key
            ORDER BY
                CASE
                    WHEN relationship.status = 'active'
                     AND relationship.tier IN ('validated_claim', 'fact')
                     AND (relationship.valid_to IS NULL OR relationship.valid_to > statement_timestamp())
                        THEN 0
                    WHEN relationship.status = 'active'
                     AND relationship.tier IN ('validated_claim', 'fact')
                        THEN 1
                    ELSE 2
                END,
                relationship.created_at,
                relationship.relationship_id
        )
    ),
    collisions AS (
        SELECT alias.team_id,
               alias.relationship_id,
               alias.owner_profile_id,
               ranked.canonical_relationship_id,
               canonical.semantic_group_key AS canonical_semantic_group_key,
               canonical.valid_to AS canonical_valid_to,
               alias.semantic_group_key AS previous_semantic_group_key,
               alias.tier AS previous_tier,
               alias.status AS previous_status,
               alias.valid_to AS alias_valid_to
        FROM ranked
        JOIN relationship_records AS alias
          ON alias.team_id = ranked.team_id
         AND alias.relationship_id = ranked.relationship_id
        JOIN relationship_records AS canonical
          ON canonical.team_id = ranked.team_id
         AND canonical.relationship_id = ranked.canonical_relationship_id
        WHERE ranked.identity_rank > 1
    ),
    updated AS (
        UPDATE relationship_records AS alias
        SET identity_alias_of_relationship_id = collision.canonical_relationship_id,
            semantic_group_key = collision.canonical_semantic_group_key,
            tier = CASE
                WHEN collision.previous_status = 'active' THEN 'candidate'
                ELSE alias.tier
            END,
            status = CASE
                WHEN collision.previous_status = 'active' THEN 'needs_review'
                ELSE alias.status
            END,
            recorded_to = CASE
                WHEN collision.previous_status = 'active'
                    THEN COALESCE(alias.recorded_to, statement_timestamp())
                ELSE alias.recorded_to
            END,
            metadata = alias.metadata || jsonb_build_object(
                'relationship_identity_migration',
                jsonb_build_object(
                    'version', '2026072500',
                    'canonical_relationship_id', collision.canonical_relationship_id,
                    'previous_semantic_group_key', collision.previous_semantic_group_key,
                    'previous_tier', collision.previous_tier,
                    'previous_status', collision.previous_status,
                    'canonical_valid_to', collision.canonical_valid_to,
                    'alias_valid_to', collision.alias_valid_to
                )
            ),
            version = alias.version + 1,
            updated_at = statement_timestamp()
        FROM collisions AS collision
        WHERE alias.team_id = collision.team_id
          AND alias.relationship_id = collision.relationship_id
          AND alias.identity_alias_of_relationship_id IS NULL
        RETURNING alias.team_id,
                  alias.relationship_id,
                  alias.owner_profile_id,
                  alias.tier,
                  alias.status,
                  collision.canonical_relationship_id,
                  collision.canonical_valid_to,
                  collision.alias_valid_to,
                  collision.previous_tier,
                  collision.previous_status
    ),
    inserted_transitions AS (
        INSERT INTO relationship_transition_events (
            team_id, relationship_id, owner_profile_id,
            from_tier, from_status, to_tier, to_status,
            reason, metadata, idempotency_key
        )
        SELECT updated.team_id,
               updated.relationship_id,
               updated.owner_profile_id,
               updated.previous_tier,
               updated.previous_status,
               updated.tier,
               updated.status,
               'relationship_identity_valid_to_conflict',
               jsonb_build_object(
                   'migration_version', '2026072500',
                   'canonical_relationship_id', updated.canonical_relationship_id,
                   'canonical_valid_to', updated.canonical_valid_to,
                   'alias_valid_to', updated.alias_valid_to
               ),
               'migration:2026072500:identity_alias:' || updated.relationship_id::text
        FROM updated
        ON CONFLICT (team_id, owner_profile_id, idempotency_key)
        WHERE idempotency_key <> ''
        DO NOTHING
        RETURNING transition_id
    )
    INSERT INTO review_tasks (
        team_id, owner_profile_id, ingest_id, placement_item_id,
        relationship_id, observation_id, task_type, status,
        reason, payload, dedupe_key, updated_at
    )
    SELECT updated.team_id,
           updated.owner_profile_id,
           observation.ingest_id,
           observation.placement_item_id,
           updated.relationship_id,
           observation.observation_id,
           'relationship_needs_review',
           'open',
           'relationship_identity_valid_to_conflict',
           jsonb_build_object(
               'migration_version', '2026072500',
               'canonical_relationship_id', updated.canonical_relationship_id,
               'alias_relationship_id', updated.relationship_id,
               'canonical_valid_to', updated.canonical_valid_to,
               'alias_valid_to', updated.alias_valid_to,
               'previous_tier', updated.previous_tier,
               'previous_status', updated.previous_status
           ),
           'migration:2026072500:identity_alias:' || updated.relationship_id::text,
           statement_timestamp()
    FROM updated
    LEFT JOIN LATERAL (
        SELECT relationship_observation.ingest_id,
               relationship_observation.placement_item_id,
               relationship_observation.observation_id
        FROM relationship_observations AS relationship_observation
        WHERE relationship_observation.team_id = updated.team_id
          AND relationship_observation.relationship_id = updated.relationship_id
          AND relationship_observation.owner_profile_id = updated.owner_profile_id
        ORDER BY relationship_observation.created_at,
                 relationship_observation.observation_id
        LIMIT 1
    ) AS observation ON true
    WHERE updated.previous_status = 'active'
    ON CONFLICT (team_id, dedupe_key)
    WHERE dedupe_key <> '' AND status IN ('open', 'acknowledged')
    DO UPDATE SET updated_at = EXCLUDED.updated_at;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    invalid_index RECORD;
BEGIN
    FOR invalid_index IN
        SELECT namespace.nspname AS schema_name,
               index_class.relname AS index_name
        FROM pg_index AS index_state
        JOIN pg_class AS index_class
          ON index_class.oid = index_state.indexrelid
        JOIN pg_namespace AS namespace
          ON namespace.oid = index_class.relnamespace
        WHERE NOT index_state.indisvalid
          AND index_class.relname IN (
              'relationship_records_identity_alias_idx',
              'relationship_records_canonical_identity_unique',
              'relationship_records_active_one_current_canonical_unique'
          )
    LOOP
        EXECUTE format(
            'DROP INDEX %I.%I',
            invalid_index.schema_name,
            invalid_index.index_name
        );
    END LOOP;
END $$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS relationship_records_identity_alias_idx
    ON relationship_records(team_id, identity_alias_of_relationship_id)
    WHERE identity_alias_of_relationship_id IS NOT NULL;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS relationship_records_canonical_identity_unique
    ON relationship_records (
        team_id, owner_profile_id, subject_entity_id, predicate_key,
        object_entity_id, object_value_id, polarity, valid_from, scope_key
    )
    NULLS NOT DISTINCT
    WHERE identity_alias_of_relationship_id IS NULL;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS relationship_records_active_one_current_canonical_unique
    ON relationship_records (
        team_id, owner_profile_id, subject_entity_id, predicate_key,
        polarity, valid_from, scope_key
    )
    NULLS NOT DISTINCT
    WHERE identity_alias_of_relationship_id IS NULL
      AND current_cardinality = 'one'
      AND status = 'active'
      AND tier IN ('validated_claim', 'fact');

-- +goose StatementBegin
DROP VIEW IF EXISTS semantic_edges;
CREATE VIEW semantic_edges
WITH (security_invoker = true) AS
SELECT relationship_id,
       team_id,
       owner_profile_id,
       semantic_group_key,
       subject_entity_id,
       predicate_key,
       predicate_version,
       object_entity_id,
       object_value_id,
       relationship_kind,
       current_cardinality,
       polarity,
       scope_key,
       valid_from,
       valid_to,
       tier,
       support_count,
       source_group_count,
       version
FROM relationship_records
WHERE identity_alias_of_relationship_id IS NULL
  AND status = 'active'
  AND tier IN ('validated_claim', 'fact');
-- +goose StatementEnd

DROP INDEX CONCURRENTLY IF EXISTS relationship_records_active_one_current_unique;

ALTER TABLE relationship_records
    DROP CONSTRAINT IF EXISTS relationship_records_identity_unique;

-- +goose Down

-- +goose StatementBegin
DO $$
DECLARE
    alias_count BIGINT;
BEGIN
    PERFORM set_config('app.tx_mode', 'migration', true);
    PERFORM set_config('app.current_team_id', '', true);
    PERFORM set_config('app.current_profile_id', '', true);

    SELECT count(*)
    INTO alias_count
    FROM relationship_records
    WHERE identity_alias_of_relationship_id IS NOT NULL;

    IF alias_count > 0 THEN
        RAISE EXCEPTION
            'cannot roll back 2026072500: % relationship identity aliases require review-preserving forward migration',
            alias_count;
    END IF;
END $$;
-- +goose StatementEnd

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS relationship_records_identity_unique_with_valid_to_idx
    ON relationship_records (
        team_id, owner_profile_id, subject_entity_id, predicate_key,
        object_entity_id, object_value_id, polarity, valid_from, valid_to, scope_key
    )
    NULLS NOT DISTINCT;

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'relationship_records'::regclass
          AND conname = 'relationship_records_identity_unique'
    ) THEN
        ALTER TABLE relationship_records
            ADD CONSTRAINT relationship_records_identity_unique
            UNIQUE USING INDEX relationship_records_identity_unique_with_valid_to_idx;
    ELSIF to_regclass('relationship_records_identity_unique_with_valid_to_idx') IS NOT NULL THEN
        DROP INDEX relationship_records_identity_unique_with_valid_to_idx;
    END IF;
END $$;
-- +goose StatementEnd

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS relationship_records_active_one_current_unique
    ON relationship_records (
        team_id, owner_profile_id, subject_entity_id, predicate_key,
        polarity, valid_from, valid_to, scope_key
    )
    NULLS NOT DISTINCT
    WHERE current_cardinality = 'one'
      AND status = 'active'
      AND tier IN ('validated_claim', 'fact');

-- +goose StatementBegin
DROP VIEW IF EXISTS semantic_edges;
CREATE VIEW semantic_edges
WITH (security_invoker = true) AS
SELECT relationship_id,
       team_id,
       owner_profile_id,
       semantic_group_key,
       subject_entity_id,
       predicate_key,
       predicate_version,
       object_entity_id,
       object_value_id,
       relationship_kind,
       current_cardinality,
       polarity,
       scope_key,
       valid_from,
       valid_to,
       tier,
       support_count,
       source_group_count,
       version
FROM relationship_records
WHERE status = 'active'
  AND tier IN ('validated_claim', 'fact');
-- +goose StatementEnd

DROP INDEX CONCURRENTLY IF EXISTS relationship_records_active_one_current_canonical_unique;
DROP INDEX CONCURRENTLY IF EXISTS relationship_records_canonical_identity_unique;
DROP INDEX CONCURRENTLY IF EXISTS relationship_records_identity_alias_idx;

ALTER TABLE relationship_records
    DROP CONSTRAINT IF EXISTS relationship_records_identity_alias_owner_fk;
ALTER TABLE relationship_records
    DROP CONSTRAINT IF EXISTS relationship_records_identity_alias_not_self_check;
ALTER TABLE relationship_records
    DROP COLUMN IF EXISTS identity_alias_of_relationship_id;
