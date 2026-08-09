//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupporterConflictPolicyMigrationRewritesActiveWorkflow(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026080901)
	teamID, profileID := insertMigrationTeamProfile(t, ctx, sqlDB)
	subjectID := uuid.NewString()
	openConflictID := uuid.NewString()
	overdueConflictID := uuid.NewString()
	resolvedConflictID := uuid.NewString()
	openPositionID := uuid.NewString()
	overduePositionID := uuid.NewString()
	resolvedPositionID := uuid.NewString()
	attemptID := uuid.NewString()
	planID := uuid.NewString()
	reviewRunID := uuid.NewString()
	legacyReviewRunID := uuid.NewString()
	dueAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Microsecond)
	nextReviewAt := dueAt.Add(-time.Minute)
	overdueDueAt := dueAt.Add(-4 * time.Hour)
	overdueNextReviewAt := overdueDueAt.Add(-time.Minute)
	resolvedDueAt := dueAt.Add(-6 * time.Hour)
	resolvedNextReviewAt := resolvedDueAt.Add(-time.Minute)

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_team_refs (team_id) VALUES ($1::uuid)
		`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_profile_refs (team_id, profile_id)
			VALUES ($1::uuid, $2::uuid)
		`, teamID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO team_predicate_definitions (
				team_id, predicate_key, version, relationship_kind, current_cardinality
			) VALUES ($1::uuid, 'migration_predicate', 1, 'state', 'one')
		`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO entity_records (team_id, entity_id, entity_kind)
			VALUES ($1::uuid, $2::uuid, 'project')
		`, teamID, subjectID); err != nil {
			return err
		}
		for _, fixture := range []struct {
			conflictID string
			positionID string
			scopeKey   string
			status     string
			dueAt      time.Time
			nextAt     time.Time
		}{
			{openConflictID, openPositionID, "migration-open", "open", dueAt, nextReviewAt},
			{overdueConflictID, overduePositionID, "migration-overdue", "overdue", overdueDueAt, overdueNextReviewAt},
		} {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO relationship_conflict_cases (
					team_id, conflict_id, semantic_scope_key, status,
					subject_entity_id, predicate_key, predicate_version,
					relationship_kind, current_cardinality, review_due_at,
					next_review_at, review_ttl_days, policy_version, version, attempts
				) VALUES (
					$1::uuid, $2::uuid, $3, $4, $5::uuid, 'migration_predicate', 1,
					'state', 'one', $6, $7, 7, 'cross_profile_conflict_v1', 4, 3
				)
			`, teamID, fixture.conflictID, fixture.scopeKey, fixture.status, subjectID, fixture.dueAt, fixture.nextAt); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO relationship_conflict_positions (
					team_id, conflict_id, position_id, position_key, object_entity_id,
					support_group_count, authoritative_group_count
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid, 3, 1)
			`, teamID, fixture.conflictID, fixture.positionID, "entity:"+subjectID, subjectID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO relationship_conflict_cases (
				team_id, conflict_id, semantic_scope_key, status,
				subject_entity_id, predicate_key, predicate_version,
				relationship_kind, current_cardinality, review_due_at,
				next_review_at, review_ttl_days, policy_version, version, attempts
			) VALUES (
				$1::uuid, $2::uuid, 'migration-resolved', 'resolved', $3::uuid,
				'migration_predicate', 1, 'state', 'one', $4, $5, 7,
				'legacy_resolved_policy', 9, 2
			)
		`, teamID, resolvedConflictID, subjectID, resolvedDueAt, resolvedNextReviewAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO relationship_conflict_positions (
				team_id, conflict_id, position_id, position_key, object_entity_id,
				support_group_count, authoritative_group_count
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid, 4, 2)
		`, teamID, resolvedConflictID, resolvedPositionID, "entity:"+subjectID, subjectID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO relationship_conflict_ai_assessment_attempts (
				team_id, assessment_attempt_id, conflict_id, case_version,
				local_assessment_date, model, policy_version, status
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 4, CURRENT_DATE,
			          'migration-model', 'overdue_conflict_ai_v1', 'reserved')
		`, teamID, attemptID, openConflictID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO relationship_conflict_resolution_plans (
				team_id, resolution_plan_id, conflict_id, expected_case_version,
				preferred_position_id, assessment_attempt_id, method, effective_at, status
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 4, $4::uuid, $5::uuid,
			          'ai', now(), 'resolution_pending')
		`, teamID, planID, openConflictID, openPositionID, attemptID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO relationship_conflict_review_runs (
				team_id, review_run_id, local_run_date, policy_version,
				status, worker_id, timezone, lease_until
			) VALUES ($1::uuid, $2::uuid, CURRENT_DATE, 'cross_profile_conflict_v1',
			          'running', 'legacy-worker', 'UTC', now() + interval '1 hour')
		`, teamID, reviewRunID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO relationship_conflict_review_runs (
				team_id, review_run_id, local_run_date, policy_version,
				status, worker_id, timezone, lease_until
			) VALUES ($1::uuid, $2::uuid, CURRENT_DATE, 'another_legacy_policy',
			          'reserved', 'another-legacy-worker', 'UTC', now() + interval '1 hour')
		`, teamID, legacyReviewRunID)
		return err
	}))

	migrationStarted := time.Now().UTC()
	runGooseUpTo(t, ctx, sqlDB, 2026080902)
	migrationFinished := time.Now().UTC()

	var openPolicy, openStatus string
	var openVersion, openAttempts int
	var openDue, openNext time.Time
	var overdueNext time.Time
	var resolvedPolicy, resolvedStatus string
	var resolvedVersion, resolvedAttempts int
	var resolvedNext time.Time
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			SELECT policy_version, status, version, attempts, review_due_at, next_review_at
			FROM relationship_conflict_cases
			WHERE team_id = $1::uuid AND conflict_id = $2::uuid
		`, teamID, openConflictID).Scan(&openPolicy, &openStatus, &openVersion, &openAttempts, &openDue, &openNext); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT next_review_at
			FROM relationship_conflict_cases
			WHERE team_id = $1::uuid AND conflict_id = $2::uuid
		`, teamID, overdueConflictID).Scan(&overdueNext); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `
			SELECT policy_version, status, version, attempts, next_review_at
			FROM relationship_conflict_cases
			WHERE team_id = $1::uuid AND conflict_id = $2::uuid
		`, teamID, resolvedConflictID).Scan(&resolvedPolicy, &resolvedStatus, &resolvedVersion, &resolvedAttempts, &resolvedNext)
	}))
	assert.Equal(t, "cross_profile_supporter_majority_after_ttl", openPolicy)
	assert.Equal(t, "open", openStatus)
	assert.Equal(t, 5, openVersion)
	assert.Zero(t, openAttempts)
	assert.WithinDuration(t, dueAt, openDue, time.Microsecond)
	assert.WithinDuration(t, dueAt, openNext, time.Microsecond)
	assert.WithinDuration(t, migrationStarted, overdueNext, migrationFinished.Sub(migrationStarted)+time.Second)
	assert.Equal(t, "legacy_resolved_policy", resolvedPolicy)
	assert.Equal(t, "resolved", resolvedStatus)
	assert.Equal(t, 9, resolvedVersion)
	assert.Equal(t, 2, resolvedAttempts)
	assert.WithinDuration(t, resolvedNextReviewAt, resolvedNext, time.Microsecond)

	var attemptStatus, failureClass string
	var attemptCompletedAt sql.NullTime
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT status, failure_class, completed_at
			FROM relationship_conflict_ai_assessment_attempts
			WHERE team_id = $1::uuid AND assessment_attempt_id = $2::uuid
		`, teamID, attemptID).Scan(&attemptStatus, &failureClass, &attemptCompletedAt)
	}))
	assert.Equal(t, "superseded", attemptStatus)
	assert.Equal(t, "policy_replaced", failureClass)
	assert.True(t, attemptCompletedAt.Valid)

	var planStatus, planFailure string
	var reviewStatus, reviewError string
	var legacyReviewStatus, legacyReviewError string
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			SELECT status, failure_reason
			FROM relationship_conflict_resolution_plans
			WHERE team_id = $1::uuid AND resolution_plan_id = $2::uuid
		`, teamID, planID).Scan(&planStatus, &planFailure); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT status, last_error
			FROM relationship_conflict_review_runs
			WHERE team_id = $1::uuid AND review_run_id = $2::uuid
		`, teamID, reviewRunID).Scan(&reviewStatus, &reviewError); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `
			SELECT status, last_error
			FROM relationship_conflict_review_runs
			WHERE team_id = $1::uuid AND review_run_id = $2::uuid
		`, teamID, legacyReviewRunID).Scan(&legacyReviewStatus, &legacyReviewError)
	}))
	assert.Equal(t, "superseded", planStatus)
	assert.Equal(t, "policy_replaced", planFailure)
	assert.Equal(t, "failed", reviewStatus)
	assert.Equal(t, "policy_replaced", reviewError)
	assert.Equal(t, "failed", legacyReviewStatus)
	assert.Equal(t, "policy_replaced", legacyReviewError)

	var migrationEvents, assessmentEvents int
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*)
			FROM relationship_conflict_events
			WHERE team_id = $1::uuid AND conflict_id IN ($2::uuid, $3::uuid)
			  AND action = 'policy_migrated'
		`, teamID, openConflictID, overdueConflictID).Scan(&migrationEvents); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `
			SELECT count(*)
			FROM relationship_conflict_ai_assessment_events
			WHERE team_id = $1::uuid AND assessment_attempt_id = $2::uuid
			  AND action = 'superseded' AND outcome = 'policy_replaced'
		`, teamID, attemptID).Scan(&assessmentEvents)
	}))
	assert.Equal(t, 2, migrationEvents)
	assert.Equal(t, 1, assessmentEvents)
	assert.True(t, columnExists(t, ctx, sqlDB, "relationship_conflict_positions", "support_group_count"))
	assert.True(t, columnExists(t, ctx, sqlDB, "relationship_conflict_positions", "authoritative_group_count"))

	// Re-executing the audit insert must not append duplicate rows.
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO relationship_conflict_events (
				team_id, conflict_id, action, outcome, actor_kind, policy_version,
				idempotency_key, metadata
			) VALUES (
				$1::uuid, $2::uuid, 'policy_migrated', 'supporter_majority_after_ttl',
				'system', 'cross_profile_supporter_majority_after_ttl',
				'case:' || $2::text || ':policy_migrated:supporter_majority_after_ttl',
				'{}'::jsonb
			)
			ON CONFLICT (team_id, idempotency_key) WHERE idempotency_key <> '' DO NOTHING
		`, teamID, openConflictID)
		return err
	}))
	var repeatEvents int
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT count(*)
			FROM relationship_conflict_events
			WHERE team_id = $1::uuid AND action = 'policy_migrated'
		`, teamID).Scan(&repeatEvents)
	}))
	assert.Equal(t, 2, repeatEvents)

	runGooseUpTo(t, ctx, sqlDB, 2026080903)
	var constraintValidated bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT convalidated
		FROM pg_constraint
		WHERE conname = 'relationship_conflict_events_action_check'
	`).Scan(&constraintValidated))
	assert.True(t, constraintValidated)
}
