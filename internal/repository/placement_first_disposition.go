package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

func appendPlacementFirstDisposition(
	ctx context.Context,
	tx *gorm.DB,
	teamID, ownerProfileID, placementRunID, status string,
	createdAt, completedAt time.Time,
) (*PlacementFirstDisposition, error) {
	if completedAt.IsZero() {
		return nil, errors.New("placement first disposition requires completed_at")
	}
	inserted, err := insertPlacementFirstDisposition(ctx, tx, teamID, ownerProfileID, placementRunID, status)
	if err != nil {
		return nil, err
	}
	if !inserted {
		return nil, nil
	}
	return &PlacementFirstDisposition{
		Status:      status,
		CreatedAt:   createdAt.UTC(),
		CompletedAt: completedAt.UTC(),
	}, nil
}

func insertPlacementFirstDisposition(
	ctx context.Context,
	tx *gorm.DB,
	teamID, ownerProfileID, placementRunID, status string,
) (bool, error) {
	payload, err := marshalJSON(map[string]any{"telemetry": "first_disposition"})
	if err != nil {
		return false, err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO placement_outcomes (
		    team_id, placement_run_id, owner_profile_id,
		    outcome_kind, status, idempotency_key, payload
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid,
		    'telemetry_first_disposition', ?, ?, ?::jsonb
		)
		ON CONFLICT (team_id, owner_profile_id, idempotency_key)
		WHERE idempotency_key <> ''
		DO NOTHING
		RETURNING outcome_id::text
	`, teamID, placementRunID, ownerProfileID, status,
		"telemetry:first_disposition:"+placementRunID, string(payload)).Rows()
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, rows.Err()
	}
	var outcomeID string
	if err := rows.Scan(&outcomeID); err != nil {
		return false, err
	}
	return true, rows.Err()
}
