package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (r *LedgerRepositoryImpl) ApplyOverdueConflictResolution(
	ctx context.Context,
	input ApplyOverdueConflictResolutionInput,
) (*ApplyOverdueConflictResolutionResult, error) {
	input = normalizeApplyOverdueConflictResolutionInput(input)
	if err := validateApplyOverdueConflictResolutionInput(input); err != nil {
		return nil, err
	}
	result := &ApplyOverdueConflictResolutionResult{
		ConflictID:          input.ConflictID,
		PreferredPositionID: input.PreferredPositionID,
		Method:              input.Method,
	}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := setConflictSystemTeamContext(ctx, tx, input.TeamID); err != nil {
			return err
		}
		if err := ensureActiveTeamForMutation(ctx, tx, input.TeamID); err != nil {
			return err
		}
		var status string
		var version int
		if err := tx.WithContext(ctx).Raw(`
			SELECT status, version
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			FOR UPDATE
		`, input.TeamID, input.ConflictID).Row().Scan(&status, &version); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				result.Stale = true
				return nil
			}
			return err
		}
		if status != string(domain.RelationshipConflictOverdue) || version != input.ExpectedCaseVersion {
			result.Stale = true
			return markConflictResolutionPlanSuperseded(ctx, tx, input.TeamID, input.ConflictID, input.ExpectedCaseVersion)
		}
		records, err := loadRelationshipConflictRecordsByID(ctx, tx, input.TeamID, []string{input.ConflictID}, nil)
		if err != nil {
			return err
		}
		if len(records) != 1 {
			return ErrConflictAssessmentStale
		}
		current, dismissed, err := refreshRelationshipConflictCaseSnapshotForReview(ctx, tx, ReviewRelationshipConflictCaseInput{
			TeamID:      input.TeamID,
			WorkerID:    input.WorkerID,
			ReviewRunID: input.ReviewRunID,
			ConflictID:  input.ConflictID,
			Now:         input.Now,
		}, &records[0])
		if err != nil {
			return err
		}
		if dismissed || current.Version != input.ExpectedCaseVersion {
			result.Stale = true
			return nil
		}
		if err := validateConflictResolutionPosition(ctx, tx, input.TeamID, input.ConflictID, input.PreferredPositionID); err != nil {
			return err
		}
		if err := validateConflictResolutionAssessment(ctx, tx, input); err != nil {
			return err
		}
		records[0] = *current
		effectiveAt, effectiveTimeBasis := conflictResolutionEffectiveTime(records[0], input.PreferredPositionID, input.Now)
		planID, planStatus, err := ensureConflictResolutionPlan(ctx, tx, input, effectiveAt, effectiveTimeBasis)
		if err != nil {
			return err
		}
		if planStatus == "applied" {
			result.Resolved = true
			return nil
		}
		if planStatus == "superseded" || planStatus == "failed" {
			result.Stale = true
			return nil
		}
		targets, err := loadConflictLosingEvidenceTargets(ctx, tx, input.TeamID, input.ConflictID, input.PreferredPositionID)
		if err != nil {
			return err
		}
		if len(targets) > conflictResolutionMaxFragments {
			pendingKey := "case:" + input.ConflictID + ":plan:" + planID + ":pending"
			var pendingEventExists bool
			if err := tx.WithContext(ctx).Raw(`
				SELECT EXISTS (
					SELECT 1
					FROM relationship_conflict_events
					WHERE team_id = ?::uuid AND idempotency_key = ?
				)
			`, input.TeamID, pendingKey).Row().Scan(&pendingEventExists); err != nil {
				return err
			}
			if !pendingEventExists {
				if err := appendRelationshipConflictEvent(ctx, tx, input.TeamID, input.ConflictID, input.PreferredPositionID, "", "", string(domain.RelationshipConflictEventResolutionPending), "fanout_bound", pendingKey, map[string]any{
					"resolution_plan_id":    planID,
					"target_fragment_count": len(targets),
				}); err != nil {
					return err
				}
				result.PendingTransitioned = true
			}
			result.Pending = true
			return nil
		}
		systemProfileID, err := ensureConflictSystemProfile(ctx, tx, input.TeamID)
		if err != nil {
			return err
		}
		evaluation := RelationshipConflictEvaluation{
			Outcome:             ConflictReviewOutcomeResolve,
			Stage:               "overdue_" + input.Method,
			PreferredPositionID: input.PreferredPositionID,
			Reason:              conflictResolutionReason(input.Method),
			EffectiveAt:         &effectiveAt,
			EffectiveTimeBasis:  effectiveTimeBasis,
		}
		updated, err := resolveRelationshipConflictCase(ctx, tx, ReviewRelationshipConflictCaseInput{
			TeamID:      input.TeamID,
			WorkerID:    input.WorkerID,
			ReviewRunID: input.ReviewRunID,
			ConflictID:  input.ConflictID,
			Now:         input.Now,
		}, &records[0], evaluation, r.embeddingJobMaxAttempts)
		if err != nil {
			return err
		}
		retracted, err := retractConflictLosingEvidence(ctx, tx, input, systemProfileID, targets)
		if err != nil {
			return err
		}
		derived, err := enqueueConflictDerivedEvidenceTasks(ctx, tx, planID, conflictDerivedEvidenceTargets(
			input.TeamID,
			input.ConflictID,
			systemProfileID,
			input.PreferredPositionID,
			targets,
			retracted,
		))
		if err != nil {
			return err
		}
		updatePlan := tx.WithContext(ctx).Exec(`
			UPDATE relationship_conflict_resolution_plans
			SET status = 'applied',
			    applied_at = now(),
			    failure_reason = ''
			WHERE team_id = ?::uuid
			  AND resolution_plan_id = ?::uuid
			  AND status = 'resolution_pending'
		`, input.TeamID, planID)
		if updatePlan.Error != nil {
			return updatePlan.Error
		}
		if updatePlan.RowsAffected != 1 {
			return ErrConflictAssessmentStale
		}
		result.Resolved = true
		result.UpdatedRelationships = updated
		result.RetractedEvidenceIDs = retracted
		result.DerivedEvidence = derived
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("conflict review: apply overdue resolution: %w", err)
	}
	return result, nil
}

