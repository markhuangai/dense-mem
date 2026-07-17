package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (r *V2LedgerRepositoryImpl) GetPlacementRun(
	ctx context.Context,
	input V2GetPlacementRunInput,
) (*V2CreateIngestResult, error) {
	input = normalizeV2GetPlacementRunInput(input)
	if err := validateV2GetPlacementRunInput(input); err != nil {
		return nil, err
	}
	var result *V2CreateIngestResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		loaded, err := loadV2PlacementRunStatus(ctx, tx, input)
		if err != nil {
			return err
		}
		result = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 ledger: get placement run: %w", err)
	}
	return result, nil
}

func normalizeV2GetPlacementRunInput(input V2GetPlacementRunInput) V2GetPlacementRunInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	return input
}

func validateV2GetPlacementRunInput(input V2GetPlacementRunInput) error {
	for label, value := range map[string]string{
		"team_id":          input.TeamID,
		"owner_profile_id": input.OwnerProfileID,
		"ingest_id":        input.IngestID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	return nil
}

func loadV2PlacementRunStatus(
	ctx context.Context,
	tx *gorm.DB,
	input V2GetPlacementRunInput,
) (*V2CreateIngestResult, error) {
	result := &V2CreateIngestResult{TeamID: input.TeamID, IngestID: input.IngestID}
	err := tx.WithContext(ctx).Raw(`
		SELECT placement_run_id::text, status
		FROM placement_runs
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND ingest_id = ?::uuid
	`, input.TeamID, input.OwnerProfileID, input.IngestID).Row().Scan(
		&result.PlacementRunID,
		&result.Status,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrV2PlacementNotFound
		}
		return nil, err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT placement_item_id::text, fragment_id::text, evidence_index,
		       status, category, version
		FROM placement_items
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND ingest_id = ?::uuid
		ORDER BY evidence_index ASC
	`, input.TeamID, input.OwnerProfileID, input.IngestID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item V2PlacementItem
		if err := rows.Scan(
			&item.PlacementItemID,
			&item.FragmentID,
			&item.EvidenceIndex,
			&item.Status,
			&item.Category,
			&item.Version,
		); err != nil {
			return nil, err
		}
		result.Items = append(result.Items, item)
	}
	return result, rows.Err()
}
