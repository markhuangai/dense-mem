//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// testConfig implements ConfigProvider for testing
type testConfig struct {
	dsn string
}

func (c *testConfig) GetPostgresDSN() string {
	return c.dsn
}

// getTestDSN returns the DSN to use for testing.
// It checks DATABASE_URL environment variable first, then tries to start a test container.
func getTestDSN(ctx context.Context) (string, func(), error) {
	// First, try DATABASE_URL environment variable
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn, func() {}, nil
	}

	// Try to start a test container
	container, err := postgres.Run(ctx,
		"pgvector/pgvector:0.8.2-pg18-trixie",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		return "", nil, fmt.Errorf("failed to start postgres container: %w", err)
	}

	// Get the connection string
	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		return "", nil, err
	}

	cleanup := func() {
		_ = container.Terminate(ctx)
	}

	return connStr, cleanup, nil
}

// skipIfNoPostgres skips the test if postgres is not available.
func skipIfNoPostgres(t *testing.T, ctx context.Context) (string, func()) {
	dsn, cleanup, err := getTestDSN(ctx)
	if err != nil {
		t.Skipf("Postgres not available: %v", err)
	}
	return dsn, cleanup
}

// TestOpenSuccess verifies successful connection and ping.
func TestOpenSuccess(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	cfg := &testConfig{dsn: dsn}

	db, err := Open(ctx, cfg)
	require.NoError(t, err, "Open should succeed with valid postgres")
	require.NotNil(t, db, "Open should return a non-nil db")

	// Verify we can get the underlying sql.DB and check pool settings
	sqlDB, err := db.DB()
	require.NoError(t, err, "should be able to get underlying sql.DB")

	// Verify pool configuration
	stats := sqlDB.Stats()
	assert.LessOrEqual(t, stats.MaxOpenConnections, 25, "max open connections should be 25")

	// Clean up
	err = sqlDB.Close()
	assert.NoError(t, err, "Close should not error")
}

// TestOpenFailsOnUnreachable verifies error returned (not panic) when Postgres is unreachable.
func TestOpenFailsOnUnreachable(t *testing.T) {
	ctx := context.Background()

	// Use an invalid connection string with a non-routable IP to avoid DNS resolution issues
	cfg := &testConfig{dsn: "host=192.0.2.1 port=5432 user=test password=test dbname=test sslmode=disable connect_timeout=1"}

	db, err := Open(ctx, cfg)
	assert.Error(t, err, "Open should return an error for unreachable postgres")
	assert.Nil(t, db, "Open should return nil db on error")
	// Error could be from connection failure or ping failure
	assert.Contains(t, err.Error(), "failed to", "error should indicate failure")
}

// TestOpenFailsOnEmptyDSN verifies error when DSN is empty.
func TestOpenFailsOnEmptyDSN(t *testing.T) {
	ctx := context.Background()
	cfg := &testConfig{dsn: ""}

	db, err := Open(ctx, cfg)
	assert.Error(t, err, "Open should return an error for empty DSN")
	assert.Nil(t, db, "Open should return nil db on error")
	assert.Contains(t, err.Error(), "DSN is empty", "error should indicate empty DSN")
}

// TestMigratorRunUp verifies migrations apply and schema changes are present.
func TestMigratorRunUp(t *testing.T) {
	ctx := context.Background()

	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	m := NewMigratorWithDB(sqlDB)

	// Run up migrations
	err := m.RunUp(ctx)
	// Should succeed since we have a valid migrations directory
	assert.NoError(t, err, "RunUp should succeed")
	err = m.RunUp(ctx)
	assert.NoError(t, err, "repeat RunUp should be idempotent")
}

// TestMigratorRunDownRejectsPostCutoverCleanup verifies the latest cleanup
// boundary is intentionally irreversible.
func TestMigratorRunDownRejectsPostCutoverCleanup(t *testing.T) {
	ctx := context.Background()

	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	m := NewMigratorWithDB(sqlDB)

	// First run up
	err := m.RunUp(ctx)
	require.NoError(t, err, "RunUp should succeed")

	// Then run down
	err = m.RunDown(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "irreversible migration: post-cutover legacy cleanup")
}

