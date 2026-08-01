package repository

import (
	"context"
	"database/sql"
	"fmt"

	"gorm.io/gorm"
)

const maxPlacementFirstDispositionBackfillBatchSize = 1_000

// PlacementFirstDispositionBackfillResult describes one coordinated marker
// backfill batch. SweepComplete is true only after a full keyset sweep found
// no eligible unmarked runs, including rows skipped by a prior locked batch.
type PlacementFirstDispositionBackfillResult struct {
	Candidates          int64
	Inserted            int64
	SweepComplete       bool
	WaitingForActiveRun bool
}

type placementFirstDispositionBackfillCursor struct {
	TeamID   string
	IngestID string
}

type placementFirstDispositionBackfillState struct {
	Cursor    *placementFirstDispositionBackfillCursor
	Completed bool
}

type placementFirstDispositionBackfillBatch struct {
	Candidates   int64
	Inserted     int64
	LastTeamID   sql.NullString
	LastIngestID sql.NullString
}

// BackfillPlacementFirstDispositionMarkers appends a bounded batch of
// non-emitting suppression markers for Remember runs that already reached
// their first terminal or review disposition before a new worker could record
// it. The persisted state makes the scan restart-safe and prevents a completed
// recovery from scanning historical data after restart.
func (r *LedgerRepositoryImpl) BackfillPlacementFirstDispositionMarkers(
	ctx context.Context,
	limit int,
) (PlacementFirstDispositionBackfillResult, error) {
	if limit < 1 {
		return PlacementFirstDispositionBackfillResult{}, fmt.Errorf("placement first disposition backfill limit must be positive")
	}
	if limit > maxPlacementFirstDispositionBackfillBatchSize {
		limit = maxPlacementFirstDispositionBackfillBatchSize
	}
	result := PlacementFirstDispositionBackfillResult{}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		state, err := lockPlacementFirstDispositionBackfillState(ctx, tx)
		if err != nil {
			return err
		}
		if state.Completed {
			result.SweepComplete = true
			return nil
		}
		batch, err := appendPlacementFirstDispositionBackfillBatch(ctx, tx, state.Cursor, limit)
		if err != nil {
			return err
		}
		result.Candidates = batch.Candidates
		result.Inserted = batch.Inserted
		if batch.Candidates > 0 {
			if !batch.LastTeamID.Valid || !batch.LastIngestID.Valid {
				return fmt.Errorf("placement first disposition backfill missing keyset cursor")
			}
			return updatePlacementFirstDispositionBackfillCursor(ctx, tx, &placementFirstDispositionBackfillCursor{
				TeamID:   batch.LastTeamID.String,
				IngestID: batch.LastIngestID.String,
			})
		}

		pendingAfterCursor, err := placementFirstDispositionBackfillCandidatesExist(ctx, tx, state.Cursor)
		if err != nil {
			return err
		}
		if pendingAfterCursor {
			return nil
		}
		if state.Cursor == nil {
			pendingRuns, err := placementFirstDispositionBackfillPendingRunsExist(ctx, tx)
			if err != nil {
				return err
			}
			if pendingRuns {
				result.WaitingForActiveRun = true
				return nil
			}
			if err := completePlacementFirstDispositionBackfill(ctx, tx); err != nil {
				return err
			}
			result.SweepComplete = true
			return nil
		}
		if err := updatePlacementFirstDispositionBackfillCursor(ctx, tx, nil); err != nil {
			return err
		}
		pendingBeforeCursor, err := placementFirstDispositionBackfillCandidatesExist(ctx, tx, nil)
		if err != nil {
			return err
		}
		if !pendingBeforeCursor {
			pendingRuns, err := placementFirstDispositionBackfillPendingRunsExist(ctx, tx)
			if err != nil {
				return err
			}
			if pendingRuns {
				result.WaitingForActiveRun = true
				return nil
			}
			if err := completePlacementFirstDispositionBackfill(ctx, tx); err != nil {
				return err
			}
			result.SweepComplete = true
		}
		return nil
	})
	if err != nil {
		return PlacementFirstDispositionBackfillResult{}, fmt.Errorf("placement first disposition backfill: %w", err)
	}
	return result, nil
}

