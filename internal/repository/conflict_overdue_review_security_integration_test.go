package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestConflictResolutionDeletionOnlyMarkerRequiresSystemOrigin(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-deletion-only-origin-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "conflict-deletion-only-origin-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	marker := map[string]any{"conflict_resolution_deletion_only": true}

	callerIngest, err := ledgerRepo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "caller-deletion-only-marker",
		RequestHash:    sha256Hex("caller-deletion-only-marker"),
		SourceSummary:  conflictResolutionDeletionOnlySourceSummary,
		Metadata:       marker,
		Evidence: []EvidenceInput{{
			Content:    "Caller-selected deletion-only marker.",
			SourceType: "observation",
			Authority:  string(domain.AuthorityInferred),
			Metadata:   marker,
		}},
	})
	require.NoError(t, err)
	require.Len(t, callerIngest.Evidence, 1)

	var callerDeletionOnly bool
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		var err error
		callerDeletionOnly, err = isConflictResolutionDeletionOnlyFragment(ctx, tx, teamID, callerIngest.Evidence[0].FragmentID)
		return err
	}))
	assert.False(t, callerDeletionOnly)

	var systemProfileID string
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		if err := setConflictSystemTeamContext(ctx, tx, teamID); err != nil {
			return err
		}
		var err error
		systemProfileID, err = ensureConflictSystemProfile(ctx, tx, teamID)
		return err
	}))
	systemIngest, err := ledgerRepo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: systemProfileID,
		IdempotencyKey: "system-deletion-only-marker",
		RequestHash:    sha256Hex("system-deletion-only-marker"),
		SourceSummary:  conflictResolutionDeletionOnlySourceSummary,
		Metadata:       marker,
		Evidence: []EvidenceInput{{
			Content:    "System-staged deletion-only marker.",
			SourceType: "observation",
			Authority:  string(domain.AuthorityInferred),
			Metadata:   marker,
		}},
	})
	require.NoError(t, err)
	require.Len(t, systemIngest.Evidence, 1)

	var systemDeletionOnly, missingDeletionOnly bool
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		var err error
		systemDeletionOnly, err = isConflictResolutionDeletionOnlyFragment(ctx, tx, teamID, systemIngest.Evidence[0].FragmentID)
		if err != nil {
			return err
		}
		missingDeletionOnly, err = isConflictResolutionDeletionOnlyFragment(ctx, tx, teamID, uuid.NewString())
		return err
	}))
	assert.True(t, systemDeletionOnly)
	assert.False(t, missingDeletionOnly)
}

