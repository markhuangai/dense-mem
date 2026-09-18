//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const (
	migrationControlRetirementBaseVersion        int64 = 20260913010001
	migrationControlRetirementPredecessorVersion int64 = 20260913020001
	migrationControlRetirementVersion            int64 = 20260917010001
)

var migrationControlRetirementTables = []string{
	"v2_migration_runs",
	"v2_migration_corpus_items",
	"v2_migration_source_maps",
	"v2_migration_checkpoints",
	"v2_migration_errors",
	"v2_migration_exclusions",
	"v2_migration_gate_results",
	"v2_migration_operator_actions",
}

func TestMigrationControlRetirementRequiresApprovedOperationalPreflight(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE public.v2_migration_runs
			   SET preflight_approved = false
		`)
		return err
	}))

	err := migrationUpTo(ctx, sqlDB, migrationControlRetirementVersion)
	require.Error(t, err)
	require.Contains(t, err.Error(), "approved detached-release retirement preflight")
	require.False(t, migrationControlRetirementApplied(t, ctx, sqlDB))
	for _, table := range migrationControlRetirementTables {
		require.True(t, tableExists(t, ctx, sqlDB, table), "%s must remain after preflight rejection", table)
	}
}

func TestMigrationControlRetirementRejectsUnboundOperatorMetadata(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE v2_migration_operator_actions
			   SET metadata = '{"approved_commit":"fixture"}'::jsonb
			 WHERE action = 'retire_migration_control'
		`)
		return err
	}))

	err := migrationUpTo(ctx, sqlDB, migrationControlRetirementVersion)
	require.Error(t, err)
	require.Contains(t, err.Error(), "operator authorization is incomplete")
	require.False(t, migrationControlRetirementApplied(t, ctx, sqlDB))
	for _, table := range migrationControlRetirementTables {
		require.True(t, tableExists(t, ctx, sqlDB, table), "%s must remain after metadata rejection", table)
	}
}

func TestMigrationControlRetirementRejectsHiddenLineageColumnWithNOBYPASSRLS(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementPredecessorVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `ALTER TABLE public.knowledge_ingests ADD COLUMN migration_run_id uuid`)
		return err
	}))

	roleName := "dense_mem_migration_control_retirement_hidden_column_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedRole := quoteMigrationIdentifier(roleName)
	if _, err := sqlDB.ExecContext(ctx, "CREATE ROLE "+quotedRole+" NOLOGIN NOSUPERUSER NOBYPASSRLS"); err != nil {
		if isPostgresInsufficientPrivilege(err) {
			t.Skipf("migration retirement hidden-column RLS test requires role administration: %v", err)
		}
		require.NoError(t, err)
	}
	defer func() {
		_, _ = sqlDB.ExecContext(ctx, "RESET ROLE")
		_, _ = sqlDB.ExecContext(ctx, "REASSIGN OWNED BY "+quotedRole+" TO CURRENT_USER")
		_, _ = sqlDB.ExecContext(ctx, "DROP OWNED BY "+quotedRole)
		_, _ = sqlDB.ExecContext(ctx, "DROP ROLE IF EXISTS "+quotedRole)
	}()

	for _, statement := range []string{
		"GRANT USAGE, CREATE ON SCHEMA public TO " + quotedRole,
		"ALTER TABLE public.goose_db_version OWNER TO " + quotedRole,
		"ALTER TABLE public.v2_compatibility_markers OWNER TO " + quotedRole,
	} {
		require.NoError(t, func() error {
			_, err := sqlDB.ExecContext(ctx, statement)
			return err
		}(), statement)
	}
	for _, table := range migrationControlRetirementTables {
		require.NoError(t, func() error {
			_, err := sqlDB.ExecContext(ctx, "ALTER TABLE public."+table+" OWNER TO "+quotedRole)
			return err
		}(), table)
	}

	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	_, err := sqlDB.ExecContext(ctx, "SET ROLE "+quotedRole)
	require.NoError(t, err)
	err = migrationUpTo(ctx, sqlDB, migrationControlRetirementVersion)
	_, resetErr := sqlDB.ExecContext(ctx, "RESET ROLE")
	require.NoError(t, resetErr)

	require.Error(t, err)
	require.Contains(t, err.Error(), "knowledge_ingests.migration_run_id is still present")
	require.False(t, migrationControlRetirementApplied(t, ctx, sqlDB))
	for _, table := range migrationControlRetirementTables {
		require.True(t, tableExists(t, ctx, sqlDB, table), "%s must remain after a hidden-column rejection", table)
	}
}

