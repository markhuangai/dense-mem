-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite analysis:
-- - Historical Hypotheses are retained. Rows that represent the same durable
--   target become canonical aliases; no provenance row is deleted.
-- - The target-identity backfill and unique indexes scan the Hypothesis table.
--   Apply during a maintenance window for installations with large history.
-- - New derivation and path-evaluation tables are append-only and RLS-bound.
-- - The down migration is intentionally irreversible once those histories exist.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE hypotheses
    ADD COLUMN IF NOT EXISTS target_identity TEXT NULL;

UPDATE hypotheses
SET target_identity = 'sha256:' || encode(
        digest(
            convert_to(team_id::text, 'UTF8') || decode('00', 'hex') ||
            convert_to(subject_entity_id::text, 'UTF8') || decode('00', 'hex') ||
            convert_to(lower(btrim(predicate_key)), 'UTF8') || decode('00', 'hex') ||
            convert_to(
                CASE
                    WHEN object_entity_id IS NOT NULL THEN 'entity:' || object_entity_id::text
                    ELSE 'value:' || object_value_id::text
                END,
                'UTF8'
            ),
            'sha256'
        ),
        'hex'
    ),
    updated_at = clock_timestamp()
WHERE target_identity IS NULL
  AND subject_entity_id IS NOT NULL
  AND btrim(COALESCE(predicate_key, '')) <> ''
  AND ((object_entity_id IS NULL) <> (object_value_id IS NULL));

WITH ranked AS (
    SELECT team_id,
           hypothesis_id,
           first_value(hypothesis_id) OVER (
               PARTITION BY team_id, target_identity
               ORDER BY CASE status
                            WHEN 'submitted' THEN 0
                            WHEN 'reinforced' THEN 1
                            WHEN 'proposed' THEN 2
                            WHEN 'stale' THEN 3
                            WHEN 'rejected' THEN 4
                            ELSE 5
                        END,
                        updated_at DESC,
                        hypothesis_id
           ) AS canonical_hypothesis_id
    FROM hypotheses
    WHERE canonical_hypothesis_id IS NULL
      AND target_identity IS NOT NULL
), aliases AS (
    SELECT team_id, hypothesis_id, canonical_hypothesis_id
    FROM ranked
    WHERE hypothesis_id <> canonical_hypothesis_id
)
UPDATE hypotheses hypothesis
SET canonical_hypothesis_id = aliases.canonical_hypothesis_id,
    updated_at = clock_timestamp()
FROM aliases
WHERE hypothesis.team_id = aliases.team_id
  AND hypothesis.hypothesis_id = aliases.hypothesis_id;

CREATE UNIQUE INDEX IF NOT EXISTS hypotheses_team_target_identity_canonical_unique
    ON hypotheses(team_id, target_identity)
    WHERE target_identity IS NOT NULL
      AND canonical_hypothesis_id IS NULL;