func (r *LedgerRepositoryImpl) ResumePendingOverdueConflictResolution(
	ctx context.Context,
	input ResumePendingOverdueConflictResolutionInput,
) (*ApplyOverdueConflictResolutionResult, bool, error) {
	input = normalizeResumePendingOverdueConflictResolutionInput(input)
	if err := validateResumePendingOverdueConflictResolutionInput(input); err != nil {
		return nil, false, err
	}
	var resolutionInput ApplyOverdueConflictResolutionInput
	found := false
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := setConflictSystemTeamContext(ctx, tx, input.TeamID); err != nil {
			return err
		}
		if err := ensureActiveTeamForMutation(ctx, tx, input.TeamID); err != nil {
			return err
		}
		var expectedCaseVersion int
		var preferredPositionID string
		var assessmentAttemptID string
		var method string
		err := tx.WithContext(ctx).Raw(`
			SELECT plan.expected_case_version,
			       plan.preferred_position_id::text,
			       COALESCE(plan.assessment_attempt_id::text, ''),
			       plan.method
			FROM relationship_conflict_resolution_plans AS plan
			JOIN relationship_conflict_cases AS conflict
			  ON conflict.team_id = plan.team_id
			 AND conflict.conflict_id = plan.conflict_id
			WHERE plan.team_id = ?::uuid
			  AND plan.conflict_id = ?::uuid
			  AND plan.status = 'resolution_pending'
			  AND conflict.status = 'overdue'
			  AND conflict.version = plan.expected_case_version
			FOR UPDATE OF plan, conflict
		`, input.TeamID, input.ConflictID).Row().Scan(
			&expectedCaseVersion,
			&preferredPositionID,
			&assessmentAttemptID,
			&method,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		resolutionInput = ApplyOverdueConflictResolutionInput{
			TeamID:              input.TeamID,
			ConflictID:          input.ConflictID,
			ReviewRunID:         input.ReviewRunID,
			WorkerID:            input.WorkerID,
			ExpectedCaseVersion: expectedCaseVersion,
			PreferredPositionID: preferredPositionID,
			AssessmentAttemptID: assessmentAttemptID,
			Method:              method,
			Now:                 input.Now,
		}
		found = true
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("conflict review: resume pending overdue resolution: %w", err)
	}
	if !found {
		return nil, false, nil
	}
	result, err := r.ApplyOverdueConflictResolution(ctx, resolutionInput)
	if err != nil {
		return nil, true, err
	}
	return result, true, nil
}