func TestMigrationControlRetirementDropsOnlyApprovedTablesAndPreservesMarker(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)
	retainedTeamID, _ := insertMigrationTeamProfile(t, ctx, sqlDB)
	beforeCounts := migrationControlRetirementTableCounts(t, ctx, sqlDB)
	for _, table := range migrationControlRetirementTables {
		want := int64(1)
		if table == "v2_migration_gate_results" {
			want = 5
		}
		require.Equal(t, want, beforeCounts[table], "%s should contain the fixture row(s)", table)
	}
	markerBefore := migrationControlRetirementAllMarkerSnapshot(t, ctx, sqlDB)
	retainedDataBefore := migrationControlRetirementRetainedDataSnapshot(t, ctx, sqlDB)
	retainedTeamNameBefore := migrationControlRetirementTeamName(t, ctx, sqlDB, retainedTeamID)
	ownedObjectsBefore := migrationControlRetirementOwnedObjectCounts(t, ctx, sqlDB)

	migrator := NewMigratorWithDB(sqlDB)
	require.NoError(t, migrator.RunMigrationControlRetirement(ctx))

	for _, table := range migrationControlRetirementTables {
		require.False(t, tableExists(t, ctx, sqlDB, table), "%s must be retired", table)
	}
	require.True(t, tableExists(t, ctx, sqlDB, "v2_compatibility_markers"))
	require.True(t, tableExists(t, ctx, sqlDB, "teams"))
	require.True(t, tableExists(t, ctx, sqlDB, "review_tasks"))
	require.Equal(t, markerBefore, migrationControlRetirementAllMarkerSnapshot(t, ctx, sqlDB))
	require.Equal(t, retainedDataBefore, migrationControlRetirementRetainedDataSnapshot(t, ctx, sqlDB))
	require.Equal(t, retainedTeamNameBefore, migrationControlRetirementTeamName(t, ctx, sqlDB, retainedTeamID))

	ownedObjectsAfter := migrationControlRetirementOwnedObjectCounts(t, ctx, sqlDB)
	for name, count := range ownedObjectsBefore {
		if name != "sequences" {
			require.NotZero(t, count, "%s should have table-owned objects in the fixture schema", name)
		}
		require.Zero(t, ownedObjectsAfter[name], "%s should be removed with its retired table", name)
	}
	require.True(t, migrationControlRetirementApplied(t, ctx, sqlDB))

	err := migrationDown(ctx, sqlDB)
	require.Error(t, err)
	require.Contains(t, err.Error(), "20260917010001 is irreversible")
	require.True(t, migrationControlRetirementApplied(t, ctx, sqlDB))
}

