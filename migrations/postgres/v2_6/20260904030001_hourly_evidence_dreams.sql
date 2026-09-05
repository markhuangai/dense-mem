-- Lock/rewrite impact: existing lane columns are backfilled in place and
-- bounded constraints/indexes are added; the session lock timeout limits DDL
-- waits. The additive evidence tables and indexes do not rewrite existing
-- evidence or hypothesis payloads.
-- RLS impact: migration statements run in session-scoped migration mode before
-- touching FORCE RLS tables. Runtime reads remain team-scoped and runtime
-- inserts remain profile-scoped; append-only triggers protect both histories.
-- Backfill: pre-existing hypotheses and Dream runs receive the graph lane and
-- zero evidence counters. No evidence target or derivation history is
-- synthesized from legacy rows.
-- Backward compatibility: graph Dream rows keep their existing behavior and
-- uniqueness is extended only by lane, while evidence discovery is additive
-- and dormant unless the hourly scheduler invokes it. Target attempts are
-- bounded operational reservations: only unvalidated reservations expire;
-- validated outcomes remain durable for pass-cap enforcement.
-- Rollback: Down refuses after either new append-only history table has rows;
-- before first use it removes only the additive schema and restores graph-run
-- uniqueness.

-- +goose NO TRANSACTION

-- Hourly evidence discovery adds only append-only state. Existing graph rows
-- remain graph-lane rows and no historical provenance is removed.

-- +goose Up

-- NO TRANSACTION requires session-scoped policy and timeout settings so every
-- statement touching FORCE RLS tables runs with migration authority.
SET app.tx_mode = 'migration';
SET app.current_team_id = '';
SET app.current_profile_id = '';
SET app.allowed_space_ids = '';
SET lock_timeout = '30s';

ALTER TABLE hypotheses
    ADD COLUMN IF NOT EXISTS lane TEXT NOT NULL DEFAULT 'graph';

UPDATE hypotheses
SET lane = 'graph'
WHERE lane IS NULL OR btrim(lane) = '';

ALTER TABLE hypotheses
    DROP CONSTRAINT IF EXISTS hypotheses_lane_check;
ALTER TABLE hypotheses
    ADD CONSTRAINT hypotheses_lane_check
    CHECK (lane IN ('graph', 'evidence_discovery')) NOT VALID;
ALTER TABLE hypotheses VALIDATE CONSTRAINT hypotheses_lane_check;

ALTER TABLE dream_cycle_runs
    ADD COLUMN IF NOT EXISTS lane TEXT NOT NULL DEFAULT 'graph',
    ADD COLUMN IF NOT EXISTS evidence_targets INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS evaluated_evidence_targets INTEGER NOT NULL DEFAULT 0;

UPDATE dream_cycle_runs
SET lane = 'graph'
WHERE lane IS NULL OR btrim(lane) = '';

ALTER TABLE dream_cycle_runs
    DROP CONSTRAINT IF EXISTS dream_cycle_runs_lane_check,
    DROP CONSTRAINT IF EXISTS dream_cycle_runs_evidence_counts_check;
ALTER TABLE dream_cycle_runs
    ADD CONSTRAINT dream_cycle_runs_lane_check
    CHECK (lane IN ('graph', 'evidence_discovery')) NOT VALID,
    ADD CONSTRAINT dream_cycle_runs_evidence_counts_check
    CHECK (evidence_targets >= 0 AND evaluated_evidence_targets >= 0) NOT VALID;
ALTER TABLE dream_cycle_runs VALIDATE CONSTRAINT dream_cycle_runs_lane_check;
ALTER TABLE dream_cycle_runs VALIDATE CONSTRAINT dream_cycle_runs_evidence_counts_check;

-- Keep the predecessor arbiter so an older graph-only binary can continue to
-- claim its existing window shape during a rolling deployment.
CREATE UNIQUE INDEX IF NOT EXISTS dream_cycle_runs_team_window_canonical_unique
    ON dream_cycle_runs(team_id, window_key)
    WHERE canonical_run_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS dream_cycle_runs_team_lane_window_canonical_unique
    ON dream_cycle_runs(team_id, lane, window_key)
    WHERE canonical_run_id IS NULL;

