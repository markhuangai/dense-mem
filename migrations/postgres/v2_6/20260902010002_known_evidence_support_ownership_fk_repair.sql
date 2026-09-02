-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: dropping four catalog constraints takes a brief
-- ACCESS EXCLUSIVE lock and does not rewrite the support heap.
-- RLS impact: migration mode is explicit; no application row policy changes.
-- Backfill: none; this repair changes only constraint metadata.
-- Backward compatibility: existing support rows retain their provenance, and
-- the unscoped team-existence keys and evidence-owner keys remain authoritative.
-- Rollback: intentionally a no-op because restoring the legacy owner-scoped
-- keys would make valid cross-owner evidence support fail again.
-- The ownership migration attempted to drop the legacy owner-scoped keys using
-- names that PostgreSQL had truncated, so this repair matches their definitions.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);

DO $$
DECLARE
    constraint_row RECORD;
BEGIN
    FOR constraint_row IN
        SELECT constraint_item.conname
        FROM pg_constraint AS constraint_item
        WHERE constraint_item.conrelid = 'relationship_evidence_supports'::regclass
          AND constraint_item.contype = 'f'
          AND (
              (
                  constraint_item.confrelid = 'evidence_fragments'::regclass
                  AND pg_get_constraintdef(constraint_item.oid) LIKE
                      'FOREIGN KEY (team_id, fragment_id, owner_profile_id) REFERENCES evidence_fragments%'
              )
              OR (
                  constraint_item.confrelid = 'evidence_sources'::regclass
                  AND pg_get_constraintdef(constraint_item.oid) LIKE
                      'FOREIGN KEY (team_id, source_id, owner_profile_id) REFERENCES evidence_sources%'
              )
              OR (
                  constraint_item.confrelid = 'evidence_source_revisions'::regclass
                  AND pg_get_constraintdef(constraint_item.oid) LIKE
                      'FOREIGN KEY (team_id, source_revision_id, owner_profile_id) REFERENCES evidence_source_revisions%'
              )
              OR (
                  constraint_item.confrelid = 'evidence_source_revisions'::regclass
                  AND pg_get_constraintdef(constraint_item.oid) LIKE
                      'FOREIGN KEY (team_id, source_id, source_revision_id, owner_profile_id) REFERENCES evidence_source_revisions%'
              )
          )
    LOOP
        EXECUTE format(
            'ALTER TABLE relationship_evidence_supports DROP CONSTRAINT %I',
            constraint_row.conname
        );
    END LOOP;
END $$;

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- This repair is intentionally not reversed: the parent migration's target
-- state excludes the legacy owner-scoped keys, and restoring them would make
-- valid cross-owner evidence support fail again.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);

-- +goose StatementEnd