func TestV2SearchStorageMigrationAllowsIndexGenerationLifecycleOnly(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	m := NewMigratorWithDB(sqlDB)
	require.NoError(t, m.RunUp(ctx))

	contractID := uuid.NewString()
	generationID := uuid.NewString()
	tx, err := sqlDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SELECT set_config('app.tx_mode', 'migration', true)`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO embedding_contracts (
		    embedding_contract_id, contract_key, version, provider, model,
		    dimensions, distance_metric, vector_normalization,
		    document_format_version, query_format_version, lifecycle_state
		) VALUES ($1::uuid, 'test-search-lifecycle', 1, 'test', 'test-model', 3, 'cosine', 'provider', 1, 1, 'active')
	`, contractID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO search_index_generations (
		    search_index_generation_id, generation, embedding_contract_id,
		    embedding_dimensions, ann_strategy, activation_state
		) VALUES ($1::uuid, 1, $2::uuid, 3, 'exact', 'building')
	`, generationID, contractID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		UPDATE search_index_generations
		SET activation_state = 'active',
		    activated_at = now()
		WHERE search_index_generation_id = $1::uuid
	`, generationID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	tx, err = sqlDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SELECT set_config('app.tx_mode', 'migration', true)`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		UPDATE search_index_generations
		SET metadata = '{"changed": true}'::jsonb
		WHERE search_index_generation_id = $1::uuid
	`, generationID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "immutable fields cannot be changed")
	require.NoError(t, tx.Rollback())
}

func TestV2SemanticLedgerMigrationUpgradesPopulated1703(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026071703)
	teamID, profileID := insertV2MigrationTeamProfile(t, ctx, sqlDB)
	insertV2MigrationAuthorityFixture(t, ctx, sqlDB, teamID, profileID, "primary")

	m := NewMigratorWithDB(sqlDB)
	require.NoError(t, m.RunUp(ctx))

	_, err := sqlDB.ExecContext(ctx, `
		INSERT INTO evidence_sources (team_id, owner_profile_id, source_key, source_kind, authority)
		VALUES ($1::uuid, $2::uuid, 'doc://post-1704-inferred', 'document', 'inferred')
	`, teamID, profileID)
	require.NoError(t, err)
}

func TestV2SemanticLedgerMigrationRejectsLegacyDerivedAuthority(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026071703)
	teamID, profileID := insertV2MigrationTeamProfile(t, ctx, sqlDB)
	insertV2MigrationAuthorityFixture(t, ctx, sqlDB, teamID, profileID, "derived")

	m := NewMigratorWithDB(sqlDB)
	err := m.RunUp(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "canonical evidence authority required")
	assert.Contains(t, err.Error(), "derived=1")
}

func TestV2SemanticLedgerMigrationGuardedRollbackRejectsCanonicalAuthority(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026071704)
	teamID, profileID := insertV2MigrationTeamProfile(t, ctx, sqlDB)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_team_refs (team_id)
			VALUES ($1::uuid)
			ON CONFLICT (team_id) DO NOTHING
		`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_profile_refs (team_id, profile_id)
			VALUES ($1::uuid, $2::uuid)
			ON CONFLICT (team_id, profile_id) DO NOTHING
		`, teamID, profileID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_sources (team_id, owner_profile_id, source_key, source_kind, authority)
			VALUES ($1::uuid, $2::uuid, 'doc://rollback-inferred', 'document', 'inferred')
		`, teamID, profileID)
		return err
	}))

	m := NewMigratorWithDB(sqlDB)
	require.NoError(t, m.RunDown(ctx))

	err := m.RunDown(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot roll back 2026071704")
	assert.Contains(t, err.Error(), "inferred=1")
}

func TestPostCutoverCleanupMigrationFreshUpgradeSucceeds(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	m := NewMigratorWithDB(sqlDB)
	require.NoError(t, m.RunUp(ctx))

	for _, tableName := range []string{
		"profiles",
		"api_keys",
		"memory_dispute_sessions",
		"memory_placement_items",
		"memory_placement_runs",
		"community_detection_runs",
	} {
		assert.False(t, tableExists(t, ctx, sqlDB, tableName), "%s should be dropped", tableName)
	}
	assert.False(t, columnExists(t, ctx, sqlDB, "placement_runs", "migration_claim_epoch"))

	var markerCount int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)::int
		FROM v2_compatibility_markers
	`).Scan(&markerCount))
	assert.Zero(t, markerCount, "fresh authority marker is created by server bootstrap after migrations")
}

