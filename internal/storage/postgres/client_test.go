package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testConfig struct {
	dsn string
}

func (c *testConfig) GetPostgresDSN() string {
	return c.dsn
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

func TestOpenFailsOnUnreachable(t *testing.T) {
	ctx := context.Background()
	cfg := &testConfig{dsn: "host=192.0.2.1 port=5432 user=test password=test dbname=test sslmode=disable connect_timeout=1"}

	db, err := Open(ctx, cfg)
	assert.Error(t, err, "Open should return an error for unreachable postgres")
	assert.Nil(t, db, "Open should return nil db on error")
	assert.Contains(t, err.Error(), "failed to", "error should indicate failure")
}

func TestOpenFailsOnEmptyDSN(t *testing.T) {
	ctx := context.Background()
	cfg := &testConfig{dsn: ""}

	db, err := Open(ctx, cfg)
	assert.Error(t, err, "Open should return an error for empty DSN")
	assert.Nil(t, db, "Open should return nil db on error")
	assert.Contains(t, err.Error(), "DSN is empty", "error should indicate empty DSN")
}

func TestDBPingTimeout(t *testing.T) {
	ctx := context.Background()
	cfg := &testConfig{dsn: "host=192.0.2.1 port=5432 user=test password=test dbname=test sslmode=disable connect_timeout=1"}

	start := time.Now()
	db, err := Open(ctx, cfg)
	elapsed := time.Since(start)

	assert.Error(t, err, "Open should return an error for unreachable postgres")
	assert.Nil(t, db, "Open should return nil db on error")
	assert.Less(t, elapsed, 10*time.Second, "should fail quickly with connect timeout")
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
