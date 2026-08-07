-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

-- Quarantine expiry is a fixed, bounded retention window for the raw
-- provider-facing payload copy. The append-only ledger keeps identifiers,
-- hashes, security events, and staged evidence for audit and lineage; those
-- immutable source rows are not part of the raw-payload retention boundary.
-- The placement_runs CHECK is installed NOT VALID to avoid an ACCESS EXCLUSIVE
-- full-table scan during installation; validation uses the lower-impact
-- SHARE UPDATE EXCLUSIVE lock before the backfill.
ALTER TABLE placement_runs
    ADD COLUMN IF NOT EXISTS quarantine_expires_at TIMESTAMPTZ NULL;

ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_quarantine_expiry_check;
ALTER TABLE placement_runs
    ADD CONSTRAINT placement_runs_quarantine_expiry_check CHECK (
        quarantine_expires_at IS NULL
        OR quarantine_expires_at >= created_at + interval '24 hours'
    ) NOT VALID;

ALTER TABLE placement_runs
    VALIDATE CONSTRAINT placement_runs_quarantine_expiry_check;

UPDATE placement_runs
SET quarantine_expires_at = COALESCE(completed_at, created_at) + interval '24 hours'
WHERE status = 'quarantined'
  AND quarantine_expires_at IS NULL;

CREATE TABLE IF NOT EXISTS submission_quarantine_payloads (
    team_id UUID NOT NULL,
    quarantine_payload_id UUID NOT NULL DEFAULT gen_random_uuid(),
    placement_run_id UUID NOT NULL,
    ingest_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    proposal JSONB NOT NULL DEFAULT '{}'::jsonb,
    evidence JSONB NOT NULL DEFAULT '[]'::jsonb,
    assessor_response JSONB NOT NULL DEFAULT '{}'::jsonb,
    payload_sha256 TEXT NOT NULL DEFAULT '',
    quarantined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (team_id, quarantine_payload_id),
    UNIQUE (team_id, placement_run_id),
    FOREIGN KEY (team_id, placement_run_id, ingest_id, owner_profile_id)
        REFERENCES placement_runs(team_id, placement_run_id, ingest_id, owner_profile_id)
        ON DELETE RESTRICT,
    CONSTRAINT submission_quarantine_payloads_expiry_check
        CHECK (expires_at = quarantined_at + interval '24 hours'),
    CONSTRAINT submission_quarantine_payloads_json_check CHECK (
        jsonb_typeof(proposal) = 'object'
        AND jsonb_typeof(evidence) = 'array'
        AND jsonb_typeof(assessor_response) = 'object'
    )
);

CREATE INDEX IF NOT EXISTS submission_quarantine_payloads_expiry_idx
    ON submission_quarantine_payloads(expires_at ASC, team_id, quarantine_payload_id);

CREATE TABLE IF NOT EXISTS submission_quarantine_tombstones (
    team_id UUID NOT NULL,
    fragment_id UUID NOT NULL,
    ingest_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    content_hash TEXT NOT NULL,
    tombstoned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, fragment_id),
    FOREIGN KEY (team_id, fragment_id, ingest_id, owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, ingest_id, owner_profile_id)
        ON DELETE RESTRICT
);

ALTER TABLE submission_quarantine_tombstones ENABLE ROW LEVEL SECURITY;
ALTER TABLE submission_quarantine_tombstones FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS submission_quarantine_tombstones_system_only ON submission_quarantine_tombstones;
DROP POLICY IF EXISTS submission_quarantine_tombstones_owner_insert ON submission_quarantine_tombstones;
CREATE POLICY submission_quarantine_tombstones_system_only ON submission_quarantine_tombstones
    FOR SELECT USING (current_setting('app.tx_mode', true) IN ('system', 'migration'));
CREATE POLICY submission_quarantine_tombstones_owner_insert ON submission_quarantine_tombstones
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) = 'profile'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
        )
    );

ALTER TABLE submission_quarantine_payloads ENABLE ROW LEVEL SECURITY;
ALTER TABLE submission_quarantine_payloads FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS submission_quarantine_payloads_system_only ON submission_quarantine_payloads;
DROP POLICY IF EXISTS submission_quarantine_payloads_owner_insert ON submission_quarantine_payloads;
DROP POLICY IF EXISTS submission_quarantine_payloads_system_delete ON submission_quarantine_payloads;
CREATE POLICY submission_quarantine_payloads_system_only ON submission_quarantine_payloads
    FOR SELECT USING (current_setting('app.tx_mode', true) IN ('system', 'migration'));
CREATE POLICY submission_quarantine_payloads_owner_insert ON submission_quarantine_payloads
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) = 'profile'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
            AND expires_at = quarantined_at + interval '24 hours'
        )
    );
CREATE POLICY submission_quarantine_payloads_system_delete ON submission_quarantine_payloads
    FOR DELETE USING (current_setting('app.tx_mode', true) IN ('system', 'migration'));

COMMENT ON TABLE submission_quarantine_payloads IS
    'System/migration-only raw quarantined submission payload copy. Purge after exactly 24 hours; immutable source ledger rows remain for audit and lineage; public reads are forbidden.';

COMMENT ON TABLE submission_quarantine_tombstones IS
    'System/migration-only hashes identifying raw quarantine fragments after payload purge; source evidence remains immutable audit history.';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $dense_mem_irreversible_quarantine_payloads$
BEGIN
    RAISE EXCEPTION 'irreversible migration: submission quarantine payload retention and tombstones cannot be rolled back';
END
$dense_mem_irreversible_quarantine_payloads$;
-- +goose StatementEnd
