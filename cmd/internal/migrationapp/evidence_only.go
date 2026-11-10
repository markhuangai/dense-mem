package migrationapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

const evidenceOnlyMigrationVersion int64 = 20261110010000

func migrateEvidenceOnly(ctx context.Context, db *gorm.DB, migrator *postgres.Migrator, logger infoLogger) (err error) {
	started := time.Now()
	phase, outcome := "prepare", "not_started"
	logInfo(ctx, logger, "evidence_only_migration_started", "migration_version", evidenceOnlyMigrationVersion,
		"inventory_identity", postgres.EvidenceOnlyMigrationInventorySHA256, "phase", phase, "transaction_outcome", outcome)
	defer func() {
		if err == nil {
			return
		}
		event := "evidence_only_migration_failed"
		if strings.Contains(err.Error(), "evidence_only_migration_blocked") {
			event = "evidence_only_migration_blocked"
		}
		attrs := []any{"migration_version", evidenceOnlyMigrationVersion,
			"inventory_identity", postgres.EvidenceOnlyMigrationInventorySHA256, "phase", phase,
			"error", err, "elapsed_ms", time.Since(started).Milliseconds(), "transaction_outcome", outcome}
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) {
			attrs = append(attrs, "sqlstate", databaseError.Code, "database_detail", databaseError.Detail,
				"database_context", databaseError.Where, "statement_position", databaseError.Position,
				"internal_statement_position", databaseError.InternalPosition, "internal_statement", databaseError.InternalQuery)
		}
		logError(ctx, logger, event, attrs...)
	}()
	if err = migrator.PrepareEvidenceOnlyMigration(ctx); err != nil {
		return err
	}
	phase = "read_database_version"
	var latest int64
	if err = db.WithContext(ctx).Raw(`SELECT COALESCE(max(version_id),0) FROM goose_db_version WHERE is_applied`).Row().Scan(&latest); err != nil {
		return err
	}
	if latest >= evidenceOnlyMigrationVersion {
		outcome = "committed"
	}
	phase = "apply"
	if latest < evidenceOnlyMigrationVersion {
		outcome = "unconfirmed"
	}
	if err = migrator.RunUp(ctx); err != nil {
		if latest < evidenceOnlyMigrationVersion {
			verificationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			var applied bool
			if checkErr := db.WithContext(verificationCtx).Raw(`SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=? AND is_applied)`, evidenceOnlyMigrationVersion).Row().Scan(&applied); checkErr != nil {
				err = fmt.Errorf("%w; verify migration outcome: %w", err, checkErr)
			} else if !applied {
				outcome = "not_committed"
			}
		}
		return err
	}
	phase, outcome = "read_receipt", "committed"
	var receipt []byte
	err = postgres.NewRLS().WithSystemReadOnlyRepeatableTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT metadata FROM v2_compatibility_markers WHERE marker_kind='evidence_only' AND version='dense-mem.v3.0.0' AND status='compatible'`).Row().Scan(&receipt)
	})
	if err != nil {
		return fmt.Errorf("Evidence-only migration receipt: %w", err)
	}
	var metadata map[string]any
	if err = json.Unmarshal(receipt, &metadata); err != nil {
		return err
	}
	logInfo(ctx, logger, "evidence_only_migration_completed", "migration_version", evidenceOnlyMigrationVersion,
		"database_version", latest, "inventory_identity", postgres.EvidenceOnlyMigrationInventorySHA256, "receipt", metadata,
		"elapsed_ms", time.Since(started).Milliseconds(), "transaction_outcome", outcome)
	return nil
}
