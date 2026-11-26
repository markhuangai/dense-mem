package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
)

const (
	RelationalTopicPrepareVersion int64 = 20261125010000
	RelationalTopicDataVersion    int64 = 20261125010001
	RelationalTopicLayout               = "relational-topics-v1"
	RelationalTopicCutoverVersion int64 = 20261125010002
	relationalTopicDataFile             = "20261125010001_relational_topics_data.sql"
)

func isRelationalTopicPrepareLock(err error) bool {
	var failure *goose.PartialError
	return IsLockNotAvailable(err) && errors.As(err, &failure) &&
		failure.Failed != nil && failure.Failed.Source != nil && failure.Failed.Source.Version == RelationalTopicPrepareVersion
}

func retryRelationalTopicPrepare(ctx context.Context, provider *goose.Provider, lockErr error) error {
	retryCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	progress := MigrationProgressFromContext(ctx)
	if progress != nil {
		progress.SetMigrationVersion(RelationalTopicPrepareVersion)
	}

	// PostgreSQL maintenance can hold a conflicting lock even after application writers stop.
	for attempts := int64(1); ; attempts++ {
		if progress != nil {
			progress.Report("prepare_lock", "migration", "waiting", "attempts", "attempts", &attempts, nil)
		}
		select {
		case <-retryCtx.Done():
		case <-ticker.C:
		}
		if retryCtx.Err() != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			const reason = "feature_storage_relational_topics_prepare_lock_timeout: table lock unavailable (SQLSTATE 55P03)"
			if progress != nil {
				progress.FeatureStorageDatabaseFailure(lockErr)
				progress.SetFeatureTransactionOutcome("unconfirmed")
				progress.FeatureStorageFailure("failed", reason)
			}
			return &MigrationFailure{Cause: lockErr, Reason: reason}
		}

		_, err := provider.UpTo(retryCtx, RelationalTopicPrepareVersion)
		switch {
		case err == nil:
			if progress != nil {
				attempts++
				progress.Report("prepare_lock", "migration", "completed", "attempts", "attempts", &attempts, nil)
			}
			return nil
		case isRelationalTopicPrepareLock(err):
			lockErr = err
		case retryCtx.Err() == nil || !errors.Is(err, retryCtx.Err()):
			return err
		}
	}
}

func relationalTopicDataMigration() *goose.Migration {
	return goose.NewGoMigration(RelationalTopicDataVersion, &goose.GoFunc{
		Mode:  goose.TransactionDisabled,
		RunDB: copyRelationalTopics,
	}, &goose.GoFunc{
		Mode: goose.TransactionDisabled,
		RunDB: func(context.Context, *sql.DB) error {
			return errors.New("offline topic upgrade requires forward recovery or backup restoration")
		},
	})
}

func copyRelationalTopics(ctx context.Context, db *sql.DB) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, DiscardAdvisoryLockConnection(conn)) }()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(746,3)`); err != nil {
		return err
	}
	progress := MigrationProgressFromContext(ctx)
	if progress != nil {
		progress.SetMigrationVersion(RelationalTopicDataVersion)
	}
	migration := featureMigration{conn: conn, progress: progress}
	const unit = "current_topic_records"
	for _, phase := range []string{"copy", "verify"} {
		var checkpoint featureCheckpoint
		readCheckpoint := func(ctx context.Context, tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT cursor,row_count,batch_count,fingerprint,complete
				FROM data_migration_checkpoints WHERE migration_version=$1 AND phase=$2 AND work_unit=$3`,
				RelationalTopicDataVersion, phase, unit).Scan(&checkpoint.Cursor, &checkpoint.Rows, &checkpoint.Batches, &checkpoint.Fingerprint, &checkpoint.Complete)
		}
		if err := migration.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
			if err := tx.QueryRowContext(ctx, `SELECT inventory_fingerprint,schema_fingerprint
				FROM data_migration_runs WHERE migration_version=$1 AND executor_version=$2`,
				RelationalTopicDataVersion, RelationalTopicLayout).Scan(&migration.inventory, &migration.schema); err != nil {
				return err
			}
			return readCheckpoint(ctx, tx)
		}); err != nil {
			return fmt.Errorf("relational Topics %s checkpoint: %w", phase, err)
		}
		if checkpoint.Batches > 0 {
			migration.report(phase, unit, "resumed", checkpoint)
		}
		for !checkpoint.Complete {
			migration.report(phase, unit, "started", checkpoint)
			err := migration.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, `SELECT dense_mem_copy_relational_topics($1)`, phase); err != nil {
					return err
				}
				return readCheckpoint(ctx, tx)
			})
			if err != nil {
				reason := relationalTopicFailureReason(err)
				if progress != nil {
					progress.FeatureStorageDatabaseFailure(err)
					state := "failed"
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						state = "cancelled"
					}
					progress.FeatureStorageFailure(state, reason)
				}
				return &MigrationFailure{Cause: err, Reason: reason}
			}
			migration.report(phase, unit, "batch_committed", checkpoint)
		}
		if phase == "verify" {
			migration.report(phase, unit, "verified", checkpoint)
		}
	}
	return nil
}

func relationalTopicFailureReason(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Message == "relational_topics_copy_blocked: destination write rejected":
			return "relational_topics_destination_write_rejected"
		case strings.HasPrefix(pgErr.Message, "relational Topics fingerprint mismatch for record "):
			return "relational_topics_fingerprint_mismatch"
		}
	}
	return MigrationFailureReason(err)
}

// CheckUpgradeFloor runs before startup calls Goose, including its initialization writes.
func (m *Migrator) CheckUpgradeFloor(ctx context.Context) (int64, error) {
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var goose, applicationSchema bool
	err = tx.QueryRowContext(ctx, `SELECT to_regclass('public.goose_db_version') IS NOT NULL,
        to_regclass('public.teams') IS NOT NULL OR to_regclass('public.team_profiles') IS NOT NULL`).Scan(&goose, &applicationSchema)
	if err != nil {
		return 0, err
	}
	var version int64
	var floorApplied bool
	if goose {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(version_id),0),
            COALESCE(bool_or(version_id=$1),false) FROM goose_db_version WHERE is_applied`, evidenceOnlyMigrationVersion).Scan(&version, &floorApplied); err != nil {
			return 0, err
		}
	}
	if (applicationSchema || version > 0) && !floorApplied {
		return 0, fmt.Errorf("%w: upgrade the existing database through released v2.8.0 before this version", ErrInvalidMigrationState)
	}
	latest, err := latestMigrationVersion(m.dir)
	if err != nil {
		return 0, err
	}
	if version > latest {
		return 0, fmt.Errorf("%w: database migration is newer than this binary", ErrInvalidMigrationState)
	}
	return version, tx.Commit()
}