CREATE INDEX IF NOT EXISTS dream_cycle_runs_team_lane_started_idx
    ON dream_cycle_runs(team_id, lane, started_at DESC, run_id)
    WHERE canonical_run_id IS NULL;

CREATE TABLE IF NOT EXISTS dream_evidence_target_attempts (
    team_id UUID NOT NULL,
    attempt_id UUID NOT NULL DEFAULT gen_random_uuid(),
    target_evidence_id UUID NOT NULL,
    target_content_hash TEXT NOT NULL,
    space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL,
    pass_number SMALLINT NOT NULL,
    reservation_token UUID NOT NULL,
    status TEXT NOT NULL DEFAULT 'reserved',
    accepted_proposals INTEGER NOT NULL DEFAULT 0,
    created_hypotheses INTEGER NOT NULL DEFAULT 0,
    evaluation_persisted BOOLEAN NOT NULL DEFAULT FALSE,
    reserved_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    reservation_expires_at TIMESTAMPTZ NOT NULL,
    dispatch_started_at TIMESTAMPTZ NULL,
    validated_at TIMESTAMPTZ NULL,
    abandoned_at TIMESTAMPTZ NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, attempt_id),
    FOREIGN KEY (team_id, space_id) REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, target_evidence_id) REFERENCES evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
    CONSTRAINT dream_evidence_target_attempts_space_generation_check CHECK (space_generation > 0),
    CONSTRAINT dream_evidence_target_attempts_hash_check CHECK (btrim(target_content_hash) <> ''),
    CONSTRAINT dream_evidence_target_attempts_pass_check CHECK (pass_number IN (1, 2)),
    CONSTRAINT dream_evidence_target_attempts_status_check CHECK (status IN ('reserved', 'validated', 'abandoned')),
    CONSTRAINT dream_evidence_target_attempts_counts_check CHECK (accepted_proposals >= 0 AND created_hypotheses >= 0),
    CONSTRAINT dream_evidence_target_attempts_validated_shape_check CHECK (
        (status = 'validated' AND validated_at IS NOT NULL)
        OR (status <> 'validated')
    ),
    CONSTRAINT dream_evidence_target_attempts_target_unique
        UNIQUE (team_id, target_evidence_id, target_content_hash, pass_number)
);

ALTER TABLE dream_evidence_target_attempts
    ADD COLUMN IF NOT EXISTS dispatch_started_at TIMESTAMPTZ NULL;

CREATE INDEX IF NOT EXISTS dream_evidence_target_attempts_target_idx
    ON dream_evidence_target_attempts(team_id, target_evidence_id, target_content_hash, pass_number);

ALTER TABLE dream_evidence_target_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE dream_evidence_target_attempts FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS dream_evidence_target_attempts_select ON dream_evidence_target_attempts;
CREATE POLICY dream_evidence_target_attempts_select ON dream_evidence_target_attempts
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) IN ('team', 'profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

DROP POLICY IF EXISTS dream_evidence_target_attempts_insert ON dream_evidence_target_attempts;
CREATE POLICY dream_evidence_target_attempts_insert ON dream_evidence_target_attempts
    FOR INSERT WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));

DROP POLICY IF EXISTS dream_evidence_target_attempts_update ON dream_evidence_target_attempts;
CREATE POLICY dream_evidence_target_attempts_update ON dream_evidence_target_attempts
    FOR UPDATE USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));

