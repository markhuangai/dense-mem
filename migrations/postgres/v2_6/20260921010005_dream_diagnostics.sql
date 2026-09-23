-- Dream diagnostics are a bounded control-plane projection. They retain
-- execution metadata only; semantic hypotheses and accepted Relationships
-- remain owned by their existing tables.
-- Lock/rewrite impact: creates the capture and path-run link tables plus their
-- indexes. It does not rewrite the existing path-evaluation table or replace
-- the legacy path uniqueness constraint.
-- RLS impact: FORCE RLS applies to captures and path-run links; existing
-- Dream-path policies and transaction-local team/profile enforcement remain
-- unchanged.
-- Backfill: none; historical captures and path-run links remain unavailable
-- rather than receiving synthetic lineage.
-- Backward compatibility: older binaries retain their inferable path
-- uniqueness constraint and ignore the additive capture/link tables. New
-- writers add links without changing old writer behavior.
-- Rollback: this migration is irreversible because captures, tombstone windows,
-- and path-run lineage are durable diagnostic history. Down refuses; use a
-- verified backup or a later ordered recovery migration.

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

CREATE TABLE IF NOT EXISTS dream_path_evaluation_run_links (
    team_id UUID NOT NULL,
    path_evaluation_id UUID NOT NULL,
    run_id UUID NOT NULL,
    space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, path_evaluation_id, run_id),
    FOREIGN KEY (team_id, path_evaluation_id)
        REFERENCES dream_path_evaluations(team_id, path_evaluation_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, run_id)
        REFERENCES dream_cycle_runs(team_id, run_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, space_id)
        REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT,
    CONSTRAINT dream_path_evaluation_run_links_space_generation_check
        CHECK (space_generation > 0)
);

CREATE INDEX IF NOT EXISTS dream_path_evaluation_run_links_run_idx
    ON dream_path_evaluation_run_links(team_id, run_id, created_at DESC, path_evaluation_id);

DROP TRIGGER IF EXISTS dream_path_evaluation_run_links_append_only ON dream_path_evaluation_run_links;
CREATE TRIGGER dream_path_evaluation_run_links_append_only
    BEFORE UPDATE OR DELETE ON dream_path_evaluation_run_links
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

ALTER TABLE dream_path_evaluation_run_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE dream_path_evaluation_run_links FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS dream_path_evaluation_run_links_select ON dream_path_evaluation_run_links;
CREATE POLICY dream_path_evaluation_run_links_select ON dream_path_evaluation_run_links FOR SELECT USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) IN ('team', 'profile')
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
);

DROP POLICY IF EXISTS dream_path_evaluation_run_links_insert ON dream_path_evaluation_run_links;
CREATE POLICY dream_path_evaluation_run_links_insert ON dream_path_evaluation_run_links FOR INSERT WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
);

ALTER TABLE dream_diagnostic_captures
    ADD COLUMN IF NOT EXISTS tombstone_expires_at TIMESTAMPTZ NULL;

CREATE INDEX IF NOT EXISTS dream_diagnostic_captures_tombstone_expiry_idx
    ON dream_diagnostic_captures(tombstone_expires_at, team_id, capture_id)
    WHERE capture_state = 'expired';

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION '20260921010005_dream_diagnostics is irreversible; restore a verified backup or roll forward';
END $$;
-- +goose StatementEnd
