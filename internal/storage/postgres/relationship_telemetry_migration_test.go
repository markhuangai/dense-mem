//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelationshipTelemetryMigrationRecoversInvalidIndexesAndRollsBack(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, 2026080903)
	createRelationshipTelemetryIndexes(t, ctx, db)
	markRelationshipTelemetryIndexesInvalid(t, ctx, db)

	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.UpContext(ctx, db, getMigrationsDir()))

	for _, indexName := range relationshipTelemetryIndexNames {
		assert.True(t, indexIsValid(t, ctx, db, indexName), "rebuilt index %s should be valid", indexName)
		assert.False(t, indexExists(t, ctx, db, indexName+"_invalid"), "temporary index %s should be removed", indexName+"_invalid")
	}

	require.NoError(t, goose.DownContext(ctx, db, getMigrationsDir()))
	for _, indexName := range relationshipTelemetryIndexNames {
		assert.False(t, indexExists(t, ctx, db, indexName), "down migration should remove %s", indexName)
	}
}

var relationshipTelemetryIndexNames = []string{
	"relationship_transition_events_telemetry_window_idx",
	"relationship_correction_events_telemetry_window_idx",
	"relationship_records_telemetry_status_idx",
}

func createRelationshipTelemetryIndexes(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	statements := []string{
		`CREATE INDEX relationship_transition_events_telemetry_window_idx ON relationship_transition_events(created_at, team_id, owner_profile_id, to_status)`,
		`CREATE INDEX relationship_correction_events_telemetry_window_idx ON relationship_correction_events(created_at, team_id, owner_profile_id)`,
		`CREATE INDEX relationship_records_telemetry_status_idx ON relationship_records(team_id, owner_profile_id, status)`,
	}
	for _, statement := range statements {
		_, err := db.ExecContext(ctx, statement)
		require.NoError(t, err)
	}
}

func markRelationshipTelemetryIndexesInvalid(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, indexName := range relationshipTelemetryIndexNames {
		_, err := db.ExecContext(ctx, `
			UPDATE pg_index
			SET indisvalid = false, indisready = false
			WHERE indexrelid = $1::regclass
		`, indexName)
		require.NoError(t, err)
	}
}

func indexIsValid(t *testing.T, ctx context.Context, db *sql.DB, indexName string) bool {
	t.Helper()
	var valid bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT indisvalid
		FROM pg_index
		WHERE indexrelid = $1::regclass
	`, indexName).Scan(&valid))
	return valid
}