func TestOverdueConflictWorkflowTablesEnforceCrossTeamRLS(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "overdue-workflow-rls", 3, "exact", "")
	teamA := createLedgerTeam(t, adminDB, rls, "overdue-workflow-rls-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "overdue-workflow-rls-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "overdue-workflow-rls-owner-b")
	teamB := createLedgerTeam(t, adminDB, rls, "overdue-workflow-rls-team-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamB, "overdue-workflow-rls-owner-c")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "product", "GraphDB")
	preferred := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamA, ownerA, "worker-overdue-workflow-rls-a",
		"overdue-workflow-rls-a", "Dense-Mem uses PostgreSQL.", subject.EntityID, postgres.EntityID, "source-group-overdue-workflow-rls-a",
	)
	loser := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamA, ownerB, "worker-overdue-workflow-rls-b",
		"overdue-workflow-rls-b", "Dense-Mem uses GraphDB.", subject.EntityID, graphdb.EntityID, "source-group-overdue-workflow-rls-b",
	)
	conflictID, caseVersion := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamA, ownerA, subject.EntityID)

	var preferredPositionID, targetFragmentID, systemProfileID string
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		if err := setConflictSystemTeamContext(ctx, tx, teamA); err != nil {
			return err
		}
		var err error
		systemProfileID, err = ensureConflictSystemProfile(ctx, tx, teamA)
		if err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT member.position_id::text
			FROM relationship_conflict_position_members AS member
			WHERE member.team_id = ?::uuid
			  AND member.conflict_id = ?::uuid
			  AND member.relationship_id = ?::uuid
			  AND member.active
		`, teamA, conflictID, preferred.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&preferredPositionID); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT support.fragment_id::text
			FROM relationship_evidence_supports AS support
			WHERE support.team_id = ?::uuid
			  AND support.relationship_id = ?::uuid
			  AND support.owner_profile_id = ?::uuid
		`, teamA, loser.RelationshipResults[0].Relationship.RelationshipID, ownerB).Row().Scan(&targetFragmentID)
	}))

	assessmentAttemptID := uuid.NewString()
	resolutionPlanID := uuid.NewString()
	derivedTaskID := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		if err := setConflictSystemTeamContext(ctx, tx, teamA); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO relationship_conflict_ai_assessment_attempts (
			    team_id, assessment_attempt_id, conflict_id, case_version, local_assessment_date,
			    model, policy_version, status, failure_class, completed_at
		) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?, CURRENT_DATE,
			    'test-model', ?, 'failed', 'test_failure', now()
		)
		`, teamA, assessmentAttemptID, conflictID, caseVersion, domain.ConflictOverduePolicyVersion).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO relationship_conflict_ai_assessment_events (
			    team_id, assessment_attempt_id, action, outcome
		) VALUES (
			    ?::uuid, ?::uuid, 'failed', 'test_failure'
		)
		`, teamA, assessmentAttemptID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO relationship_conflict_resolution_plans (
			    team_id, resolution_plan_id, conflict_id, expected_case_version,
			    preferred_position_id, assessment_attempt_id, method, effective_at
		) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?, ?::uuid, ?::uuid, 'ai', now()
		)
		`, teamA, resolutionPlanID, conflictID, caseVersion, preferredPositionID, assessmentAttemptID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO relationship_conflict_evidence_derivations (
			    team_id, conflict_id, target_fragment_id, target_owner_profile_id,
			    selected_position_id, system_profile_id
		) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid
		)
		`, teamA, conflictID, targetFragmentID, ownerB, preferredPositionID, systemProfileID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO relationship_conflict_derived_evidence_tasks (
			    team_id, derived_evidence_task_id, resolution_plan_id, conflict_id,
			    target_fragment_id, target_owner_profile_id, selected_position_id,
			    system_profile_id, source_group_key, origin_evidence_index
		) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid, 'test-source-group', 0
		)
		`, teamA, derivedTaskID, resolutionPlanID, conflictID, targetFragmentID, ownerB, preferredPositionID, systemProfileID).Error
	}))

	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamB, ownerC, func(tx *gorm.DB) error {
		var total int
		if err := tx.Raw(`
			SELECT
			    (SELECT count(*) FROM relationship_conflict_ai_assessment_attempts WHERE team_id = ?::uuid) +
			    (SELECT count(*) FROM relationship_conflict_ai_assessment_events WHERE team_id = ?::uuid) +
			    (SELECT count(*) FROM relationship_conflict_resolution_plans WHERE team_id = ?::uuid) +
			    (SELECT count(*) FROM relationship_conflict_evidence_derivations WHERE team_id = ?::uuid) +
			    (SELECT count(*) FROM relationship_conflict_derived_evidence_tasks WHERE team_id = ?::uuid)
		`, teamA, teamA, teamA, teamA, teamA).Row().Scan(&total); err != nil {
			return err
		}
		if total != 0 {
			return errors.New("cross-team workflow rows were visible")
		}
		updated := tx.Exec(`
			UPDATE relationship_conflict_ai_assessment_attempts
			SET failure_class = 'cross_team'
			WHERE team_id = ?::uuid
		`, teamA)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 0 {
			return errors.New("cross-team workflow row was updated")
		}
		return nil
	}))
	err := rls.WithTeamProfileTx(ctx, appDB, teamB, ownerC, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO relationship_conflict_ai_assessment_events (
			    team_id, assessment_attempt_id, action, outcome
		) VALUES (
			    ?::uuid, ?::uuid, 'failed', 'cross_team'
		)
		`, teamA, assessmentAttemptID).Error
	})
	require.Error(t, err)

	stale := commitOverdueConflictResolutionWithVectors(t, ctx, ledgerRepo, ApplyOverdueConflictResolutionInput{
		TeamID:              teamB,
		ConflictID:          conflictID,
		ReviewRunID:         uuid.NewString(),
		WorkerID:            "worker-overdue-workflow-rls-cross-team",
		ExpectedCaseVersion: caseVersion,
		PreferredPositionID: preferredPositionID,
		AssessmentAttemptID: assessmentAttemptID,
		Method:              "ai",
		Now:                 time.Now().UTC(),
	})
	assert.True(t, stale.Stale)
}

