package repository

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// PurgeExpiredSubmissionQuarantinePayloads runs only in system mode. It
// claims a bounded batch, appends a purge outcome, and then removes the raw
// payload while retaining the append-only ledger identifiers, hashes, security
// events, and staged evidence required for audit and lineage.
func (r *LedgerRepositoryImpl) PurgeExpiredSubmissionQuarantinePayloads(ctx context.Context, now time.Time, batchSize int) (int, error) {
	if batchSize <= 0 || batchSize > 100 {
		batchSize = 100
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	purged := 0
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, quarantine_payload_id::text,
			       placement_run_id::text, owner_profile_id::text
			FROM submission_quarantine_payloads
			WHERE expires_at <= ?
			ORDER BY expires_at ASC, team_id, quarantine_payload_id
			LIMIT ?
			FOR UPDATE SKIP LOCKED
		`, now, batchSize).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		type candidate struct {
			teamID, payloadID, runID, ownerID string
		}
		candidates := []candidate{}
		for rows.Next() {
			var item candidate
			if err := rows.Scan(&item.teamID, &item.payloadID, &item.runID, &item.ownerID); err != nil {
				return err
			}
			candidates = append(candidates, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, item := range candidates {
			if err := tx.WithContext(ctx).Exec(`
				INSERT INTO placement_outcomes (
				    team_id, placement_run_id, owner_profile_id, space_id, space_generation,
				    outcome_kind, status, payload, created_at
				)
				SELECT run.team_id, run.placement_run_id, run.owner_profile_id,
				       run.space_id, run.space_generation,
				       'submission_quarantine_purged', 'purged',
				       jsonb_build_object('quarantine_payload_id', ?::text, 'purged_at', ?::timestamptz), ?
				FROM placement_runs AS run
				WHERE run.team_id = ?::uuid
				  AND run.placement_run_id = ?::uuid
				  AND run.owner_profile_id = ?::uuid
				ON CONFLICT DO NOTHING
			`, item.payloadID, now, now, item.teamID, item.runID, item.ownerID).Error; err != nil {
				return err
			}
			if err := tx.WithContext(ctx).Exec(`
				DELETE FROM submission_quarantine_payloads
				WHERE team_id = ?::uuid AND quarantine_payload_id = ?::uuid
			`, item.teamID, item.payloadID).Error; err != nil {
				return err
			}
			purged++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("submission quarantine purge: %w", err)
	}
	return purged, nil
}

// SubmissionQuarantinePurgeMetrics is the narrow metric surface needed by the
// retention worker. It is intentionally separate from request metrics because
// purge failures have no request identity.
type SubmissionQuarantinePurgeMetrics interface {
	IncSubmissionQuarantinePurgeFailure()
}

func drainExpiredSubmissionQuarantinePayloads(
	ctx context.Context,
	now time.Time,
	batchSize int,
	purge func(context.Context, time.Time, int) (int, error),
) error {
	for {
		purged, err := purge(ctx, now, batchSize)
		if err != nil {
			return err
		}
		if purged < batchSize {
			return nil
		}
	}
}

func observeSubmissionQuarantinePurgeFailure(ctx context.Context, logger *slog.Logger, metrics SubmissionQuarantinePurgeMetrics, err error) {
	if logger != nil {
		logger.ErrorContext(ctx, "submission quarantine purge failed",
			"error_class", submissionQuarantinePurgeErrorClass(err),
		)
	}
	if metrics != nil {
		metrics.IncSubmissionQuarantinePurgeFailure()
	}
}

func submissionQuarantinePurgeErrorClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && validSubmissionQuarantineSQLState(postgresError.Code) {
		return "sqlstate_" + postgresError.Code
	}
	return "database_operation"
}

func validSubmissionQuarantineSQLState(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, character := range code {
		if (character < '0' || character > '9') && (character < 'A' || character > 'Z') {
			return false
		}
	}
	return true
}

// StartSubmissionQuarantinePurger starts the global retention worker. It is
// intentionally independent of active-team workers so expired payloads are
// purged even when no team has a queued placement run.
func (r *LedgerRepositoryImpl) StartSubmissionQuarantinePurger(
	ctx context.Context,
	interval time.Duration,
	logger *slog.Logger,
	metrics SubmissionQuarantinePurgeMetrics,
) {
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				err := drainExpiredSubmissionQuarantinePayloads(ctx, now.UTC(), 100, r.PurgeExpiredSubmissionQuarantinePayloads)
				if err != nil && ctx.Err() == nil {
					observeSubmissionQuarantinePurgeFailure(ctx, logger, metrics, err)
				}
			}
		}
	}()
}
