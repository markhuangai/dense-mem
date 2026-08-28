package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestRelationshipConflictReviewerDismissesStaleCaseAfterRetraction(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-review-dismiss-stale", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-review-dismiss-stale-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-dismiss-stale-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-dismiss-stale-owner-b")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")
	active := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-dismiss-stale-a",
		"dismiss-stale-conflict-a", "Dense-Mem uses PostgreSQL.", subject.EntityID, postgres.EntityID, "source-group-dismiss-stale-a",
	)
	stale := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-dismiss-stale-b",
		"dismiss-stale-conflict-b", "Dense-Mem uses GraphDB.", subject.EntityID, graphdb.EntityID, "source-group-dismiss-stale-b",
	)

	conflictID, conflictVersion := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	_, err := semanticRepo.RetractRelationship(ctx, RetractRelationshipInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		RelationshipID: stale.RelationshipResults[0].Relationship.RelationshipID,
		Reason:         "source corrected before conflict review",
		IdempotencyKey: "dismiss-stale-retract",
	})
	require.NoError(t, err)
	historicalKnownAt := time.Now().UTC()

	result := reviewConflictCaseForTest(t, ctx, ledgerRepo, teamID, "conflict-reviewer-dismiss-stale", conflictID, time.Now().UTC())
	assert.Equal(t, ConflictReviewOutcomeNoop, result.Outcome)
	assert.Equal(t, ConflictReviewStageDismissedNoConflict, result.Stage)

	var status string
	var version int
	var activeMembers, dismissedEvents int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT status, version
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Row().Scan(&status, &version))
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_conflict_position_members
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			  AND active
		`, teamID, conflictID).Scan(&activeMembers).Error)
		return tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_conflict_events
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			  AND action = 'dismissed'
		`, teamID, conflictID).Scan(&dismissedEvents).Error
	}))
	assert.Equal(t, "dismissed", status)
	assert.Greater(t, version, conflictVersion)
	assert.Equal(t, int64(1), activeMembers)
	assert.Equal(t, int64(1), dismissedEvents)

	var historicalConflicts []RelationshipConflictCaseRecord
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		var loadErr error
		historicalConflicts, loadErr = loadRelationshipConflictRecords(
			ctx,
			tx,
			teamID,
			[]string{active.RelationshipResults[0].Relationship.RelationshipID},
			&historicalKnownAt,
		)
		return loadErr
	}))
	require.Len(t, historicalConflicts, 1)
	assert.NotEqual(t, "dismissed", historicalConflicts[0].Status)
	assert.Empty(t, historicalConflicts[0].PreferredPositionID)
	assert.True(t, historicalConflicts[0].NextReviewAt.IsZero())
	assert.Nil(t, historicalConflicts[0].DismissedAt)
	require.NotEmpty(t, historicalConflicts[0].Positions)
	for _, position := range historicalConflicts[0].Positions {
		assert.Equal(t, "candidate", position.Disposition)
	}
}

func TestRelationshipConflictReviewerDoesNotExhaustAttemptsBeforeDueDate(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-review-attempts", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-review-attempts-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-attempts-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-attempts-owner-b")
	ledgerRepo := NewLedgerRepositoryWithRuntimeConfig(appDB, rls, 20, ConflictRuntimeConfig{
		ReviewTTLDays: 2,
		Timezone:      "UTC",
	})
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")
	commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-attempts-a",
		"attempts-conflict-a", "Dense-Mem uses PostgreSQL.", subject.EntityID, postgres.EntityID, "source-group-attempts-a",
	)
	commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-attempts-b",
		"attempts-conflict-b", "Dense-Mem uses GraphDB.", subject.EntityID, graphdb.EntityID, "source-group-attempts-b",
	)

	conflictID, _ := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	result := reviewConflictCaseForTest(t, ctx, ledgerRepo, teamID, "conflict-reviewer-attempts", conflictID, time.Now().UTC())
	assert.Equal(t, ConflictReviewOutcomeNoop, result.Outcome)
	assert.Equal(t, ConflictReviewStageWaitingForReviewDue, result.Stage)

	var status string
	var attempts int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status, attempts
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Row().Scan(&status, &attempts)
	}))
	assert.Equal(t, "open", status)
	assert.Equal(t, 0, attempts)
}

