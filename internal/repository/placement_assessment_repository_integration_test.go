package repository

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/verifier"
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

func TestPlacementReviewProviderRetryUsesHintAndReleasesAssessmentReservation(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-provider-retry-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "provider-retry-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)

	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement provider retry", "A provider rate limit should use durable backoff.")
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-provider-retry", time.Minute)
	require.NoError(t, err)
	reserved, err := ledgerRepo.ReservePlacementAssessmentProviderAttempt(ctx, ReservePlacementAssessmentProviderAttemptInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-provider-retry",
		ExpectedAttempts: claimed.Attempts,
	})
	require.NoError(t, err)
	require.True(t, reserved)

	requeued, err := ledgerRepo.RequeuePlacementReviewResult(ctx, RequeuePlacementReviewInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-provider-retry",
		ExpectedAttempts: claimed.Attempts,
		OutcomeKind:      "semantic_assessment_attempt",
		Payload: map[string]any{
			"failure_class":   "rate_limited",
			"provider_status": 429,
		},
		RetryAfter:             2 * time.Minute,
		ReleaseAssessorAttempt: true,
	})
	require.NoError(t, err)
	require.Equal(t, "retryable", requeued.Status)

	var delayedByHint, bounded, reservationReleased, leakedProviderMessage bool
	var failureClass, providerStatus string
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT available_at >= now() + interval '110 seconds',
			       available_at <= now() + interval '125 seconds'
			FROM placement_runs
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Row().Scan(&delayedByHint, &bounded))
		require.NoError(t, tx.Raw(`
			SELECT assessor_attempt_id IS NULL
			FROM placement_items
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
		`, teamID, ingest.Items[0].PlacementItemID).Row().Scan(&reservationReleased))
		return tx.Raw(`
			SELECT payload ->> 'failure_class',
			       payload ->> 'provider_status',
			       jsonb_exists(payload, 'provider_message')
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND outcome_id = ?::uuid
		`, teamID, requeued.OutcomeID).Row().Scan(&failureClass, &providerStatus, &leakedProviderMessage)
	})
	require.NoError(t, err)
	assert.True(t, delayedByHint)
	assert.True(t, bounded)
	assert.True(t, reservationReleased)
	assert.Equal(t, "rate_limited", failureClass)
	assert.Equal(t, "429", providerStatus)
	assert.False(t, leakedProviderMessage)
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

func TestPlacementAssessmentJSONBRoundTripPreservesCanonicalResponseHash(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-assessment-jsonb-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSemanticIngest(t, ctx, repo, teamID, ownerID, "placement-assessment-jsonb", "Canonical hashes survive JSONB key reordering.")
	input := placementAssessmentPersistInput(teamID, ownerID, ingest.Items[0])
	input.NormalizedResponse = json.RawMessage(`{"relationship_results":[],"request_id":"assessment","entity_results":[],"security_signals":[]}`)
	canonical, err := verifier.CanonicalJSON(input.NormalizedResponse)
	require.NoError(t, err)
	sum := sha256.Sum256(canonical)
	input.ResponseHash = fmt.Sprintf("sha256:%x", sum[:])

	_, existing, err := repo.PersistPlacementAssessment(ctx, input)
	require.NoError(t, err)
	assert.False(t, existing)
	loaded, err := repo.LoadPlacementAssessment(ctx, LoadPlacementAssessmentInput{
		TeamID: teamID, OwnerProfileID: ownerID, PlacementItemID: ingest.Items[0].PlacementItemID,
	})
	require.NoError(t, err)
	loadedCanonical, err := verifier.CanonicalJSON(loaded.NormalizedResponse)
	require.NoError(t, err)
	assert.Equal(t, canonical, loadedCanonical)
	sum = sha256.Sum256(loadedCanonical)
	assert.Equal(t, fmt.Sprintf("sha256:%x", sum[:]), loaded.ResponseHash)
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
	type reservationResult struct {
		reserved bool
		err      error
	}
	results := make(chan reservationResult, 2)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)
	var workers sync.WaitGroup
	workers.Add(2)
	for range 2 {
		go func() {
			defer workers.Done()
			ready.Done()
			<-start
			reserved, reserveErr := repo.ReservePlacementAssessmentProviderAttempt(ctx, input)
			results <- reservationResult{reserved: reserved, err: reserveErr}
		}()
	}
	ready.Wait()
	close(start)
	workers.Wait()
	close(results)
	var reservedCount int
	for result := range results {
		require.NoError(t, result.err)
		if result.reserved {
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

	otherReserved, err := repo.ReservePlacementAssessmentProviderAttempt(ctx, ReservePlacementAssessmentProviderAttemptInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "different-worker",
		ExpectedAttempts: claimed.Attempts,
	})
	require.NoError(t, err)
	assert.False(t, otherReserved)

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
	assert.True(t, reserved, "a later claim can start one conversation when no valid assessment exists")
}

