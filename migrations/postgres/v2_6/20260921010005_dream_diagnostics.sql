-- +goose NO TRANSACTION

-- Dream diagnostics are a bounded control-plane projection. They retain
-- execution metadata only; semantic hypotheses and accepted Relationships
-- remain owned by their existing tables.
-- Lock/rewrite impact: creates the capture table and its indexes, adds nullable
-- run linkage, validates its foreign key, and builds replacement path indexes
-- concurrently. The constraint attachment changes catalog metadata without a
-- heap rewrite.
-- RLS impact: FORCE RLS applies to captures; existing Dream-path policies and
-- transaction-local team/profile enforcement remain unchanged.
-- Backfill: none; historical captures remain unavailable and legacy paths keep
-- NULL run_id rather than receiving synthetic lineage.
-- Backward compatibility: older binaries ignore the additive capture and
-- nullable-link fields while legacy unscoped path uniqueness remains enforced.
-- Rollback: this migration is irreversible because captures, tombstone windows,
-- and run-scoped path lineage are durable diagnostic history. Down refuses; use
-- a verified backup or a later ordered recovery migration.

-- +goose Up

CREATE TABLE IF NOT EXISTS dream_diagnostic_captures (
    team_id UUID NOT NULL,
    capture_id UUID NOT NULL DEFAULT gen_random_uuid(),
    run_id UUID NOT NULL,
    hypothesis_id UUID NULL,
    phase TEXT NOT NULL,
    outcome TEXT NOT NULL,
    cause TEXT NOT NULL DEFAULT '',
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    capture_state TEXT NOT NULL DEFAULT 'not_captured',
    capture_reason TEXT NOT NULL DEFAULT '',
    captured_at TIMESTAMPTZ NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, capture_id),
    FOREIGN KEY (team_id, run_id)
        REFERENCES dream_cycle_runs(team_id, run_id) ON DELETE CASCADE,
    FOREIGN KEY (team_id, hypothesis_id)
        REFERENCES hypotheses(team_id, hypothesis_id) ON DELETE CASCADE,
    CONSTRAINT dream_diagnostic_captures_phase_check
        CHECK (phase IN ('run', 'target', 'provider', 'proposal', 'validation', 'disposition', 'feedback', 'confirmation')),
    CONSTRAINT dream_diagnostic_captures_outcome_check
        CHECK (btrim(outcome) <> ''),
    CONSTRAINT dream_diagnostic_captures_state_check
        CHECK (capture_state IN ('captured', 'truncated', 'expired', 'unavailable', 'not_captured')),
    CONSTRAINT dream_diagnostic_captures_details_size_check
        CHECK (octet_length(details::text) <= 65536),
    CONSTRAINT dream_diagnostic_captures_payload_size_check
        CHECK (octet_length(payload::text) <= 67108864),
    CONSTRAINT dream_diagnostic_captures_expiry_check
        CHECK (expires_at >= created_at AND expires_at <= created_at + INTERVAL '7 days')
);

CREATE INDEX IF NOT EXISTS dream_diagnostic_captures_run_idx
    ON dream_diagnostic_captures(team_id, run_id, created_at DESC, capture_id);

CREATE INDEX IF NOT EXISTS dream_diagnostic_captures_hypothesis_idx
    ON dream_diagnostic_captures(team_id, hypothesis_id, created_at DESC, capture_id)
    WHERE hypothesis_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS dream_diagnostic_captures_expiry_idx
    ON dream_diagnostic_captures(expires_at, team_id, capture_id);

ALTER TABLE dream_diagnostic_captures ENABLE ROW LEVEL SECURITY;
ALTER TABLE dream_diagnostic_captures FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS dream_diagnostic_captures_select ON dream_diagnostic_captures;
CREATE POLICY dream_diagnostic_captures_select ON dream_diagnostic_captures FOR SELECT USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'team'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
);

DROP POLICY IF EXISTS dream_diagnostic_captures_insert ON dream_diagnostic_captures;
CREATE POLICY dream_diagnostic_captures_insert ON dream_diagnostic_captures FOR INSERT WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) IN ('team', 'profile')
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
);

DROP POLICY IF EXISTS dream_diagnostic_captures_update ON dream_diagnostic_captures;
CREATE POLICY dream_diagnostic_captures_update ON dream_diagnostic_captures FOR UPDATE USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
) WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
);

DROP POLICY IF EXISTS dream_diagnostic_captures_delete ON dream_diagnostic_captures;
CREATE POLICY dream_diagnostic_captures_delete ON dream_diagnostic_captures FOR DELETE USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
);

ALTER TABLE dream_path_evaluations
    ADD COLUMN IF NOT EXISTS run_id UUID NULL;

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'dream_path_evaluations_run_fk'
    ) THEN
        ALTER TABLE dream_path_evaluations
            ADD CONSTRAINT dream_path_evaluations_run_fk
            FOREIGN KEY (team_id, run_id)
            REFERENCES dream_cycle_runs(team_id, run_id) ON DELETE RESTRICT NOT VALID;
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE dream_path_evaluations
    VALIDATE CONSTRAINT dream_path_evaluations_run_fk;

