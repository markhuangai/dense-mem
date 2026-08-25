package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ClaimPlacementRunInput struct {
	TeamID         string
	OwnerProfileID string
	IngestID       string
	WorkerID       string
	Lease          time.Duration
}

// ClaimPlacementRun claims one exact submission for request-scoped Remember
// processing. The predicate matches the normal lease boundary but never
// consumes another caller's queued work.
func (r *LedgerRepositoryImpl) ClaimPlacementRun(ctx context.Context, input ClaimPlacementRunInput) (*PlacementRun, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return nil, fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.IngestID); err != nil {
		return nil, fmt.Errorf("ingest_id is required: %w", err)
	}
	if input.WorkerID == "" {
		return nil, errors.New("worker_id is required")
	}
	if input.Lease < time.Second {
		return nil, errors.New("lease must be at least one second")
	}
	var run *PlacementRun
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			UPDATE placement_runs AS run
			SET status = 'processing', attempts = attempts + 1,
			    started_at = COALESCE(started_at, now()),
			    lease_until = now() + (? * interval '1 second'),
			    worker_id = ?, updated_at = now()
			WHERE run.team_id = ?::uuid AND run.owner_profile_id = ?::uuid
			  AND run.ingest_id = ?::uuid
			  AND `+activeSemanticSpaceGenerationSQL("run")+`
			  AND run.attempts < run.max_attempts
			  AND ((run.status IN ('queued', 'guarded') AND run.available_at <= now())
			       OR (run.status = 'processing' AND run.lease_until < now()))
			RETURNING run.team_id::text, run.placement_run_id::text, run.ingest_id::text,
			          run.owner_profile_id::text, run.space_id::text, run.space_generation,
			          run.status, run.attempts, run.max_attempts, run.assessor_turns_reserved,
			          run.lease_until
		`, int(input.Lease.Seconds()), input.WorkerID, input.TeamID, input.OwnerProfileID, input.IngestID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		loaded := PlacementRun{}
		var leaseUntil sql.NullTime
		if err := rows.Scan(
			&loaded.TeamID, &loaded.PlacementRunID, &loaded.IngestID, &loaded.OwnerProfileID,
			&loaded.SpaceID, &loaded.SpaceGeneration, &loaded.Status, &loaded.Attempts,
			&loaded.MaxAttempts, &loaded.AssessorTurnsReserved, &leaseUntil,
		); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if leaseUntil.Valid {
			loaded.LeaseUntil = &leaseUntil.Time
		}
		if err := tx.WithContext(ctx).Raw(`
			SELECT COALESCE(metadata #>> '{actor,correlation_id}', '')
			FROM knowledge_ingests
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, input.TeamID, input.IngestID).Scan(&loaded.CorrelationID).Error; err != nil {
			return err
		}
		run = &loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ledger: claim placement run: %w", err)
	}
	return run, nil
}
