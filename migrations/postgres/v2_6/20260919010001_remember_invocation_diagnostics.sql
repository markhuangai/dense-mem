-- Lock/rewrite impact: creates an empty append-only diagnostics table and
-- three indexes; table, policy, function, and trigger definitions are
-- metadata-only and do not rewrite existing heaps.
-- RLS impact: enables FORCE RLS with explicit system, migration, and
-- owner-scoped insert paths; retention and erasure updates remain system-only.
-- Backfill: none; invocation diagnostics are written only for new requests
-- after this migration is applied.
-- Backward compatibility: older binaries ignore this additive table and the
-- existing Remember attempt authority and capture contracts remain unchanged.
-- Rollback: irreversible after diagnostics are written because retained
-- payloads and append-only history cannot be reconstructed safely.

-- +goose Up

-- Invocation diagnostics are append-only operator records. They deliberately
-- do not reference remember_attempts: replay and conflict calls must remain
-- inspectable without becoming idempotency authority.
CREATE TABLE IF NOT EXISTS remember_invocation_diagnostics (
    team_id UUID NOT NULL,
    invocation_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    space_id UUID,
    space_generation BIGINT NOT NULL DEFAULT 0,
    canonical_attempt_id UUID,
    request_hash TEXT NOT NULL DEFAULT '',
    correlation_id TEXT NOT NULL DEFAULT '',
    classification TEXT NOT NULL DEFAULT 'execution',
    outcome TEXT NOT NULL DEFAULT 'completed',
    failed_phase TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    retryable BOOLEAN NOT NULL DEFAULT false,
    duration_ms BIGINT NOT NULL DEFAULT 0,
    request_bytes BYTEA NOT NULL DEFAULT ''::bytea,
    request_capture_state TEXT NOT NULL DEFAULT 'not_captured',
    request_capture_reason TEXT NOT NULL DEFAULT '',
    provider_exchanges JSONB NOT NULL DEFAULT '[]'::jsonb,
    response_bytes BYTEA NOT NULL DEFAULT ''::bytea,
    response_capture_state TEXT NOT NULL DEFAULT 'not_captured',
    response_capture_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    retained_by_legal_hold BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (team_id, invocation_id),
    CONSTRAINT remember_invocation_diagnostics_classification_check CHECK (classification IN ('execution', 'replay', 'conflict')),
    CONSTRAINT remember_invocation_diagnostics_outcome_check CHECK (outcome IN ('completed', 'evaluated_zero', 'failed', 'cancelled', 'replayed', 'conflict')),
    CONSTRAINT remember_invocation_diagnostics_duration_check CHECK (duration_ms >= 0),
    CONSTRAINT remember_invocation_diagnostics_capture_state_check CHECK (
        request_capture_state IN ('captured', 'truncated', 'not_captured', 'hash_only', 'provider_not_called', 'no_response', 'interrupted', 'not_delivered', 'unavailable') AND
        response_capture_state IN ('captured', 'truncated', 'not_captured', 'hash_only', 'provider_not_called', 'no_response', 'interrupted', 'not_delivered', 'unavailable')
    ),
    CONSTRAINT remember_invocation_diagnostics_capture_reason_check CHECK (
        octet_length(request_capture_reason) <= 128 AND octet_length(response_capture_reason) <= 128
    ),
    CONSTRAINT remember_invocation_diagnostics_body_size_check CHECK (
        octet_length(request_bytes) <= 16777216 AND
        octet_length(response_bytes) <= 16777216 AND
        octet_length(provider_exchanges::text) <= 67108864 AND
        octet_length(request_bytes) + octet_length(response_bytes) + octet_length(provider_exchanges::text) <= 67108864
    ),
    CONSTRAINT remember_invocation_diagnostics_expiry_check CHECK (
        expires_at >= created_at AND expires_at <= created_at + interval '7 days'
    )
);

CREATE INDEX IF NOT EXISTS remember_invocation_diagnostics_created_idx
    ON remember_invocation_diagnostics(team_id, created_at DESC, invocation_id DESC);
CREATE INDEX IF NOT EXISTS remember_invocation_diagnostics_expiry_idx
    ON remember_invocation_diagnostics(expires_at);
CREATE INDEX IF NOT EXISTS remember_invocation_diagnostics_space_idx
    ON remember_invocation_diagnostics(space_id, expires_at);

ALTER TABLE remember_invocation_diagnostics ENABLE ROW LEVEL SECURITY;
ALTER TABLE remember_invocation_diagnostics FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS remember_invocation_diagnostics_select ON remember_invocation_diagnostics;
DROP POLICY IF EXISTS remember_invocation_diagnostics_insert ON remember_invocation_diagnostics;
DROP POLICY IF EXISTS remember_invocation_diagnostics_update ON remember_invocation_diagnostics;
DROP POLICY IF EXISTS remember_invocation_diagnostics_delete ON remember_invocation_diagnostics;

CREATE POLICY remember_invocation_diagnostics_select ON remember_invocation_diagnostics
    FOR SELECT USING (current_setting('app.tx_mode', true) IN ('system', 'migration'));
CREATE POLICY remember_invocation_diagnostics_insert ON remember_invocation_diagnostics
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    );
CREATE POLICY remember_invocation_diagnostics_update ON remember_invocation_diagnostics
    FOR UPDATE USING (
        current_setting('app.tx_mode', true) = 'system'
        AND NULLIF(current_setting('app.remember_attempt_diagnostic_retention_space_id', true), '')::uuid = space_id
    ) WITH CHECK (
        current_setting('app.tx_mode', true) = 'system'
        AND NULLIF(current_setting('app.remember_attempt_diagnostic_retention_space_id', true), '')::uuid = space_id
    );
CREATE POLICY remember_invocation_diagnostics_delete ON remember_invocation_diagnostics
    FOR DELETE USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        AND (
            current_setting('app.remember_attempt_diagnostic_purge', true) = 'true'
            OR current_setting('app.tx_mode', true) = 'migration'
            OR space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
        )
    );

-- Only the legal-hold flag may change in a system retention transaction.
-- All payload and lineage fields remain append-only.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION prevent_remember_invocation_diagnostic_mutation()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE'
       AND current_setting('app.tx_mode', true) = 'system'
       AND NULLIF(current_setting('app.remember_attempt_diagnostic_retention_space_id', true), '')::uuid = OLD.space_id
       AND (to_jsonb(NEW) - ARRAY['retained_by_legal_hold']) = (to_jsonb(OLD) - ARRAY['retained_by_legal_hold']) THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE'
       AND current_setting('app.tx_mode', true) IN ('system', 'migration')
       AND (
           current_setting('app.remember_attempt_diagnostic_purge', true) = 'true'
           OR current_setting('app.tx_mode', true) = 'migration'
           OR OLD.space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
       ) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'remember_invocation_diagnostics is append-only: % operations are not allowed', TG_OP;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS remember_invocation_diagnostics_append_only ON remember_invocation_diagnostics;
CREATE TRIGGER remember_invocation_diagnostics_append_only
    BEFORE UPDATE OR DELETE ON remember_invocation_diagnostics
    FOR EACH ROW EXECUTE FUNCTION prevent_remember_invocation_diagnostic_mutation();

-- +goose Down
-- Irreversible: retained operator diagnostics cannot be reconstructed safely.
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'remember invocation diagnostics migration is irreversible; restore from backup or roll forward';
END $$;
-- +goose StatementEnd
