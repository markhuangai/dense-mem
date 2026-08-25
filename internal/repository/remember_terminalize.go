package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type RememberTerminalizeFailureInput struct {
	TeamID         string
	OwnerProfileID string
	IngestID       string
	FailedPhase    string
	ErrorCode      string
	Message        string
}

func (r *LedgerRepositoryImpl) TerminalizeRememberFailure(ctx context.Context, input RememberTerminalizeFailureInput) error {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.FailedPhase = strings.TrimSpace(input.FailedPhase)
	input.ErrorCode = strings.TrimSpace(input.ErrorCode)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("remember terminalize: team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("remember terminalize: owner_profile_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.IngestID); err != nil {
		return fmt.Errorf("remember terminalize: ingest_id is required: %w", err)
	}
	if input.ErrorCode == "" {
		input.ErrorCode = "internal_failure"
	}
	if input.FailedPhase == "" {
		input.FailedPhase = "execution"
	}
	if input.Message == "" {
		input.Message = "synchronous Remember execution did not reach a terminal state"
	}
	resultJSON, err := json.Marshal(map[string]any{
		"contract_version": domain.ContractVersion, "failure_stage": input.FailedPhase,
		"failure_code": input.ErrorCode, "retryable": true,
		"next_action": "retry_same_request", "reason": input.Message,
	})
	if err != nil {
		return err
	}
	return r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Exec(`
			UPDATE placement_items
			SET status = 'failed', category = 'failed', error = ?, result = ?::jsonb, updated_at = now()
			WHERE team_id = ?::uuid AND placement_run_id IN (
			    SELECT placement_run_id FROM placement_runs
			    WHERE team_id = ?::uuid AND ingest_id = ?::uuid AND owner_profile_id = ?::uuid
			) AND status IN ('queued', 'guarded', 'processing')
		`, input.Message, string(resultJSON), input.TeamID, input.TeamID, input.IngestID, input.OwnerProfileID).Error; err != nil {
			return err
		}
		updated := tx.WithContext(ctx).Exec(`
			UPDATE placement_runs
			SET status = 'failed', error = ?, completed_at = COALESCE(completed_at, now()),
			    worker_id = '', lease_until = NULL, updated_at = now()
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid AND owner_profile_id = ?::uuid
			  AND status IN ('queued', 'guarded', 'processing')
		`, input.Message, input.TeamID, input.IngestID, input.OwnerProfileID)
		if updated.Error != nil {
			return updated.Error
		}
		markIngestFailed := func() error {
			return tx.WithContext(ctx).Exec(`
				UPDATE knowledge_ingests
				SET status = 'failed', error = ?, completed_at = COALESCE(completed_at, now()), updated_at = now()
				WHERE team_id = ?::uuid
				  AND ingest_id = ?::uuid
				  AND owner_profile_id = ?::uuid
				  AND status IN ('queued', 'guarded', 'processing')
			`, input.Message, input.TeamID, input.IngestID, input.OwnerProfileID).Error
		}
		if updated.RowsAffected == 0 {
			var status string
			err := tx.WithContext(ctx).Raw(`
				SELECT status FROM placement_runs
				WHERE team_id = ?::uuid AND ingest_id = ?::uuid AND owner_profile_id = ?::uuid
			`, input.TeamID, input.IngestID, input.OwnerProfileID).Row().Scan(&status)
			if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPlacementNotFound
			}
			if err != nil {
				return err
			}
			if status == "failed" || status == "completed" || status == "rejected" || status == "quarantined" {
				if status == "failed" {
					return markIngestFailed()
				}
				return nil
			}
			return errors.New("remember terminalize: placement state changed")
		}
		return markIngestFailed()
	})
}