CREATE TABLE IF NOT EXISTS dream_evidence_target_evaluations (
    team_id UUID NOT NULL,
    evaluation_id UUID NOT NULL DEFAULT gen_random_uuid(),
    run_id UUID NOT NULL,
    space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL,
    target_evidence_id UUID NOT NULL,
    target_content_hash TEXT NOT NULL,
    pass_number SMALLINT NOT NULL,
    provider_model TEXT NOT NULL,
    provider_turns INTEGER NOT NULL DEFAULT 0,
    provider_input_tokens INTEGER NOT NULL DEFAULT 0,
    provider_output_tokens INTEGER NOT NULL DEFAULT 0,
    provider_proposals INTEGER NOT NULL DEFAULT 0,
    accepted_proposals INTEGER NOT NULL DEFAULT 0,
    rejected_proposals INTEGER NOT NULL DEFAULT 0,
    created_hypotheses INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, evaluation_id),
    FOREIGN KEY (team_id, run_id) REFERENCES dream_cycle_runs(team_id, run_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, space_id) REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, target_evidence_id) REFERENCES evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
    CONSTRAINT dream_evidence_target_evaluations_space_generation_check CHECK (space_generation > 0),
    CONSTRAINT dream_evidence_target_evaluations_hash_check CHECK (btrim(target_content_hash) <> ''),
    CONSTRAINT dream_evidence_target_evaluations_pass_check CHECK (pass_number IN (1, 2)),
    CONSTRAINT dream_evidence_target_evaluations_counts_check CHECK (
        provider_turns >= 0 AND provider_input_tokens >= 0 AND provider_output_tokens >= 0
        AND provider_proposals >= 0
        AND accepted_proposals >= 0 AND rejected_proposals >= 0 AND created_hypotheses >= 0
    ),
    CONSTRAINT dream_evidence_target_evaluations_model_check CHECK (btrim(provider_model) <> ''),
    CONSTRAINT dream_evidence_target_evaluations_target_unique
        UNIQUE (team_id, target_evidence_id, target_content_hash, pass_number)
);

CREATE INDEX IF NOT EXISTS dream_evidence_target_evaluations_target_idx
    ON dream_evidence_target_evaluations(team_id, target_evidence_id, created_at DESC, evaluation_id);

CREATE INDEX IF NOT EXISTS dream_evidence_target_evaluations_space_idx
    ON dream_evidence_target_evaluations(team_id, space_id, space_generation, created_at DESC);

DROP TRIGGER IF EXISTS dream_evidence_target_evaluations_append_only ON dream_evidence_target_evaluations;
CREATE TRIGGER dream_evidence_target_evaluations_append_only
    BEFORE UPDATE OR DELETE ON dream_evidence_target_evaluations
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

ALTER TABLE dream_evidence_target_evaluations ENABLE ROW LEVEL SECURITY;
ALTER TABLE dream_evidence_target_evaluations FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS dream_evidence_target_evaluations_select ON dream_evidence_target_evaluations;
CREATE POLICY dream_evidence_target_evaluations_select ON dream_evidence_target_evaluations
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) IN ('team', 'profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

DROP POLICY IF EXISTS dream_evidence_target_evaluations_insert ON dream_evidence_target_evaluations;
CREATE POLICY dream_evidence_target_evaluations_insert ON dream_evidence_target_evaluations
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) = 'profile'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

CREATE TABLE IF NOT EXISTS hypothesis_evidence_derivation_sources (
    team_id UUID NOT NULL,
    derivation_source_id UUID NOT NULL DEFAULT gen_random_uuid(),
    hypothesis_id UUID NOT NULL,
    space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL,
    evidence_id UUID NOT NULL,
    fragment_id UUID NOT NULL,
    source_id UUID NULL,
    source_revision_id UUID NULL,
    source_group_key TEXT NOT NULL,
    span_start INTEGER NOT NULL,
    span_end INTEGER NOT NULL,
    quote TEXT NOT NULL,
    authority TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, derivation_source_id),
    FOREIGN KEY (team_id, hypothesis_id) REFERENCES hypotheses(team_id, hypothesis_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, space_id) REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, evidence_id) REFERENCES evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, fragment_id) REFERENCES evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, source_id) REFERENCES evidence_sources(team_id, source_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, source_revision_id) REFERENCES evidence_source_revisions(team_id, source_revision_id) ON DELETE RESTRICT,
    CONSTRAINT hypothesis_evidence_derivation_space_generation_check CHECK (space_generation > 0),
    CONSTRAINT hypothesis_evidence_derivation_source_pair_check CHECK (
        (source_id IS NULL AND source_revision_id IS NULL)
        OR (source_id IS NOT NULL AND source_revision_id IS NOT NULL)
    ),
    CONSTRAINT hypothesis_evidence_derivation_group_check CHECK (btrim(source_group_key) <> ''),
    CONSTRAINT hypothesis_evidence_derivation_span_check CHECK (span_start >= 0 AND span_end > span_start),
    CONSTRAINT hypothesis_evidence_derivation_quote_check CHECK (btrim(quote) <> ''),
    CONSTRAINT hypothesis_evidence_derivation_authority_check CHECK (
        authority IN ('authoritative', 'primary', 'secondary', 'inferred', 'unknown')
    ),
    CONSTRAINT hypothesis_evidence_derivation_unique UNIQUE (
        team_id, hypothesis_id, evidence_id, span_start, span_end
    )
);

