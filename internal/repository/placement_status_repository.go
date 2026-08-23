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
	var submittedAt, nextAttemptAt, startedAt, updatedAt, completedAt sql.NullTime
	var quarantineExpiresAt sql.NullTime
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
		       run.quarantine_expires_at
		FROM placement_runs AS run
		JOIN knowledge_ingests AS ingest
		  ON ingest.team_id = run.team_id
		 AND ingest.ingest_id = run.ingest_id
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
		&quarantineExpiresAt,
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
	result.SubmittedAt = nullableStatusTime(submittedAt)
	result.NextAttemptAt = nullableStatusTime(nextAttemptAt)
	result.StartedAt = nullableStatusTime(startedAt)
	result.UpdatedAt = nullableStatusTime(updatedAt)
	result.CompletedAt = nullableStatusTime(completedAt)
	if quarantineExpiresAt.Valid {
		value := quarantineExpiresAt.Time.UTC()
		result.QuarantineExpiresAt = &value
	}
	evidenceRows, err := tx.WithContext(ctx).Raw(`
		SELECT fragment.fragment_id::text, fragment.evidence_index, fragment.content, fragment.content_hash, fragment.authority,
		       COALESCE(fragment.source_id::text, intent.source_id::text, ''),
		       COALESCE(fragment.source_revision_id::text, intent.source_revision_id::text, '')
		FROM evidence_fragments AS fragment
		LEFT JOIN remember_source_revision_intents AS intent
		  ON intent.team_id = fragment.team_id
		 AND intent.ingest_id = fragment.ingest_id
		 AND intent.fragment_id = fragment.fragment_id
		 AND intent.owner_profile_id = fragment.owner_profile_id
		WHERE fragment.team_id = ?::uuid
		  AND fragment.owner_profile_id = ?::uuid
		  AND fragment.ingest_id = ?::uuid
		  AND `+activeSemanticSpaceGenerationSQL("fragment")+`
		ORDER BY fragment.evidence_index ASC
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
	if err := hydrateEvidenceLifecycleLineage(ctx, tx, result.TeamID, result.Evidence); err != nil {
		return nil, err
	}
	if err := loadSubmissionRelationshipResults(ctx, tx, input, result); err != nil {
		return nil, err
	}
	return result, nil
}

func loadSubmissionRelationshipResults(
	ctx context.Context,
	tx *gorm.DB,
	input GetPlacementRunInput,
	result *CreateIngestResult,
) error {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT relationship_ref, disposition, reason, splits
		FROM submission_relationship_results
		WHERE team_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND ingest_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		ORDER BY relationship_ref ASC
	`, input.TeamID, result.PlacementRunID, input.IngestID, input.OwnerProfileID).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item SubmissionRelationshipResult
		var splitsRaw []byte
		if err := rows.Scan(&item.RelationshipRef, &item.Disposition, &item.Reason, &splitsRaw); err != nil {
			return err
		}
		if len(splitsRaw) > 0 && string(splitsRaw) != "null" {
			if err := json.Unmarshal(splitsRaw, &item.Splits); err != nil {
				return err
			}
		}
		if item.Splits == nil {
			item.Splits = []SubmissionRelationshipSplitInput{}
		}
		result.RelationshipResults = append(result.RelationshipResults, item)
	}
	return rows.Err()
}

func nullableStatusTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}
