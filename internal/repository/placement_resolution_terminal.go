package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func quarantinePlacementRunForUserResolution(ctx context.Context, tx *gorm.DB, scope placementResolutionScope) (*PlacementFirstDisposition, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		UPDATE placement_runs
		SET status = 'quarantined',
		    error = '',
		    lease_until = NULL,
		    worker_id = '',
		    completed_at = now(),
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND status <> 'processing'
		RETURNING created_at, completed_at
	`, scope.TeamID, scope.OwnerProfileID, scope.PlacementRunID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, ErrPlacementResolutionInvalidState
	}
	var createdAt, completedAt time.Time
	if err := rows.Scan(&createdAt, &completedAt); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return appendPlacementFirstDisposition(ctx, tx, scope.TeamID, scope.OwnerProfileID, scope.PlacementRunID, string(domain.PlacementRunQuarantined), createdAt, completedAt)
}

func finishPlacementRunAfterUserResolution(ctx context.Context, tx *gorm.DB, scope placementResolutionScope) (*PlacementFirstDisposition, error) {
	var openCount, reviewCount int64
	if err := tx.WithContext(ctx).Raw(`
		SELECT
		    COUNT(*) FILTER (WHERE status IN ('queued', 'processing')),
		    COUNT(*) FILTER (WHERE status = 'awaiting_review')
		FROM placement_items
		WHERE team_id = ?::uuid
		  AND placement_run_id = ?::uuid
	`, scope.TeamID, scope.PlacementRunID).Row().Scan(&openCount, &reviewCount); err != nil {
		return nil, err
	}
	if openCount > 0 {
		return nil, nil
	}
	runStatus := string(domain.PlacementRunCompleted)
	if reviewCount > 0 {
		runStatus = string(domain.PlacementRunAwaitingReview)
	}
	rows, err := tx.WithContext(ctx).Raw(`
		UPDATE placement_runs
		SET status = ?,
		    error = '',
		    lease_until = NULL,
		    worker_id = '',
		    completed_at = now(),
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND status <> 'processing'
		RETURNING created_at, completed_at
	`, runStatus, scope.TeamID, scope.OwnerProfileID, scope.PlacementRunID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, ErrPlacementResolutionInvalidState
	}
	var createdAt, completedAt time.Time
	if err := rows.Scan(&createdAt, &completedAt); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return appendPlacementFirstDisposition(ctx, tx, scope.TeamID, scope.OwnerProfileID, scope.PlacementRunID, runStatus, createdAt, completedAt)
}
