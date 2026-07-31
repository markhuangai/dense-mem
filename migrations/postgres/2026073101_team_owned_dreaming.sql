-- +goose Up
-- +goose StatementBegin

-- This migration takes ACCESS EXCLUSIVE locks while it rewrites the two
-- ownership columns. Apply it during a maintenance window on installations
-- with a large dream history. Canonical aliases preserve duplicate legacy rows
-- instead of deleting their provenance.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

-- A legacy force-enable was effective even when the global enabled value was
-- false. Preserve that effective state before global disable becomes absolute.
UPDATE app_config
SET value = 'true',
    updated_at = clock_timestamp()
WHERE key = 'DREAMING_ENABLED'
  AND lower(btrim(value)) IN ('', 'false')
  AND EXISTS (
      SELECT 1
      FROM app_config force_config
      WHERE force_config.key = 'DREAMING_FORCE_ENABLED'
        AND lower(btrim(force_config.value)) = 'true'
  );

DELETE FROM app_config
WHERE key IN (
    'DREAMING_REFLECT_ENABLED',
    'DREAMING_REEVALUATE_ENABLED',
    'DREAMING_DREAM_ENABLED'
);

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

DROP POLICY IF EXISTS hypotheses_insert ON hypotheses;
DROP POLICY IF EXISTS hypotheses_update ON hypotheses;
DROP POLICY IF EXISTS hypotheses_select ON hypotheses;
DROP INDEX IF EXISTS hypotheses_content_hash_unique;
DROP INDEX IF EXISTS hypotheses_related_active_idx;
ALTER TABLE hypotheses DROP CONSTRAINT IF EXISTS hypotheses_team_id_owner_profile_id_fkey;
ALTER TABLE hypotheses RENAME COLUMN owner_profile_id TO created_by_profile_id;
ALTER TABLE hypotheses ALTER COLUMN created_by_profile_id DROP NOT NULL;
ALTER TABLE hypotheses
    ADD CONSTRAINT hypotheses_team_id_created_by_profile_id_fkey
    FOREIGN KEY (team_id, created_by_profile_id)
    REFERENCES semantic_profile_refs(team_id, profile_id)
    ON DELETE RESTRICT;
ALTER TABLE hypotheses
    ADD COLUMN IF NOT EXISTS canonical_hypothesis_id UUID NULL;

DROP POLICY IF EXISTS dream_cycle_runs_insert ON dream_cycle_runs;
DROP POLICY IF EXISTS dream_cycle_runs_update ON dream_cycle_runs;
DROP POLICY IF EXISTS dream_cycle_runs_select ON dream_cycle_runs;
ALTER TABLE dream_cycle_runs DROP CONSTRAINT IF EXISTS dream_cycle_runs_window_unique;
DROP INDEX IF EXISTS dream_cycle_runs_due_idx;
ALTER TABLE dream_cycle_runs DROP CONSTRAINT IF EXISTS dream_cycle_runs_team_id_owner_profile_id_fkey;
ALTER TABLE dream_cycle_runs RENAME COLUMN owner_profile_id TO initiated_by_profile_id;
ALTER TABLE dream_cycle_runs ALTER COLUMN initiated_by_profile_id DROP NOT NULL;
ALTER TABLE dream_cycle_runs
    ADD CONSTRAINT dream_cycle_runs_team_id_initiated_by_profile_id_fkey
    FOREIGN KEY (team_id, initiated_by_profile_id)
    REFERENCES semantic_profile_refs(team_id, profile_id)
    ON DELETE RESTRICT;
ALTER TABLE dream_cycle_runs
    ADD COLUMN IF NOT EXISTS canonical_run_id UUID NULL;
ALTER TABLE dream_cycle_runs DROP CONSTRAINT IF EXISTS dream_cycle_runs_status_check;
ALTER TABLE dream_cycle_runs
    ADD CONSTRAINT dream_cycle_runs_status_check
    CHECK (status IN ('running', 'completed', 'failed', 'skipped', 'cancelled', 'missed'));