func lockPlacementFirstDispositionBackfillState(
	ctx context.Context,
	tx *gorm.DB,
) (placementFirstDispositionBackfillState, error) {
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO telemetry_first_disposition_backfill_state (state_key)
		VALUES ('placement_first_disposition')
		ON CONFLICT (state_key) DO NOTHING
	`).Error; err != nil {
		return placementFirstDispositionBackfillState{}, err
	}
	var state struct {
		TeamID      sql.NullString
		IngestID    sql.NullString
		CompletedAt sql.NullTime
	}
	if err := tx.WithContext(ctx).Raw(`
		SELECT cursor_team_id::text, cursor_ingest_id::text, completed_at
		FROM telemetry_first_disposition_backfill_state
		WHERE state_key = 'placement_first_disposition'
		FOR UPDATE
	`).Row().Scan(&state.TeamID, &state.IngestID, &state.CompletedAt); err != nil {
		return placementFirstDispositionBackfillState{}, err
	}
	if state.TeamID.Valid != state.IngestID.Valid {
		return placementFirstDispositionBackfillState{}, fmt.Errorf("placement first disposition backfill cursor is invalid")
	}
	if state.CompletedAt.Valid {
		if state.TeamID.Valid {
			return placementFirstDispositionBackfillState{}, fmt.Errorf("placement first disposition backfill completion is invalid")
		}
		return placementFirstDispositionBackfillState{Completed: true}, nil
	}
	if !state.TeamID.Valid {
		return placementFirstDispositionBackfillState{}, nil
	}
	return placementFirstDispositionBackfillState{Cursor: &placementFirstDispositionBackfillCursor{
		TeamID:   state.TeamID.String,
		IngestID: state.IngestID.String,
	}}, nil
}

func appendPlacementFirstDispositionBackfillBatch(
	ctx context.Context,
	tx *gorm.DB,
	cursor *placementFirstDispositionBackfillCursor,
	limit int,
) (placementFirstDispositionBackfillBatch, error) {
	query := fmt.Sprintf(`
		WITH candidate_runs AS MATERIALIZED (
			SELECT
				ingest.team_id,
				ingest.ingest_id,
				run.placement_run_id,
				run.owner_profile_id,
				run.status AS marker_status
			FROM knowledge_ingests AS ingest
			JOIN placement_runs AS run
			  ON run.team_id = ingest.team_id
			 AND run.ingest_id = ingest.ingest_id
			WHERE %s
			  AND (
			      (
			          run.status IN ('awaiting_review', 'completed', 'failed', 'quarantined')
			          AND run.completed_at IS NOT NULL
			      )
			      OR (
			          run.status IN ('queued', 'guarded', 'processing')
			          AND %s
			      )
			  )
			  AND NOT EXISTS (
				  SELECT 1
				  FROM placement_outcomes AS marker
				  WHERE marker.team_id = run.team_id
				    AND marker.placement_run_id = run.placement_run_id
				    AND marker.outcome_kind = 'telemetry_first_disposition'
			  )`, placementRememberTelemetryOriginCondition, placementFirstDispositionPriorOutcomeCondition)
	args := make([]any, 0, 3)
	if cursor != nil {
		query += `
			  AND (ingest.team_id, ingest.ingest_id) > (?::uuid, ?::uuid)`
		args = append(args, cursor.TeamID, cursor.IngestID)
	}
	query += `
			ORDER BY ingest.team_id ASC, ingest.ingest_id ASC
			LIMIT ?
			FOR UPDATE OF run SKIP LOCKED
		), inserted AS (
			INSERT INTO placement_outcomes (
				team_id, placement_run_id, owner_profile_id,
				outcome_kind, status, idempotency_key, payload
			)
			SELECT
				team_id,
				placement_run_id,
				owner_profile_id,
				'telemetry_first_disposition',
				CASE
				    WHEN marker_status IN ('queued', 'guarded', 'processing') THEN 'suppressed'
				    ELSE marker_status
				END,
				'telemetry:first_disposition:' || placement_run_id::text,
				'{"backfilled": true, "telemetry": "first_disposition"}'::jsonb
			FROM candidate_runs
			ON CONFLICT (team_id, placement_run_id)
			WHERE outcome_kind = 'telemetry_first_disposition'
			DO NOTHING
			RETURNING outcome_id
		)
		SELECT
			(SELECT count(*) FROM candidate_runs)::bigint,
			(SELECT count(*) FROM inserted)::bigint,
			(
				SELECT team_id::text
				FROM candidate_runs
				ORDER BY team_id DESC, ingest_id DESC
				LIMIT 1
			),
			(
				SELECT ingest_id::text
				FROM candidate_runs
				ORDER BY team_id DESC, ingest_id DESC
				LIMIT 1
			)
	`
	args = append(args, limit)
	var batch placementFirstDispositionBackfillBatch
	err := tx.WithContext(ctx).Raw(query, args...).Row().Scan(
		&batch.Candidates,
		&batch.Inserted,
		&batch.LastTeamID,
		&batch.LastIngestID,
	)
	return batch, err
}

func placementFirstDispositionBackfillCandidatesExist(
	ctx context.Context,
	tx *gorm.DB,
	cursor *placementFirstDispositionBackfillCursor,
) (bool, error) {
	query := fmt.Sprintf(`
		SELECT EXISTS (
			SELECT 1
			FROM knowledge_ingests AS ingest
			JOIN placement_runs AS run
			  ON run.team_id = ingest.team_id
			 AND run.ingest_id = ingest.ingest_id
			WHERE %s
			  AND (
			      (
			          run.status IN ('awaiting_review', 'completed', 'failed', 'quarantined')
			          AND run.completed_at IS NOT NULL
			      )
			      OR (
			          run.status IN ('queued', 'guarded', 'processing')
			          AND %s
			      )
			  )
			  AND NOT EXISTS (
				  SELECT 1
				  FROM placement_outcomes AS marker
				  WHERE marker.team_id = run.team_id
				    AND marker.placement_run_id = run.placement_run_id
				    AND marker.outcome_kind = 'telemetry_first_disposition'
			  )`, placementRememberTelemetryOriginCondition, placementFirstDispositionPriorOutcomeCondition)
	args := make([]any, 0, 2)
	if cursor != nil {
		query += `
			  AND (ingest.team_id, ingest.ingest_id) > (?::uuid, ?::uuid)`
		args = append(args, cursor.TeamID, cursor.IngestID)
	}
	query += `
		)
	`
	var exists bool
	err := tx.WithContext(ctx).Raw(query, args...).Row().Scan(&exists)
	return exists, err
}

func placementFirstDispositionBackfillPendingRunsExist(
	ctx context.Context,
	tx *gorm.DB,
) (bool, error) {
	var exists bool
	err := tx.WithContext(ctx).Raw(fmt.Sprintf(`
		SELECT EXISTS (
			SELECT 1
			FROM knowledge_ingests AS ingest
			JOIN placement_runs AS run
			  ON run.team_id = ingest.team_id
			 AND run.ingest_id = ingest.ingest_id
			WHERE %s
			  AND run.status IN ('queued', 'guarded', 'processing')
			  AND NOT (%s)
			  AND NOT EXISTS (
				  SELECT 1
				  FROM placement_outcomes AS marker
				  WHERE marker.team_id = run.team_id
				    AND marker.placement_run_id = run.placement_run_id
				    AND marker.outcome_kind = 'telemetry_first_disposition'
			  )
		)
	`, placementRememberTelemetryOriginCondition, placementFirstDispositionPriorOutcomeCondition)).Row().Scan(&exists)
	return exists, err
}

func updatePlacementFirstDispositionBackfillCursor(
	ctx context.Context,
	tx *gorm.DB,
	cursor *placementFirstDispositionBackfillCursor,
) error {
	var teamID, ingestID any
	if cursor != nil {
		teamID = cursor.TeamID
		ingestID = cursor.IngestID
	}
	result := tx.WithContext(ctx).Exec(`
		UPDATE telemetry_first_disposition_backfill_state
		SET cursor_team_id = ?::uuid,
			cursor_ingest_id = ?::uuid,
			completed_at = NULL,
			updated_at = clock_timestamp()
		WHERE state_key = 'placement_first_disposition'
	`, teamID, ingestID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("placement first disposition backfill state is missing")
	}
	return nil
}

func completePlacementFirstDispositionBackfill(ctx context.Context, tx *gorm.DB) error {
	result := tx.WithContext(ctx).Exec(`
		UPDATE telemetry_first_disposition_backfill_state
		SET cursor_team_id = NULL,
			cursor_ingest_id = NULL,
			completed_at = clock_timestamp(),
			updated_at = clock_timestamp()
		WHERE state_key = 'placement_first_disposition'
	`)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("placement first disposition backfill state is missing")
	}
	return nil
}