func TestRelationshipConflictReviewerResetsAttemptsAfterOverdueAndSnapshotVersionChange(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-review-reset-attempts", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-review-reset-attempts-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-reset-attempts-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "conflict-review-reset-attempts-owner-b")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")
	commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-reset-attempts-a",
		"reset-attempts-conflict-a", "Dense-Mem uses PostgreSQL.", subject.EntityID, postgres.EntityID, "source-group-reset-attempts-a",
	)
	commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-reset-attempts-b",
		"reset-attempts-conflict-b", "Dense-Mem uses GraphDB.", subject.EntityID, graphdb.EntityID, "source-group-reset-attempts-b",
	)

	conflictID, _ := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	reviewNow := time.Now().UTC()
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_conflict_cases
			SET review_due_at = ?,
			    next_review_at = ?,
			    attempts = 4
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, reviewNow.Add(-time.Minute), reviewNow.Add(-time.Minute), teamID, conflictID).Error
	}))

	result := reviewConflictCaseForTest(t, ctx, ledgerRepo, teamID, "conflict-reviewer-reset-attempts", conflictID, reviewNow)
	assert.Equal(t, ConflictReviewOutcomeOverdue, result.Outcome)

	var status string
	var attempts int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status, attempts
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Row().Scan(&status, &attempts)
	}))
	assert.Equal(t, "overdue", status)
	assert.Equal(t, 0, attempts)
	reservation, dossier, reserved, err := ledgerRepo.ReserveOverdueConflictAssessment(ctx, ReserveOverdueConflictAssessmentInput{
		TeamID:              teamID,
		ConflictID:          conflictID,
		ReviewRunID:         uuid.NewString(),
		WorkerID:            "worker-reset-attempts-assessment",
		LocalAssessmentDate: reviewNow,
		Model:               "test-model",
		PolicyVersion:       domain.ConflictOverduePolicyVersion,
	})
	require.NoError(t, err)
	require.True(t, reserved)
	require.NotNil(t, dossier)
	require.NotEmpty(t, dossier.Positions)
	require.NotNil(t, reservation)

	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec(`
			UPDATE relationship_conflict_cases
			SET attempts = 4,
			    lease_worker_id = 'stale-worker',
			    lease_until = now() + interval '1 minute',
			    last_error = 'stale lease'
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Error)
		return bumpRelationshipConflictCaseVersion(ctx, tx, teamID, conflictID)
	}))

	var leaseWorkerID string
	var lastError string
	var assessmentStatus string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT attempts, lease_worker_id, COALESCE(last_error, '')
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Row().Scan(&attempts, &leaseWorkerID, &lastError)
	}))
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status
			FROM relationship_conflict_ai_assessment_attempts
			WHERE team_id = ?::uuid
			  AND assessment_attempt_id = ?::uuid
		`, teamID, reservation.AssessmentAttemptID).Row().Scan(&assessmentStatus)
	}))
	assert.Equal(t, 0, attempts)
	assert.Empty(t, leaseWorkerID)
	assert.Empty(t, lastError)
	assert.Equal(t, "superseded", assessmentStatus)
}

func TestEnsureConflictSystemProfileAvoidsLegacyUserNameCollision(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-system-profile-name-collision-team")
	legacyName := conflictSystemProfileNamePrefix + teamID
	userProfileID := createLedgerProfile(t, adminDB, rls, teamID, legacyName)

	var systemProfileID string
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		if err := setConflictSystemTeamContext(ctx, tx, teamID); err != nil {
			return err
		}
		var err error
		systemProfileID, err = ensureConflictSystemProfile(ctx, tx, teamID)
		return err
	}))
	require.NotEmpty(t, systemProfileID)
	assert.NotEqual(t, userProfileID, systemProfileID)

	var systemName, authSource string
	var isSystem bool
	var ownershipAliasCount int
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		if err := setConflictSystemTeamContext(ctx, tx, teamID); err != nil {
			return err
		}
		if err := tx.Raw(`
				SELECT display_name, kind, kind = 'system'
				FROM actor_identities
				WHERE team_id = ?::uuid
				  AND id = ?::uuid
		`, teamID, systemProfileID).Row().Scan(&systemName, &authSource, &isSystem); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT COUNT(*)
			FROM ownership_aliases
			WHERE team_id = ?::uuid
			  AND legacy_owner_id = ?::uuid
		`, teamID, systemProfileID).Row().Scan(&ownershipAliasCount)
	}))
	assert.NotEqual(t, legacyName, systemName)
	assert.True(t, strings.HasPrefix(systemName, conflictSystemProfileNamePrefix))
	assert.Equal(t, "system", authSource)
	assert.True(t, isSystem)
	assert.Equal(t, 1, ownershipAliasCount)

	unscopedTeamID := createLedgerTeam(t, adminDB, rls, "conflict-system-profile-unscoped-team")
	err := rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO actor_identities (id, kind, team_id, display_name, active)
			VALUES (gen_random_uuid(), 'system', ?::uuid, ?, false)
		`, unscopedTeamID, newConflictSystemProfileName()).Error
	})
	require.Error(t, err)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "42501", pgErr.Code)
	assert.Contains(t, pgErr.Message, "row-level security policy")
}

