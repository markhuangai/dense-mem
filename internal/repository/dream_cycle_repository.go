package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

func (r *SemanticRepositoryImpl) ClaimDreamCycle(ctx context.Context, input DreamCycleClaimInput) (*DreamCycleRun, error) {
	return r.claimDreamCycle(ctx, input, false)
}

func (r *SemanticRepositoryImpl) ClaimScheduledDreamCycle(ctx context.Context, input DreamCycleClaimInput) (*DreamCycleRun, error) {
	return r.claimDreamCycle(ctx, input, true)
}

func (r *SemanticRepositoryImpl) claimDreamCycle(ctx context.Context, input DreamCycleClaimInput, system bool) (*DreamCycleRun, error) {
	input = normalizeDreamCycleClaimInput(input)
	if err := validateDreamCycleClaimInput(input, system); err != nil {
		return nil, err
	}
	snapshot, err := marshalJSONArray(input.SourceSnapshot)
	if err != nil {
		return nil, err
	}
	var run *DreamCycleRun
	err = r.withDreamWriteTx(ctx, input.TeamID, input.InitiatedByProfileID, system, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			INSERT INTO dream_cycle_runs (
			    team_id, initiated_by_profile_id, run_date, window_key, lease_until,
			    source_snapshot
			) VALUES (
			    ?::uuid, NULLIF(?, '')::uuid, ?, ?, ?, ?::jsonb
			)
			ON CONFLICT (team_id, window_key)
			WHERE canonical_run_id IS NULL
			DO NOTHING
			RETURNING team_id::text, run_id::text, COALESCE(initiated_by_profile_id::text, ''),
			          run_date, window_key, status, input_count,
			          created_hypotheses, rejected_hypotheses, error,
			          started_at, completed_at
		`, input.TeamID, input.InitiatedByProfileID, input.RunDate, input.WindowKey,
			input.LeaseUntil, string(snapshot)).Rows()
		if err != nil {
			return err
		}
		if rows.Next() {
			loaded, err := scanDreamCycleRun(rows)
			closeErr := rows.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			loaded.Claimed = true
			run = loaded
			return nil
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		existing, err := loadDreamCycleByWindow(ctx, tx, input.TeamID, input.WindowKey)
		if err != nil {
			return err
		}
		existing.Claimed = false
		run = existing
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dream: claim cycle: %w", err)
	}
	return run, nil
}

func (r *SemanticRepositoryImpl) CompleteDreamCycle(ctx context.Context, input DreamCycleCompleteInput) error {
	return r.completeDreamCycle(ctx, input, false)
}

func (r *SemanticRepositoryImpl) CompleteScheduledDreamCycle(ctx context.Context, input DreamCycleCompleteInput) error {
	return r.completeDreamCycle(ctx, input, true)
}

func (r *SemanticRepositoryImpl) completeDreamCycle(ctx context.Context, input DreamCycleCompleteInput, system bool) error {
	input = normalizeDreamCycleCompleteInput(input)
	if err := validateDreamCycleCompleteInput(input, system); err != nil {
		return err
	}
	snapshot, err := marshalJSONArray(input.SourceSnapshot)
	if err != nil {
		return err
	}
	err = r.withDreamWriteTx(ctx, input.TeamID, input.InitiatedByProfileID, system, func(tx *gorm.DB) error {
		res := tx.WithContext(ctx).Exec(`
			UPDATE dream_cycle_runs
			SET status = ?,
			    input_count = ?,
			    created_hypotheses = ?,
			    rejected_hypotheses = ?,
			    source_snapshot = ?::jsonb,
			    error = ?,
			    lease_until = NULL,
			    completed_at = now(),
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND run_id = ?::uuid
		`, input.Status, input.InputCount, input.CreatedHypotheses, input.RejectedHypotheses,
			string(snapshot), input.Error, input.TeamID, input.RunID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return sql.ErrNoRows
		}
		if system {
			return insertScheduledDreamAudit(ctx, tx, input)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("dream: complete cycle: %w", err)
	}
	return nil
}

func (r *SemanticRepositoryImpl) RecordMissedScheduledDreamCycle(ctx context.Context, input DreamCycleClaimInput) (*DreamCycleRun, error) {
	input = normalizeDreamCycleClaimInput(input)
	if err := validateDreamCycleClaimInput(input, true); err != nil {
		return nil, err
	}
	snapshot, err := marshalJSONArray(input.SourceSnapshot)
	if err != nil {
		return nil, err
	}
	var run *DreamCycleRun
	err = r.withDreamWriteTx(ctx, input.TeamID, "", true, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			INSERT INTO dream_cycle_runs (
			    team_id, run_date, window_key, status, source_snapshot,
			    completed_at, error
			) VALUES (
			    ?::uuid, ?, ?, 'missed', ?::jsonb, now(),
			    'scheduler was not running during the configured window'
			)
			ON CONFLICT (team_id, window_key)
			WHERE canonical_run_id IS NULL
			DO NOTHING
			RETURNING team_id::text, run_id::text, COALESCE(initiated_by_profile_id::text, ''),
			          run_date, window_key, status, input_count,
			          created_hypotheses, rejected_hypotheses, error,
			          started_at, completed_at
		`, input.TeamID, input.RunDate, input.WindowKey, string(snapshot)).Rows()
		if err != nil {
			return err
		}
		if rows.Next() {
			loaded, err := scanDreamCycleRun(rows)
			closeErr := rows.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			loaded.Claimed = true
			run = loaded
			return insertScheduledDreamAudit(ctx, tx, DreamCycleCompleteInput{
				TeamID: input.TeamID,
				RunID:  loaded.RunID,
				Status: loaded.Status,
				Error:  loaded.Error,
			})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		existing, err := loadDreamCycleByWindow(ctx, tx, input.TeamID, input.WindowKey)
		if err != nil {
			return err
		}
		existing.Claimed = false
		run = existing
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dream: record missed cycle: %w", err)
	}
	return run, nil
}

