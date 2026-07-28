//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCoreSchemaTeamsTable(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := migratedSQLDB(t, ctx)
	defer cleanup()

	assert.True(t, tableExists(t, ctx, sqlDB, "teams"))
	assert.False(t, tableExists(t, ctx, sqlDB, "profiles"))

	for _, col := range []string{
		"id", "name", "description", "metadata", "config",
		"status", "created_at", "updated_at", "deleted_at",
	} {
		assert.True(t, columnExists(t, ctx, sqlDB, "teams", col), "teams.%s should exist", col)
	}

	assert.True(t, constraintExists(t, ctx, sqlDB, "teams_status_check"))
}

func TestCoreSchemaTeamProfilesTable(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := migratedSQLDB(t, ctx)
	defer cleanup()

	assert.True(t, tableExists(t, ctx, sqlDB, "team_profiles"))
	assert.False(t, tableExists(t, ctx, sqlDB, "api_keys"))

	for _, col := range []string{
		"id", "team_id", "key_hash", "key_prefix", "key_suffix", "name",
		"scopes", "role", "rate_limit", "expires_at", "revoked_at",
		"last_used_at", "created_at", "updated_at", "auth_source",
	} {
		assert.True(t, columnExists(t, ctx, sqlDB, "team_profiles", col), "team_profiles.%s should exist", col)
	}

	var teamID string
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			INSERT INTO teams (id, name, status)
			VALUES (gen_random_uuid(), 'test-team', 'active')
			RETURNING id
		`).Scan(&teamID)
	}))

	var err error
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO team_profiles (id, team_id, key_hash, key_prefix, key_suffix, name, scopes)
			VALUES (gen_random_uuid(), $1::uuid, 'hash456', 'prefix2', 'refix2', 'writer', ARRAY['read'])
		`, teamID)
		return nil
	}))
	assert.NoError(t, err)

	err = execPostgresTxModeRollback(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `
			INSERT INTO team_profiles (id, key_hash, key_prefix, scopes)
			VALUES (gen_random_uuid(), 'hash789', 'prefix3', ARRAY['read'])
		`)
		return execErr
	})
	assert.Error(t, err)

	err = execPostgresTxModeRollback(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `
			INSERT INTO team_profiles (id, team_id, key_hash, key_prefix, key_suffix, name, scopes)
			VALUES (gen_random_uuid(), $1::uuid, 'hashabc', 'prefix2', 'refix2', 'reader', ARRAY['read'])
		`, teamID)
		return execErr
	})
	assert.Error(t, err)
}

func TestCoreSchemaAuditLogTable(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := migratedSQLDB(t, ctx)
	defer cleanup()

	assert.True(t, tableExists(t, ctx, sqlDB, "audit_log"))

	for _, col := range []string{
		"id", "team_id", "timestamp", "operation", "entity_type",
		"entity_id", "before_payload", "after_payload", "actor_profile_id",
		"actor_role", "client_ip", "correlation_id", "metadata",
	} {
		assert.True(t, columnExists(t, ctx, sqlDB, "audit_log", col), "audit_log.%s should exist", col)
	}

	assert.False(t, columnExists(t, ctx, sqlDB, "audit_log", "profile_id"))
	assert.False(t, columnExists(t, ctx, sqlDB, "audit_log", "actor_key_id"))
}

func TestCoreSchemaIndexes(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := migratedSQLDB(t, ctx)
	defer cleanup()

	for _, idxName := range []string{
		"idx_teams_name_unique_active",
		"idx_team_profiles_team_id",
		"idx_team_profiles_key_prefix_unique",
		"idx_audit_log_team_timestamp",
		"idx_audit_log_timestamp",
		"placement_runs_team_expired_claim_idx",
	} {
		assert.True(t, indexExists(t, ctx, sqlDB, idxName), "index %s should exist", idxName)
	}

	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `SET enable_seqscan = off`)
	require.NoError(t, err)
	rows, err := conn.QueryContext(ctx, `
		EXPLAIN (COSTS OFF)
		SELECT run.placement_run_id
		FROM placement_runs AS run
		WHERE run.team_id = '00000000-0000-0000-0000-000000000001'::uuid
		  AND run.attempts < run.max_attempts
		  AND run.status = 'processing'
		  AND run.lease_until IS NOT NULL
		  AND run.lease_until < now()
		ORDER BY run.lease_until ASC, run.created_at ASC, run.placement_run_id ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`)
	require.NoError(t, err)
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	require.NoError(t, rows.Err())
	assert.Contains(t, plan.String(), "placement_runs_team_expired_claim_idx")

	var isUnique bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT indisunique
		FROM pg_index
		WHERE indrelid = 'teams'::regclass
		  AND indpred IS NOT NULL
	`).Scan(&isUnique))
	assert.True(t, isUnique)
}

func migratedSQLDB(t *testing.T, ctx context.Context) (*sql.DB, func()) {
	t.Helper()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	m := NewMigratorWithDB(sqlDB)
	require.NoError(t, m.RunUp(ctx))
	return sqlDB, cleanup
}

func execPostgresTxModeRollback(ctx context.Context, db *sql.DB, txMode string, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tx_mode', $1, true)`, txMode); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.current_team_id', '', true)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.current_profile_id', '', true)`); err != nil {
		return err
	}
	return fn(tx)
}

func constraintExists(t *testing.T, ctx context.Context, db *sql.DB, constraintName string) bool {
	t.Helper()
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.check_constraints
			WHERE constraint_name = $1
		)
	`, constraintName).Scan(&exists)
	require.NoError(t, err)
	return exists
}

func indexExists(t *testing.T, ctx context.Context, db *sql.DB, indexName string) bool {
	t.Helper()
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_indexes
			WHERE schemaname = 'public'
			  AND indexname = $1
		)
	`, indexName).Scan(&exists)
	require.NoError(t, err)
	return exists
}