func TestMigrationControlRetirementRejectsChangedApprovedRowCounts(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO v2_migration_errors (run_id, phase, error_code, message)
			SELECT run_id, 'retirement', 'late-row', 'added after the approved snapshot'
			  FROM v2_migration_runs
			 ORDER BY updated_at DESC, run_id DESC
			 LIMIT 1
		`)
		return err
	}))

	err := migrationUpTo(ctx, sqlDB, migrationControlRetirementVersion)
	require.Error(t, err)
	require.Contains(t, err.Error(), "approved row-count snapshot differs for v2_migration_errors")
	require.False(t, migrationControlRetirementApplied(t, ctx, sqlDB))
	for _, table := range migrationControlRetirementTables {
		require.True(t, tableExists(t, ctx, sqlDB, table), "%s must remain after row-count rejection", table)
	}
}

func TestMigrationControlRetirementRunsWithNOBYPASSRLS(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	// Apply the ordered migration immediately before retirement while still
	// using the bootstrap owner; the RLS role owns only the retirement boundary.
	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementPredecessorVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)
	markerBefore := migrationControlRetirementAllMarkerSnapshot(t, ctx, sqlDB)

	roleName := "dense_mem_migration_control_retirement_rls_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedRole := quoteMigrationIdentifier(roleName)
	if _, err := sqlDB.ExecContext(ctx, "CREATE ROLE "+quotedRole+" NOLOGIN NOSUPERUSER NOBYPASSRLS"); err != nil {
		if isPostgresInsufficientPrivilege(err) {
			t.Skipf("migration retirement RLS test requires role administration: %v", err)
		}
		require.NoError(t, err)
	}
	defer func() {
		_, _ = sqlDB.ExecContext(ctx, "RESET ROLE")
		_, _ = sqlDB.ExecContext(ctx, "REASSIGN OWNED BY "+quotedRole+" TO CURRENT_USER")
		_, _ = sqlDB.ExecContext(ctx, "DROP OWNED BY "+quotedRole)
		_, _ = sqlDB.ExecContext(ctx, "DROP ROLE IF EXISTS "+quotedRole)
	}()

	for _, statement := range []string{
		"GRANT USAGE, CREATE ON SCHEMA public TO " + quotedRole,
		"ALTER TABLE public.goose_db_version OWNER TO " + quotedRole,
		"ALTER TABLE public.v2_compatibility_markers OWNER TO " + quotedRole,
	} {
		require.NoError(t, func() error {
			_, err := sqlDB.ExecContext(ctx, statement)
			return err
		}(), statement)
	}
	for _, table := range migrationControlRetirementTables {
		require.NoError(t, func() error {
			_, err := sqlDB.ExecContext(ctx, "ALTER TABLE public."+table+" OWNER TO "+quotedRole)
			return err
		}(), table)
	}

	// Keep SET ROLE and the retirement migration on one pooled connection so
	// the migration runs with the same FORCE-RLS NOBYPASSRLS owner.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	_, err := sqlDB.ExecContext(ctx, "SET ROLE "+quotedRole)
	require.NoError(t, err)
	require.NoError(t, migrationUpTo(ctx, sqlDB, migrationControlRetirementVersion))
	_, err = sqlDB.ExecContext(ctx, "RESET ROLE")
	require.NoError(t, err)

	for _, table := range migrationControlRetirementTables {
		require.False(t, tableExists(t, ctx, sqlDB, table), "%s must be retired", table)
	}
	require.Equal(t, markerBefore, migrationControlRetirementAllMarkerSnapshot(t, ctx, sqlDB))
}

func TestMigrationControlRetirementRequiresCompatibleMarker(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(context.Context, *sql.DB) error
		needle string
	}{
		{
			name: "missing marker",
			mutate: func(ctx context.Context, db *sql.DB) error {
				return execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `
						DELETE FROM v2_compatibility_markers
						WHERE marker_kind = 'v2_cutover'
						  AND version = 'dense-mem.v2.6.1.cutover.v1'
					`)
					return err
				})
			},
			needle: "latest compatible v2.6.1 cutover marker is required",
		},
		{
			name: "incompatible marker",
			mutate: func(ctx context.Context, db *sql.DB) error {
				return execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `
						UPDATE v2_compatibility_markers
						SET status = 'incompatible'
						WHERE marker_kind = 'v2_cutover'
						  AND version = 'dense-mem.v2.6.1.cutover.v1'
					`)
					return err
				})
			},
			needle: "latest compatible v2.6.1 cutover marker is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			sqlDB, cleanup := openMigrationSQLDB(t, ctx)
			t.Cleanup(cleanup)
			runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
			require.NoError(t, tt.mutate(ctx, sqlDB))

			err := migrationUpTo(ctx, sqlDB, migrationControlRetirementVersion)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.needle)
			require.False(t, migrationControlRetirementApplied(t, ctx, sqlDB))
			for _, table := range migrationControlRetirementTables {
				require.True(t, tableExists(t, ctx, sqlDB, table), "%s must remain after a rejected retirement", table)
			}
		})
	}
}

func TestMigrationControlRetirementRejectsMissingDetachmentHistory(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	t.Cleanup(cleanup)
	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			DELETE FROM goose_db_version
			WHERE version_id = 20260913010001 AND is_applied
		`)
		return err
	}))

	err := runMigrationControlRetirementOnly(t, ctx, sqlDB)
	require.Error(t, err)
	require.Contains(t, err.Error(), "detached release 20260913010001 is not applied")
	require.False(t, migrationControlRetirementApplied(t, ctx, sqlDB))
	for _, table := range migrationControlRetirementTables {
		require.True(t, tableExists(t, ctx, sqlDB, table))
	}
}

