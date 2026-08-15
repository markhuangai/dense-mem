-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS entity_correction_plans (
    team_id UUID NOT NULL,
    plan_token UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    action TEXT NOT NULL,
    source_entity_id UUID NOT NULL,
    target_entity_id UUID NULL,
    new_entity_id UUID NULL,
    selected_observation_ids UUID[] NOT NULL DEFAULT ARRAY[]::uuid[],
    blocked_observation_ids UUID[] NOT NULL DEFAULT ARRAY[]::uuid[],
    affected_relationships JSONB NOT NULL DEFAULT '[]'::jsonb,
    evidence JSONB NOT NULL DEFAULT '[]'::jsonb,
    impact_summary TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'planned',
    correction_event_id UUID NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    applied_at TIMESTAMPTZ NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, plan_token),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, source_entity_id) REFERENCES entity_records(team_id, entity_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, target_entity_id) REFERENCES entity_records(team_id, entity_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, new_entity_id) REFERENCES entity_records(team_id, entity_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, correction_event_id) REFERENCES entity_correction_events(team_id, correction_event_id) ON DELETE RESTRICT,
    CONSTRAINT entity_correction_plans_action_check CHECK (action IN ('merge', 'split')),
    CONSTRAINT entity_correction_plans_status_check CHECK (status IN ('planned', 'applied')),
    CONSTRAINT entity_correction_plans_affected_array_check CHECK (jsonb_typeof(affected_relationships) = 'array'),
    CONSTRAINT entity_correction_plans_evidence_array_check CHECK (jsonb_typeof(evidence) = 'array'),
    CONSTRAINT entity_correction_plans_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object'),
    CONSTRAINT entity_correction_plans_merge_target_check CHECK (
        (action = 'merge' AND target_entity_id IS NOT NULL)
        OR (action = 'split' AND target_entity_id IS NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS entity_correction_plans_idempotency_unique
    ON entity_correction_plans(team_id, owner_profile_id, idempotency_key)
    WHERE idempotency_key <> '';

ALTER TABLE entity_correction_plans ENABLE ROW LEVEL SECURITY;
ALTER TABLE entity_correction_plans FORCE ROW LEVEL SECURITY;

CREATE POLICY entity_correction_plans_select ON entity_correction_plans
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) IN ('team', 'profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

CREATE POLICY entity_correction_plans_insert ON entity_correction_plans
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) = 'profile'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
        )
    );

CREATE POLICY entity_correction_plans_update ON entity_correction_plans
    FOR UPDATE USING (
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
DROP POLICY IF EXISTS entity_correction_plans_update ON entity_correction_plans;
DROP POLICY IF EXISTS entity_correction_plans_insert ON entity_correction_plans;
DROP POLICY IF EXISTS entity_correction_plans_select ON entity_correction_plans;
DROP INDEX IF EXISTS entity_correction_plans_idempotency_unique;
DROP TABLE IF EXISTS entity_correction_plans;
-- +goose StatementEnd
