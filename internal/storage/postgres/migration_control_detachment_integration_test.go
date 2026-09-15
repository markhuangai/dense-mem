//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	migrationControlDetachmentBaseVersion int64 = 20260910020001
	migrationControlDetachmentVersion     int64 = 20260913010001
)

var migrationControlDetachmentTables = []string{
	"v2_migration_runs",
	"v2_migration_corpus_items",
	"v2_migration_source_maps",
	"v2_migration_checkpoints",
	"v2_migration_errors",
	"v2_migration_exclusions",
	"v2_migration_gate_results",
	"v2_migration_operator_actions",
}

type migrationControlFixture struct {
	teamID    string
	profileID string
	runID     string
	markerID  string
	ingestID  string
}

func TestMigrationControlDetachmentPreservesRetainedTablesAndMarker(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, migrationControlDetachmentBaseVersion)
	fixture := seedMigrationControlFixture(t, ctx, sqlDB)
	beforeCounts := migrationControlTableCounts(t, ctx, sqlDB)
	beforeMarker := migrationControlMarkerSnapshot(t, ctx, sqlDB, fixture.markerID)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	_, err := sqlDB.ExecContext(ctx, `SET quote_all_identifiers = on`)
	require.NoError(t, err)

	runGooseUpTo(t, ctx, sqlDB, migrationControlDetachmentVersion)

	for _, table := range migrationControlDetachmentTables {
		require.True(t, tableExists(t, ctx, sqlDB, table), "%s must be retained", table)
	}
	require.Equal(t, beforeCounts, migrationControlTableCounts(t, ctx, sqlDB))
	afterMarker := migrationControlMarkerSnapshot(t, ctx, sqlDB, fixture.markerID)
	require.Equal(t, beforeMarker, afterMarker)
	require.False(t, columnExists(t, ctx, sqlDB, "knowledge_ingests", "migration_run_id"))
	require.False(t, indexExists(t, ctx, sqlDB, "knowledge_ingests_migration_run_idx"))
	for _, constraint := range []string{
		"knowledge_ingests_migration_run_id_fkey",
		"v2_compatibility_markers_run_id_fkey",
		"v2_migration_corpus_items_team_id_fkey",
		"v2_migration_corpus_items_team_id_owner_profile_id_fkey",
		"v2_migration_corpus_items_team_id_ingest_id_fkey",
		"v2_migration_corpus_items_team_id_placement_item_id_fkey",
	} {
		require.False(t, migrationControlConstraintExists(t, ctx, sqlDB, constraint), "%s must be detached", constraint)
	}

	var remaining int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_constraint AS constraint_row
		JOIN pg_class AS source_table ON source_table.oid = constraint_row.conrelid
		JOIN pg_namespace AS source_namespace ON source_namespace.oid = source_table.relnamespace
		JOIN pg_class AS target_table ON target_table.oid = constraint_row.confrelid
		JOIN pg_namespace AS target_namespace ON target_namespace.oid = target_table.relnamespace
		WHERE constraint_row.contype = 'f'
		  AND source_namespace.nspname = 'public'
		  AND target_namespace.nspname = 'public'
		  AND ((source_table.relname = ANY ($1::text[])) <> (target_table.relname = ANY ($1::text[])))
	`, "{"+strings.Join(migrationControlDetachmentTables, ",")+"}").Scan(&remaining))
	require.Zero(t, remaining, "canonical and migration-control schemas must have no cross-boundary foreign keys")

	err = migrationDown(ctx, sqlDB)
	require.Error(t, err)
	require.Contains(t, err.Error(), "20260913010001 is irreversible")
	require.True(t, migrationControlApplied(t, ctx, sqlDB))
}

func TestMigrationControlDetachmentRejectsUnexpectedFunctionAndRollsBack(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, migrationControlDetachmentBaseVersion)

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			CREATE FUNCTION public.dense_mem_detachment_unexpected_reference()
			RETURNS bigint
			LANGUAGE sql
			AS $$ SELECT count(*) FROM public.v2_migration_runs $$
		`)
		return err
	}))
	defer func() {
		_ = execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `DROP FUNCTION IF EXISTS public.dense_mem_detachment_unexpected_reference()`)
			return err
		})
	}()

	err := migrationUpTo(ctx, sqlDB, migrationControlDetachmentVersion)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected retained object references")
	require.True(t, columnExists(t, ctx, sqlDB, "knowledge_ingests", "migration_run_id"))
	require.True(t, indexExists(t, ctx, sqlDB, "knowledge_ingests_migration_run_idx"))
	require.True(t, migrationControlConstraintExists(t, ctx, sqlDB, "knowledge_ingests_migration_run_id_fkey"))
	require.False(t, migrationControlApplied(t, ctx, sqlDB))
}

