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

type submissionReplacementTarget struct {
	PlacementRunID string
}

func lockSubmissionReplacementTarget(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	ownerProfileID string,
	ingestID string,
) (*submissionReplacementTarget, error) {
	if _, err := uuid.Parse(ingestID); err != nil {
		return nil, fmt.Errorf("%w: invalid submission id", ErrSubmissionReplacementNotFound)
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT run.placement_run_id::text,
		       run.owner_profile_id::text,
		       COALESCE(run.semantic_hold_state, ''),
		       COALESCE(run.superseded_by_placement_run_id::text, ''),
		       EXISTS (
		           SELECT 1
		           FROM submission_holds AS hold
		           WHERE hold.team_id = run.team_id
		             AND hold.placement_run_id = run.placement_run_id
		       ) AS has_hold,
		       EXISTS (
		           SELECT 1
		           FROM placement_runs AS successor
		           WHERE successor.team_id = run.team_id
		             AND successor.replaces_placement_run_id = run.placement_run_id
		             AND successor.status IN ('queued', 'guarded', 'processing')
		       ) AS has_active_successor
		FROM placement_runs AS run
		WHERE run.team_id = ?::uuid
		  AND run.ingest_id = ?::uuid
		FOR UPDATE
	`, teamID, ingestID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, ErrSubmissionReplacementNotFound
	}
	var target submissionReplacementTarget
	var targetOwner, holdState, supersededBy string
	var hasHold, hasActiveSuccessor bool
	if err := rows.Scan(&target.PlacementRunID, &targetOwner, &holdState, &supersededBy, &hasHold, &hasActiveSuccessor); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if targetOwner != ownerProfileID || !hasHold {
		return nil, ErrSubmissionReplacementNotFound
	}
	if supersededBy != "" || holdState == "superseded" || hasActiveSuccessor {
		return nil, ErrSubmissionReplacementConflict
	}
	if holdState != "active" && holdState != "expired" {
		return nil, ErrSubmissionReplacementNotFound
	}
	return &target, nil
}

type ExpireSubmissionHoldsInput struct {
	TeamID string
	Now    time.Time
}

type ExpireSubmissionHoldsResult struct {
	Expired int64
}

type SemanticSubmissionHoldExpiry interface {
	ExpireSubmissionHolds(ctx context.Context, input ExpireSubmissionHoldsInput) (ExpireSubmissionHoldsResult, error)
}

func (r *LedgerRepositoryImpl) ExpireSubmissionHolds(
	ctx context.Context,
	input ExpireSubmissionHoldsInput,
) (ExpireSubmissionHoldsResult, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return ExpireSubmissionHoldsResult{}, fmt.Errorf("team_id is required: %w", err)
	}
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	}
	input.Now = input.Now.UTC()
	result := ExpireSubmissionHoldsResult{}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('app.current_team_id', ?, true)", input.TeamID).Error; err != nil {
			return fmt.Errorf("set hold expiry team context: %w", err)
		}
		if err := ensureActiveTeamForMutation(ctx, tx, input.TeamID); err != nil {
			return err
		}
		type expiredHold struct {
			OwnerProfileID string
			PlacementRunID string
			HeldAt         time.Time
			ExpiresAt      time.Time
			ReasonCode     string
		}
		rows, err := tx.WithContext(ctx).Raw(`
			UPDATE placement_runs AS run
			SET semantic_hold_state = 'expired',
			    semantic_hold_version = run.semantic_hold_version + 1,
			    semantic_hold_updated_at = ?,
			    updated_at = ?
			FROM submission_holds AS hold
			WHERE run.team_id = ?::uuid
			  AND hold.team_id = run.team_id
			  AND hold.placement_run_id = run.placement_run_id
			  AND run.semantic_hold_state = 'active'
			  AND hold.expires_at <= ?
			RETURNING run.owner_profile_id::text, run.placement_run_id::text,
			          hold.held_at, hold.expires_at, hold.reason_code
		`, input.Now, input.Now, input.TeamID, input.Now).Rows()
		if err != nil {
			return err
		}
		expired := make([]expiredHold, 0)
		for rows.Next() {
			var hold expiredHold
			if err := rows.Scan(&hold.OwnerProfileID, &hold.PlacementRunID, &hold.HeldAt, &hold.ExpiresAt, &hold.ReasonCode); err != nil {
				_ = rows.Close()
				return err
			}
			expired = append(expired, hold)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, hold := range expired {
			payload := map[string]any{
				"reason_code": hold.ReasonCode,
				"held_at":     hold.HeldAt,
				"expires_at":  hold.ExpiresAt,
				"expired_at":  input.Now,
			}
			if _, err := insertPlacementOutcome(ctx, tx, PlacementOutcomeInput{
				TeamID:         input.TeamID,
				OwnerProfileID: hold.OwnerProfileID,
				PlacementRunID: hold.PlacementRunID,
				OutcomeKind:    "submission_hold_expired",
				Status:         "expired",
				IdempotencyKey: "submission-hold:" + hold.PlacementRunID + ":expired",
				Payload:        payload,
			}); err != nil {
				return err
			}
		}
		result.Expired = int64(len(expired))
		return nil
	})
	if err != nil {
		return ExpireSubmissionHoldsResult{}, fmt.Errorf("submission hold expiry: %w", err)
	}
	return result, nil
}

func createSubmissionHoldProjection(
	ctx context.Context,
	tx *gorm.DB,
	scope SubmissionAssessmentRunScope,
	reasonCode string,
) error {
	reasonCode = strings.TrimSpace(reasonCode)
	if reasonCode == "" {
		reasonCode = "policy_review"
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT run.completed_at, assessment.assessment_id::text
		FROM placement_runs AS run
		JOIN placement_assessments AS assessment
		  ON assessment.team_id = run.team_id
		 AND assessment.placement_run_id = run.placement_run_id
		 AND assessment.ingest_id = run.ingest_id
		 AND assessment.owner_profile_id = run.owner_profile_id
		 AND assessment.assessment_scope = 'submission'
		WHERE run.team_id = ?::uuid
		  AND run.owner_profile_id = ?::uuid
		  AND run.ingest_id = ?::uuid
		  AND run.placement_run_id = ?::uuid
		  AND run.status = 'awaiting_review'
		  AND run.completed_at IS NOT NULL
		FOR UPDATE OF run
	`, scope.TeamID, scope.OwnerProfileID, scope.IngestID, scope.PlacementRunID).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return ErrSubmissionAssessmentScopeMismatch
	}
	var heldAt time.Time
	var assessmentID string
	if err := rows.Scan(&heldAt, &assessmentID); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO submission_holds (
		    team_id, placement_run_id, ingest_id, assessment_id, owner_profile_id,
		    reason_code, held_at, expires_at
		) VALUES (?, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?)
		ON CONFLICT (team_id, placement_run_id) DO NOTHING
	`, scope.TeamID, scope.PlacementRunID, scope.IngestID, assessmentID, scope.OwnerProfileID,
		reasonCode, heldAt, heldAt.Add(24*time.Hour)).Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec(`
		UPDATE placement_runs
		SET semantic_hold_state = 'active',
		    semantic_hold_version = CASE WHEN semantic_hold_version < 1 THEN 1 ELSE semantic_hold_version + 1 END,
		    semantic_hold_updated_at = ?,
		    updated_at = ?
		WHERE team_id = ?::uuid
		  AND placement_run_id = ?::uuid
	`, heldAt, heldAt, scope.TeamID, scope.PlacementRunID).Error; err != nil {
		return err
	}
	_, err = insertPlacementOutcome(ctx, tx, PlacementOutcomeInput{
		TeamID:         scope.TeamID,
		OwnerProfileID: scope.OwnerProfileID,
		PlacementRunID: scope.PlacementRunID,
		OutcomeKind:    "submission_hold_created",
		Status:         "active",
		IdempotencyKey: "submission-hold:" + scope.PlacementRunID + ":created",
		Payload: map[string]any{
			"reason_code": reasonCode,
			"held_at":     heldAt,
			"expires_at":  heldAt.Add(24 * time.Hour),
		},
	})
	return err
}

func releaseSubmissionReplacement(
	ctx context.Context,
	tx *gorm.DB,
	scope SubmissionAssessmentRunScope,
	status string,
	reason string,
) error {
	status = strings.TrimSpace(status)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "successor_terminal"
	}
	var targetRunID string
	if err := tx.WithContext(ctx).Raw(`
		SELECT replaces_placement_run_id::text
		FROM placement_runs
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND replaces_placement_run_id IS NOT NULL
	`, scope.TeamID, scope.OwnerProfileID, scope.PlacementRunID).Row().Scan(&targetRunID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	_, err := insertPlacementOutcome(ctx, tx, PlacementOutcomeInput{
		TeamID:         scope.TeamID,
		OwnerProfileID: scope.OwnerProfileID,
		PlacementRunID: scope.PlacementRunID,
		OutcomeKind:    "submission_replacement_released",
		Status:         status,
		IdempotencyKey: "submission-replacement:" + scope.PlacementRunID + ":released",
		Payload: map[string]any{
			"target_placement_run_id": targetRunID,
			"reason":                  reason,
		},
	})
	return err
}

func promoteSubmissionReplacement(
	ctx context.Context,
	tx *gorm.DB,
	scope SubmissionAssessmentRunScope,
) error {
	var targetRunID, targetState string
	row := tx.WithContext(ctx).Raw(`
		SELECT successor.replaces_placement_run_id::text, target.semantic_hold_state
		FROM placement_runs AS successor
		JOIN placement_runs AS target
		  ON target.team_id = successor.team_id
		 AND target.placement_run_id = successor.replaces_placement_run_id
		 AND target.owner_profile_id = successor.owner_profile_id
		JOIN submission_holds AS hold
		  ON hold.team_id = target.team_id
		 AND hold.placement_run_id = target.placement_run_id
		WHERE successor.team_id = ?::uuid
		  AND successor.owner_profile_id = ?::uuid
		  AND successor.placement_run_id = ?::uuid
		  AND successor.replaces_placement_run_id IS NOT NULL
		FOR UPDATE OF target
	`, scope.TeamID, scope.OwnerProfileID, scope.PlacementRunID).Row()
	if err := row.Scan(&targetRunID, &targetState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if targetState != "active" && targetState != "expired" {
		return ErrSubmissionReplacementConflict
	}
	if err := tx.WithContext(ctx).Exec(`
		UPDATE placement_runs
		SET semantic_hold_state = 'superseded',
		    semantic_hold_version = semantic_hold_version + 1,
		    semantic_hold_updated_at = now(),
		    superseded_by_placement_run_id = ?::uuid,
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND semantic_hold_state IN ('active', 'expired')
	`, scope.PlacementRunID, scope.TeamID, targetRunID).Error; err != nil {
		return err
	}
	if _, err := insertPlacementOutcome(ctx, tx, PlacementOutcomeInput{
		TeamID:         scope.TeamID,
		OwnerProfileID: scope.OwnerProfileID,
		PlacementRunID: targetRunID,
		OutcomeKind:    "submission_hold_superseded",
		Status:         "superseded",
		IdempotencyKey: "submission-hold:" + targetRunID + ":superseded-by:" + scope.PlacementRunID,
		Payload: map[string]any{
			"successor_placement_run_id": scope.PlacementRunID,
		},
	}); err != nil {
		return err
	}
	_, err := insertPlacementOutcome(ctx, tx, PlacementOutcomeInput{
		TeamID:         scope.TeamID,
		OwnerProfileID: scope.OwnerProfileID,
		PlacementRunID: scope.PlacementRunID,
		OutcomeKind:    "submission_replacement_promoted",
		Status:         "accepted",
		IdempotencyKey: "submission-replacement:" + scope.PlacementRunID + ":promoted",
		Payload: map[string]any{
			"target_placement_run_id": targetRunID,
		},
	})
	return err
}
