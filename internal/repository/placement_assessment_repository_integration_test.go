package repository

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPlacementAssessmentIsAppendOnceClaimBoundAndOwnerScoped(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-assessment-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "owner-b")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSemanticIngest(t, ctx, repo, teamID, ownerA, "placement-assessment-first", "An assessment is persisted before commit.")
	item := ingest.Items[0]
	require.NotEmpty(t, item.ClaimKey)

	firstInput := placementAssessmentPersistInput(teamID, ownerA, item)
	missingModel := firstInput
	missingModel.Model = ""
	_, _, err := repo.PersistPlacementAssessment(ctx, missingModel)
	require.EqualError(t, err, "model is required")

	first, existing, err := repo.PersistPlacementAssessment(ctx, firstInput)
	require.NoError(t, err)
	assert.False(t, existing)
	require.NotEmpty(t, first.AssessmentID)
	assert.Equal(t, firstInput.ResponseHash, first.ResponseHash)

	replacement := firstInput
	replacement.Model = "replacement-model"
	replacement.ResponseHash = "sha256:replacement"
	replacement.NormalizedResponse = json.RawMessage(`{"request_id":"replacement","security_signals":[],"entity_results":[],"relationship_results":[]}`)
	winner, existing, err := repo.PersistPlacementAssessment(ctx, replacement)
	require.NoError(t, err)
	assert.True(t, existing)
	assert.Equal(t, first.AssessmentID, winner.AssessmentID)
	assert.Equal(t, first.ResponseHash, winner.ResponseHash)

	loaded, err := repo.LoadPlacementAssessment(ctx, LoadPlacementAssessmentInput{
		TeamID: teamID, OwnerProfileID: ownerA, PlacementItemID: item.PlacementItemID,
	})
	require.NoError(t, err)
	assert.Equal(t, first.AssessmentID, loaded.AssessmentID)
	assert.Equal(t, firstInput.ResponseHash, loaded.ResponseHash)

	_, err = repo.LoadPlacementAssessment(ctx, LoadPlacementAssessmentInput{
		TeamID: teamID, OwnerProfileID: ownerB, PlacementItemID: item.PlacementItemID,
	})
	require.ErrorIs(t, err, ErrPlacementAssessmentNotFound)

	second := createSemanticIngest(t, ctx, repo, teamID, ownerA, "placement-assessment-second", "A claim key cannot be substituted.")
	wrongClaim := placementAssessmentPersistInput(teamID, ownerA, second.Items[0])
	wrongClaim.ClaimKey = uuid.NewString()
	_, _, err = repo.PersistPlacementAssessment(ctx, wrongClaim)
	require.ErrorIs(t, err, ErrPlacementAssessmentClaimMismatch)

	err = rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE placement_assessments
			SET model = 'mutated'
			WHERE team_id = ?::uuid
			  AND assessment_id = ?::uuid
		`, teamID, first.AssessmentID).Error
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "append-only")
}

func TestPlacementAssessmentConcurrentPersistenceConvergesOnOneAssessment(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-assessment-concurrent-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSemanticIngest(t, ctx, repo, teamID, ownerID, "placement-assessment-concurrent", "Concurrent persistence converges on one assessment.")
	input := placementAssessmentPersistInput(teamID, ownerID, ingest.Items[0])
	inputs := []PersistPlacementAssessmentInput{input, input}
	inputs[1].ResponseHash = "sha256:concurrent-second"
	inputs[1].NormalizedResponse = json.RawMessage(`{"request_id":"second","security_signals":[],"entity_results":[],"relationship_results":[]}`)

	type persistResult struct {
		assessment *PlacementAssessment
		existing   bool
		err        error
	}
	results := make(chan persistResult, len(inputs))
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(len(inputs))
	for _, persistInput := range inputs {
		go func(value PersistPlacementAssessmentInput) {
			defer workers.Done()
			<-start
			assessment, existing, persistErr := repo.PersistPlacementAssessment(ctx, value)
			results <- persistResult{assessment: assessment, existing: existing, err: persistErr}
		}(persistInput)
	}
	close(start)
	workers.Wait()
	close(results)

	var persisted, reused *PlacementAssessment
	for result := range results {
		require.NoError(t, result.err)
		require.NotNil(t, result.assessment)
		if result.existing {
			reused = result.assessment
		} else {
			persisted = result.assessment
		}
	}
	require.NotNil(t, persisted)
	require.NotNil(t, reused)
	assert.Equal(t, persisted.AssessmentID, reused.AssessmentID)
	assert.Equal(t, persisted.ResponseHash, reused.ResponseHash)

	loaded, err := repo.LoadPlacementAssessment(ctx, LoadPlacementAssessmentInput{
		TeamID: teamID, OwnerProfileID: ownerID, PlacementItemID: ingest.Items[0].PlacementItemID,
	})
	require.NoError(t, err)
	assert.Equal(t, persisted.AssessmentID, loaded.AssessmentID)
	assert.Equal(t, persisted.ResponseHash, loaded.ResponseHash)
}

func TestPlacementAssessmentProviderAttemptReservationIsSingleAndLeaseBound(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-assessment-reservation-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSemanticIngest(t, ctx, repo, teamID, ownerID, "placement-assessment-reservation", "One placement claim has one provider attempt.")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "assessment-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	input := ReservePlacementAssessmentProviderAttemptInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "assessment-worker",
		ExpectedAttempts: claimed.Attempts,
	}
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	var started sync.WaitGroup
	started.Add(2)
	for range 2 {
		go func() {
			started.Done()
			reserved, reserveErr := repo.ReservePlacementAssessmentProviderAttempt(ctx, input)
			results <- reserved
			errs <- reserveErr
		}()
	}
	started.Wait()
	var reservedCount int
	for range 2 {
		require.NoError(t, <-errs)
		if <-results {
			reservedCount++
		}
	}
	assert.Equal(t, 1, reservedCount)

	var attemptID string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT assessor_attempt_id::text
			FROM placement_items
			WHERE team_id = ?::uuid AND placement_item_id = ?::uuid
		`, teamID, ingest.Items[0].PlacementItemID).Row().Scan(&attemptID)
	}))
	assert.NotEmpty(t, attemptID)

	_, err = repo.ReservePlacementAssessmentProviderAttempt(ctx, ReservePlacementAssessmentProviderAttemptInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "different-worker",
		ExpectedAttempts: claimed.Attempts,
	})
	require.NoError(t, err)

	_, err = repo.RequeuePlacementReviewResult(ctx, RequeuePlacementReviewInput{
		TeamID:                 teamID,
		OwnerProfileID:         ownerID,
		IngestID:               ingest.IngestID,
		PlacementRunID:         ingest.PlacementRunID,
		PlacementItemID:        ingest.Items[0].PlacementItemID,
		WorkerID:               "assessment-worker",
		ExpectedAttempts:       claimed.Attempts,
		OutcomeKind:            "semantic_assessment_attempt",
		Payload:                map[string]any{"assessor_provider_attempted": true},
		ReleaseAssessorAttempt: true,
	})
	require.NoError(t, err)

	var releasedAttemptID string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT COALESCE(assessor_attempt_id::text, '')
			FROM placement_items
			WHERE team_id = ?::uuid AND placement_item_id = ?::uuid
		`, teamID, ingest.Items[0].PlacementItemID).Row().Scan(&releasedAttemptID); err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE placement_runs
			SET available_at = now()
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Error
	}))
	assert.Empty(t, releasedAttemptID, "a known failed request releases its claim reservation")

	reclaimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "assessment-worker-retry", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, reclaimed)
	reserved, err := repo.ReservePlacementAssessmentProviderAttempt(ctx, ReservePlacementAssessmentProviderAttemptInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "assessment-worker-retry",
		ExpectedAttempts: reclaimed.Attempts,
	})
	require.NoError(t, err)
	assert.True(t, reserved, "a later claim can make one request when no valid assessment exists")
}

func TestPlacementAssessmentPolicyReloadsTeamThresholdAndConfigVersion(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-assessment-policy-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)

	initial, err := ledgerRepo.LoadAutoWriteConfidencePolicy(ctx, LoadAutoWriteConfidencePolicyInput{
		TeamID: teamID, OwnerProfileID: ownerID, GlobalThreshold: 0.7,
	})
	require.NoError(t, err)
	assert.Equal(t, 0.7, initial.Threshold)
	assert.Equal(t, "global", initial.Source)
	assert.Equal(t, int64(1), initial.ConfigVersion)

	profileRepo := NewProfileRepository(appDB, rls)
	team, err := profileRepo.GetByID(ctx, uuid.MustParse(teamID))
	require.NoError(t, err)
	team.Config = map[string]any{
		"memory_write": map[string]any{
			"auto_write_confidence_threshold": 0.83,
		},
	}
	require.NoError(t, profileRepo.Update(ctx, team))

	updated, err := ledgerRepo.LoadAutoWriteConfidencePolicy(ctx, LoadAutoWriteConfidencePolicyInput{
		TeamID: teamID, OwnerProfileID: ownerID, GlobalThreshold: 0.7,
	})
	require.NoError(t, err)
	assert.Equal(t, 0.83, updated.Threshold)
	assert.Equal(t, "team", updated.Source)
	assert.Equal(t, initial.ConfigVersion+1, updated.ConfigVersion)
	assert.Equal(t, AssessmentPolicyVersion, updated.Version)
}

func TestPlacementAssessmentReviewExpiryChangesOnlyWorkflowState(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-assessment-expiry-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSemanticIngest(t, ctx, repo, teamID, ownerID, "placement-assessment-expiry", "Review expiry must not grant support.")
	item := ingest.Items[0]
	assessment, _, err := repo.PersistPlacementAssessment(ctx, placementAssessmentPersistInput(teamID, ownerID, item))
	require.NoError(t, err)

	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE placement_runs
			SET status = 'awaiting_review', completed_at = now()
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE placement_items
			SET status = 'awaiting_review', category = 'candidate'
			WHERE team_id = ?::uuid AND placement_item_id = ?::uuid
		`, teamID, item.PlacementItemID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO review_tasks (
			    team_id, owner_profile_id, ingest_id, placement_item_id,
			    task_type, status, reason, payload, dedupe_key,
			    assessment_id, expires_at
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    'relationship_needs_review', 'open', 'support_confidence',
			    '{"semantic_kind":"support_confidence","question":"Provide evidence.","options":[{"action":"submit_new_evidence"}],"guidance":"Submit exact evidence."}'::jsonb,
			    '', ?::uuid, now() - interval '1 minute'
			)
		`, teamID, ownerID, ingest.IngestID, item.PlacementItemID, assessment.AssessmentID).Error
	}))

	expired, err := repo.ExpirePlacementAssessmentReviews(ctx, ExpirePlacementAssessmentReviewsInput{
		TeamID: teamID,
		Now:    time.Now().UTC(),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), expired)

	var taskStatus, itemStatus, runStatus string
	var taskVersion int
	var verificationCount, supportCount int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT status, version
			FROM review_tasks
			WHERE team_id = ?::uuid AND assessment_id = ?::uuid
		`, teamID, assessment.AssessmentID).Row().Scan(&taskStatus, &taskVersion); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status FROM placement_items
			WHERE team_id = ?::uuid AND placement_item_id = ?::uuid
		`, teamID, item.PlacementItemID).Row().Scan(&itemStatus); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status FROM placement_runs
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Row().Scan(&runStatus); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT count(*) FROM verification_events WHERE team_id = ?::uuid`, teamID).Row().Scan(&verificationCount); err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*) FROM relationship_evidence_supports WHERE team_id = ?::uuid`, teamID).Row().Scan(&supportCount)
	}))
	assert.Equal(t, "expired", taskStatus)
	assert.Equal(t, 2, taskVersion)
	assert.Equal(t, "completed", itemStatus)
	assert.Equal(t, "completed", runStatus)
	assert.Zero(t, verificationCount)
	assert.Zero(t, supportCount)
}

func TestPlacementAssessmentReviewExpiryUsesInclusiveBoundaryAndIsIdempotent(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-assessment-expiry-boundary-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSemanticIngest(t, ctx, repo, teamID, ownerID, "placement-assessment-expiry-boundary", "Expiry has an inclusive, deterministic boundary.")
	item := ingest.Items[0]
	assessment, _, err := repo.PersistPlacementAssessment(ctx, placementAssessmentPersistInput(teamID, ownerID, item))
	require.NoError(t, err)
	boundary := time.Date(2040, time.January, 2, 3, 4, 5, 0, time.UTC)

	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE placement_runs
			SET status = 'awaiting_review', completed_at = now()
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE placement_items
			SET status = 'awaiting_review', category = 'candidate'
			WHERE team_id = ?::uuid AND placement_item_id = ?::uuid
		`, teamID, item.PlacementItemID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO review_tasks (
			    team_id, owner_profile_id, ingest_id, placement_item_id,
			    task_type, status, reason, payload, dedupe_key,
			    assessment_id, expires_at
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    'relationship_needs_review', 'open', 'support_confidence',
			    '{"semantic_kind":"support_confidence","question":"Provide evidence.","options":[{"action":"submit_new_evidence"}],"guidance":"Submit exact evidence."}'::jsonb,
			    '', ?::uuid, ?
			)
		`, teamID, ownerID, ingest.IngestID, item.PlacementItemID, assessment.AssessmentID, boundary).Error
	}))

	expired, err := repo.ExpirePlacementAssessmentReviews(ctx, ExpirePlacementAssessmentReviewsInput{
		TeamID: teamID,
		Now:    boundary.Add(-time.Nanosecond),
	})
	require.NoError(t, err)
	assert.Zero(t, expired, "the task remains open immediately before its expiry")

	type expiryResult struct {
		count int64
		err   error
	}
	results := make(chan expiryResult, 2)
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	for range 2 {
		go func() {
			defer workers.Done()
			<-start
			count, expireErr := repo.ExpirePlacementAssessmentReviews(ctx, ExpirePlacementAssessmentReviewsInput{
				TeamID: teamID,
				Now:    boundary,
			})
			results <- expiryResult{count: count, err: expireErr}
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	var expiredAtBoundary int64
	for result := range results {
		require.NoError(t, result.err)
		expiredAtBoundary += result.count
	}
	assert.Equal(t, int64(1), expiredAtBoundary, "only one worker transitions the task at the exact boundary")

	expired, err = repo.ExpirePlacementAssessmentReviews(ctx, ExpirePlacementAssessmentReviewsInput{
		TeamID: teamID,
		Now:    boundary.Add(time.Nanosecond),
	})
	require.NoError(t, err)
	assert.Zero(t, expired, "expiry is idempotent after the transition")

	var taskStatus, itemStatus, runStatus string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT status FROM review_tasks
			WHERE team_id = ?::uuid AND assessment_id = ?::uuid
		`, teamID, assessment.AssessmentID).Row().Scan(&taskStatus); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status FROM placement_items
			WHERE team_id = ?::uuid AND placement_item_id = ?::uuid
		`, teamID, item.PlacementItemID).Row().Scan(&itemStatus); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT status FROM placement_runs
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Row().Scan(&runStatus)
	}))
	assert.Equal(t, "expired", taskStatus)
	assert.Equal(t, "completed", itemStatus)
	assert.Equal(t, "completed", runStatus)
}

func TestPlacementAssessmentReviewExpiryProcessesMigratedSemanticTaskWithoutAssessment(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-assessment-legacy-expiry-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSemanticIngest(t, ctx, repo, teamID, ownerID, "placement-assessment-legacy-expiry", "A migrated semantic review has no assessment record.")
	item := ingest.Items[0]

	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE placement_runs
			SET status = 'awaiting_review', completed_at = now()
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE placement_items
			SET status = 'awaiting_review', category = 'candidate'
			WHERE team_id = ?::uuid AND placement_item_id = ?::uuid
		`, teamID, item.PlacementItemID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO review_tasks (
			    team_id, owner_profile_id, ingest_id, placement_item_id,
			    task_type, status, reason, payload, dedupe_key, expires_at
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    'identity_needs_review', 'open', 'ambiguous_entity',
			    '{"mention_ref":"legacy"}'::jsonb,
			    '', now() - interval '1 minute'
			)
		`, teamID, ownerID, ingest.IngestID, item.PlacementItemID).Error
	}))

	expired, err := repo.ExpirePlacementAssessmentReviews(ctx, ExpirePlacementAssessmentReviewsInput{
		TeamID: teamID,
		Now:    time.Now().UTC(),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), expired)

	var taskStatus, itemStatus, runStatus string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT status FROM review_tasks
			WHERE team_id = ?::uuid AND placement_item_id = ?::uuid
		`, teamID, item.PlacementItemID).Row().Scan(&taskStatus); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status FROM placement_items
			WHERE team_id = ?::uuid AND placement_item_id = ?::uuid
		`, teamID, item.PlacementItemID).Row().Scan(&itemStatus); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT status FROM placement_runs
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Row().Scan(&runStatus)
	}))
	assert.Equal(t, "expired", taskStatus)
	assert.Equal(t, "completed", itemStatus)
	assert.Equal(t, "completed", runStatus)
}

func placementAssessmentPersistInput(teamID, ownerID string, item PlacementItem) PersistPlacementAssessmentInput {
	return PersistPlacementAssessmentInput{
		TeamID:                  teamID,
		OwnerProfileID:          ownerID,
		PlacementItemID:         item.PlacementItemID,
		ClaimKey:                item.ClaimKey,
		RequestID:               "semantic-assessment:" + item.ClaimKey,
		AssessorContractVersion: "dense-mem.v2.4",
		Model:                   "assessment-model",
		PromptRevision:          "v2.4",
		Tokenizer:               "o200k_base",
		InputTokens:             10,
		OutputTokens:            10,
		CandidateContextTokens:  2,
		NormalizedResponse:      json.RawMessage(`{"request_id":"assessment","security_signals":[],"entity_results":[],"relationship_results":[]}`),
		ResponseHash:            "sha256:assessment-" + item.ClaimKey,
		ValidatedAt:             time.Now().UTC(),
	}
}
