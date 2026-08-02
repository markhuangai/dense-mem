//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMigratorRunUpSerializesConcurrentStartup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()
	if os.Getenv("DATABASE_URL") != "" {
		isolatedDSN, isolatedCleanup := createMigrationTestDatabase(t, ctx, dsn)
		dsn = isolatedDSN
		defer isolatedCleanup()
	}

	dbs := make([]*sql.DB, 2)
	for i := range dbs {
		db, err := Open(ctx, &testConfig{dsn: dsn})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		dbs[i] = sqlDB
		defer sqlDB.Close()
	}

	start := make(chan struct{})
	errs := make(chan error, len(dbs))
	var ready sync.WaitGroup
	ready.Add(len(dbs))
	for _, db := range dbs {
		go func(sqlDB *sql.DB) {
			ready.Done()
			<-start
			errs <- NewMigratorWithDB(sqlDB).RunUp(ctx)
		}(db)
	}
	ready.Wait()
	close(start)
	for range dbs {
		require.NoError(t, <-errs)
	}

	var duplicateVersions int
	require.NoError(t, dbs[0].QueryRowContext(ctx, `
		SELECT count(*)
		FROM (
			SELECT version_id
			FROM goose_db_version
			WHERE is_applied
			GROUP BY version_id
			HAVING count(*) > 1
		) AS duplicates
	`).Scan(&duplicateVersions))
	require.Zero(t, duplicateVersions)
}