-- One canonical team run represents every historical per-profile attempt for
-- a time window. Existing hypothesis references move to that canonical run.
WITH ranked AS (
    SELECT team_id,
           run_id,
           first_value(run_id) OVER (
               PARTITION BY team_id, window_key
               ORDER BY CASE status
                            WHEN 'completed' THEN 0
                            WHEN 'running' THEN 1
                            WHEN 'failed' THEN 2
                            WHEN 'skipped' THEN 3
                            WHEN 'cancelled' THEN 4
                            ELSE 5
                        END,
                        completed_at DESC NULLS LAST,
                        updated_at DESC,
                        run_id
           ) AS canonical_run_id
    FROM dream_cycle_runs
), aliases AS (
    SELECT team_id, run_id, canonical_run_id
    FROM ranked
    WHERE run_id <> canonical_run_id
)
UPDATE dream_cycle_runs run
SET canonical_run_id = aliases.canonical_run_id,
    updated_at = clock_timestamp()
FROM aliases
WHERE run.team_id = aliases.team_id
  AND run.run_id = aliases.run_id;

UPDATE hypotheses hypothesis
SET cycle_run_id = run.canonical_run_id,
    updated_at = clock_timestamp()
FROM dream_cycle_runs run
WHERE hypothesis.team_id = run.team_id
  AND hypothesis.cycle_run_id = run.run_id
  AND run.canonical_run_id IS NOT NULL;

ALTER TABLE dream_cycle_runs
    ADD CONSTRAINT dream_cycle_runs_canonical_run_fk
    FOREIGN KEY (team_id, canonical_run_id)
    REFERENCES dream_cycle_runs(team_id, run_id)
    ON DELETE RESTRICT;

-- Canonicalize same-team duplicate hypotheses. The canonical row keeps the
-- strongest lifecycle state and merges source provenance from aliases.
WITH ranked AS (
    SELECT team_id,
           hypothesis_id,
           first_value(hypothesis_id) OVER (
               PARTITION BY team_id, content_hash
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
    WHERE content_hash IS NOT NULL
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

WITH grouped AS (
    SELECT team_id,
           content_hash,
           (array_agg(hypothesis_id ORDER BY hypothesis_id)
                FILTER (WHERE canonical_hypothesis_id IS NULL))[1] AS hypothesis_id
    FROM hypotheses
    WHERE content_hash IS NOT NULL
    GROUP BY team_id, content_hash
), source_refs AS (
    SELECT hypothesis.team_id,
           hypothesis.content_hash,
           jsonb_agg(DISTINCT ref.value) AS value
    FROM hypotheses hypothesis
    CROSS JOIN LATERAL jsonb_array_elements(hypothesis.source_refs) AS ref(value)
    WHERE hypothesis.content_hash IS NOT NULL
    GROUP BY hypothesis.team_id, hypothesis.content_hash
), source_versions_by_key AS (
    SELECT hypothesis.team_id,
           hypothesis.content_hash,
           source.key,
           max(CASE WHEN source.value ~ '^-?[0-9]+$' THEN source.value::integer ELSE 0 END) AS version
    FROM hypotheses hypothesis
    CROSS JOIN LATERAL jsonb_each_text(hypothesis.source_versions) AS source(key, value)
    WHERE hypothesis.content_hash IS NOT NULL
    GROUP BY hypothesis.team_id, hypothesis.content_hash, source.key
), source_versions AS (
    SELECT team_id,
           content_hash,
           jsonb_object_agg(key, to_jsonb(version)) AS value
    FROM source_versions_by_key
    GROUP BY team_id, content_hash
), source_owners AS (
    SELECT hypothesis.team_id,
           hypothesis.content_hash,
           array_agg(DISTINCT owner.profile_id) AS value
    FROM hypotheses hypothesis
    CROSS JOIN LATERAL unnest(hypothesis.source_owner_profile_ids) AS owner(profile_id)
    WHERE hypothesis.content_hash IS NOT NULL
    GROUP BY hypothesis.team_id, hypothesis.content_hash
)
UPDATE hypotheses hypothesis
SET source_refs = COALESCE(source_refs.value, '[]'::jsonb),
    source_versions = COALESCE(source_versions.value, '{}'::jsonb),
    source_owner_profile_ids = COALESCE(source_owners.value, ARRAY[]::uuid[]),
    updated_at = clock_timestamp()
FROM grouped
LEFT JOIN source_refs
  ON source_refs.team_id = grouped.team_id
 AND source_refs.content_hash = grouped.content_hash
LEFT JOIN source_versions
  ON source_versions.team_id = grouped.team_id
 AND source_versions.content_hash = grouped.content_hash
LEFT JOIN source_owners
  ON source_owners.team_id = grouped.team_id
 AND source_owners.content_hash = grouped.content_hash
WHERE hypothesis.team_id = grouped.team_id
  AND hypothesis.hypothesis_id = grouped.hypothesis_id;

ALTER TABLE hypotheses
    ADD CONSTRAINT hypotheses_canonical_hypothesis_fk
    FOREIGN KEY (team_id, canonical_hypothesis_id)
    REFERENCES hypotheses(team_id, hypothesis_id)
    ON DELETE RESTRICT;

CREATE UNIQUE INDEX hypotheses_team_content_hash_canonical_unique
    ON hypotheses(team_id, content_hash)
    WHERE content_hash IS NOT NULL
      AND canonical_hypothesis_id IS NULL;

CREATE INDEX hypotheses_related_active_idx
    ON hypotheses(team_id, status, updated_at DESC)
    WHERE canonical_hypothesis_id IS NULL
      AND status IN ('proposed', 'reinforced');

CREATE UNIQUE INDEX dream_cycle_runs_team_window_canonical_unique
    ON dream_cycle_runs(team_id, window_key)
    WHERE canonical_run_id IS NULL;

CREATE INDEX dream_cycle_runs_due_idx
    ON dream_cycle_runs(team_id, started_at DESC)
    WHERE canonical_run_id IS NULL;

CREATE TABLE IF NOT EXISTS hypothesis_feedback_events (
    team_id UUID NOT NULL,
    feedback_event_id UUID NOT NULL DEFAULT gen_random_uuid(),
    hypothesis_id UUID NOT NULL,
    actor_profile_id UUID NOT NULL,
    decision TEXT NOT NULL,
    feedback TEXT NOT NULL DEFAULT '',
    submitted_ingest_id UUID NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, feedback_event_id),
    FOREIGN KEY (team_id, hypothesis_id)
        REFERENCES hypotheses(team_id, hypothesis_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, actor_profile_id)
        REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, submitted_ingest_id)
        REFERENCES knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT,
    CONSTRAINT hypothesis_feedback_events_decision_check CHECK (
        decision IN ('reject', 'stale', 'reinforce', 'confirm_true', 'confirm_false', 'promote_candidate')
    )
);