func TestPlacementAssessmentReviewTaskUpsertsRetainAssessmentMetadata(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-assessment-review-upsert-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	content := "Mark works on Dense-Mem."
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID, "placement-assessment-review-upsert", content)
	assessment, _, err := ledgerRepo.PersistPlacementAssessment(ctx, placementAssessmentPersistInput(teamID, ownerID, ingest.Items[0]))
	require.NoError(t, err)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Mark")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	applied := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:            teamID,
		OwnerProfileID:    ownerID,
		IngestID:          ingest.IngestID,
		PlacementItemID:   ingest.Items[0].PlacementItemID,
		ProposalRef:       "existing-relationship",
		SubjectRef:        "subject",
		SubjectEntityID:   subject.EntityID,
		OriginalPredicate: "works on",
		PredicateKey:      "works_on",
		PredicateVersion:  1,
		ObjectRef:         "object",
		ObjectEntityID:    object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:assessment-review-upsert",
			SpanStart:      0,
			SpanEnd:        len(content),
			Quote:          content,
			Authority:      "primary",
		},
	})
	commit := CommitPlacementSemanticInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		PlacementRunID:  ingest.PlacementRunID,
		PlacementItemID: ingest.Items[0].PlacementItemID,
	}
	expectedExpiry := time.Date(2040, time.January, 2, 3, 4, 5, 0, time.UTC)
	correctionID := uuid.NewString()
	conflictID := uuid.NewString()

	testCases := []struct {
		name   string
		insert func(tx *gorm.DB, assessmentID string) (string, error)
	}{
		{
			name: "entity",
			insert: func(tx *gorm.DB, assessmentID string) (string, error) {
				return insertEntityReviewTask(ctx, tx, commit, PlacementEntityResolutionInput{
					MentionRef:    "entity-upsert",
					Action:        "ambiguous",
					EntityKind:    "person",
					CanonicalName: "Mark",
					AssessmentID:  assessmentID,
				}, uuid.NewString())
			},
		},
		{
			name: "semantic_relationship",
			insert: func(tx *gorm.DB, assessmentID string) (string, error) {
				return insertPlacementSemanticReviewTask(ctx, tx, commit, ApplyRelationshipDecisionInput{
					ProposalRef:        "semantic-relationship-upsert",
					SubjectRef:         "subject",
					ObjectRef:          "object",
					OriginalPredicate:  "works on",
					Polarity:           "+",
					EvidenceVerdict:    "entailed",
					SemanticReviewKind: "support_confidence",
					AssessmentID:       assessmentID,
				}, applied,
					&PlacementCorrectionTargetInput{RelationshipID: correctionID, ExpectedVersion: 2},
					&PlacementConflictContextInput{ConflictID: conflictID, ExpectedVersion: 3},
				)
			},
		},
		{
			name: "relationship",
			insert: func(tx *gorm.DB, assessmentID string) (string, error) {
				review, reviewErr := insertRelationshipReview(ctx, tx, commit, PlacementRelationshipReviewInput{
					Ref:               "relationship-upsert",
					SubjectRef:        "subject",
					OriginalPredicate: "works on",
					ObjectRef:         "object",
					Polarity:          "+",
					Reason:            "relationship_needs_review",
					AssessmentID:      assessmentID,
				})
				if reviewErr != nil {
					return "", reviewErr
				}
				return review.ReviewTaskID, nil
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var firstTaskID string
			err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
				var insertErr error
				firstTaskID, insertErr = testCase.insert(tx, assessment.AssessmentID)
				if insertErr != nil {
					return insertErr
				}
				return tx.Exec(`
					UPDATE review_tasks
					SET expires_at = ?
					WHERE team_id = ?::uuid AND review_task_id = ?::uuid
				`, expectedExpiry, teamID, firstTaskID).Error
			})
			require.NoError(t, err)

			var secondTaskID string
			err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
				var insertErr error
				secondTaskID, insertErr = testCase.insert(tx, "")
				return insertErr
			})
			require.NoError(t, err)
			assert.Equal(t, firstTaskID, secondTaskID)

			var storedAssessmentID string
			var storedExpiry time.Time
			var storedVersion int
			err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
				return tx.Raw(`
					SELECT assessment_id::text, expires_at, version
					FROM review_tasks
					WHERE team_id = ?::uuid AND review_task_id = ?::uuid
				`, teamID, firstTaskID).Row().Scan(&storedAssessmentID, &storedExpiry, &storedVersion)
			})
			require.NoError(t, err)
			assert.Equal(t, assessment.AssessmentID, storedAssessmentID)
			assert.True(t, storedExpiry.Equal(expectedExpiry))
			assert.Equal(t, 2, storedVersion)

			if testCase.name != "semantic_relationship" {
				return
			}
			var storedCorrectionID, storedConflictID string
			var storedCorrectionVersion, storedConflictVersion int
			err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
				return tx.Raw(`
					SELECT payload->'correction_target'->>'relationship_id',
					       (payload->'correction_target'->>'expected_version')::int,
					       payload->'conflict_context'->>'conflict_id',
					       (payload->'conflict_context'->>'expected_version')::int
					FROM review_tasks
					WHERE team_id = ?::uuid AND review_task_id = ?::uuid
				`, teamID, firstTaskID).Row().Scan(
					&storedCorrectionID,
					&storedCorrectionVersion,
					&storedConflictID,
					&storedConflictVersion,
				)
			})
			require.NoError(t, err)
			assert.Equal(t, correctionID, storedCorrectionID)
			assert.Equal(t, 2, storedCorrectionVersion)
			assert.Equal(t, conflictID, storedConflictID)
			assert.Equal(t, 3, storedConflictVersion)
		})
	}
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
	var verificationCount, supportCount, expiryOutcomeCount int64
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
		if err := tx.Raw(`SELECT count(*) FROM relationship_evidence_supports WHERE team_id = ?::uuid`, teamID).Row().Scan(&supportCount); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT count(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
			  AND outcome_kind = 'semantic_review_expired'
			  AND status = 'expired'
			  AND payload->>'actor' = 'system'
			  AND payload->>'reason' = 'semantic_review_expired'
		`, teamID, item.PlacementItemID).Row().Scan(&expiryOutcomeCount)
	}))
	assert.Equal(t, "expired", taskStatus)
	assert.Equal(t, 2, taskVersion)
	assert.Equal(t, "completed", itemStatus)
	assert.Equal(t, "completed", runStatus)
	assert.Zero(t, verificationCount)
	assert.Zero(t, supportCount)
	assert.Equal(t, int64(1), expiryOutcomeCount)
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
	var expiryOutcomeCount int64
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
		if err := tx.Raw(`
			SELECT status FROM placement_runs
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Row().Scan(&runStatus); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT count(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_item_id = ?::uuid
			  AND outcome_kind = 'semantic_review_expired'
			  AND status = 'expired'
		`, teamID, item.PlacementItemID).Row().Scan(&expiryOutcomeCount)
	}))
	assert.Equal(t, "expired", taskStatus)
	assert.Equal(t, "completed", itemStatus)
	assert.Equal(t, "completed", runStatus)
	assert.Equal(t, int64(1), expiryOutcomeCount)
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

	hydrated, err := repo.GetPlacementRun(ctx, GetPlacementRunInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       ingest.IngestID,
	})
	require.NoError(t, err)
	require.Len(t, hydrated.Items, 1)
	require.Len(t, hydrated.Items[0].ReviewTasks, 1)
	assert.Equal(t, "identity", hydrated.Items[0].ReviewTasks[0].Kind)
	assert.Equal(t, "open", hydrated.Items[0].ReviewTasks[0].Status)

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
		Tokenizer:               "o200k_base",
		InputTokens:             10,
		OutputTokens:            10,
		CandidateContextTokens:  2,
		NormalizedResponse:      json.RawMessage(`{"request_id":"assessment","security_signals":[],"entity_results":[],"relationship_results":[]}`),
		ResponseHash:            "sha256:assessment-" + item.ClaimKey,
		ValidatedAt:             time.Now().UTC(),
	}
}
