package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (r *LedgerRepositoryImpl) StageConflictDerivedEvidence(
	ctx context.Context,
	target ConflictDerivedEvidenceTarget,
) (*StageConflictDerivedEvidenceResult, error) {
	target = normalizeConflictDerivedEvidenceTarget(target)
	if err := validateConflictDerivedEvidenceTarget(target); err != nil {
		return nil, err
	}
	content := "Overdue conflict review retracted prior evidence. This deletion-only derivation cannot establish a semantic relationship."
	requestHash := sha256Hex(strings.Join([]string{
		target.ConflictID,
		target.SpaceID,
		fmt.Sprint(target.SpaceGeneration),
		target.TargetFragmentID,
		target.SelectedPositionID,
		target.SourceGroupKey,
		fmt.Sprint(target.EvidenceIndex),
		content,
	}, "\x00"))
	ingest, err := r.commitDerivedEvidenceIngest(ctx, target, content, requestHash)
	if err != nil {
		return nil, fmt.Errorf("conflict review: stage derived evidence: %w", err)
	}
	if ingest == nil || ingest.IngestID == "" || len(ingest.Evidence) != 1 || ingest.Evidence[0].FragmentID == "" {
		return nil, errors.New("conflict review: derived evidence ingest is incomplete")
	}
	result := &StageConflictDerivedEvidenceResult{
		IngestID:            ingest.IngestID,
		ReplacementFragment: ingest.Evidence[0].FragmentID,
		Existing:            ingest.Existing,
	}
	err = r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := setConflictSystemTeamContext(ctx, tx, target.TeamID); err != nil {
			return err
		}
		if err := ensureActiveTeamForMutation(ctx, tx, target.TeamID); err != nil {
			return err
		}
		var existingReplacement string
		err := tx.WithContext(ctx).Raw(`
			INSERT INTO relationship_conflict_evidence_derivations (
			    team_id, space_id, space_generation, conflict_id, target_fragment_id, target_owner_profile_id,
			    selected_position_id, replacement_fragment_id, system_profile_id
			) VALUES (
			    ?::uuid, ?::uuid, ?,
			    ?::uuid, ?::uuid, ?::uuid,
			    ?::uuid, ?::uuid, ?::uuid
		)
			ON CONFLICT (team_id, conflict_id, target_fragment_id) DO NOTHING
			RETURNING replacement_fragment_id::text
		`, target.TeamID, target.SpaceID, target.SpaceGeneration, target.ConflictID, target.TargetFragmentID, target.TargetOwnerProfileID,
			target.SelectedPositionID, result.ReplacementFragment, target.SystemProfileID).Row().Scan(&existingReplacement)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if existingReplacement == "" {
			if err := tx.WithContext(ctx).Raw(`
				SELECT replacement_fragment_id::text
				FROM relationship_conflict_evidence_derivations
				WHERE team_id = ?::uuid
				  AND conflict_id = ?::uuid
				  AND target_fragment_id = ?::uuid
			`, target.TeamID, target.ConflictID, target.TargetFragmentID).Row().Scan(&existingReplacement); err != nil {
				return err
			}
			result.Existing = true
		}
		if existingReplacement != result.ReplacementFragment {
			return ErrConflictAssessmentStale
		}
		updateTask := tx.WithContext(ctx).Exec(`
			UPDATE relationship_conflict_derived_evidence_tasks
			SET status = 'completed',
			    lease_worker_id = NULL,
			    lease_until = NULL,
			    last_failure_class = '',
			    updated_at = now(),
			    completed_at = COALESCE(completed_at, now())
			WHERE team_id = ?::uuid
			  AND derived_evidence_task_id = ?::uuid
			  AND conflict_id = ?::uuid
			  AND target_fragment_id = ?::uuid
			  AND target_owner_profile_id = ?::uuid
			  AND selected_position_id = ?::uuid
			  AND system_profile_id = ?::uuid
			  AND source_group_key = ?
			  AND origin_evidence_index = ?
			  AND space_id = ?::uuid
			  AND space_generation = ?
		`, target.TeamID, target.TaskID, target.ConflictID, target.TargetFragmentID,
			target.TargetOwnerProfileID, target.SelectedPositionID, target.SystemProfileID,
			target.SourceGroupKey, target.EvidenceIndex, target.SpaceID, target.SpaceGeneration)
		if updateTask.Error != nil {
			return updateTask.Error
		}
		if updateTask.RowsAffected != 1 {
			return ErrConflictAssessmentStale
		}
		return appendRelationshipConflictEvent(ctx, tx, target.TeamID, target.ConflictID, target.SelectedPositionID, "", "", string(domain.RelationshipConflictEventDerivedStaged), "staged", "case:"+target.ConflictID+":evidence:"+target.TargetFragmentID+":derived_staged", map[string]any{
			"derived_evidence_task_id": target.TaskID,
			"target_fragment_id":       target.TargetFragmentID,
			"replacement_fragment_id":  result.ReplacementFragment,
			"replacement_ingest_id":    result.IngestID,
			"source_group_key":         target.SourceGroupKey,
			"origin_evidence_index":    target.EvidenceIndex,
			"authority":                string(domain.AuthorityInferred),
		})
	})
	if err != nil {
		return nil, fmt.Errorf("conflict review: record derived evidence: %w", err)
	}
	return result, nil
}

