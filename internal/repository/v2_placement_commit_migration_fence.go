package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func validateV2PlacementMigrationFence(migrationRunID string, migrationEpoch int) error {
	if migrationRunID == "" {
		return nil
	}
	if _, err := uuid.Parse(migrationRunID); err != nil {
		return fmt.Errorf("migration_run_id is invalid: %w", err)
	}
	if migrationEpoch < 1 {
		return errors.New("migration_epoch must be greater than zero when migration_run_id is set")
	}
	return nil
}

func lockV2MigrationRunForPlacementCommit(ctx context.Context, tx *gorm.DB, input V2CommitPlacementSemanticInput) error {
	if input.MigrationRunID == "" {
		return nil
	}
	if err := setV2PlacementTransactionMode(ctx, tx, "system"); err != nil {
		return err
	}
	var found int
	err := tx.WithContext(ctx).Raw(`
		SELECT 1
		FROM v2_migration_runs AS migration
		JOIN knowledge_ingests AS ingest
		  ON ingest.migration_run_id = migration.run_id
		WHERE migration.run_id = ?::uuid
		  AND migration.state = 'running'
		  AND migration.claim_epoch = ?
		  AND ingest.team_id = ?::uuid
		  AND ingest.owner_profile_id = ?::uuid
		  AND ingest.ingest_id = ?::uuid
		FOR UPDATE OF migration
	`, input.MigrationRunID, input.MigrationEpoch, input.TeamID, input.OwnerProfileID, input.IngestID).Scan(&found).Error
	if err != nil {
		return err
	}
	if found != 1 {
		return ErrV2PlacementLeaseLost
	}
	return setV2PlacementTransactionMode(ctx, tx, "profile")
}

func setV2PlacementTransactionMode(ctx context.Context, tx *gorm.DB, mode string) error {
	switch mode {
	case "profile", "system":
	default:
		return fmt.Errorf("unsupported v2 placement transaction mode %q", mode)
	}
	if err := tx.WithContext(ctx).Exec("SELECT set_config('app.tx_mode', ?, true)", mode).Error; err != nil {
		return fmt.Errorf("set v2 placement transaction mode %q: %w", mode, err)
	}
	return nil
}
