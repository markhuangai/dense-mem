package migrationapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

const evidenceOnlyMigrationVersion int64 = 20261110010000

func migrateEvidenceOnly(ctx context.Context, db *gorm.DB, migrator *postgres.Migrator, logger infoLogger) (err error) {
	included, err := migrator.IncludesEvidenceOnlyMigration()
	if err != nil {
		return err
	}
	if !included {
		return migrator.RunUp(ctx)
	}
	started := time.Now()
	progress := postgres.MigrationProgressFromContext(ctx)
	if progress == nil {
		ctx, progress = postgres.ObserveMigration(ctx, func(s postgres.MigrationSnapshot) {
			logInfo(ctx, logger, "evidence_only_migration_progress", progressAttrs(s)...)
		})
	}
	progress.SetMigrationVersion(evidenceOnlyMigrationVersion)
	detach := attachProgress(ctx, progress)
	outcome := "not_started"
	attempted, skipped := false, false
	var latest int64
	var receipt map[string]any
	verificationFailure := ""
	logInfo(ctx, logger, "evidence_only_migration_started", progressAttrs(progress.Snapshot())...)
	defer func() {
		detach()
		if attempted {
			progress.CaptureFinished()
		}
		snapshot := progress.Snapshot()
		if skipped {
			snapshot.CaptureState = "not_applicable"
		}
		attrs := append(progressAttrs(snapshot), "elapsed_ms", time.Since(started).Milliseconds(), "transaction_outcome", outcome,
			"committed_work", outcome == "committed" && attempted && snapshot.TransactionStarted, "previous_commit_verified", skipped && outcome == "committed", "collection_queries", snapshot.CollectionQueries, "sql_notices", snapshot.SQLNotices, "collection_query_scope", "additional_client_round_trips")
		if verificationFailure != "" {
			attrs = append(attrs, "outcome_verification_error", verificationFailure)
		}
		if attempted && snapshot.CaptureState != "complete" {
			logWarn(context.WithoutCancel(ctx), logger, "evidence_only_migration_capture_incomplete", attrs...)
		}
		if err != nil {
			reason := postgres.MigrationFailureReason(err)
			attrs = append(attrs, "error", reason, "failure_reason", reason, "database_detail_omitted", true)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				attrs = append(attrs, "sqlstate", pgErr.Code)
			}
			event := "evidence_only_migration_failed"
			if reason == "legal hold" || reason == "unhandled retained dependency" || reason == "retained fingerprint mismatch" || reason == "retained ontology fingerprint" || reason == "missing inventory table" {
				event = "evidence_only_migration_blocked"
			}
			logError(context.WithoutCancel(ctx), logger, event, attrs...)
			err = &postgres.MigrationFailure{Cause: err, Reason: reason}
		} else {
			logInfo(ctx, logger, "evidence_only_migration_completed", append(attrs, "receipt", receipt, "database_version", latest)...)
		}
	}()
	if err = migrator.PrepareEvidenceOnlyMigration(ctx); err != nil {
		return err
	}
	progress.RecordCollectionQuery()
	if err = db.WithContext(ctx).Raw(`SELECT COALESCE(max(version_id),0) FROM goose_db_version WHERE is_applied`).Row().Scan(&latest); err != nil {
		return err
	}
	skipped = latest >= evidenceOnlyMigrationVersion
	if skipped {
		logInfo(ctx, logger, "evidence_only_migration_skipped", append(progressAttrs(progress.Snapshot()), "reason", "already_applied")...)
	}
	attempted = !skipped
	if err = migrator.RunUp(ctx); err != nil {
		verificationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if verified, _, checkErr := evidenceOnlyOutcome(verificationCtx, db, progress); checkErr != nil {
			outcome = "unconfirmed"
			verificationFailure = postgres.MigrationFailureReason(checkErr)
		} else {
			outcome = verified
		}
		return err
	}
	if attempted && !progress.Snapshot().TransactionStarted {
		skipped = true
		attempted = false
		logInfo(ctx, logger, "evidence_only_migration_skipped", append(progressAttrs(progress.Snapshot()), "reason", "applied_by_concurrent_starter")...)
	}
	var verifyErr error
	outcome, receipt, verifyErr = evidenceOnlyOutcome(ctx, db, progress)
	if verifyErr != nil {
		outcome = "unconfirmed"
		verificationFailure = postgres.MigrationFailureReason(verifyErr)
		return fmt.Errorf("evidence-only migration receipt: %w", verifyErr)
	}
	if outcome != "committed" {
		return errors.New("evidence-only migration receipt is missing or incompatible")
	}
	return nil
}

func evidenceOnlyOutcome(ctx context.Context, db *gorm.DB, progress *postgres.MigrationProgress) (outcome string, metadata map[string]any, err error) {
	outcome = "unconfirmed"
	snapshot := progress.Snapshot()
	err = postgres.NewRLS().WithSystemReadOnlyRepeatableTx(ctx, db, func(tx *gorm.DB) error {
		var applied, compatible, active bool
		progress.RecordCollectionQuery()
		if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=? AND is_applied),
   EXISTS(SELECT 1 FROM v2_compatibility_markers WHERE marker_kind='evidence_only' AND version='dense-mem.v2.8.0' AND status='compatible' AND metadata->>'retained_fingerprints_verified'='true' AND metadata->>'transaction_outcome'='committed'),
   EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=? AND backend_start=? AND xact_start IS NOT NULL AND pid<>pg_backend_pid())`, evidenceOnlyMigrationVersion, snapshot.BackendPID, snapshot.BackendStart).Row().Scan(&applied, &compatible, &active); err != nil {
			return err
		}
		if applied && compatible {
			var receipt []byte
			progress.RecordCollectionQuery()
			if err := tx.Raw(`SELECT metadata FROM v2_compatibility_markers WHERE marker_kind='evidence_only' AND version='dense-mem.v2.8.0' AND status='compatible'`).Row().Scan(&receipt); err != nil {
				return err
			}
			if err := json.Unmarshal(receipt, &metadata); err != nil {
				return err
			}
			outcome = "committed"
		} else if !applied && !compatible && !active {
			if !snapshot.TransactionStarted {
				outcome = "not_started"
			} else {
				outcome = "rolled_back"
			}
		}
		return nil
	})
	return outcome, metadata, err
}
