-- +goose Up

ALTER TABLE dream_diagnostic_captures
    ADD COLUMN IF NOT EXISTS tombstone_expires_at TIMESTAMPTZ NULL;

CREATE INDEX IF NOT EXISTS dream_diagnostic_captures_tombstone_expiry_idx
    ON dream_diagnostic_captures(tombstone_expires_at, team_id, capture_id)
    WHERE capture_state = 'expired';

-- +goose Down

DROP INDEX IF EXISTS dream_diagnostic_captures_tombstone_expiry_idx;
ALTER TABLE dream_diagnostic_captures
    DROP COLUMN IF EXISTS tombstone_expires_at;