func TestMigrationControlDetachmentRejectsUnexpectedDependenciesAndRollsBack(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, migrationControlDetachmentBaseVersion)

	cases := []struct {
		name    string
		install func() error
		remove  func() error
		needle  string
	}{
		{
			name: "foreign key",
			install: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `
						ALTER TABLE public.v2_migration_corpus_items
						ADD CONSTRAINT dense_mem_detachment_unexpected_fk
						FOREIGN KEY (team_id) REFERENCES public.teams(id) NOT VALID
					`)
					return err
				})
			},
			remove: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `
						ALTER TABLE public.v2_migration_corpus_items
						DROP CONSTRAINT IF EXISTS dense_mem_detachment_unexpected_fk
					`)
					return err
				})
			},
			needle: "unexpected cross-boundary foreign keys",
		},
		{
			name: "foreign key definition drift",
			install: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, `
						ALTER TABLE public.knowledge_ingests
						DROP CONSTRAINT knowledge_ingests_migration_run_id_fkey
					`); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `
						ALTER TABLE public.knowledge_ingests
						ADD CONSTRAINT knowledge_ingests_migration_run_id_fkey
						FOREIGN KEY (migration_run_id)
						REFERENCES public.v2_migration_runs(run_id)
						ON DELETE RESTRICT ON UPDATE CASCADE
					`)
					return err
				})
			},
			remove: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, `
						ALTER TABLE public.knowledge_ingests
						DROP CONSTRAINT IF EXISTS knowledge_ingests_migration_run_id_fkey
					`); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `
						ALTER TABLE public.knowledge_ingests
						ADD CONSTRAINT knowledge_ingests_migration_run_id_fkey
						FOREIGN KEY (migration_run_id)
						REFERENCES public.v2_migration_runs(run_id)
						ON DELETE RESTRICT
					`)
					return err
				})
			},
			needle: "knowledge ingest lineage FK definition is not the verified target",
		},
		{
			name: "placement foreign key definition drift",
			install: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, `
						CREATE TABLE public.placement_items (
							team_id UUID NOT NULL,
							placement_item_id UUID NOT NULL,
							PRIMARY KEY (team_id, placement_item_id)
						)
					`); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `
						ALTER TABLE public.v2_migration_corpus_items
						ADD CONSTRAINT v2_migration_corpus_items_team_id_placement_item_id_fkey
						FOREIGN KEY (team_id, placement_item_id)
						REFERENCES public.placement_items(team_id, placement_item_id)
						ON DELETE RESTRICT ON UPDATE CASCADE
					`)
					return err
				})
			},
			remove: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, `
						ALTER TABLE public.v2_migration_corpus_items
						DROP CONSTRAINT IF EXISTS v2_migration_corpus_items_team_id_placement_item_id_fkey
					`); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS public.placement_items`)
					return err
				})
			},
			needle: "corpus placement FK definition is not the verified target",
		},
		{
			name: "index",
			install: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `
						CREATE INDEX dense_mem_detachment_unexpected_index
						ON public.knowledge_ingests (migration_run_id)
					`)
					return err
				})
			},
			remove: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS public.dense_mem_detachment_unexpected_index`)
					return err
				})
			},
			needle: "unexpected dependency on knowledge_ingests.migration_run_id",
		},
		{
			name: "unique index definition drift",
			install: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, `DROP INDEX public.knowledge_ingests_migration_run_idx`); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `
						CREATE UNIQUE INDEX knowledge_ingests_migration_run_idx
						ON public.knowledge_ingests (team_id, migration_run_id)
						WHERE migration_run_id IS NOT NULL
					`)
					return err
				})
			},
			remove: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS public.knowledge_ingests_migration_run_idx`); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `
						CREATE INDEX knowledge_ingests_migration_run_idx
						ON public.knowledge_ingests (team_id, migration_run_id)
						WHERE migration_run_id IS NOT NULL
					`)
					return err
				})
			},
			needle: "expected one verified knowledge ingest lineage index",
		},
		{
			name: "view",
			install: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `
						CREATE VIEW public.dense_mem_detachment_unexpected_view AS
						SELECT run_id FROM public.v2_migration_runs
					`)
					return err
				})
			},
			remove: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `DROP VIEW IF EXISTS public.dense_mem_detachment_unexpected_view`)
					return err
				})
			},
			needle: "unexpected retained object references",
		},
		{
			name: "function",
			install: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `
						CREATE FUNCTION public.dense_mem_detachment_unexpected_dependency()
						RETURNS bigint
						LANGUAGE sql
						AS $$ SELECT count(*) FROM public.v2_migration_runs $$
					`)
					return err
				})
			},
			remove: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `DROP FUNCTION IF EXISTS public.dense_mem_detachment_unexpected_dependency()`)
					return err
				})
			},
			needle: "unexpected retained object references",
		},
		{
			name: "trigger",
			install: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, `
						CREATE FUNCTION public.dense_mem_detachment_trigger_fn()
						RETURNS trigger
						LANGUAGE plpgsql
						AS $$ BEGIN RETURN NEW; END $$
					`); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `
						CREATE TRIGGER v2_migration_detachment_unexpected_trigger
						BEFORE INSERT ON public.knowledge_ingests
						FOR EACH ROW WHEN (NEW.migration_run_id IS NOT NULL)
						EXECUTE FUNCTION public.dense_mem_detachment_trigger_fn()
					`)
					return err
				})
			},
			remove: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, `
						DROP TRIGGER IF EXISTS v2_migration_detachment_unexpected_trigger ON public.knowledge_ingests
					`); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `DROP FUNCTION IF EXISTS public.dense_mem_detachment_trigger_fn()`)
					return err
				})
			},
			needle: "unexpected dependency on knowledge_ingests.migration_run_id",
		},
		{
			name: "policy",
			install: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `
						CREATE POLICY dense_mem_detachment_unexpected_policy
						ON public.knowledge_ingests
						USING (current_setting('v2_migration_runs.guard', true) IS NOT NULL)
					`)
					return err
				})
			},
			remove: func() error {
				return execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `
						DROP POLICY IF EXISTS dense_mem_detachment_unexpected_policy ON public.knowledge_ingests
					`)
					return err
				})
			},
			needle: "unexpected retained object references",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, tc.install())
			t.Cleanup(func() {
				require.NoError(t, tc.remove())
			})

			err := migrationUpTo(ctx, sqlDB, migrationControlDetachmentVersion)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.needle)
			require.True(t, columnExists(t, ctx, sqlDB, "knowledge_ingests", "migration_run_id"))
			require.True(t, indexExists(t, ctx, sqlDB, "knowledge_ingests_migration_run_idx"))
			require.True(t, migrationControlConstraintExists(t, ctx, sqlDB, "knowledge_ingests_migration_run_id_fkey"))
			require.False(t, migrationControlApplied(t, ctx, sqlDB))
		})
	}
}

func TestMigrationControlDetachmentLockFailureRollsBackAndCanRetry(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, migrationControlDetachmentBaseVersion)
	sqlDB.SetMaxOpenConns(4)

	blockerConn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	blockerTx, err := blockerConn.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = blockerTx.ExecContext(ctx, `LOCK TABLE public.knowledge_ingests IN ROW EXCLUSIVE MODE`)
	require.NoError(t, err)

	err = migrationUpTo(ctx, sqlDB, migrationControlDetachmentVersion)
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "lock")
	require.True(t, columnExists(t, ctx, sqlDB, "knowledge_ingests", "migration_run_id"))
	require.False(t, migrationControlApplied(t, ctx, sqlDB))

	require.NoError(t, blockerTx.Rollback())
	require.NoError(t, blockerConn.Close())
	runGooseUpTo(t, ctx, sqlDB, migrationControlDetachmentVersion)
	require.False(t, columnExists(t, ctx, sqlDB, "knowledge_ingests", "migration_run_id"))
}

func TestMigrationControlDetachmentAcquiresDDLLocksBeforeValidationLocks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, migrationControlDetachmentBaseVersion)
	sqlDB.SetMaxOpenConns(4)

	blockerConn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	blockerTx, err := blockerConn.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = blockerTx.ExecContext(ctx, `LOCK TABLE public.v2_migration_runs IN ROW EXCLUSIVE MODE`)
	require.NoError(t, err)

	migrationDone := make(chan error, 1)
	go func() {
		migrationDone <- migrationUpTo(ctx, sqlDB, migrationControlDetachmentVersion)
	}()

	var ddlGranted, validationLockWaiting bool
	observed := assert.Eventually(t, func() bool {
		err := sqlDB.QueryRowContext(ctx, `
			SELECT
				(
					SELECT count(DISTINCT relation_row.oid) = 3
					FROM pg_locks AS lock_row
					JOIN pg_class AS relation_row ON relation_row.oid = lock_row.relation
					JOIN pg_namespace AS namespace_row ON namespace_row.oid = relation_row.relnamespace
					WHERE namespace_row.nspname = 'public'
					  AND relation_row.relname IN ('knowledge_ingests', 'v2_compatibility_markers', 'v2_migration_corpus_items')
					  AND lock_row.mode = 'AccessExclusiveLock'
					  AND lock_row.granted
				),
				EXISTS (
					SELECT 1
					FROM pg_locks AS lock_row
					JOIN pg_class AS relation_row ON relation_row.oid = lock_row.relation
					JOIN pg_namespace AS namespace_row ON namespace_row.oid = relation_row.relnamespace
					WHERE namespace_row.nspname = 'public'
					  AND relation_row.relname = 'v2_migration_runs'
					  AND lock_row.mode = 'ShareRowExclusiveLock'
					  AND NOT lock_row.granted
				)
		`).Scan(&ddlGranted, &validationLockWaiting)
		return err == nil && ddlGranted && validationLockWaiting
	}, 900*time.Millisecond, 10*time.Millisecond, "migration did not acquire DDL locks before waiting on validation locks")

	require.NoError(t, blockerTx.Rollback())
	require.NoError(t, blockerConn.Close())
	require.NoError(t, <-migrationDone)
	require.True(t, observed)
	require.False(t, columnExists(t, ctx, sqlDB, "knowledge_ingests", "migration_run_id"))
}

func seedMigrationControlFixture(t *testing.T, ctx context.Context, db *sql.DB) migrationControlFixture {
	t.Helper()
	teamID, profileID := insertMigrationTeamProfile(t, ctx, db)
	fixture := migrationControlFixture{
		teamID: teamID, profileID: profileID,
		runID: uuid.NewString(), markerID: uuid.NewString(), ingestID: uuid.NewString(),
	}
	require.NoError(t, execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO v2_migration_runs (
				run_id, migration_contract_version, corpus_version, source_kind, state
			) VALUES ($1::uuid, 'detachment-test', 'detachment-test', 'neo4j', 'cut_over')
		`, fixture.runID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key, request_hash,
				status, metadata, migration_run_id
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4, $5, 'queued',
				'{"_dense_mem_telemetry_origin":"remember"}'::jsonb, $6::uuid
			)
		`, fixture.teamID, fixture.ingestID, fixture.profileID,
			"detachment-ingest-"+fixture.ingestID, "detachment-request-"+fixture.ingestID, fixture.runID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO v2_migration_corpus_items (
				run_id, team_id, owner_profile_id, source_kind, source_id,
				item_kind, outcome, ingest_id
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'neo4j', $4, 'evidence', 'accepted', $5::uuid)
		`, fixture.runID, fixture.teamID, fixture.profileID, "detachment-source-"+fixture.ingestID, fixture.ingestID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO v2_compatibility_markers (
				marker_id, marker_kind, version, status, run_id, corpus_hash, gate_report_hash, metadata
			) VALUES ($1::uuid, 'v2_detachment_fixture', 'v1', 'compatible', $2::uuid, 'fixture-corpus', 'fixture-gates', '{"fixture":true}'::jsonb)
		`, fixture.markerID, fixture.runID)
		return err
	}))
	return fixture
}

func migrationControlTableCounts(t *testing.T, ctx context.Context, db *sql.DB) map[string]int64 {
	t.Helper()
	counts := make(map[string]int64, len(migrationControlDetachmentTables))
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		for _, table := range migrationControlDetachmentTables {
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

func migrationControlMarkerSnapshot(t *testing.T, ctx context.Context, db *sql.DB, markerID string) string {
	t.Helper()
	var snapshot string
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT marker_id::text || '|' || run_id::text || '|' || metadata::text
			FROM v2_compatibility_markers
			WHERE marker_id = $1::uuid
		`, markerID).Scan(&snapshot)
	}))
	return snapshot
}

func migrationControlApplied(t *testing.T, ctx context.Context, db *sql.DB) bool {
	t.Helper()
	var applied bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = $1 AND is_applied)
	`, migrationControlDetachmentVersion).Scan(&applied))
	return applied
}

func migrationControlConstraintExists(t *testing.T, ctx context.Context, db *sql.DB, constraintName string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_constraint
			WHERE conname = $1
		)
	`, constraintName).Scan(&exists))
	return exists
}
