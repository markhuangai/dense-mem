-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE hypotheses DROP CONSTRAINT IF EXISTS hypotheses_status_check;

UPDATE hypotheses
SET status = CASE status
    WHEN 'candidate' THEN 'proposed'
    WHEN 'promoted_candidate' THEN 'submitted'
    ELSE status
END
WHERE status IN ('candidate', 'promoted_candidate');

ALTER TABLE hypotheses
    ALTER COLUMN status SET DEFAULT 'proposed',
    ADD COLUMN IF NOT EXISTS statement TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS rationale TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS likelihood NUMERIC(5,4) NULL,
    ADD COLUMN IF NOT EXISTS confidence NUMERIC(5,4) NULL,
    ADD COLUMN IF NOT EXISTS subject_entity_id UUID NULL,
    ADD COLUMN IF NOT EXISTS predicate_key TEXT NULL,
    ADD COLUMN IF NOT EXISTS predicate_version INTEGER NULL,
    ADD COLUMN IF NOT EXISTS object_entity_id UUID NULL,
    ADD COLUMN IF NOT EXISTS object_value_id UUID NULL,
    ADD COLUMN IF NOT EXISTS source_refs JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS source_versions JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS source_owner_profile_ids UUID[] NOT NULL DEFAULT ARRAY[]::uuid[],
    ADD COLUMN IF NOT EXISTS content_hash TEXT NULL,
    ADD COLUMN IF NOT EXISTS cycle_run_id UUID NULL,
    ADD COLUMN IF NOT EXISTS generator_kind TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS generator_version TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS invalidated_reason TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS submitted_ingest_id UUID NULL,
    ADD COLUMN IF NOT EXISTS submitted_at TIMESTAMPTZ NULL;

UPDATE hypotheses
SET statement = COALESCE(NULLIF(statement, ''), payload->>'statement', payload->>'text', ''),
    rationale = COALESCE(NULLIF(rationale, ''), payload->>'rationale', ''),
    source_refs = CASE
        WHEN jsonb_typeof(payload->'source_refs') = 'array' THEN payload->'source_refs'
        ELSE source_refs
    END,
    source_versions = CASE
        WHEN jsonb_typeof(payload->'source_versions') = 'object' THEN payload->'source_versions'
        ELSE source_versions
    END
WHERE payload <> '{}'::jsonb;

