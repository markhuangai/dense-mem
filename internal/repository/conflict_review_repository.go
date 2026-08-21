package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (r *LedgerRepositoryImpl) ReserveRelationshipConflictReviewRun(
	ctx context.Context,
	input ConflictReviewRunInput,
) (*ConflictReviewRunRecord, bool, error) {
	var err error
	input, err = normalizeConflictReviewRunInput(input)
	if err != nil {
		return nil, false, err
	}
	if err := validateConflictReviewRunInput(input); err != nil {
		return nil, false, err
	}
	var record *ConflictReviewRunRecord
	var claimed bool
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH inserted AS (
				INSERT INTO relationship_conflict_review_runs (
				    team_id, local_run_date, policy_version, status,
				    worker_id, timezone, lease_until, started_at
				) VALUES (
				    ?::uuid, ?, ?, 'running', ?, ?, now() + (?::int * interval '1 second'), now()
				)
				ON CONFLICT (team_id, local_run_date, policy_version)
				DO NOTHING
				RETURNING team_id::text, review_run_id::text, local_run_date, status, worker_id, true AS claimed
			),
			reclaimed AS (
				UPDATE relationship_conflict_review_runs
				SET status = 'running',
				    worker_id = ?,
				    timezone = ?,
				    lease_until = now() + (?::int * interval '1 second'),
				    started_at = COALESCE(started_at, now()),
				    updated_at = now()
				WHERE team_id = ?::uuid
				  AND local_run_date = ?
				  AND policy_version = ?
				  AND status IN ('reserved', 'failed', 'running')
				  AND (lease_until IS NULL OR lease_until < now() OR worker_id = ?)
				  AND NOT EXISTS (SELECT 1 FROM inserted)
				RETURNING team_id::text, review_run_id::text, local_run_date, status, worker_id, true AS claimed
			)
			SELECT * FROM inserted
			UNION ALL
			SELECT * FROM reclaimed
			UNION ALL
			SELECT team_id::text, review_run_id::text, local_run_date, status, worker_id, false AS claimed
			FROM relationship_conflict_review_runs
			WHERE team_id = ?::uuid
			  AND local_run_date = ?
			  AND policy_version = ?
			  AND NOT EXISTS (SELECT 1 FROM inserted)
			  AND NOT EXISTS (SELECT 1 FROM reclaimed)
			LIMIT 1
		`, input.TeamID, input.LocalRunDate, string(domain.ConflictPolicyVersion),
			input.WorkerID, input.Timezone, int(input.Lease.Seconds()),
			input.WorkerID, input.Timezone, int(input.Lease.Seconds()),
			input.TeamID, input.LocalRunDate, string(domain.ConflictPolicyVersion), input.WorkerID,
			input.TeamID, input.LocalRunDate, string(domain.ConflictPolicyVersion)).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		loaded := ConflictReviewRunRecord{}
		if err := rows.Scan(&loaded.TeamID, &loaded.ReviewRunID, &loaded.LocalRunDate, &loaded.Status, &loaded.WorkerID, &claimed); err != nil {
			return err
		}
		record = &loaded
		return rows.Err()
	})
	if err != nil {
		return nil, false, fmt.Errorf("conflict review: reserve run: %w", err)
	}
	return record, claimed, nil
}

func (r *LedgerRepositoryImpl) ClaimRelationshipConflictCases(
	ctx context.Context,
	input ClaimRelationshipConflictCasesInput,
) ([]RelationshipConflictCaseRecord, error) {
	input = normalizeClaimRelationshipConflictCasesInput(input)
	if err := validateClaimRelationshipConflictCasesInput(input); err != nil {
		return nil, err
	}
	var records []RelationshipConflictCaseRecord
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH selected AS (
				SELECT conflict_id
				FROM relationship_conflict_cases
					WHERE team_id = ?::uuid
					  AND status IN ('open', 'overdue')
					  AND `+activeSemanticSpaceGenerationSQL("relationship_conflict_cases")+`
					  AND next_review_at <= ?
					  AND attempts < ?
					  AND (lease_until IS NULL OR lease_until < clock_timestamp() OR lease_worker_id = ?)
					  AND (
					      cardinality(?::uuid[]) = 0
					      OR conflict_id <> ALL(?::uuid[])
					  )
					ORDER BY next_review_at, created_at, conflict_id
					FOR UPDATE SKIP LOCKED
					LIMIT ?
			),
			updated AS (
					UPDATE relationship_conflict_cases AS conflict
					SET lease_worker_id = ?,
					    lease_until = clock_timestamp() + (?::int * interval '1 second'),
					    attempts = attempts + 1,
					    last_review_run_id = ?::uuid,
					    updated_at = now()
				FROM selected
				WHERE conflict.team_id = ?::uuid
				  AND conflict.conflict_id = selected.conflict_id
				  AND `+activeSemanticSpaceGenerationSQL("conflict")+`
				RETURNING conflict.conflict_id::text
				)
				SELECT conflict_id FROM updated
			`, input.TeamID, input.Now, input.MaxAttempts, input.WorkerID,
			pq.Array(input.ExcludedConflictIDs), pq.Array(input.ExcludedConflictIDs),
			input.Limit, input.WorkerID, int(input.Lease.Seconds()),
			input.ReviewRunID, input.TeamID).Rows()
		if err != nil {
			return err
		}
		conflictIDs := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			conflictIDs = append(conflictIDs, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		loaded, loadErr := loadRelationshipConflictRecordsByID(ctx, tx, input.TeamID, conflictIDs, nil)
		records = loaded
		return loadErr
	})
	if err != nil {
		return nil, fmt.Errorf("conflict review: claim cases: %w", err)
	}
	return records, nil
}