func TestPostCutoverCleanupMigrationWithCompatibleMarkerDropsLegacyTables(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026072302)
	teamID, profileID := insertV2MigrationTeamProfile(t, ctx, sqlDB)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE profiles (id uuid PRIMARY KEY)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO profiles (id) VALUES ($1::uuid)`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `CREATE TABLE api_keys (id uuid PRIMARY KEY, team_id uuid NOT NULL)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO api_keys (id, team_id) VALUES ($1::uuid, $2::uuid)`, profileID, teamID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO v2_compatibility_markers (
				marker_kind, version, status, corpus_hash, gate_report_hash, metadata
			) VALUES (
				'v2_cutover', 'dense-mem.v2.1.cutover.v1', 'compatible', 'sha256:corpus', 'sha256:gates', '{}'::jsonb
			)
		`)
		return err
	}))

	runGooseUpTo(t, ctx, sqlDB, 2026072303)

	assert.False(t, tableExists(t, ctx, sqlDB, "profiles"))
	assert.False(t, tableExists(t, ctx, sqlDB, "api_keys"))
	assert.True(t, tableExists(t, ctx, sqlDB, "v2_migration_runs"))
	assert.True(t, tableExists(t, ctx, sqlDB, "v2_compatibility_markers"))
}

func TestPostCutoverCleanupMigrationBlocksNonemptyDatabaseWithoutMarker(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026072302)
	insertV2MigrationTeamProfile(t, ctx, sqlDB)

	require.NoError(t, goose.SetDialect("postgres"))
	err := goose.UpToContext(ctx, sqlDB, getMigrationsDir(), 2026072303)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compatible cutover marker missing")
	assert.Contains(t, err.Error(), "teams")
	assert.True(t, tableExists(t, ctx, sqlDB, "community_detection_runs"), "cleanup DDL must not run after guard failure")
}

func TestPostCutoverCleanupMigrationBlocksLegacyProfileWithoutCanonicalTeam(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026072302)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE profiles (id uuid PRIMARY KEY)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO profiles (id) VALUES ($1::uuid)`, uuid.NewString()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO v2_compatibility_markers (
				marker_kind, version, status, corpus_hash, gate_report_hash, metadata
			) VALUES (
				'v2_cutover', 'dense-mem.v2.1.cutover.v1', 'compatible', 'sha256:corpus', 'sha256:gates', '{}'::jsonb
			)
		`)
		return err
	}))

	require.NoError(t, goose.SetDialect("postgres"))
	err := goose.UpToContext(ctx, sqlDB, getMigrationsDir(), 2026072303)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "legacy profiles missing canonical teams")
	assert.True(t, tableExists(t, ctx, sqlDB, "profiles"), "cleanup DDL must not run after guard failure")
}

