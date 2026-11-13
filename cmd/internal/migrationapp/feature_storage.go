package migrationapp

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func migrateFeatureStorage(ctx context.Context, migrator *postgres.Migrator, logger infoLogger) error {
	ctx, progress := postgres.ObserveMigration(ctx, func(s postgres.MigrationSnapshot) {
		reportFeatureStorageProgress(ctx, logger, s)
	})
	detach := attachProgress(ctx, progress)
	defer detach()
	return migrator.RunUp(ctx)
}

func reportFeatureStorageProgress(ctx context.Context, logger infoLogger, s postgres.MigrationSnapshot) {
	attrs := progressAttrs(s)
	switch s.State {
	case "failed":
		logError(context.WithoutCancel(ctx), logger, "feature_storage_migration_failed", attrs...)
	case "cancelled":
		logWarn(context.WithoutCancel(ctx), logger, "feature_storage_migration_cancelled", attrs...)
	default:
		logInfo(ctx, logger, "feature_storage_migration_progress", attrs...)
	}
}