func TestOverdueConflictDossierKeepsStrongestSupportInSourceGroup(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "overdue-authority-dedup", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "overdue-authority-dedup-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "overdue-authority-dedup-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "overdue-authority-dedup-owner-b")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")
	preferredContent := "Dense-Mem uses PostgreSQL. Dense-Mem uses PostgreSQL."
	preferredQuote := "Dense-Mem uses PostgreSQL."
	preferredSecondStart := strings.LastIndex(preferredContent, preferredQuote)
	require.Greater(t, preferredSecondStart, 0)
	preferred := commitPlacementRelationshipForConflictTestWithOptions(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-overdue-authority-dedup-a",
		"overdue-authority-dedup-a", preferredContent, subject.EntityID, postgres.EntityID, "source-group-overdue-authority-dedup-a",
		conflictTestRelationshipOptions{
			authority: "primary",
			additionalSupports: []conflictTestAdditionalSupport{{
				sourceGroupKey: "source-group-overdue-authority-dedup-a",
				spanStart:      preferredSecondStart,
				spanEnd:        preferredSecondStart + len(preferredQuote),
				quote:          preferredQuote,
				authority:      "inferred",
			}},
		},
	)
	commitPlacementRelationshipForConflictTestWithOptions(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-overdue-authority-dedup-b",
		"overdue-authority-dedup-b", "Dense-Mem uses GraphDB.", subject.EntityID, graphdb.EntityID, "source-group-overdue-authority-dedup-b",
		conflictTestRelationshipOptions{authority: "secondary"},
	)
	conflictID, _ := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	reviewNow := time.Now().UTC()
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_conflict_cases
			SET review_due_at = ?, next_review_at = ?
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, reviewNow.Add(-time.Minute), reviewNow.Add(-time.Minute), teamID, conflictID).Error
	}))
	review := reviewConflictCaseForTest(t, ctx, ledgerRepo, teamID, "worker-overdue-authority-dedup-review", conflictID, reviewNow)
	require.Equal(t, ConflictReviewOutcomeOverdue, review.Outcome)
	reservation, dossier, reserved, err := ledgerRepo.ReserveOverdueConflictAssessment(ctx, ReserveOverdueConflictAssessmentInput{
		TeamID:              teamID,
		ConflictID:          conflictID,
		ReviewRunID:         uuid.NewString(),
		WorkerID:            "worker-overdue-authority-dedup-assessment",
		LocalAssessmentDate: reviewNow,
		Model:               "test-model",
		PolicyVersion:       domain.ConflictOverduePolicyVersion,
	})
	require.NoError(t, err)
	require.True(t, reserved)
	require.NotNil(t, reservation)
	require.NotNil(t, dossier)

	preferredPositionID := ""
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT position_id::text
			FROM relationship_conflict_position_members
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			  AND relationship_id = ?::uuid
			  AND active
		`, teamID, conflictID, preferred.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&preferredPositionID)
	}))
	positions := make([]domain.ConflictResolutionPosition, 0, len(dossier.Positions))
	for _, position := range dossier.Positions {
		positions = append(positions, domain.ConflictResolutionPosition{
			PositionID: position.PositionID,
			Supports:   position.Supports,
		})
	}
	winner, ok := domain.SelectConflictLastWriteWinner(positions)
	require.True(t, ok)
	assert.Equal(t, preferredPositionID, winner.PositionID)
	assert.Equal(t, "primary", winner.Authority)
}
