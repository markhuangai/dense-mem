-- +goose Up

-- Failure diagnostics are stored as ordered exchanges so the control portal can
-- show the original request, provider traffic, and caller response together.
CREATE TABLE IF NOT EXISTS remember_attempt_diagnostics (
    team_id UUID NOT NULL,
    diagnostic_id UUID NOT NULL DEFAULT gen_random_uuid(),
    attempt_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    sequence_no INTEGER NOT NULL,
    kind TEXT NOT NULL,
    component TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    request_bytes BYTEA NOT NULL DEFAULT ''::bytea,
    response_bytes BYTEA NOT NULL DEFAULT ''::bytea,
    request_content_type TEXT NOT NULL DEFAULT '',
    response_content_type TEXT NOT NULL DEFAULT '',
    status_code INTEGER NOT NULL DEFAULT 0,
    outcome TEXT NOT NULL DEFAULT 'captured',
    captured_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    retained_by_legal_hold BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (team_id, diagnostic_id),
    UNIQUE (team_id, attempt_id, sequence_no),
    CONSTRAINT remember_attempt_diagnostics_kind_check CHECK (kind IN ('original_request', 'provider_exchange', 'caller_response')),
    CONSTRAINT remember_attempt_diagnostics_body_size_check CHECK (
        octet_length(request_bytes) <= 16777216 AND octet_length(response_bytes) <= 16777216
    ),
    CONSTRAINT remember_attempt_diagnostics_expiry_check CHECK (expires_at >= captured_at AND expires_at <= captured_at + interval '7 days'),
    FOREIGN KEY (team_id, attempt_id, owner_profile_id)
        REFERENCES remember_attempts(team_id, attempt_id, owner_profile_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS remember_attempt_diagnostics_expiry_idx
    ON remember_attempt_diagnostics(expires_at);
CREATE INDEX IF NOT EXISTS remember_attempt_diagnostics_attempt_idx
    ON remember_attempt_diagnostics(team_id, attempt_id, sequence_no);

ALTER TABLE remember_attempt_diagnostics ENABLE ROW LEVEL SECURITY;
ALTER TABLE remember_attempt_diagnostics FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS remember_attempt_diagnostics_select ON remember_attempt_diagnostics;
DROP POLICY IF EXISTS remember_attempt_diagnostics_insert ON remember_attempt_diagnostics;
DROP POLICY IF EXISTS remember_attempt_diagnostics_update ON remember_attempt_diagnostics;
DROP POLICY IF EXISTS remember_attempt_diagnostics_delete ON remember_attempt_diagnostics;

CREATE POLICY remember_attempt_diagnostics_select ON remember_attempt_diagnostics
    FOR SELECT USING (current_setting('app.tx_mode', true) IN ('system', 'migration'));
CREATE POLICY remember_attempt_diagnostics_insert ON remember_attempt_diagnostics
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    );
CREATE POLICY remember_attempt_diagnostics_update ON remember_attempt_diagnostics
    FOR UPDATE USING (
        current_setting('app.tx_mode', true) = 'system'
        AND NULLIF(current_setting('app.remember_attempt_diagnostic_retention_space_id', true), '')::uuid IS NOT NULL
    );
CREATE POLICY remember_attempt_diagnostics_delete ON remember_attempt_diagnostics
    FOR DELETE USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        AND (
            current_setting('app.remember_attempt_diagnostic_purge', true) = 'true'
            OR current_setting('app.tx_mode', true) = 'migration'
            OR EXISTS (
                SELECT 1
                FROM remember_attempts AS attempt
                WHERE attempt.team_id = remember_attempt_diagnostics.team_id
                  AND attempt.attempt_id = remember_attempt_diagnostics.attempt_id
                  AND attempt.owner_profile_id = remember_attempt_diagnostics.owner_profile_id
                  AND attempt.space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
            )
        )
    );

-- Preserve still-retained legacy bytes while the old artifact table is being
-- retired. Hash-only request summaries remain unavailable by design.
-- +goose StatementBegin
DO $$
DECLARE invalid_rows BIGINT;
BEGIN
    SELECT count(*)
      INTO invalid_rows
    FROM remember_failure_artifacts AS artifact
    LEFT JOIN remember_attempts AS attempt
      ON attempt.team_id = artifact.team_id
     AND attempt.attempt_id = artifact.attempt_id
     AND attempt.owner_profile_id = artifact.owner_profile_id
    LEFT JOIN private_memory_legal_holds AS hold
      ON hold.space_id = attempt.space_id AND hold.released_at IS NULL
    WHERE (artifact.expires_at > clock_timestamp() OR hold.id IS NOT NULL)
      AND (artifact.byte_count <> octet_length(artifact.content_bytes)
           OR artifact.content_sha256 <> 'sha256:' || encode(digest(artifact.content_bytes, 'sha256'), 'hex'));
    IF invalid_rows > 0 THEN
        RAISE EXCEPTION 'remember failure diagnostics migration found % retained legacy rows with invalid byte count or SHA-256', invalid_rows;
    END IF;
END $$;
-- +goose StatementEnd

INSERT INTO remember_attempt_diagnostics (
    team_id, attempt_id, owner_profile_id, sequence_no, kind, component,
    request_bytes, response_bytes, request_content_type, response_content_type,
    outcome, captured_at, expires_at, retained_by_legal_hold
)
SELECT artifact.team_id,
       artifact.attempt_id,
       artifact.owner_profile_id,
       row_number() OVER (PARTITION BY artifact.team_id, artifact.attempt_id ORDER BY artifact.captured_at, artifact.artifact_id),
       CASE WHEN artifact.artifact_kind IN ('request', 'policy_rejected_request') THEN 'original_request' ELSE 'provider_exchange' END,
       'legacy_failure_artifact',
       CASE WHEN artifact.artifact_kind IN ('request', 'policy_rejected_request') THEN artifact.content_bytes ELSE ''::bytea END,
       CASE WHEN artifact.artifact_kind IN ('request', 'policy_rejected_request') THEN ''::bytea ELSE artifact.content_bytes END,
       CASE WHEN artifact.artifact_kind IN ('request', 'policy_rejected_request') THEN artifact.content_type ELSE '' END,
       CASE WHEN artifact.artifact_kind IN ('request', 'policy_rejected_request') THEN '' ELSE artifact.content_type END,
       'legacy_migrated', artifact.captured_at, artifact.expires_at, artifact.retained_by_legal_hold
FROM remember_failure_artifacts AS artifact
LEFT JOIN remember_attempts AS attempt
  ON attempt.team_id = artifact.team_id
 AND attempt.attempt_id = artifact.attempt_id
 AND attempt.owner_profile_id = artifact.owner_profile_id
LEFT JOIN private_memory_legal_holds AS hold
  ON hold.space_id = attempt.space_id AND hold.released_at IS NULL
WHERE (artifact.expires_at > clock_timestamp() OR hold.id IS NOT NULL)
ON CONFLICT (team_id, attempt_id, sequence_no) DO NOTHING;

-- The diagnostic table is now the sole authority. This irreversible cleanup
-- removes the legacy hash-bearing artifact surface after verified migration.
DROP TABLE IF EXISTS remember_failure_artifacts;

-- +goose Down
DROP TABLE IF EXISTS remember_attempt_diagnostics;