func TestOverdueConflictResolutionRetiresLosingEvidenceAndStagesDeletionOnlyDerivation(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "overdue-conflict-resolution", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "overdue-conflict-resolution-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "overdue-conflict-resolution-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "overdue-conflict-resolution-owner-b")
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
		t, ctx, ledgerRepo, teamID, ownerA, "worker-overdue-preferred",
		"overdue-conflict-preferred", preferredContent, subject.EntityID, postgres.EntityID, "source-group-overdue-preferred",
		conflictTestRelationshipOptions{
			authority: "primary",
			additionalSupports: []conflictTestAdditionalSupport{{
				sourceGroupKey: "source-group-overdue-preferred-second",
				spanStart:      preferredSecondStart,
				spanEnd:        preferredSecondStart + len(preferredQuote),
				quote:          preferredQuote,
			}},
		},
	)
	loserContent := "Dense-Mem uses GraphDB. Dense-Mem uses GraphDB."
	loserQuote := "Dense-Mem uses GraphDB."
	loserSecondStart := strings.LastIndex(loserContent, loserQuote)
	require.Greater(t, loserSecondStart, 0)
	loser := commitPlacementRelationshipForConflictTestWithOptions(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-overdue-loser",
		"overdue-conflict-loser", loserContent, subject.EntityID, graphdb.EntityID, "source-group-overdue-loser",
		conflictTestRelationshipOptions{
			authority: "primary",
			additionalSupports: []conflictTestAdditionalSupport{{
				sourceGroupKey: "source-group-overdue-loser-second",
				spanStart:      loserSecondStart,
				spanEnd:        loserSecondStart + len(loserQuote),
				quote:          loserQuote,
			}},
		},
	)

	conflictID, _ := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	reviewNow := time.Now().UTC()
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_conflict_cases
			SET review_due_at = ?,
			    next_review_at = ?
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, reviewNow.Add(-time.Minute), reviewNow.Add(-time.Minute), teamID, conflictID).Error
	}))
	result := reviewConflictCaseForTest(t, ctx, ledgerRepo, teamID, "worker-overdue-review", conflictID, reviewNow)
	require.Equal(t, ConflictReviewOutcomeOverdue, result.Outcome)

	localDate := time.Date(2026, time.August, 2, 0, 30, 0, 0, time.FixedZone("UTC+14", 14*60*60))
	reservation, dossier, reserved, err := ledgerRepo.ReserveOverdueConflictAssessment(ctx, ReserveOverdueConflictAssessmentInput{
		TeamID:              teamID,
		ConflictID:          conflictID,
		ReviewRunID:         uuid.NewString(),
		WorkerID:            "worker-overdue-assessment",
		LocalAssessmentDate: localDate,
		Model:               "test-model",
		PolicyVersion:       domain.ConflictOverduePolicyVersion,
	})
	require.NoError(t, err)
	require.True(t, reserved)
	require.NotNil(t, reservation)
	require.NotNil(t, dossier)
	assert.Len(t, dossier.Positions, 2)
	assert.Len(t, dossier.Evidence, 4)
	assert.Len(t, dossier.Positions[0].Supports, 2)
	assert.Len(t, dossier.Positions[1].Supports, 2)
	var storedLocalAssessmentDate string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT local_assessment_date::text
			FROM relationship_conflict_ai_assessment_attempts
			WHERE team_id = ?::uuid
			  AND assessment_attempt_id = ?::uuid
		`, teamID, reservation.AssessmentAttemptID).Row().Scan(&storedLocalAssessmentDate)
	}))
	assert.Equal(t, "2026-08-02", storedLocalAssessmentDate)

	_, _, reservedAgain, err := ledgerRepo.ReserveOverdueConflictAssessment(ctx, ReserveOverdueConflictAssessmentInput{
		TeamID:              teamID,
		ConflictID:          conflictID,
		ReviewRunID:         uuid.NewString(),
		WorkerID:            "worker-overdue-assessment-retry",
		LocalAssessmentDate: localDate,
		Model:               "test-model",
		PolicyVersion:       domain.ConflictOverduePolicyVersion,
	})
	require.NoError(t, err)
	assert.False(t, reservedAgain)

	preferredPositionID, losingFragmentID := "", ""
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT member.position_id::text
			FROM relationship_conflict_position_members AS member
			WHERE member.team_id = ?::uuid
			  AND member.conflict_id = ?::uuid
			  AND member.relationship_id = ?::uuid
			  AND member.active
		`, teamID, conflictID, preferred.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&preferredPositionID))
		return tx.Raw(`
			SELECT support.fragment_id::text
			FROM relationship_evidence_supports AS support
			WHERE support.team_id = ?::uuid
			  AND support.relationship_id = ?::uuid
			  AND support.owner_profile_id = ?::uuid
		`, teamID, loser.RelationshipResults[0].Relationship.RelationshipID, ownerB).Row().Scan(&losingFragmentID)
	}))
	require.NotEmpty(t, preferredPositionID)
	require.NotEmpty(t, losingFragmentID)
	confidence := 0.92
	_, err = ledgerRepo.CompleteOverdueConflictAssessment(ctx, CompleteOverdueConflictAssessmentInput{
		TeamID:              teamID,
		ConflictID:          conflictID,
		AssessmentAttemptID: reservation.AssessmentAttemptID,
		CaseVersion:         reservation.CaseVersion,
		ReviewRunID:         uuid.NewString(),
		Decision:            "selected",
		SelectedPositionID:  preferredPositionID,
		Confidence:          &confidence,
		ProviderTurns:       1,
		ResponseHash:        "sha256:test",
	})
	require.NoError(t, err)
	applied := commitOverdueConflictResolutionWithVectors(t, ctx, ledgerRepo, ApplyOverdueConflictResolutionInput{
		TeamID:              teamID,
		ConflictID:          conflictID,
		ReviewRunID:         uuid.NewString(),
		WorkerID:            "worker-overdue-apply",
		ExpectedCaseVersion: reservation.CaseVersion,
		PreferredPositionID: preferredPositionID,
		AssessmentAttemptID: reservation.AssessmentAttemptID,
		Method:              "ai",
		Now:                 reviewNow,
	})
	require.True(t, applied.Resolved)
	assert.Contains(t, applied.RetractedEvidenceIDs, losingFragmentID)
	require.Len(t, applied.DerivedEvidence, 1)
	assert.NotEmpty(t, applied.DerivedEvidence[0].TaskID)

	claimedDerived, err := ledgerRepo.ClaimConflictDerivedEvidenceTasks(ctx, ClaimConflictDerivedEvidenceTasksInput{
		TeamID:      teamID,
		ReviewRunID: uuid.NewString(),
		WorkerID:    "worker-overdue-derived-first",
		Limit:       1,
		Lease:       time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimedDerived, 1)
	assert.Equal(t, applied.DerivedEvidence[0].TaskID, claimedDerived[0].TaskID)
	require.NoError(t, ledgerRepo.RecordConflictDerivedEvidenceFailure(ctx, claimedDerived[0], "staging_failed"))

	claimedDerived, err = ledgerRepo.ClaimConflictDerivedEvidenceTasks(ctx, ClaimConflictDerivedEvidenceTasksInput{
		TeamID:      teamID,
		ReviewRunID: uuid.NewString(),
		WorkerID:    "worker-overdue-derived-retry",
		Limit:       1,
		Lease:       time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimedDerived, 1)
	derived, err := ledgerRepo.StageConflictDerivedEvidence(ctx, claimedDerived[0])
	require.NoError(t, err)
	require.NotEmpty(t, derived.IngestID)
	require.NotEmpty(t, derived.ReplacementFragment)

	var conflictStatus, resolutionReason, loserStatus, searchState, systemProfileID, authSource, replacementAuthority, derivedContractVersion string
	var loserSupportCount, derivationCount, replacementSearchDocuments, relationshipCountBeforeDerived, derivedTaskAttempts int64
	var derivedTaskStatus string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT status, resolution_reason
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Row().Scan(&conflictStatus, &resolutionReason))
		require.NoError(t, tx.Raw(`
			SELECT status, support_count
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, loser.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&loserStatus, &loserSupportCount))
		require.NoError(t, tx.Raw(`
			SELECT search_state
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND source_kind = 'evidence'
			  AND source_id = ?::uuid
		`, teamID, losingFragmentID).Row().Scan(&searchState))
		require.NoError(t, tx.Raw(`
			SELECT id::text, kind
			FROM actor_identities
			WHERE team_id = ?::uuid
			  AND kind = 'system'
		`, teamID).Row().Scan(&systemProfileID, &authSource))
		require.NoError(t, tx.Raw(`
			SELECT fragment.authority
			FROM relationship_conflict_evidence_derivations AS derivation
			JOIN evidence_fragments AS fragment
			  ON fragment.team_id = derivation.team_id
			 AND fragment.fragment_id = derivation.replacement_fragment_id
			WHERE derivation.team_id = ?::uuid
			  AND derivation.conflict_id = ?::uuid
			  AND derivation.target_fragment_id = ?::uuid
		`, teamID, conflictID, losingFragmentID).Row().Scan(&replacementAuthority))
		require.NoError(t, tx.Raw(`
			SELECT count(*)
			FROM relationship_conflict_evidence_derivations
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Scan(&derivationCount).Error)
		require.NoError(t, tx.Raw(`
			SELECT status, attempts
			FROM relationship_conflict_derived_evidence_tasks
			WHERE team_id = ?::uuid
			  AND derived_evidence_task_id = ?::uuid
		`, teamID, applied.DerivedEvidence[0].TaskID).Row().Scan(&derivedTaskStatus, &derivedTaskAttempts))
		require.NoError(t, tx.Raw(`
			SELECT count(*)
			FROM relationship_records
			WHERE team_id = ?::uuid
		`, teamID).Scan(&relationshipCountBeforeDerived).Error)
		err := tx.Raw(`
			SELECT count(*)
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND source_kind = 'evidence'
			  AND source_id = ?::uuid
		`, teamID, derived.ReplacementFragment).Scan(&replacementSearchDocuments).Error
		if err != nil {
			return err
		}
		return tx.Raw(`
			SELECT metadata ->> 'contract_version'
			FROM knowledge_ingests
			WHERE team_id = ?::uuid
			  AND ingest_id = ?::uuid
		`, teamID, derived.IngestID).Row().Scan(&derivedContractVersion)
	}))
	assert.Equal(t, "resolved", conflictStatus)
	assert.Equal(t, domain.ConflictResolutionReasonAI, resolutionReason)
	assert.Equal(t, "superseded", loserStatus)
	assert.Equal(t, int64(0), loserSupportCount)
	assert.Equal(t, "not_required", searchState)
	assert.NotEmpty(t, systemProfileID)
	assert.Equal(t, "system", authSource)
	assert.Equal(t, "inferred", replacementAuthority)
	assert.Equal(t, domain.ContractVersion, derivedContractVersion)
	assert.Equal(t, int64(1), derivationCount)
	assert.Equal(t, "completed", derivedTaskStatus)
	assert.Equal(t, int64(2), derivedTaskAttempts)
	assert.Equal(t, int64(0), replacementSearchDocuments)

	derivedIngest, err := ledgerRepo.GetPlacementRun(ctx, GetPlacementRunInput{
		TeamID:         teamID,
		OwnerProfileID: systemProfileID,
		IngestID:       derived.IngestID,
	})
	require.NoError(t, err)
	require.Len(t, derivedIngest.Items, 1)
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-overdue-derived", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, derivedIngest.PlacementRunID, claimed.PlacementRunID)
	derivedCommit, err := commitAcceptedSubmissionFixture(t, ctx, ledgerRepo, CommitPlacementSemanticInput{
		TeamID:           teamID,
		OwnerProfileID:   systemProfileID,
		IngestID:         derivedIngest.IngestID,
		PlacementRunID:   derivedIngest.PlacementRunID,
		PlacementItemID:  derivedIngest.Items[0].PlacementItemID,
		WorkerID:         "worker-overdue-derived",
		ExpectedAttempts: claimed.Attempts,
		EntityResolutions: []PlacementEntityResolutionInput{
			{MentionRef: "subject", Action: "reuse", EntityID: subject.EntityID},
			{MentionRef: "object", Action: "reuse", EntityID: postgres.EntityID},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, derivedCommit.RelationshipResults)

	var relationshipCountAfterDerived, derivedSearchDocuments int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT count(*)
			FROM relationship_records
			WHERE team_id = ?::uuid
		`, teamID).Scan(&relationshipCountAfterDerived).Error)
		return tx.Raw(`
			SELECT count(*)
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND source_kind = 'evidence'
			  AND source_id = ?::uuid
		`, teamID, derived.ReplacementFragment).Scan(&derivedSearchDocuments).Error
	}))
	assert.Equal(t, relationshipCountBeforeDerived, relationshipCountAfterDerived)
	assert.Equal(t, int64(0), derivedSearchDocuments)
}

func TestOverdueConflictResolutionRejectsAssessmentAfterEvidenceRetraction(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "overdue-conflict-stale-evidence", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "overdue-conflict-stale-evidence-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "overdue-conflict-stale-evidence-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "overdue-conflict-stale-evidence-owner-b")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")
	preferred := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-overdue-stale-preferred",
		"overdue-stale-preferred", "Dense-Mem uses PostgreSQL.", subject.EntityID, postgres.EntityID, "source-group-overdue-stale-preferred",
	)
	loser := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-overdue-stale-loser",
		"overdue-stale-loser", "Dense-Mem uses GraphDB.", subject.EntityID, graphdb.EntityID, "source-group-overdue-stale-loser",
	)

	conflictID, _ := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	reviewNow := time.Now().UTC()
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_conflict_cases
			SET review_due_at = ?,
			    next_review_at = ?
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, reviewNow.Add(-time.Minute), reviewNow.Add(-time.Minute), teamID, conflictID).Error
	}))
	review := reviewConflictCaseForTest(t, ctx, ledgerRepo, teamID, "worker-overdue-stale-review", conflictID, reviewNow)
	require.Equal(t, ConflictReviewOutcomeOverdue, review.Outcome)

	reservation, dossier, reserved, err := ledgerRepo.ReserveOverdueConflictAssessment(ctx, ReserveOverdueConflictAssessmentInput{
		TeamID:              teamID,
		ConflictID:          conflictID,
		ReviewRunID:         uuid.NewString(),
		WorkerID:            "worker-overdue-stale-assessment",
		LocalAssessmentDate: reviewNow,
		Model:               "test-model",
		PolicyVersion:       domain.ConflictOverduePolicyVersion,
	})
	require.NoError(t, err)
	require.True(t, reserved)
	require.NotNil(t, dossier)
	require.NotEmpty(t, dossier.Positions)
	require.NotNil(t, reservation)

	preferredPositionID, losingFragmentID := "", ""
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT member.position_id::text
			FROM relationship_conflict_position_members AS member
			WHERE member.team_id = ?::uuid
			  AND member.conflict_id = ?::uuid
			  AND member.relationship_id = ?::uuid
			  AND member.active
		`, teamID, conflictID, preferred.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&preferredPositionID))
		return tx.Raw(`
			SELECT support.fragment_id::text
			FROM relationship_evidence_supports AS support
			WHERE support.team_id = ?::uuid
			  AND support.relationship_id = ?::uuid
			  AND support.owner_profile_id = ?::uuid
		`, teamID, loser.RelationshipResults[0].Relationship.RelationshipID, ownerB).Row().Scan(&losingFragmentID)
	}))
	confidence := 0.92
	_, err = ledgerRepo.CompleteOverdueConflictAssessment(ctx, CompleteOverdueConflictAssessmentInput{
		TeamID:              teamID,
		ConflictID:          conflictID,
		AssessmentAttemptID: reservation.AssessmentAttemptID,
		CaseVersion:         reservation.CaseVersion,
		ReviewRunID:         uuid.NewString(),
		Decision:            "selected",
		SelectedPositionID:  preferredPositionID,
		Confidence:          &confidence,
		ProviderTurns:       1,
		ResponseHash:        "sha256:stale-evidence",
	})
	require.NoError(t, err)

	_, err = ledgerRepo.RetractEvidence(ctx, RetractEvidenceInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		EvidenceIDs:    []string{losingFragmentID},
		Reason:         "source was withdrawn while assessment was in flight",
		IdempotencyKey: "overdue-stale-evidence-retract",
		RequestHash:    "sha256:overdue-stale-evidence-retract",
	})
	require.NoError(t, err)

	applied := commitOverdueConflictResolutionWithVectors(t, ctx, ledgerRepo, ApplyOverdueConflictResolutionInput{
		TeamID:              teamID,
		ConflictID:          conflictID,
		ReviewRunID:         uuid.NewString(),
		WorkerID:            "worker-overdue-stale-apply",
		ExpectedCaseVersion: reservation.CaseVersion,
		PreferredPositionID: preferredPositionID,
		AssessmentAttemptID: reservation.AssessmentAttemptID,
		Method:              "ai",
		Now:                 reviewNow,
	})
	assert.True(t, applied.Stale)
	assert.False(t, applied.Resolved)
	assert.Empty(t, applied.RetractedEvidenceIDs)

	var conflictStatus, preferredStatus, loserStatus string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT status
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, teamID, conflictID).Row().Scan(&conflictStatus))
		require.NoError(t, tx.Raw(`
			SELECT status
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, preferred.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&preferredStatus))
		return tx.Raw(`
			SELECT status
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, loser.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&loserStatus)
	}))
	assert.Equal(t, "dismissed", conflictStatus)
	assert.Equal(t, "active", preferredStatus)
	assert.Equal(t, "pending_evidence", loserStatus)
}

