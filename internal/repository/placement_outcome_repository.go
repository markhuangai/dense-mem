package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type PlacementOutcomeInput struct {
	TeamID             string
	OwnerProfileID     string
	PlacementRunID     string
	PlacementItemID    string
	OutcomeKind        string
	Status             string
	IdempotencyKey     string
	Payload            map[string]any
	UpdateItemStatus   string
	UpdateItemCategory string
}

func (r *LedgerRepositoryImpl) AppendPlacementOutcome(ctx context.Context, input PlacementOutcomeInput) (string, error) {
	input = normalizePlacementOutcomeInput(input)
	if err := validatePlacementOutcomeInput(input); err != nil {
		return "", err
	}
	var outcomeID string
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		var err error
		outcomeID, err = insertPlacementOutcome(ctx, tx, input)
		if err != nil {
			return err
		}
		if input.UpdateItemStatus != "" || input.UpdateItemCategory != "" {
			return updatePlacementItemOutcome(ctx, tx, input)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("ledger: append placement outcome: %w", err)
	}
	return outcomeID, nil
}

func normalizePlacementOutcomeInput(input PlacementOutcomeInput) PlacementOutcomeInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.PlacementRunID = strings.TrimSpace(input.PlacementRunID)
	input.PlacementItemID = strings.TrimSpace(input.PlacementItemID)
	input.OutcomeKind = strings.TrimSpace(input.OutcomeKind)
	input.Status = strings.TrimSpace(input.Status)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.UpdateItemStatus = strings.TrimSpace(input.UpdateItemStatus)
	input.UpdateItemCategory = strings.TrimSpace(input.UpdateItemCategory)
	return input
}

func validatePlacementOutcomeInput(input PlacementOutcomeInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.PlacementRunID); err != nil {
		return fmt.Errorf("placement_run_id is required: %w", err)
	}
	if input.PlacementItemID != "" {
		if _, err := uuid.Parse(input.PlacementItemID); err != nil {
			return fmt.Errorf("placement_item_id is invalid: %w", err)
		}
	}
	if input.OutcomeKind == "" {
		return errors.New("outcome_kind is required")
	}
	if input.Status == "" {
		return errors.New("status is required")
	}
	if input.UpdateItemStatus != "" && !contains([]string{"queued", "processing", "awaiting_review", "completed", "failed", "quarantined"}, input.UpdateItemStatus) {
		return fmt.Errorf("unsupported placement item status %q", input.UpdateItemStatus)
	}
	if input.UpdateItemCategory != "" && !contains([]string{"pending", "fragment_only", "candidate", "validated_claim", "fact", "quarantined", "failed"}, input.UpdateItemCategory) {
		return fmt.Errorf("unsupported placement item category %q", input.UpdateItemCategory)
	}
	return nil
}

func insertPlacementOutcome(ctx context.Context, tx *gorm.DB, input PlacementOutcomeInput) (string, error) {
	payload, err := marshalJSON(input.Payload)
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO placement_outcomes (
		    team_id, placement_run_id, placement_item_id, owner_profile_id,
		    outcome_kind, status, idempotency_key, payload
		) VALUES (
		    ?::uuid, ?::uuid, NULLIF(?, '')::uuid, ?::uuid, ?, ?, ?, ?::jsonb
		)
		RETURNING outcome_id::text
	`, input.TeamID, input.PlacementRunID, input.PlacementItemID, input.OwnerProfileID,
		input.OutcomeKind, input.Status, input.IdempotencyKey, string(payload)).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", errors.New("placement outcome insert returned no row")
	}
	var outcomeID string
	if err := rows.Scan(&outcomeID); err != nil {
		return "", err
	}
	return outcomeID, rows.Err()
}

func updatePlacementItemOutcome(ctx context.Context, tx *gorm.DB, input PlacementOutcomeInput) error {
	if input.PlacementItemID == "" {
		return errors.New("placement_item_id is required when updating placement item")
	}
	result := tx.WithContext(ctx).Exec(`
		UPDATE placement_items
		SET status = COALESCE(NULLIF(?, ''), status),
		    category = COALESCE(NULLIF(?, ''), category),
		    result = ?::jsonb,
		    version = version + 1,
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND placement_item_id = ?::uuid
	`, input.UpdateItemStatus, input.UpdateItemCategory, mustJSON(input.Payload),
		input.TeamID, input.OwnerProfileID, input.PlacementItemID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("placement item not found")
	}
	return nil
}

func mustJSON(value map[string]any) string {
	data, err := marshalJSON(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}