CREATE INDEX hypothesis_feedback_events_hypothesis_created_idx
    ON hypothesis_feedback_events(team_id, hypothesis_id, created_at DESC);

ALTER TABLE hypothesis_feedback_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE hypothesis_feedback_events FORCE ROW LEVEL SECURITY;

CREATE POLICY hypothesis_feedback_events_select ON hypothesis_feedback_events FOR SELECT USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) IN ('team', 'profile')
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
);

CREATE POLICY hypothesis_feedback_events_insert ON hypothesis_feedback_events FOR INSERT WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        AND actor_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
    )
);

CREATE POLICY hypotheses_select ON hypotheses FOR SELECT USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) IN ('team', 'profile')
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
);

CREATE POLICY hypotheses_insert ON hypotheses FOR INSERT WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        AND created_by_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
    )
);

CREATE POLICY hypotheses_update ON hypotheses FOR UPDATE USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
) WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
);

CREATE POLICY dream_cycle_runs_select ON dream_cycle_runs FOR SELECT USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) IN ('team', 'profile')
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
);

CREATE POLICY dream_cycle_runs_insert ON dream_cycle_runs FOR INSERT WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        AND initiated_by_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
    )
);

CREATE POLICY dream_cycle_runs_update ON dream_cycle_runs FOR UPDATE USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        AND initiated_by_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
    )
) WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        AND initiated_by_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
    )
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Irreversible boundary: canonical aliases merge team-visible legacy records
-- and append feedback provenance. A down migration cannot safely recreate the
-- old per-profile duplicates or remove scheduled rows with no profile owner.
DO $$
BEGIN
    RAISE EXCEPTION '2026073101_team_owned_dreaming is irreversible';
END $$;

-- +goose StatementEnd
