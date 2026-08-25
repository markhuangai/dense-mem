-- Lock/rewrite impact: the failed-row status update takes row locks only on
-- unfinished legacy ingests; replacing the partial idempotency index takes a
-- short table lock and does not rewrite table heaps.
-- RLS impact: the migration runs in explicit migration mode; no application
-- visibility or team/profile policy changes are introduced.
-- Backfill: unfinished ingests whose placement run already failed are marked
-- failed before the new retry-safe partial unique index is installed.
-- Backward compatibility: completed, rejected, and quarantined idempotency
-- keys remain unique; only failed keys become eligible for a same-key retry.
-- Rollback: Down restores the all-status unique index and can fail if duplicate
-- failed keys were created after Up; take a verified snapshot before reversal.

-- +goose Up
-- +goose StatementBegin

-- Operationally failed Remember attempts may be retried with the same
-- idempotency key. Keep successful domain outcomes canonical while allowing a
-- new ingest/run to replace an older failed attempt without rewriting its
-- append-only history.
UPDATE knowledge_ingests AS ingest
SET status = 'failed',
    error = COALESCE(NULLIF(run.error, ''), ingest.error),
    completed_at = COALESCE(ingest.completed_at, run.completed_at, now()),
    updated_at = now()
FROM placement_runs AS run
WHERE run.team_id = ingest.team_id
  AND run.ingest_id = ingest.ingest_id
  AND run.owner_profile_id = ingest.owner_profile_id
  AND run.status = 'failed'
  AND ingest.status IN ('queued', 'guarded', 'processing');

DROP INDEX IF EXISTS knowledge_ingests_idempotency_unique;
CREATE UNIQUE INDEX knowledge_ingests_idempotency_unique
    ON knowledge_ingests(team_id, owner_profile_id, idempotency_key)
    WHERE idempotency_key <> '' AND status <> 'failed';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS knowledge_ingests_idempotency_unique;
CREATE UNIQUE INDEX knowledge_ingests_idempotency_unique
    ON knowledge_ingests(team_id, owner_profile_id, idempotency_key)
    WHERE idempotency_key <> '';
-- +goose StatementEnd