func normalizeApplyOverdueConflictResolutionInput(input ApplyOverdueConflictResolutionInput) ApplyOverdueConflictResolutionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.ConflictID = strings.TrimSpace(input.ConflictID)
	input.ReviewRunID = strings.TrimSpace(input.ReviewRunID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.PreferredPositionID = strings.TrimSpace(input.PreferredPositionID)
	input.AssessmentAttemptID = strings.TrimSpace(input.AssessmentAttemptID)
	input.Method = strings.TrimSpace(input.Method)
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	} else {
		input.Now = input.Now.UTC()
	}
	return input
}

func normalizeResumePendingOverdueConflictResolutionInput(input ResumePendingOverdueConflictResolutionInput) ResumePendingOverdueConflictResolutionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.ConflictID = strings.TrimSpace(input.ConflictID)
	input.ReviewRunID = strings.TrimSpace(input.ReviewRunID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	} else {
		input.Now = input.Now.UTC()
	}
	return input
}

func normalizeConflictDerivedEvidenceTarget(target ConflictDerivedEvidenceTarget) ConflictDerivedEvidenceTarget {
	target.TaskID = strings.TrimSpace(target.TaskID)
	target.TeamID = strings.TrimSpace(target.TeamID)
	target.ConflictID = strings.TrimSpace(target.ConflictID)
	target.SystemProfileID = strings.TrimSpace(target.SystemProfileID)
	target.TargetFragmentID = strings.TrimSpace(target.TargetFragmentID)
	target.TargetOwnerProfileID = strings.TrimSpace(target.TargetOwnerProfileID)
	target.SelectedPositionID = strings.TrimSpace(target.SelectedPositionID)
	target.SourceGroupKey = strings.TrimSpace(target.SourceGroupKey)
	return target
}

func normalizeClaimConflictDerivedEvidenceTasksInput(input ClaimConflictDerivedEvidenceTasksInput) ClaimConflictDerivedEvidenceTasksInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.ReviewRunID = strings.TrimSpace(input.ReviewRunID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	return input
}

