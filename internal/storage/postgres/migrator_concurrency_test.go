//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

func TestMigratorRunUpAsRuntimeRoleWithoutCreateRole(t *testing.T) {
	if os.Getenv("DATABASE_URL") != "" {
		t.Skip("requires a disposable PostgreSQL instance where the test can provision roles")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	adminDB, err := Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err)
	adminSQLDB, err := adminDB.DB()
	require.NoError(t, err)
	defer adminSQLDB.Close()

	_, err = adminSQLDB.ExecContext(ctx, `
		CREATE EXTENSION IF NOT EXISTS pgcrypto;
		CREATE EXTENSION IF NOT EXISTS vector;
		CREATE ROLE dense_mem_migration_database_owner
			NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
		CREATE ROLE dense_mem_migration_runtime
			LOGIN PASSWORD 'dense_mem_migration_runtime'
			NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
		DO $$
		BEGIN
			EXECUTE format(
				'ALTER DATABASE %I OWNER TO dense_mem_migration_database_owner',
				current_database()
			);
			EXECUTE format(
				'GRANT CONNECT, CREATE, TEMPORARY ON DATABASE %I TO dense_mem_migration_runtime',
				current_database()
			);
		END
		$$;
		GRANT USAGE, CREATE ON SCHEMA public TO dense_mem_migration_runtime;
	`)
	require.NoError(t, err)

	runtimeConfig, err := pgconn.ParseConfig(dsn)
	require.NoError(t, err)
	runtimeConfig.User = "dense_mem_migration_runtime"
	runtimeConfig.Password = "dense_mem_migration_runtime"
	runtimeDB, err := Open(ctx, &testConfig{dsn: migrationConnInfo(runtimeConfig)})
	require.NoError(t, err)
	sqlDB, err := runtimeDB.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	var isSuperuser, canCreateDatabase, canCreateRole, canReplicate, bypassesRLS bool
	var ownsDatabase, canCreateInDatabase, canCreateInSchema bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT
			role.rolsuper,
			role.rolcreatedb,
			role.rolcreaterole,
			role.rolreplication,
			role.rolbypassrls,
			pg_get_userbyid(database.datdba) = CURRENT_USER,
			has_database_privilege(CURRENT_USER, current_database(), 'CREATE'),
			has_schema_privilege(CURRENT_USER, 'public', 'CREATE')
		FROM pg_roles AS role
		JOIN pg_database AS database ON database.datname = current_database()
		WHERE role.rolname = CURRENT_USER
	`).Scan(
		&isSuperuser,
		&canCreateDatabase,
		&canCreateRole,
		&canReplicate,
		&bypassesRLS,
		&ownsDatabase,
		&canCreateInDatabase,
		&canCreateInSchema,
	))
	require.False(t, isSuperuser)
	require.False(t, canCreateDatabase)
	require.False(t, canCreateRole)
	require.False(t, canReplicate)
	require.False(t, bypassesRLS)
	require.False(t, ownsDatabase)
	require.True(t, canCreateInDatabase)
	require.True(t, canCreateInSchema)

	require.NoError(t, NewMigratorWithDB(sqlDB).RunUp(ctx))

	var migrationApplied bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM goose_db_version
			WHERE version_id = 2026080603
			  AND is_applied
		)
	`).Scan(&migrationApplied))
	require.True(t, migrationApplied)

	var dedicatedRoleExists bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_roles
			WHERE rolname = 'dense_mem_portal_session_system'
		)
	`).Scan(&dedicatedRoleExists))
	require.False(t, dedicatedRoleExists)

	var portalSessionFunctionCount int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_proc AS procedure
		JOIN pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
		WHERE namespace.nspname = 'public'
		  AND procedure.proname IN (
			'dense_mem_portal_session_create',
			'dense_mem_portal_session_get',
			'dense_mem_portal_session_delete',
			'dense_mem_portal_session_delete_expired'
		  )
	`).Scan(&portalSessionFunctionCount))
	require.Zero(t, portalSessionFunctionCount)

	var unownedApplicationRelations int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_class AS class
		JOIN pg_namespace AS namespace ON namespace.oid = class.relnamespace
		WHERE namespace.nspname = 'public'
		  AND class.relkind IN ('r', 'p', 'S', 'v', 'm')
		  AND pg_get_userbyid(class.relowner) <> CURRENT_USER
	`).Scan(&unownedApplicationRelations))
	require.Zero(t, unownedApplicationRelations)
}
