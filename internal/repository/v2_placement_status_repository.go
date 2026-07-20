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
	result := &V2CreateIngestResult{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID}
	var proposalRaw []byte
	err := tx.WithContext(ctx).Raw(`
		SELECT run.placement_run_id::text, run.status, COALESCE(ingest.proposal, '{}'::jsonb)
		FROM placement_runs AS run
		JOIN knowledge_ingests AS ingest
		  ON ingest.team_id = run.team_id
		 AND ingest.ingest_id = run.ingest_id
		WHERE run.team_id = ?::uuid
		  AND run.owner_profile_id = ?::uuid
		  AND run.ingest_id = ?::uuid
	`, input.TeamID, input.OwnerProfileID, input.IngestID).Row().Scan(
		&result.PlacementRunID,
		&result.Status,
		&proposalRaw,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrV2PlacementNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal(proposalRaw, &result.Proposal); err != nil {
		return nil, err
	}
	evidenceRows, err := tx.WithContext(ctx).Raw(`
		SELECT fragment_id::text, evidence_index, content, content_hash,
		       COALESCE(source_id::text, ''), COALESCE(source_revision_id::text, '')
		FROM evidence_fragments
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND ingest_id = ?::uuid
		ORDER BY evidence_index ASC
	`, input.TeamID, input.OwnerProfileID, input.IngestID).Rows()
	if err != nil {
		return nil, err
	}
	defer evidenceRows.Close()
	for evidenceRows.Next() {
		var item V2EvidenceFragment
		if err := evidenceRows.Scan(
			&item.FragmentID,
			&item.EvidenceIndex,
			&item.Content,
			&item.ContentHash,
			&item.SourceID,
			&item.SourceRevisionID,
		); err != nil {
			return nil, err
		}
		result.Evidence = append(result.Evidence, item)
	}
	if err := evidenceRows.Err(); err != nil {
		return nil, err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT placement_item_id::text, fragment_id::text, evidence_index,
		       status, category, version, COALESCE(result, '{}'::jsonb)
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
		var resultRaw []byte
		if err := rows.Scan(
			&item.PlacementItemID,
			&item.FragmentID,
			&item.EvidenceIndex,
			&item.Status,
			&item.Category,
			&item.Version,
			&resultRaw,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(resultRaw, &item.Result); err != nil {
			return nil, err
		}
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := hydrateV2PlacementItemSearchStates(ctx, tx, result.TeamID, result.OwnerProfileID, result.Items); err != nil {
		return nil, err
	}
	return result, nil
}