DROP INDEX CONCURRENTLY IF EXISTS dream_path_evaluations_run_idx_invalid;

-- A failed concurrent build leaves an invalid index under its canonical name.
-- Park it so a later migration attempt can rebuild the lookup without blocking writes.
-- +goose StatementBegin
DO $dream_path_evaluations_run_index_recovery$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_index AS index_state
        JOIN pg_class AS index_class ON index_class.oid = index_state.indexrelid
        JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
        WHERE namespace.nspname = 'public'
          AND index_class.relname = 'dream_path_evaluations_run_idx'
          AND index_state.indisvalid IS FALSE
    ) THEN
        ALTER INDEX public.dream_path_evaluations_run_idx
            RENAME TO dream_path_evaluations_run_idx_invalid;
    END IF;
END
$dream_path_evaluations_run_index_recovery$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS dream_path_evaluations_run_idx
    ON dream_path_evaluations(team_id, run_id, created_at DESC)
    WHERE run_id IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS dream_path_evaluations_run_idx_invalid;

ALTER TABLE dream_diagnostic_captures
    ADD COLUMN IF NOT EXISTS tombstone_expires_at TIMESTAMPTZ NULL;

CREATE INDEX IF NOT EXISTS dream_diagnostic_captures_tombstone_expiry_idx
    ON dream_diagnostic_captures(tombstone_expires_at, team_id, capture_id)
    WHERE capture_state = 'expired';

-- Preserve uniqueness for legacy rows while the run-scoped replacement is
-- attached. The catalog checks below make retries safe after any statement.
DROP INDEX CONCURRENTLY IF EXISTS dream_path_evaluations_run_unique_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS dream_path_evaluations_unscoped_unique_idx_invalid;

-- A failed concurrent build leaves an invalid index under its canonical name.
-- Park those entries so the replacement can be built on the next attempt.
-- +goose StatementBegin
DO $dream_path_evaluations_index_recovery$
DECLARE
    candidate RECORD;
BEGIN
    FOR candidate IN
        SELECT index_class.relname
        FROM pg_index AS index_state
        JOIN pg_class AS index_class ON index_class.oid = index_state.indexrelid
        JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
        WHERE namespace.nspname = 'public'
          AND index_class.relname IN (
              'dream_path_evaluations_run_unique_idx',
              'dream_path_evaluations_unscoped_unique_idx'
          )
          AND index_state.indisvalid IS FALSE
    LOOP
        EXECUTE format(
            'ALTER INDEX public.%I RENAME TO %I',
            candidate.relname,
            candidate.relname || '_invalid'
        );
    END LOOP;
END
$dream_path_evaluations_index_recovery$;
-- +goose StatementEnd

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dream_path_evaluations_run_unique_idx
    ON dream_path_evaluations(
        team_id, first_relationship_id, first_relationship_version,
        second_relationship_id, second_relationship_version,
        allowed_predicate_fingerprint, run_id
    );

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dream_path_evaluations_unscoped_unique_idx
    ON dream_path_evaluations(
        team_id, first_relationship_id, first_relationship_version,
        second_relationship_id, second_relationship_version,
        allowed_predicate_fingerprint
    )
    WHERE run_id IS NULL;

-- +goose StatementBegin
DO $$
DECLARE
    constraint_uses_run_id boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM pg_constraint constraint_row
        WHERE constraint_row.conrelid = 'dream_path_evaluations'::regclass
          AND constraint_row.conname = 'dream_path_evaluations_exact_path_unique'
          AND constraint_row.contype = 'u'
          AND EXISTS (
              SELECT 1
              FROM unnest(constraint_row.conkey) AS key_column(attnum)
              JOIN pg_attribute attribute_row
                ON attribute_row.attrelid = constraint_row.conrelid
               AND attribute_row.attnum = key_column.attnum
              WHERE attribute_row.attname = 'run_id'
          )
    )
    INTO constraint_uses_run_id;

    IF NOT constraint_uses_run_id THEN
        ALTER TABLE dream_path_evaluations
            DROP CONSTRAINT IF EXISTS dream_path_evaluations_exact_path_unique;

        ALTER TABLE dream_path_evaluations
            ADD CONSTRAINT dream_path_evaluations_exact_path_unique
            UNIQUE USING INDEX dream_path_evaluations_run_unique_idx;
    END IF;
END $$;
-- +goose StatementEnd

-- ADD CONSTRAINT ... USING INDEX renames the temporary index. If the migration
-- was retried after that attachment, remove the newly recreated temporary copy.
DROP INDEX CONCURRENTLY IF EXISTS dream_path_evaluations_run_unique_idx;
DROP INDEX CONCURRENTLY IF EXISTS dream_path_evaluations_run_unique_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS dream_path_evaluations_unscoped_unique_idx_invalid;

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION '20260921010005_dream_diagnostics is irreversible; restore a verified backup or roll forward';
END $$;
-- +goose StatementEnd
