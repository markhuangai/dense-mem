package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const placementRememberTelemetryOriginCondition = `(
	ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
	OR (
		NULLIF(ingest.metadata ->> 'contract_version', '') IS NOT NULL
		AND jsonb_typeof(ingest.metadata -> 'actor') = 'object'
		AND ingest.metadata #>> '{actor,team_id}' = ingest.team_id::text
		AND ingest.metadata #>> '{actor,profile_id}' = ingest.owner_profile_id::text
	)
)`

const placementFirstDispositionPriorOutcomeCondition = `EXISTS (
	SELECT 1
	FROM placement_outcomes AS prior
	WHERE prior.team_id = run.team_id
	  AND prior.placement_run_id = run.placement_run_id
	  AND prior.outcome_kind IN (
	      'semantic_review',
	      'semantic_review_terminal',
	      'semantic_review_expired',
	      'placement_resolution'
	  )
)`

func appendPlacementFirstDisposition(
	ctx context.Context,
	tx *gorm.DB,
	teamID, ownerProfileID, placementRunID, status string,
	createdAt, completedAt time.Time,
) (*PlacementFirstDisposition, error) {
	if createdAt.IsZero() {
		return nil, errors.New("placement first disposition requires created_at")
	}
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
	isRemember, err := placementRunIsRememberTelemetryOrigin(ctx, tx, teamID, placementRunID)
	if err != nil {
		return nil, err
	}
	return &PlacementFirstDisposition{
		Status:      status,
		CreatedAt:   createdAt.UTC(),
		CompletedAt: completedAt.UTC(),
		IsRemember:  isRemember,
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
		ON CONFLICT (team_id, placement_run_id)
		WHERE outcome_kind = 'telemetry_first_disposition'
		DO NOTHING
		RETURNING outcome_id::text
	`, teamID, placementRunID, ownerProfileID, status,
		placementFirstDispositionIdempotencyKey(placementRunID), string(payload)).Rows()
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

func placementFirstDispositionIdempotencyKey(placementRunID string) string {
	return "telemetry:first_disposition:" + placementRunID
}

func placementRunIsRememberTelemetryOrigin(
	ctx context.Context,
	tx *gorm.DB,
	teamID, placementRunID string,
) (bool, error) {
	row := tx.WithContext(ctx).Raw(fmt.Sprintf(`
		SELECT COALESCE(%s, false)
		FROM placement_runs AS run
		JOIN knowledge_ingests AS ingest
		  ON ingest.team_id = run.team_id
		 AND ingest.ingest_id = run.ingest_id
		WHERE run.team_id = ?::uuid
		  AND run.placement_run_id = ?::uuid
	`, placementRememberTelemetryOriginCondition), teamID, placementRunID).Row()
	var isRemember bool
	if err := row.Scan(&isRemember); err != nil {
		return false, err
	}
	return isRemember, nil
}