func (r *LedgerRepositoryImpl) ReviewRelationshipConflictCase(
	ctx context.Context,
	input ReviewRelationshipConflictCaseInput,
) (*ReviewRelationshipConflictCaseResult, error) {
	input = normalizeReviewRelationshipConflictCaseInput(input)
	if err := validateReviewRelationshipConflictCaseInput(input); err != nil {
		return nil, err
	}
	result := &ReviewRelationshipConflictCaseResult{ConflictID: input.ConflictID}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		caseRecord, err := loadRelationshipConflictCaseForReview(ctx, tx, input)
		if err != nil {
			return err
		}
		caseRecord, dismissed, err := refreshRelationshipConflictCaseSnapshotForReview(ctx, tx, input, caseRecord)
		if err != nil {
			return err
		}
		if dismissed {
			result.Outcome = ConflictReviewOutcomeNoop
			result.Stage = domain.ConflictReviewStageDismissedNoConflict
			return nil
		}
		evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
			Now:         input.Now,
			ReviewDueAt: caseRecord.ReviewDueAt,
			Positions:   caseRecord.Positions,
		})
		result.Outcome = evaluation.Outcome
		result.Stage = evaluation.Stage
		result.PreferredPositionID = evaluation.PreferredPositionID
		if err := appendRelationshipConflictEvent(ctx, tx, input.TeamID, input.ConflictID, evaluation.PreferredPositionID, "", "", string(domain.RelationshipConflictEventEvaluated), evaluation.Outcome, "case:"+input.ConflictID+":run:"+input.ReviewRunID+":evaluated", map[string]any{
			"stage":                 evaluation.Stage,
			"reason":                evaluation.Reason,
			"total_supporter_count": evaluation.TotalSupporterCount,
		}); err != nil {
			return err
		}
		switch evaluation.Outcome {
		case ConflictReviewOutcomeResolve:
			updated, err := resolveRelationshipConflictCase(ctx, tx, input, caseRecord, evaluation, r.embeddingJobMaxAttempts)
			if err != nil {
				return err
			}
			result.UpdatedRelationships = updated
		case ConflictReviewOutcomeOverdue:
			if err := markRelationshipConflictCaseOverdue(ctx, tx, input, evaluation); err != nil {
				return err
			}
		default:
			resetAttempts := evaluation.Stage == domain.ConflictReviewStageWaitingForReviewDue
			if err := releaseRelationshipConflictCaseLease(ctx, tx, input, conflictNextReviewAt(input.Now, caseRecord.ReviewDueAt), resetAttempts); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("conflict review: review case: %w", err)
	}
	return result, nil
}