func TestPostCutoverCleanupMigrationBlocksLegacyAPIKeyWithoutCanonicalTeamProfile(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026072302)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE api_keys (id uuid PRIMARY KEY, team_id uuid NOT NULL)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO api_keys (id, team_id) VALUES ($1::uuid, $2::uuid)`, uuid.NewString(), uuid.NewString()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO v2_compatibility_markers (
				marker_kind, version, status, corpus_hash, gate_report_hash, metadata
			) VALUES (
				'v2_cutover', 'dense-mem.v2.1.cutover.v1', 'compatible', 'sha256:corpus', 'sha256:gates', '{}'::jsonb
			)
		`)
		return err
	}))

	require.NoError(t, goose.SetDialect("postgres"))
	err := goose.UpToContext(ctx, sqlDB, getMigrationsDir(), 2026072303)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "legacy api_keys missing canonical team_profiles")
	assert.True(t, tableExists(t, ctx, sqlDB, "api_keys"), "cleanup DDL must not run after guard failure")
}

// TestMigratorStatus verifies status command works.
func TestMigratorStatus(t *testing.T) {
	ctx := context.Background()

	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	m := NewMigratorWithDB(sqlDB)

	// Run status
	err := m.Status(ctx)
	// Status may write to stdout, but should not error
	assert.NoError(t, err, "Status should not error")
}

// TestOpenWithClient verifies the DB wrapper implements PostgresClient interface.
func TestOpenWithClient(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	cfg := &testConfig{dsn: dsn}

	client, err := OpenWithClient(ctx, cfg)
	require.NoError(t, err, "OpenWithClient should succeed")
	require.NotNil(t, client, "OpenWithClient should return a non-nil client")

	// Verify interface implementation
	var _ PostgresClient = client

	// Verify Ping works
	err = client.Ping(ctx)
	assert.NoError(t, err, "Ping should succeed")

	// Verify GetDB works
	db := client.GetDB()
	assert.NotNil(t, db, "GetDB should return non-nil db")

	// Verify Close works
	err = client.Close()
	assert.NoError(t, err, "Close should not error")
}

// TestDBPingTimeout verifies ping respects timeout.
func TestDBPingTimeout(t *testing.T) {
	ctx := context.Background()

	// Use an invalid host that will cause connection timeout
	cfg := &testConfig{dsn: "host=192.0.2.1 port=5432 user=test password=test dbname=test sslmode=disable connect_timeout=1"}

	// This tests that Open returns an error rather than hanging indefinitely
	start := time.Now()
	db, err := Open(ctx, cfg)
	elapsed := time.Since(start)

	assert.Error(t, err, "Open should return an error for unreachable postgres")
	assert.Nil(t, db, "Open should return nil db on error")
	// Should fail quickly (within 10 seconds due to connect_timeout)
	assert.Less(t, elapsed, 10*time.Second, "should fail quickly with connect timeout")
}

func openMigrationSQLDB(t *testing.T, ctx context.Context) (*sql.DB, func()) {
	t.Helper()
	dsn, cleanup := skipIfNoPostgres(t, ctx)
	if os.Getenv("DATABASE_URL") != "" {
		isolatedDSN, isolatedCleanup := createMigrationTestDatabase(t, ctx, dsn)
		dsn = isolatedDSN
		baseCleanup := cleanup
		cleanup = func() {
			isolatedCleanup()
			baseCleanup()
		}
	}
	cfg := &testConfig{dsn: dsn}
	db, err := Open(ctx, cfg)
	require.NoError(t, err, "Open should succeed")
	sqlDB, err := db.DB()
	require.NoError(t, err, "underlying sql.DB should be available")
	return sqlDB, func() {
		_ = sqlDB.Close()
		cleanup()
	}
}