// commitDerivedEvidenceIngest writes deletion-only conflict evidence without
// entering the retired placement ledger. The derivation is audit input, not a
// semantic claim, and therefore does not require a vector document.
func (r *LedgerRepositoryImpl) commitDerivedEvidenceIngest(ctx context.Context, target ConflictDerivedEvidenceTarget, content, requestHash string) (*EvidenceIngestResult, error) {
	input := normalizeCreateIngestInput(CreateIngestInput{
		TeamID: target.TeamID, OwnerProfileID: target.SystemProfileID, IngestID: uuid.NewString(),
		SpaceID: target.SpaceID, SpaceGeneration: target.SpaceGeneration,
		IdempotencyKey: "conflict-derived:" + target.ConflictID + ":" + target.TargetFragmentID,
		RequestHash:    requestHash, SourceSummary: conflictResolutionDeletionOnlySourceSummary,
		Status: "completed", Metadata: map[string]any{
			"contract_version": domain.ContractVersion, "conflict_id": target.ConflictID,
			"target_fragment_id": target.TargetFragmentID, "target_owner_profile_id": target.TargetOwnerProfileID,
			"selected_position_id": target.SelectedPositionID, "conflict_resolution_deletion_only": true,
			"conflict_resolution_policy_version": domain.ConflictOverduePolicyVersion,
		},
		Evidence: []EvidenceInput{{
			FragmentID: uuid.NewString(), Content: content, SourceType: "observation", Authority: string(domain.AuthorityInferred),
			SourceRef: "conflict:" + target.ConflictID, Metadata: map[string]any{
				"conflict_id": target.ConflictID, "target_fragment_id": target.TargetFragmentID,
				"target_owner_profile_id": target.TargetOwnerProfileID, "selected_position_id": target.SelectedPositionID,
				"contract_source_group": target.SourceGroupKey, "origin_evidence_index": target.EvidenceIndex,
				"conflict_resolution_deletion_only": true, "derived_authority": string(domain.AuthorityInferred),
			},
		}},
	})
	var result *EvidenceIngestResult
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := setConflictSystemTeamContext(ctx, tx, target.TeamID); err != nil {
			return err
		}
		if err := ensureActiveTeamForMutation(ctx, tx, target.TeamID); err != nil {
			return err
		}
		ingestID, created, err := insertKnowledgeIngest(ctx, tx, input)
		if err != nil {
			return err
		}
		fragmentID := input.Evidence[0].FragmentID
		if !created {
			if err := tx.WithContext(ctx).Raw(`SELECT fragment_id::text FROM evidence_fragments WHERE team_id = ?::uuid AND ingest_id = ?::uuid ORDER BY evidence_index LIMIT 1`, target.TeamID, ingestID).Row().Scan(&fragmentID); err != nil {
				return err
			}
		} else if _, err := insertEvidenceFragment(ctx, tx, input, ingestID, 0, input.Evidence[0], nil); err != nil {
			return err
		}
		result = &EvidenceIngestResult{TeamID: target.TeamID, OwnerProfileID: target.SystemProfileID, IngestID: ingestID, Existing: !created, Evidence: []EvidenceFragment{{FragmentID: fragmentID, EvidenceIndex: 0, Content: content, ContentHash: sha256Hex(content), Authority: string(domain.AuthorityInferred)}}}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (r *LedgerRepositoryImpl) RecordConflictDerivedEvidenceFailure(
	ctx context.Context,
	target ConflictDerivedEvidenceTarget,
	failureClass string,
) error {
	target = normalizeConflictDerivedEvidenceTarget(target)
	if err := validateConflictDerivedEvidenceTarget(target); err != nil {
		return err
	}
	failureClass = strings.TrimSpace(failureClass)
	if failureClass == "" || len(failureClass) > 128 {
		return errors.New("derived evidence failure_class is invalid")
	}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := setConflictSystemTeamContext(ctx, tx, target.TeamID); err != nil {
			return err
		}
		if err := ensureActiveTeamForMutation(ctx, tx, target.TeamID); err != nil {
			return err
		}
		updateTask := tx.WithContext(ctx).Exec(`
			UPDATE relationship_conflict_derived_evidence_tasks
			SET status = 'pending',
			    lease_worker_id = NULL,
			    lease_until = NULL,
			    last_failure_class = ?,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND derived_evidence_task_id = ?::uuid
			  AND space_id = ?::uuid
			  AND space_generation = ?
			  AND status <> 'completed'
		`, failureClass, target.TeamID, target.TaskID, target.SpaceID, target.SpaceGeneration)
		if updateTask.Error != nil {
			return updateTask.Error
		}
		if updateTask.RowsAffected == 0 {
			return nil
		}
		return appendRelationshipConflictEvent(ctx, tx, target.TeamID, target.ConflictID, target.SelectedPositionID, "", "", string(domain.RelationshipConflictEventDerivedFailed), failureClass, "case:"+target.ConflictID+":evidence:"+target.TargetFragmentID+":derived_failed", map[string]any{
			"derived_evidence_task_id": target.TaskID,
			"target_fragment_id":       target.TargetFragmentID,
			"failure_class":            failureClass,
		})
	})
	if err != nil {
		return fmt.Errorf("conflict review: record derived evidence failure: %w", err)
	}
	return nil
}

