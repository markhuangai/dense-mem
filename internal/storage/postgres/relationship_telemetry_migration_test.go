//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelationshipTelemetryMigrationRecoversInvalidIndexesAndRollsBack(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, 2026080903)
	markRelationshipTelemetryIndexesInvalid(t, ctx, db)

	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.UpContext(ctx, db, getMigrationsDir()))

	for _, indexName := range relationshipTelemetryIndexNames {
		assert.True(t, indexIsValid(t, ctx, db, indexName), "rebuilt index %s should be valid", indexName)
		invalidName := relationshipTelemetryInvalidIndexName(indexName)
		assert.False(t, indexExists(t, ctx, db, invalidName), "temporary index %s should be removed", invalidName)
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

func relationshipTelemetryInvalidIndexName(indexName string) string {
	return strings.TrimSuffix(indexName, "_idx") + "_invalid_idx"
}

func markRelationshipTelemetryIndexesInvalid(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, spec := range []struct {
		name    string
		table   string
		columns string
	}{
		{name: relationshipTelemetryIndexNames[0], table: "relationship_transition_events", columns: "created_at, team_id, owner_profile_id, to_status"},
		{name: relationshipTelemetryIndexNames[1], table: "relationship_correction_events", columns: "created_at, team_id, owner_profile_id"},
		{name: relationshipTelemetryIndexNames[2], table: "relationship_records", columns: "team_id, owner_profile_id, status"},
	} {
		_, err := db.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+spec.name)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+relationshipTelemetryInvalidIndexName(spec.name))
		require.NoError(t, err)
		createCanceledRelationshipTelemetryIndex(t, ctx, db, spec.name, spec.table, spec.columns)
	}
}

func createCanceledRelationshipTelemetryIndex(t *testing.T, ctx context.Context, db *sql.DB, indexName, tableName, columns string) {
	t.Helper()
	blocker, err := db.Conn(ctx)
	require.NoError(t, err)
	defer blocker.Close()
	_, err = blocker.ExecContext(ctx, "BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ")
	require.NoError(t, err)
	defer func() { _, _ = blocker.ExecContext(context.Background(), "ROLLBACK") }()
	_, err = blocker.ExecContext(ctx, "SELECT 1 FROM "+tableName+" LIMIT 1")
	require.NoError(t, err)

	buildConn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer buildConn.Close()
	var backendPID int
	require.NoError(t, buildConn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID))
	buildCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	buildDone := make(chan error, 1)
	go func() {
		_, buildErr := buildConn.ExecContext(buildCtx, fmt.Sprintf("CREATE INDEX CONCURRENTLY %s ON %s(%s)", indexName, tableName, columns))
		buildDone <- buildErr
	}()

	var invalid bool
	assert.Eventually(t, func() bool {
		return db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_index
				WHERE indexrelid = $1::regclass
				  AND NOT indisvalid
			)`, indexName).Scan(&invalid) == nil && invalid
	}, 10*time.Second, 50*time.Millisecond, "canceled index build did not create an invalid index")

	var canceled bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT pg_cancel_backend($1)", backendPID).Scan(&canceled))
	require.True(t, canceled)
	_, err = blocker.ExecContext(ctx, "COMMIT")
	require.NoError(t, err)
	require.Error(t, <-buildDone)
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
