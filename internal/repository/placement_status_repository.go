package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (r *LedgerRepositoryImpl) GetPlacementRun(
	ctx context.Context,
	input GetPlacementRunInput,
) (*CreateIngestResult, error) {
	input = normalizeGetPlacementRunInput(input)
	if err := validateGetPlacementRunInput(input); err != nil {
		return nil, err
	}
	var result *CreateIngestResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		loaded, err := loadPlacementRunStatus(ctx, tx, input)
		if err != nil {
			return err
		}
		result = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ledger: get placement run: %w", err)
	}
	return result, nil
}

func normalizeGetPlacementRunInput(input GetPlacementRunInput) GetPlacementRunInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	return input
}

func validateGetPlacementRunInput(input GetPlacementRunInput) error {
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

func loadPlacementRunStatus(
	ctx context.Context,
	tx *gorm.DB,
	input GetPlacementRunInput,
) (*CreateIngestResult, error) {
	result := &CreateIngestResult{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID}
	var proposalRaw []byte
	var semanticHoldState sql.NullString
	var submittedAt, nextAttemptAt, startedAt, updatedAt, completedAt sql.NullTime
	var quarantineExpiresAt, replacementWindowExpiresAt sql.NullTime
	err := tx.WithContext(ctx).Raw(`
		SELECT run.placement_run_id::text, run.status, COALESCE(ingest.proposal, '{}'::jsonb),
		       COALESCE(ingest.metadata ->> 'contract_version', ''),
		       COALESCE(ingest.metadata #>> '{actor,correlation_id}', ''),
		       run.attempts, run.max_attempts, ingest.created_at,
		       CASE
		           WHEN run.status IN ('queued', 'guarded')
		            AND run.attempts > 0
		            AND run.available_at > now()
		           THEN run.available_at
		       END,
		       run.started_at, run.updated_at, run.completed_at,
		       run.semantic_hold_state, run.quarantine_expires_at, hold.expires_at
		FROM placement_runs AS run
		JOIN knowledge_ingests AS ingest
		  ON ingest.team_id = run.team_id
		 AND ingest.ingest_id = run.ingest_id
		 AND ingest.space_id = run.space_id
		 AND ingest.space_generation = run.space_generation
		LEFT JOIN submission_holds AS hold
		  ON hold.team_id = run.team_id
		 AND hold.placement_run_id = run.placement_run_id
		 AND hold.space_id = run.space_id
		 AND hold.space_generation = run.space_generation
		WHERE run.team_id = ?::uuid
		  AND run.owner_profile_id = ?::uuid
		  AND run.ingest_id = ?::uuid
		  AND `+activeSemanticSpaceGenerationSQL("run")+`
	`, input.TeamID, input.OwnerProfileID, input.IngestID).Row().Scan(
		&result.PlacementRunID,
		&result.Status,
		&proposalRaw,
		&result.ContractVersion,
		&result.CorrelationID,
		&result.Attempts,
		&result.MaxAttempts,
		&submittedAt,
		&nextAttemptAt,
		&startedAt,
		&updatedAt,
		&completedAt,
		&semanticHoldState,
		&quarantineExpiresAt,
		&replacementWindowExpiresAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPlacementNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal(proposalRaw, &result.Proposal); err != nil {
		return nil, err
	}
	if semanticHoldState.Valid {
		result.SemanticHoldState = strings.TrimSpace(semanticHoldState.String)
	}
	result.SubmittedAt = nullableStatusTime(submittedAt)
	result.NextAttemptAt = nullableStatusTime(nextAttemptAt)
	result.StartedAt = nullableStatusTime(startedAt)
	result.UpdatedAt = nullableStatusTime(updatedAt)
	result.CompletedAt = nullableStatusTime(completedAt)
	if quarantineExpiresAt.Valid {
		value := quarantineExpiresAt.Time.UTC()
		result.QuarantineExpiresAt = &value
	}
	if replacementWindowExpiresAt.Valid {
		value := replacementWindowExpiresAt.Time.UTC()
		result.ReplacementWindowExpiresAt = &value
	}
	evidenceRows, err := tx.WithContext(ctx).Raw(`
		SELECT fragment_id::text, evidence_index, content, content_hash, authority,
		       COALESCE(source_id::text, ''), COALESCE(source_revision_id::text, '')
		FROM evidence_fragments AS fragment
		WHERE fragment.team_id = ?::uuid
		  AND fragment.owner_profile_id = ?::uuid
		  AND fragment.ingest_id = ?::uuid
		  AND `+activeSemanticSpaceGenerationSQL("fragment")+`
		ORDER BY evidence_index ASC
	`, input.TeamID, input.OwnerProfileID, input.IngestID).Rows()
	if err != nil {
		return nil, err
	}
	defer evidenceRows.Close()
	for evidenceRows.Next() {
		var item EvidenceFragment
		if err := evidenceRows.Scan(
			&item.FragmentID,
			&item.EvidenceIndex,
			&item.Content,
			&item.ContentHash,
			&item.Authority,
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
		SELECT placement_item_id::text, fragment_id::text, claim_key::text, evidence_index,
		       status, category, version, COALESCE(result, '{}'::jsonb)
		FROM placement_items AS item
		WHERE item.team_id = ?::uuid
		  AND item.owner_profile_id = ?::uuid
		  AND item.ingest_id = ?::uuid
		  AND `+activeSemanticSpaceGenerationSQL("item")+`
		ORDER BY evidence_index ASC
	`, input.TeamID, input.OwnerProfileID, input.IngestID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item PlacementItem
		var resultRaw []byte
		if err := rows.Scan(
			&item.PlacementItemID,
			&item.FragmentID,
			&item.ClaimKey,
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
	if err := hydratePlacementItemSearchStates(ctx, tx, result.TeamID, result.OwnerProfileID, result.Items); err != nil {
		return nil, err
	}
	if err := hydratePlacementItemReviewTasks(ctx, tx, result.TeamID, result.OwnerProfileID, result.IngestID, result.Items); err != nil {
		return nil, err
	}
	if err := hydrateEvidenceLifecycleLineage(ctx, tx, result.TeamID, result.Evidence); err != nil {
		return nil, err
	}
	return result, nil
}

func nullableStatusTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}
