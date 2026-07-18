-- +goose NO TRANSACTION
-- +goose Up

CREATE UNIQUE INDEX CONCURRENTLY idx_team_profiles_team_id_id_unique
    ON team_profiles(team_id, id);

-- +goose Down

DROP INDEX CONCURRENTLY IF EXISTS idx_team_profiles_team_id_id_unique;