func TestMigrationControlRetirementRejectsIncompleteDetachment(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `ALTER TABLE public.knowledge_ingests ADD COLUMN migration_run_id uuid`)
		return err
	}))

	err := migrationUpTo(ctx, sqlDB, migrationControlRetirementVersion)
	require.Error(t, err)
	require.Contains(t, err.Error(), "knowledge_ingests.migration_run_id is still present")
	require.False(t, migrationControlRetirementApplied(t, ctx, sqlDB))
	for _, table := range migrationControlRetirementTables {
		require.True(t, tableExists(t, ctx, sqlDB, table))
	}
}

func TestMigrationControlRetirementRejectsAlteredTarget(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			ALTER TABLE public.v2_migration_runs
			ADD COLUMN retirement_unexpected_column text NOT NULL DEFAULT 'unexpected'
		`)
		return err
	}))

	err := migrationUpTo(ctx, sqlDB, migrationControlRetirementVersion)
	require.Error(t, err)
	require.Contains(t, err.Error(), "target column inventory mismatch")
	require.False(t, migrationControlRetirementApplied(t, ctx, sqlDB))
	require.True(t, tableExists(t, ctx, sqlDB, "v2_migration_runs"))
}

func TestMigrationControlRetirementRejectsAlteredIndex(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DROP INDEX public.v2_migration_corpus_run_ingest_idx`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			CREATE INDEX v2_migration_corpus_run_ingest_idx
			    ON public.v2_migration_corpus_items(run_id, team_id)
			    WHERE ingest_id IS NOT NULL
		`)
		return err
	}))

	err := migrationUpTo(ctx, sqlDB, migrationControlRetirementVersion)
	require.Error(t, err)
	require.Contains(t, err.Error(), "target index definition mismatch")
	require.False(t, migrationControlRetirementApplied(t, ctx, sqlDB))
	require.True(t, tableExists(t, ctx, sqlDB, "v2_migration_corpus_items"))
}

func TestMigrationControlRetirementRejectsInheritance(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	t.Cleanup(cleanup)
	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			CREATE TABLE public.migration_retirement_unexpected_child ()
			INHERITS (public.v2_migration_runs)
		`)
		return err
	}))
	t.Cleanup(func() {
		require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS public.migration_retirement_unexpected_child`)
			return err
		}))
	})

	err := migrationUpTo(ctx, sqlDB, migrationControlRetirementVersion)
	require.Error(t, err)
	require.Contains(t, err.Error(), "participate in inheritance")
	require.False(t, migrationControlRetirementApplied(t, ctx, sqlDB))
	require.True(t, tableExists(t, ctx, sqlDB, "v2_migration_runs"))
}

func TestMigrationControlRetirementRejectsMissingTarget(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DROP TABLE public.v2_migration_operator_actions RESTRICT`)
		return err
	}))

	err := migrationUpTo(ctx, sqlDB, migrationControlRetirementVersion)
	require.Error(t, err)
	require.Contains(t, err.Error(), "v2_migration_operator_actions")
	require.False(t, migrationControlRetirementApplied(t, ctx, sqlDB))
}

