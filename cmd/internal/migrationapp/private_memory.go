package migrationapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	privacy "github.com/markhuangai/dense-mem/internal/privacy/contract"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

// RunPrivateMemoryMigration upgrades schema and private history before the runtime starts.
func RunPrivateMemoryMigration(ctx context.Context, db *gorm.DB, expectedFingerprint string, logger infoLogger) error {
	migrator, err := postgres.NewMigrator(db)
	if err != nil {
		return err
	}
	if err := migrator.PreparePrivateMemoryMigration(ctx); err != nil {
		return err
	}
	if err := migratePrivateMemory(ctx, db, expectedFingerprint); err != nil {
		return fmt.Errorf("migrate private memory: %w", err)
	}
	return migrateOntology(ctx, db, migrator, logger)
}

func migratePrivateMemory(ctx context.Context, db *gorm.DB, expectedFingerprint string) (err error) {
	rls := postgres.NewRLS()
	var installed bool
	var phase string
	if err := rls.WithSystemReadOnlyRepeatableTx(ctx, db, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT to_regclass('public.private_memory_migration_state') IS NOT NULL`).Row().Scan(&installed); err != nil || !installed {
			return err
		}
		return tx.Raw(`SELECT phase FROM private_memory_migration_state WHERE singleton`).Row().Scan(&phase)
	}); err != nil {
		return err
	}
	if !installed || (phase == "complete" && expectedFingerprint == "") {
		return nil
	}
	return db.WithContext(ctx).Connection(func(tx *gorm.DB) (err error) {
		// The lock and bounded data transactions share one connection, so losing the lock session also stops data work.
		conn := tx.Statement.ConnPool.(*sql.Conn)
		defer func() { err = errors.Join(err, postgres.DiscardAdvisoryLockConnection(conn)) }()
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(573,2701)`); err != nil {
			return err
		}
		repository := privacypostgres.NewPrivateMemoryRepository(tx, rls)
		report, err := repository.InspectPrivateMemoryMigration(ctx)
		if err != nil && (!errors.Is(err, privacy.ErrPrivateMemoryMigrationBlocked) || report == nil || report.Status == "pending") {
			if report != nil && len(report.Blockers) > 0 {
				return fmt.Errorf("%w: %s", err, strings.Join(report.Blockers, "; "))
			}
			return err
		}
		if report == nil {
			return fmt.Errorf("%w: preflight report is unavailable", privacy.ErrPrivateMemoryMigrationBlocked)
		}
		if expectedFingerprint != "" && report.SourceFingerprint != expectedFingerprint {
			return fmt.Errorf("%w: source fingerprint changed; rerun preflight", privacy.ErrPrivateMemoryMigrationBlocked)
		}
		if report.Status == "complete" {
			return nil
		}
		return repository.MigratePrivateMemory(ctx, report.SourceFingerprint)
	})
}
