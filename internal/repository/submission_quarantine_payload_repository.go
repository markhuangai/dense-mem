package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// PurgeExpiredSubmissionQuarantinePayloads runs only in system mode. It
// claims a bounded batch, appends a purge outcome, and then removes the raw
// payload while retaining the append-only ledger identifiers and hashes.
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
			FOR UPDATE SKIP LOCKED
			LIMIT ?
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
				    team_id, placement_run_id, owner_profile_id,
				    outcome_kind, status, payload, created_at
				) VALUES (
				    ?::uuid, ?::uuid, ?::uuid,
				    'submission_quarantine_purged', 'purged',
				    jsonb_build_object('quarantine_payload_id', ?, 'purged_at', ?), ?
				)
				ON CONFLICT DO NOTHING
			`, item.teamID, item.runID, item.ownerID, item.payloadID, now, now).Error; err != nil {
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

// StartSubmissionQuarantinePurger starts the global retention worker. It is
// intentionally independent of active-team workers so expired payloads are
// purged even when no team has a queued placement run.
func (r *LedgerRepositoryImpl) StartSubmissionQuarantinePurger(ctx context.Context, interval time.Duration) {
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
				_, _ = r.PurgeExpiredSubmissionQuarantinePayloads(ctx, now.UTC(), 100)
			}
		}
	}()
}