func (r *LedgerRepositoryImpl) ClaimConflictDerivedEvidenceTasks(
	ctx context.Context,
	input ClaimConflictDerivedEvidenceTasksInput,
) ([]ConflictDerivedEvidenceTarget, error) {
	input = normalizeClaimConflictDerivedEvidenceTasksInput(input)
	if err := validateClaimConflictDerivedEvidenceTasksInput(input); err != nil {
		return nil, err
	}
	targets := []ConflictDerivedEvidenceTarget{}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := setConflictSystemTeamContext(ctx, tx, input.TeamID); err != nil {
			return err
		}
		if err := ensureActiveTeamForMutation(ctx, tx, input.TeamID); err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			WITH selected AS (
				SELECT task.derived_evidence_task_id
				FROM relationship_conflict_derived_evidence_tasks AS task
				JOIN memory_spaces AS space
				  ON space.team_id = task.team_id
				 AND space.id = task.space_id
				 AND space.generation = task.space_generation
				 AND space.lifecycle_state = 'active'
				WHERE task.team_id = ?::uuid
				  AND task.status IN ('pending', 'processing')
				  AND (task.status = 'pending' OR task.lease_until IS NULL OR task.lease_until < clock_timestamp())
				ORDER BY task.created_at, task.derived_evidence_task_id
				FOR UPDATE SKIP LOCKED
				LIMIT ?
			), claimed AS (
				UPDATE relationship_conflict_derived_evidence_tasks AS task
				SET status = 'processing',
				    attempts = task.attempts + 1,
				    lease_worker_id = ?,
				    lease_until = clock_timestamp() + (?::int * interval '1 second'),
				    last_review_run_id = ?::uuid,
				    updated_at = now()
				FROM selected
				WHERE task.team_id = ?::uuid
				  AND task.derived_evidence_task_id = selected.derived_evidence_task_id
				RETURNING task.derived_evidence_task_id::text,
				          task.space_id::text,
				          task.space_generation,
				          task.conflict_id::text,
				          task.system_profile_id::text,
				          task.target_fragment_id::text,
				          task.target_owner_profile_id::text,
				          task.selected_position_id::text,
				          task.source_group_key,
				          task.origin_evidence_index
			)
			SELECT * FROM claimed
			ORDER BY derived_evidence_task_id
		`, input.TeamID, input.Limit, input.WorkerID, int(input.Lease.Seconds()), input.ReviewRunID, input.TeamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			target := ConflictDerivedEvidenceTarget{TeamID: input.TeamID}
			if err := rows.Scan(
				&target.TaskID,
				&target.SpaceID,
				&target.SpaceGeneration,
				&target.ConflictID,
				&target.SystemProfileID,
				&target.TargetFragmentID,
				&target.TargetOwnerProfileID,
				&target.SelectedPositionID,
				&target.SourceGroupKey,
				&target.EvidenceIndex,
			); err != nil {
				return err
			}
			targets = append(targets, target)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("conflict review: claim derived evidence tasks: %w", err)
	}
	return targets, nil
}