func TestMigrationControlRetirementRejectsUnexpectedDependenciesAndRollsBack(t *testing.T) {
	tests := []struct {
		name    string
		install func(context.Context, *sql.DB) error
		remove  func(context.Context, *sql.DB) error
		needle  string
	}{
		{
			name: "foreign key",
			install: func(ctx context.Context, db *sql.DB) error {
				return execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `
						CREATE TABLE public.migration_retirement_unexpected_fk (
							id uuid PRIMARY KEY,
							run_id uuid REFERENCES public.v2_migration_runs(run_id)
						)
					`)
					return err
				})
			},
			remove: func(ctx context.Context, db *sql.DB) error {
				return execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS public.migration_retirement_unexpected_fk`)
					return err
				})
			},
			needle: "unexpected cross-boundary foreign keys",
		},
		{
			name: "view",
			install: func(ctx context.Context, db *sql.DB) error {
				return execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `
						CREATE VIEW public.migration_retirement_unexpected_view AS
						SELECT run_id FROM public.v2_migration_runs
					`)
					return err
				})
			},
			remove: func(ctx context.Context, db *sql.DB) error {
				return execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `DROP VIEW IF EXISTS public.migration_retirement_unexpected_view`)
					return err
				})
			},
			needle: "unexpected dependency on retired tables",
		},
		{
			name: "function",
			install: func(ctx context.Context, db *sql.DB) error {
				return execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `
						CREATE FUNCTION public.migration_retirement_unexpected_function()
						RETURNS bigint
						LANGUAGE sql
						AS $$ SELECT count(*) FROM public.v2_migration_runs $$
					`)
					return err
				})
			},
			remove: func(ctx context.Context, db *sql.DB) error {
				return execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `DROP FUNCTION IF EXISTS public.migration_retirement_unexpected_function()`)
					return err
				})
			},
			needle: "unexpected retained definition references",
		},
		{
			name: "trigger",
			install: func(ctx context.Context, db *sql.DB) error {
				return execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, `
						CREATE FUNCTION public.migration_retirement_unexpected_trigger_function()
						RETURNS trigger
						LANGUAGE plpgsql
						AS $$ BEGIN PERFORM count(*) FROM public.v2_migration_runs; RETURN NEW; END $$
					`); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `
						CREATE TRIGGER migration_retirement_unexpected_trigger
						AFTER INSERT ON public.teams
						FOR EACH ROW EXECUTE FUNCTION public.migration_retirement_unexpected_trigger_function()
					`)
					return err
				})
			},
			remove: func(ctx context.Context, db *sql.DB) error {
				return execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS migration_retirement_unexpected_trigger ON public.teams`); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `DROP FUNCTION IF EXISTS public.migration_retirement_unexpected_trigger_function()`)
					return err
				})
			},
			needle: "unexpected retained definition references",
		},
		{
			name: "owned trigger",
			install: func(ctx context.Context, db *sql.DB) error {
				return execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, `
						CREATE FUNCTION public.migration_retirement_unexpected_owned_trigger_function()
						RETURNS trigger
						LANGUAGE plpgsql
						AS $$ BEGIN RETURN NEW; END $$
					`); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `
						CREATE TRIGGER migration_retirement_unexpected_owned_trigger
						AFTER INSERT ON public.v2_migration_runs
						FOR EACH ROW EXECUTE FUNCTION public.migration_retirement_unexpected_owned_trigger_function()
					`)
					return err
				})
			},
			remove: func(ctx context.Context, db *sql.DB) error {
				return execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS migration_retirement_unexpected_owned_trigger ON public.v2_migration_runs`); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `DROP FUNCTION IF EXISTS public.migration_retirement_unexpected_owned_trigger_function()`)
					return err
				})
			},
			needle: "unexpected trigger owned by a retired table",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			sqlDB, cleanup := openMigrationSQLDB(t, ctx)
			runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
			seedMigrationControlRetirementFixture(t, ctx, sqlDB)
			require.NoError(t, tt.install(ctx, sqlDB))
			t.Cleanup(cleanup)
			t.Cleanup(func() {
				require.NoError(t, tt.remove(ctx, sqlDB))
			})

			err := migrationUpTo(ctx, sqlDB, migrationControlRetirementVersion)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.needle)
			require.False(t, migrationControlRetirementApplied(t, ctx, sqlDB))
			require.True(t, tableExists(t, ctx, sqlDB, "v2_migration_runs"))
		})
	}
}

func TestMigrationControlRetirementLockFailureRollsBackAndCanRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)
	sqlDB.SetMaxOpenConns(4)

	blockerConn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	blockerTx, err := blockerConn.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = blockerTx.ExecContext(ctx, `LOCK TABLE public.v2_migration_runs IN ROW EXCLUSIVE MODE`)
	require.NoError(t, err)

	err = migrationUpTo(ctx, sqlDB, migrationControlRetirementVersion)
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "lock")
	require.False(t, migrationControlRetirementApplied(t, ctx, sqlDB))
	for _, table := range migrationControlRetirementTables {
		require.True(t, tableExists(t, ctx, sqlDB, table), "%s must remain after lock failure", table)
	}

	require.NoError(t, blockerTx.Rollback())
	require.NoError(t, blockerConn.Close())
	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementVersion)
	require.False(t, tableExists(t, ctx, sqlDB, "v2_migration_runs"))
}

func TestMigrationControlRetirementFailureAfterFirstDropRollsBack(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			CREATE TABLE public.migration_retirement_failure_state (drop_count integer NOT NULL)
		`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO public.migration_retirement_failure_state (drop_count) VALUES (0)
		`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			CREATE FUNCTION public.migration_retirement_fail_after_first_drop()
			RETURNS event_trigger
			LANGUAGE plpgsql
			AS $$
			DECLARE
				current_count integer;
			BEGIN
				IF TG_TAG = 'DROP TABLE' THEN
					UPDATE public.migration_retirement_failure_state
					SET drop_count = drop_count + 1
					RETURNING drop_count INTO current_count;
					IF current_count >= 2 THEN
						RAISE EXCEPTION 'retirement fixture failure after first drop';
					END IF;
				END IF;
			END
			$$
		`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			CREATE EVENT TRIGGER migration_retirement_failure_trigger
			ON ddl_command_end
			EXECUTE FUNCTION public.migration_retirement_fail_after_first_drop()
		`)
		return err
	}))

	err := migrationUpTo(ctx, sqlDB, migrationControlRetirementVersion)
	require.Error(t, err)
	require.Contains(t, err.Error(), "retirement fixture failure after first drop")
	require.False(t, migrationControlRetirementApplied(t, ctx, sqlDB))
	for _, table := range migrationControlRetirementTables {
		require.True(t, tableExists(t, ctx, sqlDB, table), "%s must be restored by transaction rollback", table)
	}
	var dropCount int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT drop_count FROM migration_retirement_failure_state`).Scan(&dropCount))
	require.Zero(t, dropCount, "the fixture counter update must roll back with the failed migration")

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DROP EVENT TRIGGER migration_retirement_failure_trigger`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DROP FUNCTION public.migration_retirement_fail_after_first_drop()`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DROP TABLE public.migration_retirement_failure_state`)
		return err
	}))
}

func TestMigrationControlRetirementBackupRestoreRecreatesPopulatedPreRetirementState(t *testing.T) {
	if os.Getenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS") != "1" {
		t.Skip("set DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 to run the disposable backup/restore rehearsal")
	}
	ctx := context.Background()
	container, sourceDB, sourceDSN, cleanup := openMigrationControlRetirementBackupContainer(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sourceDB, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, sourceDB)

	beforeCounts := migrationControlRetirementTableCounts(t, ctx, sourceDB)
	beforeMarkers := migrationControlRetirementAllMarkerSnapshot(t, ctx, sourceDB)
	beforeCatalog := migrationControlRetirementCatalogSignature(t, ctx, sourceDB)

	containerExec(t, ctx, container, []string{
		"sh", "-ec",
		"pg_dump --format=custom --no-owner --no-privileges --file=/tmp/dense-mem-retirement.dump --username=testuser --dbname=testdb",
	})
	containerExec(t, ctx, container, []string{
		"sh", "-ec",
		"createdb --username=testuser --owner=testuser dense_mem_retirement_restore",
	})
	containerExec(t, ctx, container, []string{
		"sh", "-ec",
		"pg_restore --exit-on-error --no-owner --no-privileges --username=testuser --dbname=dense_mem_retirement_restore /tmp/dense-mem-retirement.dump",
	})

	restoreDB := openRetirementBackupDatabase(t, ctx, sourceDSN, "dense_mem_retirement_restore")
	restoreSQLDB, err := restoreDB.DB()
	require.NoError(t, err)
	require.Equal(t, beforeCounts, migrationControlRetirementTableCounts(t, ctx, restoreSQLDB))
	require.Equal(t, beforeMarkers, migrationControlRetirementAllMarkerSnapshot(t, ctx, restoreSQLDB))
	require.Equal(t, beforeCatalog, migrationControlRetirementCatalogSignature(t, ctx, restoreSQLDB))
	require.NoError(t, restoreSQLDB.Close())

	containerExec(t, ctx, container, []string{
		"sh", "-ec",
		"dropdb --if-exists --username=testuser dense_mem_retirement_restore",
	})
}

func TestMigrationControlRetirementRejectsOlderNodeAfterUpgrade(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)
	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementVersion)

	oldMigrationsDir := t.TempDir()
	oldReleaseDir := filepath.Join(oldMigrationsDir, "v2_6")
	require.NoError(t, os.MkdirAll(oldReleaseDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(oldReleaseDir, "20260913010001_detach_migration_control.sql"), []byte(`-- +goose Up