func createMigrationTestDatabase(t *testing.T, ctx context.Context, dsn string) (string, func()) {
	t.Helper()
	adminDB, err := Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err, "Open should succeed")
	adminSQLDB, err := adminDB.DB()
	require.NoError(t, err, "underlying sql.DB should be available")

	dbName := "dense_mem_migration_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedName := quoteMigrationIdentifier(dbName)
	if _, err := adminSQLDB.ExecContext(ctx, "CREATE DATABASE "+quotedName+" TEMPLATE template0"); err != nil {
		closeErr := adminSQLDB.Close()
		if isPostgresInsufficientPrivilege(err) {
			if closeErr != nil {
				t.Errorf("close migration admin database after CREATE DATABASE privilege error: %v", closeErr)
			}
			t.Skipf("Postgres migration tests require CREATE DATABASE privilege for DATABASE_URL isolation: %v", err)
		}
		if closeErr != nil {
			t.Errorf("close migration admin database after CREATE DATABASE failure: %v", closeErr)
		}
		t.Fatalf("create migration test database %q: %v", dbName, err)
	}

	cleanup := func() {
		if _, err := adminSQLDB.ExecContext(ctx, `
			SELECT pg_terminate_backend(pid)
			FROM pg_stat_activity
			WHERE datname = $1
			  AND pid <> pg_backend_pid()
		`, dbName); err != nil {
			t.Errorf("terminate connections to migration test database %q: %v", dbName, err)
		}
		if _, err := adminSQLDB.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quotedName); err != nil {
			t.Errorf("drop migration test database %q: %v", dbName, err)
		}
		_ = adminSQLDB.Close()
	}
	return migrationDatabaseDSN(t, dsn, dbName), cleanup
}

func migrationDatabaseDSN(t *testing.T, dsn string, dbName string) string {
	t.Helper()
	config, err := pgconn.ParseConfig(dsn)
	require.NoError(t, err, "DATABASE_URL should be parseable")
	config.Database = dbName
	return migrationConnInfo(config)
}

func isPostgresInsufficientPrivilege(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501"
}

func migrationConnInfo(config *pgconn.Config) string {
	fields := make([]string, 0, 8+len(config.RuntimeParams))
	if config.Host != "" {
		fields = append(fields, "host="+quoteConnInfoValue(config.Host))
	}
	if config.Port != 0 {
		fields = append(fields, fmt.Sprintf("port=%d", config.Port))
	}
	fields = append(fields, "dbname="+quoteConnInfoValue(config.Database))
	if config.User != "" {
		fields = append(fields, "user="+quoteConnInfoValue(config.User))
	}
	if config.Password != "" {
		fields = append(fields, "password="+quoteConnInfoValue(config.Password))
	}
	if config.ConnectTimeout > 0 {
		fields = append(fields, fmt.Sprintf("connect_timeout=%d", int(config.ConnectTimeout.Seconds())))
	}
	if config.TLSConfig == nil {
		fields = append(fields, "sslmode=disable")
	}
	keys := make([]string, 0, len(config.RuntimeParams))
	for key := range config.RuntimeParams {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fields = append(fields, key+"="+quoteConnInfoValue(config.RuntimeParams[key]))
	}
	return strings.Join(fields, " ")
}

func quoteConnInfoValue(value string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value) + "'"
}

func quoteMigrationIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func TestMigrationDatabaseDSNUpdatesURL(t *testing.T) {
	dsn := "postgres://test%20user:pa%20ss@localhost:5433/old%20db?sslmode=disable&application_name=dense+mem"
	got := migrationDatabaseDSN(t, dsn, "new db")

	config, err := pgconn.ParseConfig(got)
	require.NoError(t, err)
	assert.Equal(t, "new db", config.Database)
	assert.Equal(t, "test user", config.User)
	assert.Equal(t, "pa ss", config.Password)
	assert.Equal(t, "localhost", config.Host)
	assert.Equal(t, uint16(5433), config.Port)
	assert.Equal(t, "dense mem", config.RuntimeParams["application_name"])
	assert.Nil(t, config.TLSConfig)
}

func TestMigrationDatabaseDSNPreservesQuotedConninfoValues(t *testing.T) {
	dsn := `host='localhost' user='test user' password='pa ss\'word' dbname='old db' sslmode=disable application_name='dense mem tests'`
	got := migrationDatabaseDSN(t, dsn, "new db")

	config, err := pgconn.ParseConfig(got)
	require.NoError(t, err)
	assert.Equal(t, "new db", config.Database)
	assert.Equal(t, "test user", config.User)
	assert.Equal(t, "pa ss'word", config.Password)
	assert.Equal(t, "localhost", config.Host)
	assert.Equal(t, "dense mem tests", config.RuntimeParams["application_name"])
	assert.Nil(t, config.TLSConfig)
}

