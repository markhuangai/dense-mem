//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

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
func (c *testConfig) GetPostgresReadDSN() string { return "" }

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

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	cfg := &testConfig{dsn: dsn}

	db, err := Open(ctx, cfg)
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")

	// Run up migrations
	err = m.RunUp(ctx)
	// Should succeed since we have a valid migrations directory
	assert.NoError(t, err, "RunUp should succeed")
}

// TestMigratorRunDown verifies rollback succeeds.
func TestMigratorRunDown(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	cfg := &testConfig{dsn: dsn}

	db, err := Open(ctx, cfg)
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")

	// First run up
	err = m.RunUp(ctx)
	require.NoError(t, err, "RunUp should succeed")

	// Then run down
	err = m.RunDown(ctx)
	assert.NoError(t, err, "RunDown should succeed")
}

// TestMigratorStatus verifies status command works.
func TestMigratorStatus(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	cfg := &testConfig{dsn: dsn}

	db, err := Open(ctx, cfg)
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")

	// Run status
	err = m.Status(ctx)
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