SELECT 1;
-- +goose Down
SELECT 1;
`), 0o644))

	state, err := ClassifyMigrationState(ctx, sqlDB, oldMigrationsDir)
	require.NoError(t, err)
	require.Equal(t, MigrationStateInvalid, state.Kind)
	require.Equal(t, migrationControlRetirementVersion, state.DatabaseLatest)
	require.Equal(t, migrationControlRetirementBaseVersion, state.RepositoryLatest)
	require.Equal(t, "database migration is newer than this binary", state.Reason)
	require.Error(t, ValidateStartupMigrationState(ctx, sqlDB, oldMigrationsDir))
}

func migrationControlRetirementAllMarkerSnapshot(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var snapshot string
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT COALESCE(string_agg(
				format('%s|%s|%s|%s|%s|%s|%s|%s|%s',
				       marker_id::text, marker_kind, version, status,
				       COALESCE(run_id::text, ''), corpus_hash, gate_report_hash,
				       metadata::text, created_at::text),
				E'\n' ORDER BY created_at, marker_id), '')
			FROM v2_compatibility_markers
		`).Scan(&snapshot)
	}))
	return snapshot
}

func migrationControlRetirementRetainedDataSnapshot(t *testing.T, ctx context.Context, db *sql.DB) map[string]int64 {
	t.Helper()
	tables := []string{
		"teams",
		"actor_identities",
		"team_memberships",
		"credentials",
		"ownership_aliases",
		"review_tasks",
		"search_documents",
		"operation_logs",
	}
	counts := make(map[string]int64, len(tables))
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		for _, table := range tables {
			var count int64
			if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM public.%s", table)).Scan(&count); err != nil {
				return err
			}
			counts[table] = count
		}
		return nil
	}))
	return counts
}