ALTER TABLE hypotheses
    ADD CONSTRAINT hypotheses_status_check CHECK (status IN ('proposed', 'reinforced', 'stale', 'rejected', 'submitted')),
    ADD CONSTRAINT hypotheses_statement_nonempty_when_sourced CHECK (
        statement <> '' OR content_hash IS NULL
    ),
    ADD CONSTRAINT hypotheses_probability_check CHECK (
        (likelihood IS NULL OR (likelihood >= 0 AND likelihood <= 1))
        AND (confidence IS NULL OR (confidence >= 0 AND confidence <= 1))
    ),
    ADD CONSTRAINT hypotheses_endpoint_choice_check CHECK (
        (object_entity_id IS NULL AND object_value_id IS NULL)
        OR (object_entity_id IS NULL) <> (object_value_id IS NULL)
    ),
    ADD CONSTRAINT hypotheses_source_refs_array_check CHECK (jsonb_typeof(source_refs) = 'array'),
    ADD CONSTRAINT hypotheses_source_versions_object_check CHECK (jsonb_typeof(source_versions) = 'object');

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.table_constraints
        WHERE constraint_schema = 'public'
          AND table_name = 'hypotheses'
          AND constraint_name = 'hypotheses_subject_fk'
    ) THEN
        ALTER TABLE hypotheses
            ADD CONSTRAINT hypotheses_subject_fk
            FOREIGN KEY (team_id, subject_entity_id)
            REFERENCES entity_records(team_id, entity_id)
            ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.table_constraints
        WHERE constraint_schema = 'public'
          AND table_name = 'hypotheses'
          AND constraint_name = 'hypotheses_object_entity_fk'
    ) THEN
        ALTER TABLE hypotheses
            ADD CONSTRAINT hypotheses_object_entity_fk
            FOREIGN KEY (team_id, object_entity_id)
            REFERENCES entity_records(team_id, entity_id)
            ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.table_constraints
        WHERE constraint_schema = 'public'
          AND table_name = 'hypotheses'
          AND constraint_name = 'hypotheses_object_value_fk'
    ) THEN
        ALTER TABLE hypotheses
            ADD CONSTRAINT hypotheses_object_value_fk
            FOREIGN KEY (team_id, object_value_id)
            REFERENCES value_records(team_id, value_id)
            ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.table_constraints
        WHERE constraint_schema = 'public'
          AND table_name = 'hypotheses'
          AND constraint_name = 'hypotheses_predicate_fk'
    ) THEN
        ALTER TABLE hypotheses
            ADD CONSTRAINT hypotheses_predicate_fk
            FOREIGN KEY (predicate_key, predicate_version)
            REFERENCES predicate_definitions(predicate_key, version)
            ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.table_constraints
        WHERE constraint_schema = 'public'
          AND table_name = 'hypotheses'
          AND constraint_name = 'hypotheses_submitted_ingest_fk'
    ) THEN
        ALTER TABLE hypotheses
            ADD CONSTRAINT hypotheses_submitted_ingest_fk
            FOREIGN KEY (team_id, submitted_ingest_id)
            REFERENCES knowledge_ingests(team_id, ingest_id)
            ON DELETE RESTRICT;
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS hypotheses_content_hash_unique
    ON hypotheses(team_id, owner_profile_id, content_hash)
    WHERE content_hash IS NOT NULL;

CREATE INDEX IF NOT EXISTS hypotheses_related_active_idx
    ON hypotheses(team_id, status, updated_at DESC)
    WHERE status IN ('proposed', 'reinforced');

CREATE TABLE IF NOT EXISTS dream_cycle_runs (
    team_id UUID NOT NULL,
    run_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    run_date TEXT NOT NULL,
    window_key TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running',
    lease_until TIMESTAMPTZ NULL,
    input_count INTEGER NOT NULL DEFAULT 0,
    created_hypotheses INTEGER NOT NULL DEFAULT 0,
    rejected_hypotheses INTEGER NOT NULL DEFAULT 0,
    source_snapshot JSONB NOT NULL DEFAULT '[]'::jsonb,
    error TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, run_id),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT dream_cycle_runs_window_nonempty CHECK (btrim(window_key) <> ''),
    CONSTRAINT dream_cycle_runs_status_check CHECK (status IN ('running', 'completed', 'failed', 'skipped', 'cancelled')),
    CONSTRAINT dream_cycle_runs_counts_check CHECK (
        input_count >= 0 AND created_hypotheses >= 0 AND rejected_hypotheses >= 0
    ),
    CONSTRAINT dream_cycle_runs_snapshot_array_check CHECK (jsonb_typeof(source_snapshot) = 'array'),
    CONSTRAINT dream_cycle_runs_window_unique UNIQUE (team_id, owner_profile_id, window_key)
);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.table_constraints
        WHERE constraint_schema = 'public'
          AND table_name = 'hypotheses'
          AND constraint_name = 'hypotheses_cycle_run_fk'
    ) THEN
        ALTER TABLE hypotheses
            ADD CONSTRAINT hypotheses_cycle_run_fk
            FOREIGN KEY (team_id, cycle_run_id)
            REFERENCES dream_cycle_runs(team_id, run_id)
            ON DELETE RESTRICT;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS dream_cycle_runs_due_idx
    ON dream_cycle_runs(team_id, owner_profile_id, started_at DESC);

ALTER TABLE dream_cycle_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE dream_cycle_runs FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS dream_cycle_runs_select ON dream_cycle_runs;
CREATE POLICY dream_cycle_runs_select ON dream_cycle_runs FOR SELECT USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) IN ('team', 'profile')
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
);