CREATE INDEX IF NOT EXISTS hypothesis_evidence_derivation_hypothesis_idx
    ON hypothesis_evidence_derivation_sources(team_id, hypothesis_id, created_at, derivation_source_id);

CREATE INDEX IF NOT EXISTS hypothesis_evidence_derivation_evidence_idx
    ON hypothesis_evidence_derivation_sources(team_id, evidence_id, created_at, derivation_source_id);

DROP TRIGGER IF EXISTS hypothesis_evidence_derivation_sources_append_only ON hypothesis_evidence_derivation_sources;
CREATE TRIGGER hypothesis_evidence_derivation_sources_append_only
    BEFORE UPDATE OR DELETE ON hypothesis_evidence_derivation_sources
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

ALTER TABLE hypothesis_evidence_derivation_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE hypothesis_evidence_derivation_sources FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS hypothesis_evidence_derivation_sources_select ON hypothesis_evidence_derivation_sources;
CREATE POLICY hypothesis_evidence_derivation_sources_select ON hypothesis_evidence_derivation_sources
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) IN ('team', 'profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

DROP POLICY IF EXISTS hypothesis_evidence_derivation_sources_insert ON hypothesis_evidence_derivation_sources;
CREATE POLICY hypothesis_evidence_derivation_sources_insert ON hypothesis_evidence_derivation_sources
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) = 'profile'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

RESET lock_timeout;
RESET app.allowed_space_ids;
RESET app.current_profile_id;
RESET app.current_team_id;
RESET app.tx_mode;

-- +goose Down

SET app.tx_mode = 'migration';
SET app.current_team_id = '';
SET app.current_profile_id = '';
SET app.allowed_space_ids = '';
SET lock_timeout = '30s';

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM dream_evidence_target_attempts)
       OR EXISTS (SELECT 1 FROM dream_evidence_target_evaluations)
       OR EXISTS (SELECT 1 FROM hypothesis_evidence_derivation_sources)
       OR EXISTS (SELECT 1 FROM dream_cycle_runs WHERE lane = 'evidence_discovery')
       OR EXISTS (SELECT 1 FROM hypotheses WHERE lane = 'evidence_discovery') THEN
        RAISE EXCEPTION 'cannot roll back hourly evidence Dreams: append-only history exists';
    END IF;
END $$;
-- +goose StatementEnd

DROP TABLE IF EXISTS hypothesis_evidence_derivation_sources;
DROP TABLE IF EXISTS dream_evidence_target_evaluations;
DROP INDEX IF EXISTS dream_evidence_target_attempts_target_idx;
DROP TABLE IF EXISTS dream_evidence_target_attempts;
DROP INDEX IF EXISTS dream_cycle_runs_team_lane_started_idx;
DROP INDEX IF EXISTS dream_cycle_runs_team_lane_window_canonical_unique;
CREATE UNIQUE INDEX IF NOT EXISTS dream_cycle_runs_team_window_canonical_unique
    ON dream_cycle_runs(team_id, window_key)
    WHERE canonical_run_id IS NULL;
ALTER TABLE dream_cycle_runs
    DROP CONSTRAINT IF EXISTS dream_cycle_runs_lane_check,
    DROP CONSTRAINT IF EXISTS dream_cycle_runs_evidence_counts_check,
    DROP COLUMN IF EXISTS lane,
    DROP COLUMN IF EXISTS evidence_targets,
    DROP COLUMN IF EXISTS evaluated_evidence_targets;
ALTER TABLE hypotheses
    DROP CONSTRAINT IF EXISTS hypotheses_lane_check,
    DROP COLUMN IF EXISTS lane;

RESET lock_timeout;
RESET app.allowed_space_ids;
RESET app.current_profile_id;
RESET app.current_team_id;
RESET app.tx_mode;