func (r *LedgerRepositoryImpl) CompleteRelationshipConflictReviewRun(
	ctx context.Context,
	input ConflictReviewRunCompleteInput,
) error {
	input = normalizeConflictReviewRunCompleteInput(input)
	if err := validateConflictReviewRunCompleteInput(input); err != nil {
		return err
	}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			UPDATE relationship_conflict_review_runs
			SET status = ?,
			    completed_at = CASE WHEN ? IN ('completed', 'failed') THEN now() ELSE completed_at END,
			    claimed_cases = ?,
			    resolved_cases = ?,
			    overdue_cases = ?,
			    no_op_cases = ?,
			    failed_cases = ?,
			    last_error = ?,
			    lease_until = NULL,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND review_run_id = ?::uuid
			  AND worker_id = ?
		`, input.Status, input.Status, input.ClaimedCases, input.ResolvedCases,
			input.OverdueCases, input.NoOpCases, input.FailedCases, input.LastError,
			input.TeamID, input.ReviewRunID, input.WorkerID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPlacementLeaseLost
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("conflict review: complete run: %w", err)
	}
	return nil
}

func loadRelationshipConflictCaseForReview(
	ctx context.Context,
	tx *gorm.DB,
	input ReviewRelationshipConflictCaseInput,
) (*RelationshipConflictCaseRecord, error) {
	var conflictID string
	if err := tx.WithContext(ctx).Raw(`
		SELECT conflict_id::text
		FROM relationship_conflict_cases
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
			  AND lease_worker_id = ?
			  AND last_review_run_id = ?::uuid
			  AND lease_until >= clock_timestamp()
			FOR UPDATE
		`, input.TeamID, input.ConflictID, input.WorkerID, input.ReviewRunID).Scan(&conflictID).Error; err != nil {
		return nil, err
	}
	if conflictID == "" {
		return nil, ErrPlacementLeaseLost
	}
	records, err := loadRelationshipConflictRecordsByID(ctx, tx, input.TeamID, []string{input.ConflictID}, nil)
	if err != nil {
		return nil, err
	}
	if len(records) != 1 {
		return nil, sql.ErrNoRows
	}
	return &records[0], nil
}

func refreshRelationshipConflictCaseSnapshotForReview(
	ctx context.Context,
	tx *gorm.DB,
	input ReviewRelationshipConflictCaseInput,
	record *RelationshipConflictCaseRecord,
) (*RelationshipConflictCaseRecord, bool, error) {
	source := &RelationshipRecord{
		TeamID:             record.TeamID,
		SpaceID:            record.SpaceID,
		SubjectEntityID:    record.SubjectEntityID,
		PredicateKey:       record.PredicateKey,
		RelationshipKind:   record.RelationshipKind,
		CurrentCardinality: record.CurrentCardinality,
		Polarity:           record.Polarity,
		ScopeKey:           record.ScopeKey,
	}
	placement, err := loadRelationshipConflictPlacement(ctx, tx, input.TeamID, source)
	if err != nil {
		return nil, false, err
	}
	changed, err := refreshExistingRelationshipConflictCaseSnapshot(ctx, tx, input.TeamID, input.ConflictID, placement.rows)
	if err != nil {
		return nil, false, err
	}
	if !conflictPlacementHasConflict(placement.rows) {
		if err := dismissRelationshipConflictCase(ctx, tx, input, changed); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	if changed {
		if err := bumpRelationshipConflictCaseVersion(ctx, tx, input.TeamID, input.ConflictID); err != nil {
			return nil, false, err
		}
		records, err := loadRelationshipConflictRecordsByID(ctx, tx, input.TeamID, []string{input.ConflictID}, nil)
		if err != nil {
			return nil, false, err
		}
		if len(records) != 1 {
			return nil, false, sql.ErrNoRows
		}
		return &records[0], false, nil
	}
	return record, false, nil
}

func resolveRelationshipConflictCase(
	ctx context.Context,
	tx *gorm.DB,
	input ReviewRelationshipConflictCaseInput,
	record *RelationshipConflictCaseRecord,
	evaluation RelationshipConflictEvaluation,
	embeddingJobMaxAttempts int,
) ([]string, error) {
	if evaluation.PreferredPositionID == "" {
		return nil, errors.New("preferred position is required to resolve conflict")
	}
	effectiveAt := evaluation.EffectiveAt
	if effectiveAt == nil {
		now := input.Now
		effectiveAt = &now
	}
	result := tx.WithContext(ctx).Exec(`
		UPDATE relationship_conflict_cases
		SET status = 'resolved',
		    preferred_position_id = ?::uuid,
		    resolved_at = ?,
		    effective_at = ?,
		    effective_time_basis = ?,
		    resolution_reason = ?,
		    lease_worker_id = '',
		    lease_until = NULL,
		    next_review_at = ?,
		    version = version + 1,
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
	`, evaluation.PreferredPositionID, input.Now, effectiveAt, evaluation.EffectiveTimeBasis,
		evaluation.Reason, input.Now, input.TeamID, input.ConflictID)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, sql.ErrNoRows
	}
	if err := updateConflictPositionDispositions(ctx, tx, input.TeamID, input.ConflictID, evaluation.PreferredPositionID); err != nil {
		return nil, err
	}
	updated, err := suppressConflictLosingRelationships(ctx, tx, input, record, evaluation.PreferredPositionID, *effectiveAt, embeddingJobMaxAttempts)
	if err != nil {
		return nil, err
	}
	if err := appendRelationshipConflictEvent(ctx, tx, input.TeamID, input.ConflictID, evaluation.PreferredPositionID, "", "", string(domain.RelationshipConflictEventResolved), evaluation.Reason, "case:"+input.ConflictID+":run:"+input.ReviewRunID+":resolved", map[string]any{
		"updated_relationship_ids": updated,
	}); err != nil {
		return nil, err
	}
	return updated, nil
}

func updateConflictPositionDispositions(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictID string,
	preferredPositionID string,
) error {
	return tx.WithContext(ctx).Exec(`
		UPDATE relationship_conflict_positions
		SET disposition = CASE WHEN position_id = ?::uuid THEN 'preferred' ELSE 'suppressed_current' END,
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
	`, preferredPositionID, teamID, conflictID).Error
}

type conflictSuppressedRelationship struct {
	RelationshipID string
	OwnerProfileID string
	FromStatus     string
}

func suppressConflictLosingRelationships(
	ctx context.Context,
	tx *gorm.DB,
	input ReviewRelationshipConflictCaseInput,
	record *RelationshipConflictCaseRecord,
	preferredPositionID string,
	effectiveAt time.Time,
	embeddingJobMaxAttempts int,
) ([]string, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH losers AS (
			SELECT DISTINCT member.relationship_id, member.owner_profile_id
			FROM relationship_conflict_position_members AS member
			WHERE member.team_id = ?::uuid
			  AND member.conflict_id = ?::uuid
			  AND member.position_id <> ?::uuid
		),
		updated AS (
			UPDATE relationship_records AS relationship
			SET status = 'superseded',
			    valid_to = CASE
			        WHEN relationship.valid_from IS NOT NULL
			             AND (relationship.valid_to IS NULL OR relationship.valid_to > GREATEST(?, relationship.valid_from))
			        THEN GREATEST(?, relationship.valid_from)
			        WHEN relationship.valid_to IS NULL OR relationship.valid_to > ? THEN ?
			        ELSE relationship.valid_to
			    END,
			    recorded_to = COALESCE(relationship.recorded_to, now()),
			    version = relationship.version + 1,
			    updated_at = now()
			FROM losers
			WHERE relationship.team_id = ?::uuid
			  AND relationship.relationship_id = losers.relationship_id
			  AND relationship.owner_profile_id = losers.owner_profile_id
			  AND relationship.status = 'active'
			  AND relationship.support_count > 0
			RETURNING relationship.relationship_id::text,
			          relationship.owner_profile_id::text,
			          'active'::text AS from_status
		)
		SELECT relationship_id, owner_profile_id, from_status
		FROM updated
	`, input.TeamID, input.ConflictID, preferredPositionID,
		effectiveAt, effectiveAt, effectiveAt, effectiveAt, input.TeamID).Rows()
	if err != nil {
		return nil, err
	}
	suppressed := []conflictSuppressedRelationship{}
	for rows.Next() {
		var item conflictSuppressedRelationship
		if err := rows.Scan(&item.RelationshipID, &item.OwnerProfileID, &item.FromStatus); err != nil {
			_ = rows.Close()
			return nil, err
		}
		suppressed = append(suppressed, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	updatedIDs := make([]string, 0, len(suppressed))
	for _, item := range suppressed {
		updatedIDs = append(updatedIDs, item.RelationshipID)
		relationship, err := loadRelationshipRecord(ctx, tx, input.TeamID, item.RelationshipID)
		if err != nil {
			return nil, err
		}
		if _, err := upsertPlacementRelationshipSearchDocument(ctx, tx, CommitPlacementSemanticInput{
			TeamID:         input.TeamID,
			OwnerProfileID: item.OwnerProfileID,
		}, relationship, embeddingJobMaxAttempts); err != nil {
			return nil, err
		}
		if _, err := insertRelationshipTransition(ctx, tx, transitionInput{
			TeamID:         input.TeamID,
			OwnerProfileID: item.OwnerProfileID,
			RelationshipID: item.RelationshipID,
			FromStatus:     item.FromStatus,
			ToStatus:       string(domain.RelationshipStatusSuperseded),
			Reason:         "conflict_resolved",
			IdempotencyKey: "conflict:" + input.ConflictID + ":relationship:" + item.RelationshipID + ":superseded",
		}); err != nil {
			return nil, err
		}
		if err := appendRelationshipConflictEvent(ctx, tx, input.TeamID, input.ConflictID, "", item.RelationshipID, item.OwnerProfileID, string(domain.RelationshipConflictEventRelationshipUpdated), "superseded", "case:"+input.ConflictID+":relationship:"+item.RelationshipID+":superseded", map[string]any{
			"preferred_position_id": preferredPositionID,
		}); err != nil {
			return nil, err
		}
	}
	if len(suppressed) > 0 {
		if err := markStaleEmbeddingJobs(ctx, tx, input.TeamID); err != nil {
			return nil, err
		}
	}
	return updatedIDs, nil
}

func markRelationshipConflictCaseOverdue(
	ctx context.Context,
	tx *gorm.DB,
	input ReviewRelationshipConflictCaseInput,
	evaluation RelationshipConflictEvaluation,
) error {
	result := tx.WithContext(ctx).Exec(`
		UPDATE relationship_conflict_cases
		SET status = 'overdue',
		    lease_worker_id = '',
		    lease_until = NULL,
		    next_review_at = ?,
		    attempts = 0,
		    last_error = '',
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
	`, input.Now.Add(24*time.Hour), input.TeamID, input.ConflictID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return sql.ErrNoRows
	}
	return appendRelationshipConflictEvent(ctx, tx, input.TeamID, input.ConflictID, "", "", "", string(domain.RelationshipConflictEventMarkedOverdue), evaluation.Reason, "case:"+input.ConflictID+":run:"+input.ReviewRunID+":overdue", map[string]any{
		"stage": evaluation.Stage,
	})
}

func dismissRelationshipConflictCase(
	ctx context.Context,
	tx *gorm.DB,
	input ReviewRelationshipConflictCaseInput,
	snapshotChanged bool,
) error {
	if err := supersedeReservedOverdueConflictAssessments(ctx, tx, input.TeamID, input.ConflictID); err != nil {
		return err
	}
	if err := supersedePendingOverdueConflictResolutions(ctx, tx, input.TeamID, input.ConflictID); err != nil {
		return err
	}
	result := tx.WithContext(ctx).Exec(`
		UPDATE relationship_conflict_cases
		SET status = 'dismissed',
		    lease_worker_id = '',
		    lease_until = NULL,
		    next_review_at = ?,
		    last_error = '',
		    version = version + 1,
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
		  AND status IN ('open', 'overdue')
	`, input.Now, input.TeamID, input.ConflictID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return sql.ErrNoRows
	}
	return appendRelationshipConflictEvent(ctx, tx, input.TeamID, input.ConflictID, "", "", "", string(domain.RelationshipConflictEventDismissed), domain.ConflictReviewReasonActiveConflictNoLongerExists, "case:"+input.ConflictID+":run:"+input.ReviewRunID+":dismissed", map[string]any{
		"stage":            domain.ConflictReviewStageDismissedNoConflict,
		"snapshot_changed": snapshotChanged,
	})
}

func releaseRelationshipConflictCaseLease(
	ctx context.Context,
	tx *gorm.DB,
	input ReviewRelationshipConflictCaseInput,
	nextReviewAt time.Time,
	resetAttempts bool,
) error {
	result := tx.WithContext(ctx).Exec(`
		UPDATE relationship_conflict_cases
		SET lease_worker_id = '',
		    lease_until = NULL,
		    next_review_at = ?,
		    attempts = CASE WHEN ? THEN 0 ELSE attempts END,
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
	`, nextReviewAt, resetAttempts, input.TeamID, input.ConflictID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func conflictNextReviewAt(now time.Time, due time.Time) time.Time {
	next := now.Add(24 * time.Hour)
	if due.After(now) && next.After(due) {
		return due
	}
	return next
}

func normalizeConflictReviewRunInput(input ConflictReviewRunInput) (ConflictReviewRunInput, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.Timezone = strings.TrimSpace(input.Timezone)
	if input.Timezone == "" {
		input.Timezone = defaultConflictTimezone
	}
	if input.Lease <= 0 {
		input.Lease = 5 * time.Minute
	}
	location, err := conflictLocation(input.Timezone)
	if err != nil {
		return ConflictReviewRunInput{}, err
	}
	if input.LocalRunDate.IsZero() {
		input.LocalRunDate = time.Now().In(location)
	}
	y, m, d := input.LocalRunDate.In(location).Date()
	input.LocalRunDate = time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return input, nil
}

func validateConflictReviewRunInput(input ConflictReviewRunInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	return nil
}

func normalizeClaimRelationshipConflictCasesInput(input ClaimRelationshipConflictCasesInput) ClaimRelationshipConflictCasesInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.ReviewRunID = strings.TrimSpace(input.ReviewRunID)
	input.ExcludedConflictIDs = normalizeRecallUUIDList(input.ExcludedConflictIDs)
	if input.Limit <= 0 || input.Limit > 500 {
		input.Limit = 100
	}
	if input.Lease <= 0 {
		input.Lease = 5 * time.Minute
	}
	if input.MaxAttempts <= 0 {
		input.MaxAttempts = 5
	}
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	}
	return input
}

func validateClaimRelationshipConflictCasesInput(input ClaimRelationshipConflictCasesInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.ReviewRunID); err != nil {
		return fmt.Errorf("review_run_id is required: %w", err)
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	for _, id := range input.ExcludedConflictIDs {
		if _, err := uuid.Parse(id); err != nil {
			return fmt.Errorf("excluded_conflict_ids contains invalid UUID %q: %w", id, err)
		}
	}
	return nil
}

func normalizeReviewRelationshipConflictCaseInput(input ReviewRelationshipConflictCaseInput) ReviewRelationshipConflictCaseInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.ReviewRunID = strings.TrimSpace(input.ReviewRunID)
	input.ConflictID = strings.TrimSpace(input.ConflictID)
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	}
	return input
}

func validateReviewRelationshipConflictCaseInput(input ReviewRelationshipConflictCaseInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.ReviewRunID); err != nil {
		return fmt.Errorf("review_run_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.ConflictID); err != nil {
		return fmt.Errorf("conflict_id is required: %w", err)
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	return nil
}

func normalizeConflictReviewRunCompleteInput(input ConflictReviewRunCompleteInput) ConflictReviewRunCompleteInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.ReviewRunID = strings.TrimSpace(input.ReviewRunID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.Status = strings.TrimSpace(input.Status)
	input.LastError = strings.TrimSpace(input.LastError)
	if input.Status == "" {
		input.Status = "completed"
	}
	return input
}

func validateConflictReviewRunCompleteInput(input ConflictReviewRunCompleteInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.ReviewRunID); err != nil {
		return fmt.Errorf("review_run_id is required: %w", err)
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	if input.Status != "completed" && input.Status != "failed" {
		return fmt.Errorf("unsupported review run status %q", input.Status)
	}
	return nil
}

func conflictLocation(name string) (*time.Location, error) {
	location, err := time.LoadLocation(strings.TrimSpace(name))
	if err != nil {
		return nil, fmt.Errorf("timezone is invalid: %w", err)
	}
	return location, nil
}