DROP POLICY IF EXISTS dream_cycle_runs_insert ON dream_cycle_runs;
CREATE POLICY dream_cycle_runs_insert ON dream_cycle_runs FOR INSERT WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'team'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        AND owner_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
    )
);

DROP POLICY IF EXISTS dream_cycle_runs_update ON dream_cycle_runs;
CREATE POLICY dream_cycle_runs_update ON dream_cycle_runs FOR UPDATE USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        AND owner_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
    )
) WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        AND owner_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
    )
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE hypotheses DROP CONSTRAINT IF EXISTS hypotheses_cycle_run_fk;
DROP TABLE IF EXISTS dream_cycle_runs;

DROP INDEX IF EXISTS hypotheses_related_active_idx;
DROP INDEX IF EXISTS hypotheses_content_hash_unique;

ALTER TABLE hypotheses DROP CONSTRAINT IF EXISTS hypotheses_submitted_ingest_fk;
ALTER TABLE hypotheses DROP CONSTRAINT IF EXISTS hypotheses_predicate_fk;
ALTER TABLE hypotheses DROP CONSTRAINT IF EXISTS hypotheses_object_value_fk;
ALTER TABLE hypotheses DROP CONSTRAINT IF EXISTS hypotheses_object_entity_fk;
ALTER TABLE hypotheses DROP CONSTRAINT IF EXISTS hypotheses_subject_fk;
ALTER TABLE hypotheses DROP CONSTRAINT IF EXISTS hypotheses_source_versions_object_check;
ALTER TABLE hypotheses DROP CONSTRAINT IF EXISTS hypotheses_source_refs_array_check;
ALTER TABLE hypotheses DROP CONSTRAINT IF EXISTS hypotheses_endpoint_choice_check;
ALTER TABLE hypotheses DROP CONSTRAINT IF EXISTS hypotheses_probability_check;
ALTER TABLE hypotheses DROP CONSTRAINT IF EXISTS hypotheses_statement_nonempty_when_sourced;
ALTER TABLE hypotheses DROP CONSTRAINT IF EXISTS hypotheses_status_check;

UPDATE hypotheses
SET status = CASE status
    WHEN 'proposed' THEN 'candidate'
    WHEN 'submitted' THEN 'promoted_candidate'
    ELSE status
END
WHERE status IN ('proposed', 'submitted');

ALTER TABLE hypotheses
    ALTER COLUMN status SET DEFAULT 'candidate',
    DROP COLUMN IF EXISTS statement,
    DROP COLUMN IF EXISTS rationale,
    DROP COLUMN IF EXISTS likelihood,
    DROP COLUMN IF EXISTS confidence,
    DROP COLUMN IF EXISTS subject_entity_id,
    DROP COLUMN IF EXISTS predicate_key,
    DROP COLUMN IF EXISTS predicate_version,
    DROP COLUMN IF EXISTS object_entity_id,
    DROP COLUMN IF EXISTS object_value_id,
    DROP COLUMN IF EXISTS source_refs,
    DROP COLUMN IF EXISTS source_versions,
    DROP COLUMN IF EXISTS source_owner_profile_ids,
    DROP COLUMN IF EXISTS content_hash,
    DROP COLUMN IF EXISTS cycle_run_id,
    DROP COLUMN IF EXISTS generator_kind,
    DROP COLUMN IF EXISTS generator_version,
    DROP COLUMN IF EXISTS invalidated_reason,
    DROP COLUMN IF EXISTS submitted_ingest_id,
    DROP COLUMN IF EXISTS submitted_at;

ALTER TABLE hypotheses
    ADD CONSTRAINT hypotheses_status_check CHECK (status IN ('candidate', 'reinforced', 'rejected', 'promoted_candidate', 'stale'));

-- +goose StatementEnd