func TestReserveOverdueConflictAssessmentExpiresAbandonedAttemptIntoLastWriteWins(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "overdue-conflict-abandoned", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "overdue-conflict-abandoned-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "overdue-conflict-abandoned-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "overdue-conflict-abandoned-owner-b")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")
	commitPlacementRelationshipForConflictTestWithOptions(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-overdue-abandoned-preferred",
		"overdue-conflict-abandoned-preferred", "Dense-Mem uses PostgreSQL.", subject.EntityID, postgres.EntityID, "source-group-overdue-abandoned-preferred",
		conflictTestRelationshipOptions{authority: "authoritative"},
	)
	commitPlacementRelationshipForConflictTestWithOptions(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-overdue-abandoned-loser",
		"overdue-conflict-abandoned-loser", "Dense-Mem uses GraphDB.", subject.EntityID, graphdb.EntityID, "source-group-overdue-abandoned-loser",
		conflictTestRelationshipOptions{authority: "primary"},
	)

	conflictID, _ := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	reviewNow := time.Now().UTC()
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_conflict_cases
			SET review_due_at = ?,
			    next_review_at = ?
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
		`, reviewNow.Add(-time.Minute), reviewNow.Add(-time.Minute), teamID, conflictID).Error
	}))
	result := reviewConflictCaseForTest(t, ctx, ledgerRepo, teamID, "worker-overdue-abandoned-review", conflictID, reviewNow)
	require.Equal(t, ConflictReviewOutcomeOverdue, result.Outcome)

	firstLocalDate := time.Date(2026, time.August, 2, 0, 30, 0, 0, time.FixedZone("UTC+14", 14*60*60))
	for day := 0; day < ConflictAssessmentMaxFailedDays-1; day++ {
		reservation, _, reserved, err := ledgerRepo.ReserveOverdueConflictAssessment(ctx, ReserveOverdueConflictAssessmentInput{
			TeamID:              teamID,
			ConflictID:          conflictID,
			ReviewRunID:         uuid.NewString(),
			WorkerID:            "worker-overdue-abandoned-assessment",
			LocalAssessmentDate: firstLocalDate.AddDate(0, 0, day),
			Model:               "test-model",
			PolicyVersion:       domain.ConflictOverduePolicyVersion,
		})
		require.NoError(t, err)
		require.True(t, reserved)
		require.NotNil(t, reservation)
		completed, err := ledgerRepo.CompleteOverdueConflictAssessment(ctx, CompleteOverdueConflictAssessmentInput{
			TeamID:              teamID,
			ConflictID:          conflictID,
			AssessmentAttemptID: reservation.AssessmentAttemptID,
			CaseVersion:         reservation.CaseVersion,
			ReviewRunID:         uuid.NewString(),
			Decision:            "failed",
			FailureClass:        "provider_unavailable",
		})
		require.NoError(t, err)
		assert.Equal(t, day+1, completed.FailureCount)
	}

	abandoned, _, reserved, err := ledgerRepo.ReserveOverdueConflictAssessment(ctx, ReserveOverdueConflictAssessmentInput{
		TeamID:              teamID,
		ConflictID:          conflictID,
		ReviewRunID:         uuid.NewString(),
		WorkerID:            "worker-overdue-abandoned-assessment",
		LocalAssessmentDate: firstLocalDate.AddDate(0, 0, ConflictAssessmentMaxFailedDays-1),
		Model:               "test-model",
		PolicyVersion:       domain.ConflictOverduePolicyVersion,
	})
	require.NoError(t, err)
	require.True(t, reserved)
	require.NotNil(t, abandoned)

	fallback, dossier, reserved, err := ledgerRepo.ReserveOverdueConflictAssessment(ctx, ReserveOverdueConflictAssessmentInput{
		TeamID:              teamID,
		ConflictID:          conflictID,
		ReviewRunID:         uuid.NewString(),
		WorkerID:            "worker-overdue-abandoned-recovery",
		LocalAssessmentDate: firstLocalDate.AddDate(0, 0, ConflictAssessmentMaxFailedDays),
		Model:               "test-model",
		PolicyVersion:       domain.ConflictOverduePolicyVersion,
	})
	require.NoError(t, err)
	require.True(t, reserved)
	require.NotNil(t, fallback)
	require.NotNil(t, dossier)
	assert.True(t, fallback.LastWriteWins)
	assert.Equal(t, abandoned.AssessmentAttemptID, fallback.AssessmentAttemptID)

	positions := make([]domain.ConflictResolutionPosition, 0, len(dossier.Positions))
	for _, position := range dossier.Positions {
		positions = append(positions, domain.ConflictResolutionPosition{
			PositionID: position.PositionID,
			Supports:   position.Supports,
		})
	}
	winner, ok := domain.SelectConflictLastWriteWinner(positions)
	require.True(t, ok)
	assert.Equal(t, "authoritative", winner.Authority)
	applied := commitOverdueConflictResolutionWithVectors(t, ctx, ledgerRepo, ApplyOverdueConflictResolutionInput{
		TeamID:              teamID,
		ConflictID:          conflictID,
		ReviewRunID:         uuid.NewString(),
		WorkerID:            "worker-overdue-abandoned-apply",
		ExpectedCaseVersion: fallback.CaseVersion,
		PreferredPositionID: winner.PositionID,
		AssessmentAttemptID: fallback.AssessmentAttemptID,
		Method:              "last_write_wins",
		Now:                 reviewNow.AddDate(0, 0, ConflictAssessmentMaxFailedDays),
	})
	assert.True(t, applied.Resolved)
}

func reviewConflictCaseForTest(
	t *testing.T,
	ctx context.Context,
	repo *LedgerRepositoryImpl,
	teamID string,
	workerID string,
	conflictID string,
	reviewNow time.Time,
) *ReviewRelationshipConflictCaseResult {
	t.Helper()
	run, claimed, err := repo.ReserveRelationshipConflictReviewRun(ctx, ConflictReviewRunInput{
		TeamID:       teamID,
		WorkerID:     workerID,
		LocalRunDate: reviewNow,
		Timezone:     "UTC",
		Lease:        time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, run)
	cases, err := repo.ClaimRelationshipConflictCases(ctx, ClaimRelationshipConflictCasesInput{
		TeamID:      teamID,
		WorkerID:    workerID,
		ReviewRunID: run.ReviewRunID,
		Limit:       10,
		Lease:       time.Minute,
		MaxAttempts: 5,
		Now:         reviewNow,
	})
	require.NoError(t, err)
	require.Len(t, cases, 1)
	assert.Equal(t, conflictID, cases[0].ConflictID)
	result, err := repo.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{
		TeamID:      teamID,
		WorkerID:    workerID,
		ReviewRunID: run.ReviewRunID,
		ConflictID:  conflictID,
		Now:         reviewNow,
	})
	require.NoError(t, err)
	return result
}
