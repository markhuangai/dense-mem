//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/gorm"
)

func runMigrationControlRetirementOnly(t *testing.T, ctx context.Context, db *sql.DB) error {
	t.Helper()
	migrationDir := t.TempDir()
	releaseDir := filepath.Join(migrationDir, "v2_6")
	require.NoError(t, os.MkdirAll(releaseDir, 0o755))
	sourcePath := filepath.Join(MigrationsDir(), "v2_6", "20260917010001_retire_migration_control.sql")
	contents, err := os.ReadFile(sourcePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(releaseDir, "20260917010001_retire_migration_control.sql"), contents, 0o644))
	provider, err := newMigrationProvider(migrationDir, db, false)
	require.NoError(t, err)
	_, err = provider.UpTo(ctx, migrationControlRetirementVersion)
	return err
}

func openMigrationControlRetirementBackupContainer(t *testing.T, ctx context.Context) (testcontainers.Container, *sql.DB, string, func()) {
	t.Helper()
	container, err := tcpostgres.Run(ctx, "pgvector/pgvector:0.8.2-pg18-trixie", postgresTestContainerOptions()...)
	require.NoError(t, err)
	sourceDSN, err := postgresTestContainerDSN(ctx, container)
	require.NoError(t, err)
	db, err := Open(ctx, &testConfig{dsn: sourceDSN})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	cleanup := func() {
		_ = sqlDB.Close()
		_ = container.Terminate(ctx)
	}
	return container, sqlDB, sourceDSN, cleanup
}

func openRetirementBackupDatabase(t *testing.T, ctx context.Context, sourceDSN, databaseName string) *gorm.DB {
	t.Helper()
	parsed, err := url.Parse(sourceDSN)
	require.NoError(t, err)
	parsed.Path = "/" + databaseName
	db, err := Open(ctx, &testConfig{dsn: parsed.String()})
	require.NoError(t, err)
	return db
}

func containerExec(t *testing.T, ctx context.Context, container testcontainers.Container, command []string) {
	t.Helper()
	exitCode, output, err := container.Exec(ctx, command)
	require.NoError(t, err)
	outputBytes, readErr := io.ReadAll(output)
	require.NoError(t, readErr)
	require.Zero(t, exitCode, "container command failed: %s", strings.TrimSpace(string(outputBytes)))
}

func seedMigrationControlRetirementFixture(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	runID := uuid.NewString()
	teamID := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO v2_migration_runs (
				run_id, migration_contract_version, corpus_version, source_kind, state,
				preflight_approved, backup_reference, preflight_checks
			) VALUES (
				$1::uuid, 'retirement-test', 'retirement-test', 'neo4j', 'cut_over',
				true, 'backup://retirement-fixture',
				'{
				  "detached_release_deployed": true,
				  "current_main_rehearsal": true,
				  "backup_restore_rehearsal": true,
				  "coordinated_stop": true,
				  "catalog_preflight": true,
				  "row_counts": {
				    "v2_migration_runs": 1,
				    "v2_migration_corpus_items": 1,
				    "v2_migration_source_maps": 1,
				    "v2_migration_checkpoints": 1,
				    "v2_migration_errors": 1,
				    "v2_migration_exclusions": 1,
				    "v2_migration_gate_results": 5,
				    "v2_migration_operator_actions": 1
				  }
				}'::jsonb
			)
		`, runID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO v2_migration_corpus_items (
				run_id, team_id, source_kind, source_id, item_kind, outcome
			) VALUES ($1::uuid, $2::uuid, 'neo4j', 'retirement-source', 'evidence', 'accepted')
		`, runID, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO v2_migration_source_maps (
				run_id, source_kind, source_id, target_type, target_id
			) VALUES ($1::uuid, 'neo4j', 'retirement-source', 'evidence', 'retirement-target')
		`, runID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO v2_migration_checkpoints (run_id, checkpoint_key)
			VALUES ($1::uuid, 'retirement-checkpoint')
		`, runID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO v2_migration_errors (run_id, phase, error_code, message)
			VALUES ($1::uuid, 'retirement', 'fixture', 'fixture error')
		`, runID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO v2_migration_exclusions (run_id, source_id, reason)
			VALUES ($1::uuid, 'retirement-source', 'fixture exclusion')
		`, runID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO v2_migration_gate_results (
				run_id, gate_name, outcome, evidence_ref, evidence_hash, message
			)
			SELECT $1::uuid, gate_name, 'pass', evidence_ref, evidence_hash, 'fixture gate passed'
			  FROM (VALUES
				  ('detached_release_deployed', 'fixture://detached-release', 'sha256:retirement-detached'),
				  ('current_main_rehearsal', 'fixture://current-main', 'sha256:retirement-current-main'),
				  ('backup_restore_rehearsal', 'fixture://backup-restore', 'sha256:retirement-backup-restore'),
				  ('coordinated_stop', 'fixture://coordinated-stop', 'sha256:retirement-stop'),
				  ('catalog_preflight', 'fixture://catalog', 'sha256:retirement-catalog')
				) AS gate_fixture(gate_name, evidence_ref, evidence_hash)
		`, runID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO v2_migration_operator_actions (run_id, action, actor, reason, metadata)
			VALUES (
				$1::uuid, 'retire_migration_control', 'integration-test',
				'verified retirement preflight fixture',
				'{
				  "approved_commit": "fixture",
				  "detached_release_receipt": "fixture://detached-release",
				  "current_main_rehearsal": "fixture://current-main",
				  "backup_restore_rehearsal": "fixture://backup-restore",
				  "coordinated_stop": "fixture://coordinated-stop",
				  "node_fence": "fixture://coordinated-stop",
				  "catalog_preflight": "fixture://catalog"
				}'::jsonb
			)
		`, runID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO v2_compatibility_markers (
				marker_kind, version, status, run_id, corpus_hash, gate_report_hash, metadata
			) VALUES (
				'v2_retirement_fixture', 'v1', 'compatible', $1::uuid,
				'retirement-corpus', 'retirement-gates', '{"fixture":true}'::jsonb
			)
		`, runID)
		return err
	}))
}
