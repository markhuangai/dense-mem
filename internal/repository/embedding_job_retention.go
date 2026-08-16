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

const (
	embeddingJobRetentionAge       = 14 * 24 * time.Hour
	embeddingJobRetentionBatchSize = 1000
	embeddingJobRetentionInterval  = time.Hour
)

func (r *SearchRepositoryImpl) PruneTerminalEmbeddingJobs(ctx context.Context, now time.Time, batchSize int) (int, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if batchSize <= 0 || batchSize > embeddingJobRetentionBatchSize {
		batchSize = embeddingJobRetentionBatchSize
	}
	cutoff := now.UTC().Add(-embeddingJobRetentionAge)
	deleted := 0
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			WITH candidates AS MATERIALIZED (
				SELECT team_id, embedding_job_id
				FROM embedding_jobs
				WHERE status IN ('completed', 'stale', 'cancelled')
				  AND completed_at < ?
				ORDER BY completed_at, team_id, embedding_job_id
				LIMIT ?
				FOR UPDATE SKIP LOCKED
			)
			DELETE FROM embedding_jobs AS job
			USING candidates
			WHERE job.team_id = candidates.team_id
			  AND job.embedding_job_id = candidates.embedding_job_id
		`, cutoff, batchSize)
		if result.Error != nil {
			return result.Error
		}
		deleted = int(result.RowsAffected)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("embedding job retention: %w", err)
	}
	return deleted, nil
}

func drainTerminalEmbeddingJobs(
	ctx context.Context,
	now time.Time,
	prune func(context.Context, time.Time, int) (int, error),
) (int, error) {
	total := 0
	for {
		deleted, err := prune(ctx, now, embeddingJobRetentionBatchSize)
		if err != nil {
			return total, err
		}
		total += deleted
		if deleted < embeddingJobRetentionBatchSize {
			return total, nil
		}
	}
}

func (r *SearchRepositoryImpl) StartTerminalEmbeddingJobRetention(ctx context.Context, interval time.Duration, logger *slog.Logger) {
	if interval <= 0 {
		interval = embeddingJobRetentionInterval
	}
	go func() {
		run := func(now time.Time) {
			started := time.Now()
			deleted, err := drainTerminalEmbeddingJobs(ctx, now.UTC(), r.PruneTerminalEmbeddingJobs)
			durationMillis := time.Since(started).Milliseconds()
			if err != nil {
				if ctx.Err() == nil && logger != nil {
					logger.ErrorContext(ctx, "embedding job retention failed",
						"error_class", embeddingJobRetentionErrorClass(err),
						"duration_ms", durationMillis,
					)
				}
				return
			}
			if logger != nil {
				logger.InfoContext(ctx, "embedding job retention completed",
					"deleted_count", deleted,
					"duration_ms", durationMillis,
				)
			}
		}

		run(time.Now().UTC())
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				run(now)
			}
		}
	}()
}

func embeddingJobRetentionErrorClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && validEmbeddingJobRetentionSQLState(postgresError.Code) {
		return "sqlstate_" + postgresError.Code
	}
	return "database_operation"
}

func validEmbeddingJobRetentionSQLState(code string) bool {
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
