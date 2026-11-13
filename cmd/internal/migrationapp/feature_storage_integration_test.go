//go:build integration

package migrationapp

import (
	"context"
	"testing"

	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFeatureStoragePostgresLogOnlyFailureAndRecovery(t *testing.T) {
	db, _ := openMigrationProgressDB(t)
	migrator, err := postgres.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.PrepareEvidenceOnlyMigration(context.Background()))
	seedProgressMigration(t, db, 210, 0)
	require.NoError(t, migrator.RunUpThroughEvidenceOnly(context.Background()))
	require.NoError(t, migrator.PrepareFeatureStorageMigration(context.Background()))
	installFeatureStorageFailure(t, db)
	logs := newProgressLog(t)
	err = RunUp(context.Background(), db, 0, logs.logger)
	require.ErrorContains(t, err, "feature_storage_destination_payload_mismatch")
	committed, failed := false, false
	for _, event := range logs.records(t) {
		if event["msg"] == "feature_storage_migration_progress" && event["phase"] == "copy" && event["work_unit"] == "ontology_record_revisions" && event["state"] == "batch_committed" {
			require.Equal(t, float64(100), event["committed_rows"])
			require.Equal(t, "committed", event["transaction_outcome"])
			committed = true
		}
		if event["msg"] == "feature_storage_migration_failed" {
			require.Equal(t, "feature_storage_destination_payload_mismatch", event["failure_reason"])
			require.Equal(t, "rolled_back", event["transaction_outcome"])
			require.Equal(t, "incomplete", event["capture_state"])
			require.NotEmpty(t, event["migration_execution_id"])
			require.NotEmpty(t, event["inventory_identity"])
			require.NotEmpty(t, event["source_fingerprint"])
			require.Equal(t, "resume_matching_application_after_resolving_reported_cause", event["recovery_action"])
			failed = true
		}
	}
	require.True(t, committed)
	require.True(t, failed)
	require.NoError(t, db.Exec(`DROP TRIGGER feature_storage_fixture_failure ON ontology_topic_revision_payloads;DROP FUNCTION feature_storage_fixture_failure()`).Error)
	require.NoError(t, RunUp(context.Background(), db, 0, logs.logger))
	complete := false
	for _, event := range logs.records(t) {
		if event["msg"] == "feature_storage_migration_progress" && event["phase"] == "cutover" && event["state"] == "completed" {
			require.Equal(t, "committed", event["transaction_outcome"])
			require.Equal(t, "complete", event["capture_state"])
			complete = true
		}
	}
	require.True(t, complete)
	var count int
	require.NoError(t, postgres.NewRLS().WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM ontology_topic_revision_payloads`).Row().Scan(&count)
	}))
	require.Equal(t, 210, count)
}

func TestFeatureStoragePostgresRestrictedRuntimeReceipt(t *testing.T) {
	db, _ := openOntologyUpgradeRuntimeDB(t)
	require.NoError(t, RunUp(context.Background(), db, 0, newProgressLog(t).logger))
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, postgres.ValidateStartupMigrationState(context.Background(), pool, postgres.MigrationsDir()))
	var remaining int
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_policy WHERE polname IN ('feature_storage_marker_insert','feature_storage_inventory_read')`).Row().Scan(&remaining))
	require.Zero(t, remaining)
}