CREATE TABLE IF NOT EXISTS hypothesis_derivation_sources (
    team_id UUID NOT NULL,
    derivation_source_id UUID NOT NULL DEFAULT gen_random_uuid(),
    hypothesis_id UUID NOT NULL,
    premise_position SMALLINT NOT NULL,
    relationship_id UUID NOT NULL,
    relationship_version INTEGER NOT NULL,
    support_id UUID NULL,
    observation_id UUID NULL,
    fragment_id UUID NOT NULL,
    source_id UUID NULL,
    source_revision_id UUID NULL,
    source_group_key TEXT NOT NULL DEFAULT '',
    span_start INTEGER NOT NULL,
    span_end INTEGER NOT NULL,
    quote TEXT NOT NULL,
    authority TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, derivation_source_id),
    FOREIGN KEY (team_id, hypothesis_id)
        REFERENCES hypotheses(team_id, hypothesis_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, relationship_id)
        REFERENCES relationship_records(team_id, relationship_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, support_id)
        REFERENCES relationship_evidence_supports(team_id, support_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, observation_id)
        REFERENCES relationship_observations(team_id, observation_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, fragment_id)
        REFERENCES evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, source_id)
        REFERENCES evidence_sources(team_id, source_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, source_revision_id)
        REFERENCES evidence_source_revisions(team_id, source_revision_id) ON DELETE RESTRICT,
    CONSTRAINT hypothesis_derivation_sources_premise_check
        CHECK (premise_position IN (1, 2)),
    CONSTRAINT hypothesis_derivation_sources_version_check
        CHECK (relationship_version >= 1),
    CONSTRAINT hypothesis_derivation_sources_origin_check
        CHECK ((support_id IS NULL) <> (observation_id IS NULL)),
    CONSTRAINT hypothesis_derivation_sources_source_revision_pair_check
        CHECK ((source_id IS NULL) = (source_revision_id IS NULL)),
    CONSTRAINT hypothesis_derivation_sources_group_nonempty
        CHECK (btrim(source_group_key) <> ''),
    CONSTRAINT hypothesis_derivation_sources_span_check
        CHECK (span_start >= 0 AND span_end > span_start),
    CONSTRAINT hypothesis_derivation_sources_quote_nonempty
        CHECK (btrim(quote) <> ''),
    CONSTRAINT hypothesis_derivation_sources_authority_check
        CHECK (authority IN ('authoritative', 'primary', 'secondary', 'inferred', 'unknown')),
    CONSTRAINT hypothesis_derivation_sources_unique
        UNIQUE (team_id, hypothesis_id, relationship_id, relationship_version,
                fragment_id, span_start, span_end)
);

CREATE INDEX IF NOT EXISTS hypothesis_derivation_sources_hypothesis_idx
    ON hypothesis_derivation_sources(team_id, hypothesis_id, premise_position, created_at);

DROP TRIGGER IF EXISTS hypothesis_derivation_sources_append_only ON hypothesis_derivation_sources;
CREATE TRIGGER hypothesis_derivation_sources_append_only
    BEFORE UPDATE OR DELETE ON hypothesis_derivation_sources
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

CREATE TABLE IF NOT EXISTS dream_path_evaluations (
    team_id UUID NOT NULL,
    path_evaluation_id UUID NOT NULL DEFAULT gen_random_uuid(),
    first_relationship_id UUID NOT NULL,
    first_relationship_version INTEGER NOT NULL,
    second_relationship_id UUID NOT NULL,
    second_relationship_version INTEGER NOT NULL,
    provider_model TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, path_evaluation_id),
    FOREIGN KEY (team_id, first_relationship_id)
        REFERENCES relationship_records(team_id, relationship_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, second_relationship_id)
        REFERENCES relationship_records(team_id, relationship_id) ON DELETE RESTRICT,
    CONSTRAINT dream_path_evaluations_versions_check
        CHECK (first_relationship_version >= 1 AND second_relationship_version >= 1),
    CONSTRAINT dream_path_evaluations_distinct_relationships_check
        CHECK (first_relationship_id <> second_relationship_id),
    CONSTRAINT dream_path_evaluations_model_nonempty
        CHECK (btrim(provider_model) <> ''),
    CONSTRAINT dream_path_evaluations_exact_path_unique
        UNIQUE (team_id, first_relationship_id, first_relationship_version,
                second_relationship_id, second_relationship_version)
);

CREATE INDEX IF NOT EXISTS dream_path_evaluations_first_relationship_idx
    ON dream_path_evaluations(team_id, first_relationship_id, first_relationship_version);

DROP TRIGGER IF EXISTS dream_path_evaluations_append_only ON dream_path_evaluations;
CREATE TRIGGER dream_path_evaluations_append_only
    BEFORE UPDATE OR DELETE ON dream_path_evaluations
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