func validateApplyOverdueConflictResolutionInput(input ApplyOverdueConflictResolutionInput) error {
	for _, value := range []struct {
		name string
		id   string
	}{
		{name: "team_id", id: input.TeamID},
		{name: "conflict_id", id: input.ConflictID},
		{name: "review_run_id", id: input.ReviewRunID},
		{name: "preferred_position_id", id: input.PreferredPositionID},
	} {
		if _, err := uuid.Parse(value.id); err != nil {
			return fmt.Errorf("%s is required: %w", value.name, err)
		}
	}
	if input.AssessmentAttemptID != "" {
		if _, err := uuid.Parse(input.AssessmentAttemptID); err != nil {
			return fmt.Errorf("assessment_attempt_id is invalid: %w", err)
		}
	}
	if input.ExpectedCaseVersion < 1 {
		return errors.New("expected_case_version is required")
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	if input.Method != "ai" && input.Method != "last_write_wins" {
		return errors.New("resolution method is unsupported")
	}
	return nil
}

func validateResumePendingOverdueConflictResolutionInput(input ResumePendingOverdueConflictResolutionInput) error {
	for _, value := range []struct {
		name string
		id   string
	}{
		{name: "team_id", id: input.TeamID},
		{name: "conflict_id", id: input.ConflictID},
		{name: "review_run_id", id: input.ReviewRunID},
	} {
		if _, err := uuid.Parse(value.id); err != nil {
			return fmt.Errorf("%s is required: %w", value.name, err)
		}
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	return nil
}

func validateConflictDerivedEvidenceTarget(target ConflictDerivedEvidenceTarget) error {
	for _, value := range []struct {
		name string
		id   string
	}{
		{name: "derived_evidence_task_id", id: target.TaskID},
		{name: "team_id", id: target.TeamID},
		{name: "conflict_id", id: target.ConflictID},
		{name: "system_profile_id", id: target.SystemProfileID},
		{name: "target_fragment_id", id: target.TargetFragmentID},
		{name: "target_owner_profile_id", id: target.TargetOwnerProfileID},
		{name: "selected_position_id", id: target.SelectedPositionID},
	} {
		if _, err := uuid.Parse(value.id); err != nil {
			return fmt.Errorf("%s is required: %w", value.name, err)
		}
	}
	if target.SourceGroupKey == "" {
		return errors.New("source_group_key is required")
	}
	if len(target.SourceGroupKey) > 1000 {
		return errors.New("source_group_key exceeds maximum 1000")
	}
	if target.EvidenceIndex < 0 {
		return errors.New("evidence_index must not be negative")
	}
	return nil
}

func validateClaimConflictDerivedEvidenceTasksInput(input ClaimConflictDerivedEvidenceTasksInput) error {
	for _, value := range []struct {
		name string
		id   string
	}{
		{name: "team_id", id: input.TeamID},
		{name: "review_run_id", id: input.ReviewRunID},
	} {
		if _, err := uuid.Parse(value.id); err != nil {
			return fmt.Errorf("%s is required: %w", value.name, err)
		}
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	if input.Limit < 1 || input.Limit > conflictResolutionMaxFragments {
		return fmt.Errorf("derived evidence task limit must be between 1 and %d", conflictResolutionMaxFragments)
	}
	if input.Lease < time.Second {
		return errors.New("derived evidence task lease must be at least 1 second")
	}
	return nil
}

func setConflictSystemTeamContext(ctx context.Context, tx *gorm.DB, teamID string) error {
	return tx.WithContext(ctx).Exec("SELECT set_config('app.current_team_id', ?, true)", teamID).Error
}

func validateConflictResolutionPosition(ctx context.Context, tx *gorm.DB, teamID, conflictID, positionID string) error {
	var found string
	err := tx.WithContext(ctx).Raw(`
		SELECT position_id::text
		FROM relationship_conflict_positions
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
		  AND position_id = ?::uuid
		  AND active
	`, teamID, conflictID, positionID).Row().Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflictAssessmentStale
	}
	return err
}

func validateConflictResolutionAssessment(
	ctx context.Context,
	tx *gorm.DB,
	input ApplyOverdueConflictResolutionInput,
) error {
	if input.AssessmentAttemptID == "" {
		return ErrConflictAssessmentStale
	}
	var status string
	var selectedPositionID string
	var model string
	var policyVersion string
	err := tx.WithContext(ctx).Raw(`
		SELECT status,
		       COALESCE(selected_position_id::text, ''),
		       model,
		       policy_version
		FROM relationship_conflict_ai_assessment_attempts
		WHERE team_id = ?::uuid
		  AND assessment_attempt_id = ?::uuid
		  AND conflict_id = ?::uuid
		  AND case_version = ?
	`, input.TeamID, input.AssessmentAttemptID, input.ConflictID, input.ExpectedCaseVersion).Row().Scan(
		&status,
		&selectedPositionID,
		&model,
		&policyVersion,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflictAssessmentStale
	}
	if err != nil {
		return err
	}
	switch input.Method {
	case "ai":
		if status != "selected" || selectedPositionID != input.PreferredPositionID {
			return ErrConflictAssessmentStale
		}
	case "last_write_wins":
		if status == "abstained" {
			return nil
		}
		if status != "failed" {
			return ErrConflictAssessmentStale
		}
		failureCount, err := countFailedOverdueConflictAssessments(
			ctx,
			tx,
			input.TeamID,
			input.ConflictID,
			input.ExpectedCaseVersion,
			model,
			policyVersion,
		)
		if err != nil {
			return err
		}
		if failureCount < ConflictAssessmentMaxFailedDays {
			return ErrConflictAssessmentStale
		}
	default:
		return ErrConflictAssessmentStale
	}
	return nil
}

func expirePriorOverdueConflictAssessmentReservations(
	ctx context.Context,
	tx *gorm.DB,
	input ReserveOverdueConflictAssessmentInput,
	caseVersion int,
) error {
	rows, err := tx.WithContext(ctx).Raw(`
		UPDATE relationship_conflict_ai_assessment_attempts
		SET status = 'failed',
		    failure_class = 'reservation_expired',
		    completed_at = now()
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
		  AND case_version = ?
		  AND model = ?
		  AND policy_version = ?
		  AND status = 'reserved'
		  AND local_assessment_date < ?
		RETURNING assessment_attempt_id::text
	`, input.TeamID, input.ConflictID, caseVersion, input.Model, input.PolicyVersion, input.LocalAssessmentDate).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	assessmentAttemptIDs := []string{}
	for rows.Next() {
		var assessmentAttemptID string
		if err := rows.Scan(&assessmentAttemptID); err != nil {
			return err
		}
		assessmentAttemptIDs = append(assessmentAttemptIDs, assessmentAttemptID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, assessmentAttemptID := range assessmentAttemptIDs {
		metadata := map[string]any{
			"case_version":  caseVersion,
			"failure_class": "reservation_expired",
		}
		if err := appendConflictAssessmentEvent(ctx, tx, input.TeamID, assessmentAttemptID, "failed", "reservation_expired", metadata); err != nil {
			return err
		}
		if err := appendRelationshipConflictEvent(ctx, tx, input.TeamID, input.ConflictID, "", "", "", string(domain.RelationshipConflictEventAIAssessed), "reservation_expired", "case:"+input.ConflictID+":assessment:"+assessmentAttemptID+":failed", metadata); err != nil {
			return err
		}
	}
	return nil
}

func countFailedOverdueConflictAssessments(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictID string,
	caseVersion int,
	model string,
	policyVersion string,
) (int, error) {
	var count int
	err := tx.WithContext(ctx).Raw(`
		SELECT count(*)::int
		FROM relationship_conflict_ai_assessment_attempts
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
		  AND case_version = ?
		  AND model = ?
		  AND policy_version = ?
		  AND status = 'failed'
	`, teamID, conflictID, caseVersion, model, policyVersion).Row().Scan(&count)
	return count, err
}

func latestFailedOverdueConflictAssessmentID(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictID string,
	caseVersion int,
	model string,
	policyVersion string,
) (string, error) {
	var assessmentAttemptID string
	err := tx.WithContext(ctx).Raw(`
		SELECT assessment_attempt_id::text
		FROM relationship_conflict_ai_assessment_attempts
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
		  AND case_version = ?
		  AND model = ?
		  AND policy_version = ?
		  AND status = 'failed'
		ORDER BY completed_at DESC NULLS LAST, created_at DESC, assessment_attempt_id DESC
		LIMIT 1
	`, teamID, conflictID, caseVersion, model, policyVersion).Row().Scan(&assessmentAttemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrConflictAssessmentStale
	}
	return assessmentAttemptID, err
}

func ensureConflictResolutionPlan(
	ctx context.Context,
	tx *gorm.DB,
	input ApplyOverdueConflictResolutionInput,
	effectiveAt time.Time,
	effectiveTimeBasis string,
) (string, string, error) {
	var existingID string
	var existingPositionID string
	var existingStatus string
	err := tx.WithContext(ctx).Raw(`
		SELECT resolution_plan_id::text, preferred_position_id::text, status
		FROM relationship_conflict_resolution_plans
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
		  AND expected_case_version = ?
		FOR UPDATE
	`, input.TeamID, input.ConflictID, input.ExpectedCaseVersion).Row().Scan(&existingID, &existingPositionID, &existingStatus)
	if err == nil {
		if existingPositionID != input.PreferredPositionID {
			return "", "", ErrConflictAssessmentStale
		}
		return existingID, existingStatus, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	method := input.Method
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO relationship_conflict_resolution_plans (
		    team_id, conflict_id, expected_case_version, preferred_position_id,
		    assessment_attempt_id, method, effective_at, effective_time_basis
		) VALUES (
		    ?::uuid, ?::uuid, ?, ?::uuid,
		    NULLIF(?, '')::uuid, ?, ?, ?
		)
		RETURNING resolution_plan_id::text, status
	`, input.TeamID, input.ConflictID, input.ExpectedCaseVersion, input.PreferredPositionID,
		input.AssessmentAttemptID, method, effectiveAt, effectiveTimeBasis).Rows()
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", "", sql.ErrNoRows
	}
	if err := rows.Scan(&existingID, &existingStatus); err != nil {
		return "", "", err
	}
	return existingID, existingStatus, rows.Err()
}

func markConflictResolutionPlanSuperseded(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictID string,
	expectedCaseVersion int,
) error {
	return tx.WithContext(ctx).Exec(`
		UPDATE relationship_conflict_resolution_plans
		SET status = 'superseded',
		    failure_reason = 'case_version_changed'
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
		  AND expected_case_version = ?
		  AND status = 'resolution_pending'
	`, teamID, conflictID, expectedCaseVersion).Error
}

func conflictResolutionEffectiveTime(record RelationshipConflictCaseRecord, preferredPositionID string, now time.Time) (time.Time, string) {
	for _, position := range record.Positions {
		if position.PositionID != preferredPositionID || position.EffectiveAt == nil {
			continue
		}
		basis := strings.TrimSpace(position.EffectiveTimeBasis)
		if basis == "" {
			basis = "recorded_at"
		}
		return position.EffectiveAt.UTC(), basis
	}
	return now.UTC(), "recorded_at"
}

func conflictResolutionReason(method string) string {
	if method == "last_write_wins" {
		return domain.ConflictResolutionReasonLWW
	}
	return domain.ConflictResolutionReasonAI
}

type conflictLosingEvidenceTarget struct {
	FragmentID     string
	OwnerProfileID string
	SourceGroupKey string
	EvidenceIndex  int
}

func loadConflictLosingEvidenceTargets(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictID string,
	preferredPositionID string,
) ([]conflictLosingEvidenceTarget, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH active_members AS (
			SELECT member.position_id,
			       member.relationship_id,
			       member.fragment_id,
			       member.owner_profile_id,
			       member.source_group_key
			FROM relationship_conflict_position_members AS member
			WHERE member.team_id = ?::uuid
			  AND member.conflict_id = ?::uuid
			  AND member.active
			  AND member.fragment_id IS NOT NULL
		),
		losing_members AS (
			SELECT member.fragment_id,
			       member.owner_profile_id,
			       MIN(member.source_group_key) AS source_group_key
			FROM active_members AS member
			WHERE member.position_id <> ?::uuid
			GROUP BY member.fragment_id, member.owner_profile_id
		),
		targets AS (
			SELECT loser.fragment_id, loser.owner_profile_id, loser.source_group_key
			FROM losing_members AS loser
			WHERE NOT EXISTS (
				SELECT 1
				FROM active_members AS preferred
				WHERE preferred.position_id = ?::uuid
				  AND preferred.fragment_id = loser.fragment_id
			)
			AND NOT EXISTS (
				SELECT 1
				FROM relationship_evidence_supports AS shared
				WHERE shared.team_id = ?::uuid
				  AND shared.fragment_id = loser.fragment_id
				  AND shared.owner_profile_id = loser.owner_profile_id
				  AND NOT EXISTS (
					  SELECT 1
					  FROM active_members AS case_member
					  WHERE case_member.relationship_id = shared.relationship_id
				  )
			)
		)
		SELECT target.fragment_id::text,
		       target.owner_profile_id::text,
		       target.source_group_key,
		       fragment.evidence_index
		FROM targets AS target
		JOIN evidence_fragments AS fragment
		  ON fragment.team_id = ?::uuid
		 AND fragment.fragment_id = target.fragment_id
		LEFT JOIN evidence_lifecycle_events AS lifecycle
		  ON lifecycle.team_id = fragment.team_id
		 AND lifecycle.target_fragment_id = fragment.fragment_id
		WHERE lifecycle.lifecycle_event_id IS NULL
		ORDER BY target.owner_profile_id, target.fragment_id
	`, teamID, conflictID, preferredPositionID, preferredPositionID, teamID, teamID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []conflictLosingEvidenceTarget{}
	for rows.Next() {
		var target conflictLosingEvidenceTarget
		if err := rows.Scan(&target.FragmentID, &target.OwnerProfileID, &target.SourceGroupKey, &target.EvidenceIndex); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func retractConflictLosingEvidence(
	ctx context.Context,
	tx *gorm.DB,
	input ApplyOverdueConflictResolutionInput,
	systemProfileID string,
	targets []conflictLosingEvidenceTarget,
) ([]string, error) {
	byOwner := make(map[string]map[string]struct{})
	for _, target := range targets {
		if _, ok := byOwner[target.OwnerProfileID]; !ok {
			byOwner[target.OwnerProfileID] = make(map[string]struct{})
		}
		byOwner[target.OwnerProfileID][target.FragmentID] = struct{}{}
	}
	ownerIDs := make([]string, 0, len(byOwner))
	for ownerID := range byOwner {
		ownerIDs = append(ownerIDs, ownerID)
	}
	sort.Strings(ownerIDs)
	retracted := make([]string, 0, len(targets))
	for _, ownerID := range ownerIDs {
		fragmentIDs := make([]string, 0, len(byOwner[ownerID]))
		for fragmentID := range byOwner[ownerID] {
			fragmentIDs = append(fragmentIDs, fragmentID)
		}
		fragmentIDs = sortedEvidenceIDs(fragmentIDs)
		for start := 0; start < len(fragmentIDs); start += 50 {
			end := start + 50
			if end > len(fragmentIDs) {
				end = len(fragmentIDs)
			}
			chunk := fragmentIDs[start:end]
			if err := lockEvidenceLifecycleTargetIDs(ctx, tx, input.TeamID, chunk); err != nil {
				return nil, err
			}
			operation := evidenceLifecycleOperationInput{
				TeamID:         input.TeamID,
				OwnerProfileID: ownerID,
				ActorProfileID: systemProfileID,
				Action:         "retract",
				EvidenceIDs:    chunk,
				Reason:         "overdue conflict resolution",
				IdempotencyKey: "conflict:" + input.ConflictID + ":owner:" + ownerID + ":retract:" + fmt.Sprint(start/50),
				RequestHash:    sha256Hex(input.ConflictID + "\x00" + input.PreferredPositionID + "\x00" + strings.Join(chunk, ",")),
			}
			plan, err := planEvidenceLifecycleInSystem(ctx, tx, operation)
			if err != nil {
				return nil, err
			}
			operationID, err := insertEvidenceLifecycleOperation(ctx, tx, operation, plan)
			if err != nil {
				return nil, err
			}
			if err := insertEvidenceLifecycleEvents(ctx, tx, operation, operationID); err != nil {
				return nil, err
			}
			if err := applyEvidenceLifecycleEffectsInSystem(ctx, tx, operation, operationID, plan); err != nil {
				return nil, err
			}
			for _, fragmentID := range chunk {
				if err := appendRelationshipConflictEvent(ctx, tx, input.TeamID, input.ConflictID, input.PreferredPositionID, "", ownerID, string(domain.RelationshipConflictEventEvidenceRetracted), "retracted", "case:"+input.ConflictID+":evidence:"+fragmentID+":retracted", map[string]any{
					"fragment_id":            fragmentID,
					"lifecycle_operation_id": operationID,
				}); err != nil {
					return nil, err
				}
				retracted = append(retracted, fragmentID)
			}
		}
	}
	return retracted, nil
}

func conflictDerivedEvidenceTargets(
	teamID string,
	conflictID string,
	systemProfileID string,
	selectedPositionID string,
	targets []conflictLosingEvidenceTarget,
	retractedIDs []string,
) []ConflictDerivedEvidenceTarget {
	retracted := make(map[string]struct{}, len(retractedIDs))
	for _, fragmentID := range retractedIDs {
		retracted[fragmentID] = struct{}{}
	}
	result := make([]ConflictDerivedEvidenceTarget, 0, len(retractedIDs))
	seen := make(map[string]struct{}, len(retractedIDs))
	for _, target := range targets {
		key := target.OwnerProfileID + "\x00" + target.FragmentID
		if _, ok := retracted[target.FragmentID]; !ok {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, ConflictDerivedEvidenceTarget{
			TeamID:               teamID,
			ConflictID:           conflictID,
			SystemProfileID:      systemProfileID,
			TargetFragmentID:     target.FragmentID,
			TargetOwnerProfileID: target.OwnerProfileID,
			SelectedPositionID:   selectedPositionID,
			SourceGroupKey:       target.SourceGroupKey,
			EvidenceIndex:        target.EvidenceIndex,
		})
	}
	return result
}

func enqueueConflictDerivedEvidenceTasks(
	ctx context.Context,
	tx *gorm.DB,
	resolutionPlanID string,
	targets []ConflictDerivedEvidenceTarget,
) ([]ConflictDerivedEvidenceTarget, error) {
	result := make([]ConflictDerivedEvidenceTarget, 0, len(targets))
	for _, target := range targets {
		var taskID string
		err := tx.WithContext(ctx).Raw(`
			INSERT INTO relationship_conflict_derived_evidence_tasks (
			    team_id, resolution_plan_id, conflict_id,
			    target_fragment_id, target_owner_profile_id, selected_position_id,
			    system_profile_id, source_group_key, origin_evidence_index
		) VALUES (
			    ?::uuid, ?::uuid, ?::uuid,
			    ?::uuid, ?::uuid, ?::uuid,
			    ?::uuid, ?, ?
		)
			ON CONFLICT (team_id, conflict_id, target_fragment_id) DO NOTHING
			RETURNING derived_evidence_task_id::text
		`, target.TeamID, resolutionPlanID, target.ConflictID,
			target.TargetFragmentID, target.TargetOwnerProfileID, target.SelectedPositionID,
			target.SystemProfileID, target.SourceGroupKey, target.EvidenceIndex).Row().Scan(&taskID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if taskID == "" {
			if err := tx.WithContext(ctx).Raw(`
				SELECT derived_evidence_task_id::text
				FROM relationship_conflict_derived_evidence_tasks
				WHERE team_id = ?::uuid
				  AND conflict_id = ?::uuid
				  AND target_fragment_id = ?::uuid
			`, target.TeamID, target.ConflictID, target.TargetFragmentID).Row().Scan(&taskID); err != nil {
				return nil, err
			}
		}
		target.TaskID = taskID
		result = append(result, target)
	}
	return result, nil
}
