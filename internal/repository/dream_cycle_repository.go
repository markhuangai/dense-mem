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
		query := `
			INSERT INTO dream_cycle_runs (
			    team_id, space_id, space_generation, initiated_by_profile_id, run_date, window_key, scheduled_for,
			    status, lease_token, lease_until, attempt_count, source_snapshot
			) VALUES (
			    ?::uuid, dense_mem_team_shared_space(?::uuid), dense_mem_team_shared_generation(?::uuid),
			    NULLIF(?, '')::uuid, ?, ?, ?, 'running', ?::uuid, ?, 1,
			    ?::jsonb
			)
			ON CONFLICT (team_id, window_key)
			WHERE canonical_run_id IS NULL
			DO NOTHING
			RETURNING ` + dreamCycleRunSelectColumns
		rows, err := tx.WithContext(ctx).Raw(query, input.TeamID, input.TeamID, input.TeamID, input.InitiatedByProfileID,
			input.RunDate, input.WindowKey, input.ScheduledFor, input.LeaseToken,
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

func (r *SemanticRepositoryImpl) ClaimRecoverableScheduledDreamCycle(
	ctx context.Context,
	input DreamCycleRecoveryClaimInput,
) (*DreamCycleRun, error) {
	input = normalizeDreamCycleRecoveryClaimInput(input)
	if err := validateDreamCycleRecoveryClaimInput(input); err != nil {
		return nil, err
	}
	var run *DreamCycleRun
	err := r.withDreamWriteTx(ctx, input.TeamID, "", true, func(tx *gorm.DB) error {
		if err := failExhaustedScheduledDreamRecoveries(ctx, tx, input.TeamID, input.MaxAttempts); err != nil {
			return err
		}
		query := `
			WITH candidate AS (
				SELECT run_id
				FROM dream_cycle_runs
				WHERE team_id = ?::uuid
				  AND space_id = dense_mem_team_shared_space(team_id)
				  AND space_generation = dense_mem_team_shared_generation(team_id)
				  AND canonical_run_id IS NULL
				  AND status = 'running'
				  AND scheduled_for IS NOT NULL
				  AND lease_until IS NOT NULL
				  AND lease_until <= now()
				  AND attempt_count < ?
				ORDER BY lease_until, started_at, run_id
				LIMIT 1
				FOR UPDATE SKIP LOCKED
			)
			UPDATE dream_cycle_runs run
			SET lease_token = ?::uuid,
			    lease_until = ?,
			    attempt_count = run.attempt_count + 1,
			    started_at = now(),
			    completed_at = NULL,
			    error = '',
			    updated_at = now()
			FROM candidate
				WHERE run.team_id = ?::uuid
				  AND run.space_id = dense_mem_team_shared_space(run.team_id)
				  AND run.space_generation = dense_mem_team_shared_generation(run.team_id)
				  AND run.run_id = candidate.run_id
			RETURNING ` + dreamCycleRunColumns("run")
		rows, err := tx.WithContext(ctx).Raw(query, input.TeamID, input.MaxAttempts,
			input.LeaseToken, input.LeaseUntil, input.TeamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		loaded, err := scanDreamCycleRun(rows)
		if err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		loaded.Claimed = true
		run = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dream: claim recoverable scheduled cycle: %w", err)
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
		outcomeSummary, err := marshalDreamOutcomeSummary(input.OutcomeSummary)
		if err != nil {
			return err
		}
		res := tx.WithContext(ctx).Exec(`
			UPDATE dream_cycle_runs
			SET status = ?,
			    input_count = ?,
			    created_hypotheses = ?,
			    rejected_hypotheses = ?,
			    source_snapshot = ?::jsonb,
			    provider_model = ?,
			    provider_turns = ?,
			    provider_input_tokens = ?,
			    provider_output_tokens = ?,
			    attempted_paths = ?,
			    provider_proposals = ?,
			    outcome_summary = ?::jsonb,
			    error = ?,
			    lease_until = NULL,
			    completed_at = now(),
			    updated_at = now()
				WHERE team_id = ?::uuid
				  AND space_id = dense_mem_team_shared_space(team_id)
				  AND space_generation = dense_mem_team_shared_generation(team_id)
				  AND run_id = ?::uuid
			  AND status = 'running'
			  AND lease_token = ?::uuid
			  AND lease_until > now()
		`, input.Status, input.InputCount, input.CreatedHypotheses, input.RejectedHypotheses,
			string(snapshot), input.ProviderModel, input.ProviderTurns,
			input.ProviderInputTokens, input.ProviderOutputTokens, input.AttemptedPaths,
			input.ProviderProposals, string(outcomeSummary), input.Error, input.TeamID,
			input.RunID, input.LeaseToken)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrDreamCycleLeaseLost
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

func requireCurrentDreamCycleLease(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	runID string,
	leaseToken string,
) error {
	var currentRunID string
	err := tx.WithContext(ctx).Raw(`
		SELECT run_id::text
		FROM dream_cycle_runs
		WHERE team_id = ?::uuid
		  AND space_id = dense_mem_team_shared_space(team_id)
		  AND space_generation = dense_mem_team_shared_generation(team_id)
		  AND run_id = ?::uuid
		  AND status = 'running'
		  AND lease_token = ?::uuid
		  AND lease_until > now()
		FOR UPDATE
	`, teamID, runID, leaseToken).Row().Scan(&currentRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDreamCycleLeaseLost
	}
	return err
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
		query := `
			INSERT INTO dream_cycle_runs (
			    team_id, space_id, space_generation, run_date, window_key, scheduled_for, status, source_snapshot,
			    completed_at, error
			) VALUES (
			    ?::uuid, dense_mem_team_shared_space(?::uuid), dense_mem_team_shared_generation(?::uuid), ?, ?, ?, 'missed', ?::jsonb, now(),
			    'scheduler was not running during the configured window'
			)
			ON CONFLICT (team_id, window_key)
			WHERE canonical_run_id IS NULL
			DO NOTHING
			RETURNING ` + dreamCycleRunSelectColumns
		rows, err := tx.WithContext(ctx).Raw(query, input.TeamID, input.TeamID, input.TeamID, input.RunDate,
			input.WindowKey, input.ScheduledFor, string(snapshot)).Rows()
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

func failExhaustedScheduledDreamRecoveries(ctx context.Context, tx *gorm.DB, teamID string, maxAttempts int) error {
	rows, err := tx.WithContext(ctx).Raw(`
		UPDATE dream_cycle_runs
		SET status = 'failed',
		    error = 'scheduled dream recovery attempts exhausted',
		    lease_until = NULL,
		    completed_at = now(),
		    updated_at = now(),
		    outcome_summary = outcome_summary || jsonb_build_object('recovery_exhausted', 1)
		WHERE team_id = ?::uuid
		  AND space_id = dense_mem_team_shared_space(team_id)
		  AND space_generation = dense_mem_team_shared_generation(team_id)
		  AND canonical_run_id IS NULL
		  AND status = 'running'
		  AND scheduled_for IS NOT NULL
		  AND lease_until IS NOT NULL
		  AND lease_until <= now()
		  AND attempt_count >= ?
		RETURNING run_id::text, input_count, created_hypotheses, rejected_hypotheses
	`, teamID, maxAttempts).Rows()
	if err != nil {
		return err
	}
	failedRuns := make([]struct {
		runID    string
		inputs   int
		created  int
		rejected int
	}, 0)
	for rows.Next() {
		var runID string
		var inputCount, created, rejected int
		if err := rows.Scan(&runID, &inputCount, &created, &rejected); err != nil {
			_ = rows.Close()
			return err
		}
		failedRuns = append(failedRuns, struct {
			runID    string
			inputs   int
			created  int
			rejected int
		}{runID: runID, inputs: inputCount, created: created, rejected: rejected})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, failed := range failedRuns {
		if err := insertScheduledDreamAudit(ctx, tx, DreamCycleCompleteInput{
			TeamID:             teamID,
			RunID:              failed.runID,
			Status:             "failed",
			InputCount:         failed.inputs,
			CreatedHypotheses:  failed.created,
			RejectedHypotheses: failed.rejected,
			Error:              "scheduled dream recovery attempts exhausted",
			OutcomeSummary:     map[string]int{"recovery_exhausted": 1},
		}); err != nil {
			return err
		}
	}
	return nil
}

func insertScheduledDreamAudit(ctx context.Context, tx *gorm.DB, input DreamCycleCompleteInput) error {
	operation := "dream_cycle_completed"
	switch input.Status {
	case "failed":
		operation = "dream_cycle_failed"
	case "missed":
		operation = "dream_cycle_missed"
	}
	outcomeSummary, err := marshalDreamOutcomeSummary(input.OutcomeSummary)
	if err != nil {
		return err
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
		        'rejected_dreams', ?::integer,
		        'attempted_paths', ?::integer,
		        'provider_proposals', ?::integer,
		        'outcomes', ?::jsonb
		    ),
		    'system', jsonb_build_object('scheduled', true)
		)
	`, input.TeamID, operation, input.RunID, input.Status, input.InputCount,
		input.CreatedHypotheses, input.RejectedHypotheses, input.AttemptedPaths,
		input.ProviderProposals, string(outcomeSummary)).Error
}

func marshalDreamOutcomeSummary(summary map[string]int) ([]byte, error) {
	value := make(map[string]any, len(summary))
	for key, count := range summary {
		value[key] = count
	}
	return marshalJSON(value)
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
			if err := seedTeamPredicateDefinitions(ctx, tx, teamID); err != nil {
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