ALTER TABLE hypothesis_derivation_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE hypothesis_derivation_sources FORCE ROW LEVEL SECURITY;
ALTER TABLE dream_path_evaluations ENABLE ROW LEVEL SECURITY;
ALTER TABLE dream_path_evaluations FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS hypothesis_derivation_sources_select ON hypothesis_derivation_sources;
CREATE POLICY hypothesis_derivation_sources_select ON hypothesis_derivation_sources
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) IN ('team', 'profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

DROP POLICY IF EXISTS hypothesis_derivation_sources_insert ON hypothesis_derivation_sources;
CREATE POLICY hypothesis_derivation_sources_insert ON hypothesis_derivation_sources
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) IN ('profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

DROP POLICY IF EXISTS dream_path_evaluations_select ON dream_path_evaluations;
CREATE POLICY dream_path_evaluations_select ON dream_path_evaluations
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) IN ('team', 'profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

DROP POLICY IF EXISTS dream_path_evaluations_insert ON dream_path_evaluations;
CREATE POLICY dream_path_evaluations_insert ON dream_path_evaluations
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) IN ('profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

ALTER TABLE dream_cycle_runs
    ADD COLUMN IF NOT EXISTS scheduled_for TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS lease_token UUID NULL,
    ADD COLUMN IF NOT EXISTS attempt_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS provider_model TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS provider_turns INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS provider_input_tokens INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS provider_output_tokens INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS attempted_paths INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS provider_proposals INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS outcome_summary JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE dream_cycle_runs
    DROP CONSTRAINT IF EXISTS dream_cycle_runs_status_check;
ALTER TABLE dream_cycle_runs
    ADD CONSTRAINT dream_cycle_runs_status_check
    CHECK (status IN ('queued', 'running', 'completed', 'failed', 'skipped', 'cancelled', 'missed'));

ALTER TABLE dream_cycle_runs
    DROP CONSTRAINT IF EXISTS dream_cycle_runs_attempt_count_check;
ALTER TABLE dream_cycle_runs
    ADD CONSTRAINT dream_cycle_runs_attempt_count_check CHECK (attempt_count >= 0);

ALTER TABLE dream_cycle_runs
    DROP CONSTRAINT IF EXISTS dream_cycle_runs_provider_counts_check;
ALTER TABLE dream_cycle_runs
    ADD CONSTRAINT dream_cycle_runs_provider_counts_check CHECK (
        provider_turns >= 0
        AND provider_input_tokens >= 0
        AND provider_output_tokens >= 0
        AND attempted_paths >= 0
        AND provider_proposals >= 0
    );

ALTER TABLE dream_cycle_runs
    DROP CONSTRAINT IF EXISTS dream_cycle_runs_outcome_summary_object_check;
ALTER TABLE dream_cycle_runs
    ADD CONSTRAINT dream_cycle_runs_outcome_summary_object_check
    CHECK (jsonb_typeof(outcome_summary) = 'object');

CREATE INDEX IF NOT EXISTS dream_cycle_runs_recovery_idx
    ON dream_cycle_runs(team_id, lease_until, started_at)
    WHERE canonical_run_id IS NULL
      AND status = 'running';

CREATE OR REPLACE FUNCTION hypotheses_guard_provenance()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF current_setting('app.tx_mode', true) IN ('system', 'migration') THEN
        RETURN NEW;
    END IF;
    IF NEW.team_id IS DISTINCT FROM OLD.team_id
       OR NEW.created_by_profile_id IS DISTINCT FROM OLD.created_by_profile_id
       OR NEW.canonical_hypothesis_id IS DISTINCT FROM OLD.canonical_hypothesis_id
       OR NEW.content_hash IS DISTINCT FROM OLD.content_hash
       OR NEW.target_identity IS DISTINCT FROM OLD.target_identity THEN
        RAISE EXCEPTION 'hypothesis provenance columns are immutable';
    END IF;
    RETURN NEW;
END;
$$;

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $$
BEGIN
    RAISE EXCEPTION '2026080103_evidence_grounded_dreaming is irreversible';
END $$;

-- +goose StatementEnd
