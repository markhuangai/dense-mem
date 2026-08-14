-- +goose NO TRANSACTION
-- +goose Up

-- Lock/rewrite: CREATE INDEX CONCURRENTLY avoids blocking writes; no heap rewrite occurs.
-- RLS: index-only structure change, no policy change.
CREATE INDEX CONCURRENTLY IF NOT EXISTS relationship_transition_events_relationship_created_idx
    ON relationship_transition_events(team_id, relationship_id, created_at DESC, transition_id DESC);

-- +goose Down

DROP INDEX CONCURRENTLY IF EXISTS relationship_transition_events_relationship_created_idx;