func migrationControlRetirementTeamName(t *testing.T, ctx context.Context, db *sql.DB, teamID string) string {
	t.Helper()
	var name string
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT name FROM teams WHERE id = $1::uuid`, teamID).Scan(&name)
	}))
	return name
}

func migrationControlRetirementCatalogSignature(t *testing.T, ctx context.Context, db *sql.DB) map[string]string {
	t.Helper()
	queries := map[string]string{
		"columns": `
			SELECT COALESCE(string_agg(
				format('%s|%s|%s|%s|%s|%s',
				       table_row.relname, attribute_row.attnum, attribute_row.attname,
				       format_type(attribute_row.atttypid, attribute_row.atttypmod),
				       attribute_row.attnotnull,
				       COALESCE(pg_get_expr(default_row.adbin, default_row.adrelid), '')),
				E'\n' ORDER BY table_row.relname, attribute_row.attnum), '')
			FROM pg_class AS table_row
			JOIN pg_namespace AS namespace_row ON namespace_row.oid = table_row.relnamespace
			JOIN pg_attribute AS attribute_row ON attribute_row.attrelid = table_row.oid
			LEFT JOIN pg_attrdef AS default_row
			  ON default_row.adrelid = attribute_row.attrelid
			 AND default_row.adnum = attribute_row.attnum
			WHERE namespace_row.nspname = 'public'
			  AND table_row.relname = ANY($1::text[])
			  AND attribute_row.attnum > 0
			  AND NOT attribute_row.attisdropped
		`,
		"constraints": `
			SELECT COALESCE(string_agg(
				format('%s|%s|%s|%s', source_table.relname, constraint_row.conname,
				       constraint_row.contype, pg_get_constraintdef(constraint_row.oid)),
				E'\n' ORDER BY source_table.relname, constraint_row.conname), '')
			FROM pg_constraint AS constraint_row
			JOIN pg_class AS source_table ON source_table.oid = constraint_row.conrelid
			JOIN pg_namespace AS source_namespace ON source_namespace.oid = source_table.relnamespace
			WHERE source_namespace.nspname = 'public'
			  AND source_table.relname = ANY($1::text[])
		`,
		"indexes": `
			SELECT COALESCE(string_agg(
				format('%s|%s|%s', tablename, indexname, indexdef),
				E'\n' ORDER BY tablename, indexname), '')
			FROM pg_indexes
			WHERE schemaname = 'public' AND tablename = ANY($1::text[])
		`,
		"policies": `
			SELECT COALESCE(string_agg(
				format('%s|%s|%s|%s|%s|%s|%s', schemaname, tablename, policyname,
				       cmd, roles::text, COALESCE(qual, ''), COALESCE(with_check, '')),
				E'\n' ORDER BY tablename, policyname), '')
			FROM pg_policies
			WHERE schemaname = 'public' AND tablename = ANY($1::text[])
		`,
		"relations": `
			SELECT COALESCE(string_agg(
				format('%s|%s|%s|%s|%s', table_row.relname, table_row.relkind,
				       table_row.relpersistence, table_row.relrowsecurity,
				       table_row.relforcerowsecurity),
				E'\n' ORDER BY table_row.relname), '')
			FROM pg_class AS table_row
			JOIN pg_namespace AS namespace_row ON namespace_row.oid = table_row.relnamespace
			WHERE namespace_row.nspname = 'public' AND table_row.relname = ANY($1::text[])
		`,
	}
	values := make(map[string]string, len(queries))
	arrayValue := "{" + strings.Join(migrationControlRetirementTables, ",") + "}"
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		for name, query := range queries {
			var value string
			if err := tx.QueryRowContext(ctx, query, arrayValue).Scan(&value); err != nil {
				return err
			}
			values[name] = value
		}
		return nil
	}))
	return values
}

func migrationControlRetirementTableCounts(t *testing.T, ctx context.Context, db *sql.DB) map[string]int64 {
	t.Helper()
	counts := make(map[string]int64, len(migrationControlRetirementTables))
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		for _, table := range migrationControlRetirementTables {
			var count int64
			if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM public.%s", table)).Scan(&count); err != nil {
				return err
			}
			counts[table] = count
		}
		return nil
	}))
	return counts
}

func migrationControlRetirementOwnedObjectCounts(t *testing.T, ctx context.Context, db *sql.DB) map[string]int64 {
	t.Helper()
	counts := make(map[string]int64)
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		queries := map[string]string{
			"indexes": `
				SELECT count(*)
				FROM pg_indexes
				WHERE schemaname = 'public' AND tablename = ANY($1::text[])
			`,
			"constraints": `
				SELECT count(*)
				FROM pg_constraint AS constraint_row
				JOIN pg_class AS source_table ON source_table.oid = constraint_row.conrelid
				JOIN pg_namespace AS source_namespace ON source_namespace.oid = source_table.relnamespace
				LEFT JOIN pg_class AS target_table ON target_table.oid = constraint_row.confrelid
				LEFT JOIN pg_namespace AS target_namespace ON target_namespace.oid = target_table.relnamespace
				WHERE source_namespace.nspname = 'public'
				  AND (source_table.relname = ANY($1::text[])
				       OR (target_namespace.nspname = 'public' AND target_table.relname = ANY($1::text[])))
			`,
			"policies": `
				SELECT count(*)
				FROM pg_policies
				WHERE schemaname = 'public' AND tablename = ANY($1::text[])
			`,
			"sequences": `
				SELECT count(*)
				FROM pg_depend AS dependency_row
				JOIN pg_class AS sequence_row ON sequence_row.oid = dependency_row.objid
				JOIN pg_class AS target_table ON target_table.oid = dependency_row.refobjid
				JOIN pg_namespace AS target_namespace ON target_namespace.oid = target_table.relnamespace
				WHERE dependency_row.classid = 'pg_class'::regclass
				  AND dependency_row.refclassid = 'pg_class'::regclass
				  AND sequence_row.relkind = 'S'
				  AND target_namespace.nspname = 'public'
				  AND target_table.relname = ANY($1::text[])
			`,
		}
		for name, query := range queries {
			var count int64
			if err := tx.QueryRowContext(ctx, query, "{"+strings.Join(migrationControlRetirementTables, ",")+"}").Scan(&count); err != nil {
				return err
			}
			counts[name] = count
		}
		return nil
	}))
	return counts
}

func migrationControlRetirementApplied(t *testing.T, ctx context.Context, db *sql.DB) bool {
	t.Helper()
	var applied bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM goose_db_version
			WHERE version_id = $1 AND is_applied
		)
	`, migrationControlRetirementVersion).Scan(&applied))
	return applied
}