func TestPostgresInsufficientPrivilegeDetection(t *testing.T) {
	assert.True(t, isPostgresInsufficientPrivilege(&pgconn.PgError{Code: "42501"}))
	assert.False(t, isPostgresInsufficientPrivilege(&pgconn.PgError{Code: "42P04"}))
	assert.False(t, isPostgresInsufficientPrivilege(errors.New("network failure")))
}

func runGooseUpTo(t *testing.T, ctx context.Context, db *sql.DB, version int64) {
	t.Helper()
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.UpToContext(ctx, db, getMigrationsDir(), version))
}

func execPostgresTxMode(ctx context.Context, db *sql.DB, txMode string, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tx_mode', $1, true)`, txMode); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.current_team_id', '', true)`); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.current_profile_id', '', true)`); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func tableExists(t *testing.T, ctx context.Context, db *sql.DB, tableName string) bool {
	t.Helper()
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = 'public'
			  AND table_name = $1
		)
	`, tableName).Scan(&exists)
	require.NoError(t, err)
	return exists
}

func columnExists(t *testing.T, ctx context.Context, db *sql.DB, tableName, columnName string) bool {
	t.Helper()
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = $1
			  AND column_name = $2
		)
	`, tableName, columnName).Scan(&exists)
	require.NoError(t, err)
	return exists
}

func insertV2MigrationTeamProfile(t *testing.T, ctx context.Context, db *sql.DB) (string, string) {
	t.Helper()
	teamID := uuid.NewString()
	profileID := uuid.NewString()
	keyPrefix := strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO teams (id, name, description, metadata, config)
			VALUES ($1::uuid, $2, '', '{}'::jsonb, '{}'::jsonb)
		`, teamID, "migration-team-"+teamID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO team_profiles (
				id, team_id, key_hash, key_prefix, key_suffix, name, scopes, role
			) VALUES (
				$1::uuid, $2::uuid, $3, $4, $5, $6, ARRAY['read','write']::text[], 'member'
			)
		`, profileID, teamID, "hash-"+profileID, keyPrefix, keyPrefix[:6], "migration-profile-"+profileID)
		return err
	}))
	return teamID, profileID
}

func insertV2MigrationAuthorityFixture(t *testing.T, ctx context.Context, db *sql.DB, teamID, profileID, authority string) {
	t.Helper()
	sourceID := uuid.NewString()
	sourceRevisionID := uuid.NewString()
	ingestID := uuid.NewString()
	fragmentID := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_team_refs (team_id)
			VALUES ($1::uuid)
			ON CONFLICT (team_id) DO NOTHING
		`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_profile_refs (team_id, profile_id)
			VALUES ($1::uuid, $2::uuid)
			ON CONFLICT (team_id, profile_id) DO NOTHING
		`, teamID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (team_id, ingest_id, owner_profile_id, status)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'queued')
		`, teamID, ingestID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_sources (team_id, source_id, owner_profile_id, source_key, source_kind, authority)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'document', $5)
		`, teamID, sourceID, profileID, "doc://migration-"+authority+"-"+sourceID, authority); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_source_revisions (
				team_id, source_revision_id, source_id, owner_profile_id, revision_token, content_hash
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid, 'rev-1', $5
			)
		`, teamID, sourceRevisionID, sourceID, profileID, "sha256:"+sourceRevisionID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id, source_id, source_revision_id,
				evidence_index, content, content_hash, source_type, authority
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid,
				0, 'migration authority fixture', $7, 'document', $8
			)
		`, teamID, fragmentID, ingestID, profileID, sourceID, sourceRevisionID, "sha256:"+fragmentID, authority)
		return err
	}))
}