func insertScheduledDreamAudit(ctx context.Context, tx *gorm.DB, input DreamCycleCompleteInput) error {
	operation := "dream_cycle_completed"
	if input.Status == "missed" {
		operation = "dream_cycle_missed"
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO audit_log (
		    team_id, operation, entity_type, entity_id, after_payload,
		    actor_role, metadata
		) VALUES (
		    ?::uuid, ?::text, 'dream_cycle_run', ?::text,
		    jsonb_build_object(
		        'status', ?::text,
		        'input_relationships', ?::integer,
		        'created_dreams', ?::integer,
		        'rejected_dreams', ?::integer
		    ),
		    'system', jsonb_build_object('scheduled', true)
		)
	`, input.TeamID, operation, input.RunID, input.Status, input.InputCount,
		input.CreatedHypotheses, input.RejectedHypotheses).Error
}

func (r *SemanticRepositoryImpl) withDreamWriteTx(
	ctx context.Context,
	teamID string,
	actorProfileID string,
	system bool,
	fn func(tx *gorm.DB) error,
) error {
	if !system {
		return r.withTeamProfileTx(ctx, teamID, actorProfileID, func(tx *gorm.DB) error {
			if err := ensureSemanticRefs(ctx, tx, teamID, actorProfileID); err != nil {
				return err
			}
			return fn(tx)
		})
	}
	if r == nil || r.db == nil {
		return errors.New("semantic: database is required")
	}
	if r.rls == nil {
		return errors.New("semantic: rls helper is required")
	}
	return r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('app.current_team_id', ?, true)", teamID).Error; err != nil {
			return fmt.Errorf("set dream team context: %w", err)
		}
		if err := ensureActiveTeamForMutation(ctx, tx, teamID); err != nil {
			return err
		}
		return fn(tx)
	})
}
