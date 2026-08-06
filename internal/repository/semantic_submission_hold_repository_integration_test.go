package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSubmissionHoldExpiryIsInclusiveStateOnlyAndIdempotent(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-hold-expiry-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-hold-owner")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-hold-expiry")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	persistSubmissionAssessment(t, ctx, repo, *claimed)

	_, err = repo.CompleteSubmissionAssessment(ctx, CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
			PlacementRunID: ingest.PlacementRunID, WorkerID: "submission-worker", ExpectedAttempts: claimed.Attempts,
		},
		OutcomeKind: "submission_assessment_terminal",
		Status:      "review_required",
		Category:    "candidate",
		Payload:     map[string]any{"failure_stage": "policy_review"},
	})
	require.NoError(t, err)

	var heldAt, expiresAt time.Time
	var holdState string
	var holdCount, semanticCount int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT hold.held_at, hold.expires_at, run.semantic_hold_state
			FROM submission_holds AS hold
			JOIN placement_runs AS run
			  ON run.team_id = hold.team_id AND run.placement_run_id = hold.placement_run_id
			WHERE hold.team_id = ?::uuid AND hold.placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Row().Scan(&heldAt, &expiresAt, &holdState); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT count(*) FROM submission_holds WHERE team_id = ?::uuid`, teamID).Row().Scan(&holdCount); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT count(*)
			FROM entity_resolution_events
			WHERE team_id = ?::uuid
			UNION ALL
			SELECT count(*) FROM verification_events WHERE team_id = ?::uuid
		`, teamID, teamID).Row().Scan(&semanticCount)
	}))
	assert.Equal(t, int64(1), holdCount)
	assert.Equal(t, heldAt.Add(24*time.Hour), expiresAt)
	assert.Equal(t, "active", holdState)
	assert.Zero(t, semanticCount)

	before, err := repo.ExpireSubmissionHolds(ctx, ExpireSubmissionHoldsInput{TeamID: teamID, Now: expiresAt.Add(-time.Microsecond)})
	require.NoError(t, err)
	assert.Zero(t, before.Expired)

	type expiryResult struct {
		result ExpireSubmissionHoldsResult
		err    error
	}
	results := make(chan expiryResult, 2)
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	for range 2 {
		go func() {
			defer workers.Done()
			<-start
			result, expiryErr := repo.ExpireSubmissionHolds(ctx, ExpireSubmissionHoldsInput{TeamID: teamID, Now: expiresAt})
			results <- expiryResult{result: result, err: expiryErr}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	var expiredCount int64
	for result := range results {
		require.NoError(t, result.err)
		expiredCount += result.result.Expired
	}
	assert.Equal(t, int64(1), expiredCount)

	after, err := repo.ExpireSubmissionHolds(ctx, ExpireSubmissionHoldsInput{TeamID: teamID, Now: expiresAt.Add(time.Nanosecond)})
	require.NoError(t, err)
	assert.Zero(t, after.Expired)

	var finalState, itemState, runStatus string
	var expiryEvents, evidenceCount, relationshipCount, searchCount int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT semantic_hold_state, status FROM placement_runs WHERE team_id = ?::uuid AND placement_run_id = ?::uuid`, teamID, ingest.PlacementRunID).Row().Scan(&finalState, &runStatus); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT status FROM placement_items WHERE team_id = ?::uuid AND placement_run_id = ?::uuid ORDER BY evidence_index LIMIT 1`, teamID, ingest.PlacementRunID).Row().Scan(&itemState); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT count(*) FROM placement_outcomes WHERE team_id = ?::uuid AND placement_run_id = ?::uuid AND outcome_kind = 'submission_hold_expired'`, teamID, ingest.PlacementRunID).Row().Scan(&expiryEvents); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT count(*) FROM evidence_fragments WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, ingest.IngestID).Row().Scan(&evidenceCount); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT count(*) FROM relationship_records WHERE team_id = ?::uuid`, teamID).Row().Scan(&relationshipCount); err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*) FROM search_documents WHERE team_id = ?::uuid`, teamID).Row().Scan(&searchCount)
	}))
	assert.Equal(t, "expired", finalState)
	assert.Equal(t, "awaiting_review", runStatus)
	assert.Equal(t, "awaiting_review", itemState)
	assert.Equal(t, int64(1), expiryEvents)
	assert.Equal(t, int64(2), evidenceCount)
	assert.Zero(t, relationshipCount)
	assert.Zero(t, searchCount)

	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return createSubmissionHoldProjection(ctx, tx, SubmissionAssessmentRunScope{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
			PlacementRunID: ingest.PlacementRunID,
		}, "policy_review")
	}))
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT semantic_hold_state FROM placement_runs WHERE team_id = ?::uuid AND placement_run_id = ?::uuid`, teamID, ingest.PlacementRunID).Row().Scan(&finalState)
	}))
	assert.Equal(t, "expired", finalState)
}

func TestSubmissionReplacementIsolationReleaseAndPromotion(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-replacement-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-replacement-owner")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-replacement-other-owner")
	otherTeamID := createLedgerTeam(t, adminDB, rls, "submission-replacement-other-team")
	otherTeamOwnerID := createLedgerProfile(t, adminDB, rls, otherTeamID, "submission-replacement-other-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-replacement-search", 3, "exact", "")
	repo := NewLedgerRepository(appDB, rls)
	target := createHeldSubmissionForTest(t, ctx, repo, teamID, ownerID, "submission-replacement-target")

	replacementInput := CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "submission-replacement-successor",
		RequestHash: sha256Hex("submission-replacement-successor"), ReplacesSubmissionID: target.IngestID,
		Evidence: []EvidenceInput{{Content: "Orion links Vega."}, {Content: "Vega links Lyra."}},
	}
	replacement, err := repo.CreateIngest(ctx, replacementInput)
	require.NoError(t, err)
	require.NotNil(t, replacement)
	assert.False(t, replacement.Existing)
	assert.NotEqual(t, target.PlacementRunID, replacement.PlacementRunID)

	replay, err := repo.CreateIngest(ctx, replacementInput)
	require.NoError(t, err)
	assert.True(t, replay.Existing)
	assert.Equal(t, replacement.PlacementRunID, replay.PlacementRunID)

	_, err = repo.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: otherOwnerID, IdempotencyKey: "submission-replacement-cross-profile",
		RequestHash: sha256Hex("submission-replacement-cross-profile"), ReplacesSubmissionID: target.IngestID,
		Evidence: []EvidenceInput{{Content: "Other owner replacement."}},
	})
	assert.ErrorIs(t, err, ErrSubmissionReplacementNotFound)

	_, err = repo.CreateIngest(ctx, CreateIngestInput{
		TeamID: otherTeamID, OwnerProfileID: otherTeamOwnerID, IdempotencyKey: "submission-replacement-cross-team",
		RequestHash: sha256Hex("submission-replacement-cross-team"), ReplacesSubmissionID: target.IngestID,
		Evidence: []EvidenceInput{{Content: "Other team replacement."}},
	})
	assert.ErrorIs(t, err, ErrSubmissionReplacementNotFound)

	_, err = repo.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "submission-replacement-conflict",
		RequestHash: sha256Hex("submission-replacement-conflict"), ReplacesSubmissionID: target.IngestID,
		Evidence: []EvidenceInput{{Content: "Second active replacement."}},
	})
	assert.ErrorIs(t, err, ErrSubmissionReplacementConflict)

	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	persistSubmissionAssessment(t, ctx, repo, *claimed)
	_, err = repo.CompleteSubmissionAssessment(ctx, CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: replacement.IngestID,
			PlacementRunID: replacement.PlacementRunID, WorkerID: "submission-worker", ExpectedAttempts: claimed.Attempts,
		},
		OutcomeKind: "submission_assessment_terminal", Status: "terminal_failure", Category: "failed",
		Payload: map[string]any{"failure_stage": "provider"},
	})
	require.NoError(t, err)

	var targetState, successorStatus string
	var releaseEvents int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT semantic_hold_state FROM placement_runs WHERE team_id = ?::uuid AND placement_run_id = ?::uuid`, teamID, target.PlacementRunID).Row().Scan(&targetState); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT status FROM placement_runs WHERE team_id = ?::uuid AND placement_run_id = ?::uuid`, teamID, replacement.PlacementRunID).Row().Scan(&successorStatus); err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*) FROM placement_outcomes WHERE team_id = ?::uuid AND placement_run_id = ?::uuid AND outcome_kind = 'submission_replacement_released'`, teamID, replacement.PlacementRunID).Row().Scan(&releaseEvents)
	}))
	assert.Equal(t, "active", targetState)
	assert.Equal(t, "failed", successorStatus)
	assert.Equal(t, int64(1), releaseEvents)
	promotableTarget := createHeldSubmissionForTest(t, ctx, repo, teamID, ownerID, "submission-replacement-promotable-target")
	promotableInput := replacementInput
	promotableInput.IdempotencyKey = "submission-replacement-promotable-successor"
	promotableInput.RequestHash = sha256Hex(promotableInput.IdempotencyKey)
	promotableInput.ReplacesSubmissionID = promotableTarget.IngestID
	promotable, err := repo.CreateIngest(ctx, promotableInput)
	require.NoError(t, err)
	claimedPromotable, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimedPromotable)
	promotableAssessment := persistSubmissionAssessment(t, ctx, repo, *claimedPromotable)
	committed, err := repo.CommitSubmissionAssessment(ctx, submissionAssessmentCommitFixture(*claimedPromotable, promotable, promotableAssessment.AssessmentID, false))
	require.NoError(t, err)
	assert.Equal(t, "accepted", committed.Status)

	var supersededBy string
	var promotionEvents, supersededEvents int64
	var targetHoldState, targetSuccessor string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT semantic_hold_state, superseded_by_placement_run_id::text FROM placement_runs WHERE team_id = ?::uuid AND placement_run_id = ?::uuid`, teamID, promotableTarget.PlacementRunID).Row().Scan(&targetHoldState, &supersededBy); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT status FROM placement_runs WHERE team_id = ?::uuid AND placement_run_id = ?::uuid`, teamID, promotable.PlacementRunID).Row().Scan(&targetSuccessor); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT count(*) FROM placement_outcomes WHERE team_id = ?::uuid AND placement_run_id = ?::uuid AND outcome_kind = 'submission_replacement_promoted'`, teamID, promotable.PlacementRunID).Row().Scan(&promotionEvents); err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*) FROM placement_outcomes WHERE team_id = ?::uuid AND placement_run_id = ?::uuid AND outcome_kind = 'submission_hold_superseded'`, teamID, promotableTarget.PlacementRunID).Row().Scan(&supersededEvents)
	}))
	assert.Equal(t, "superseded", targetHoldState)
	assert.Equal(t, promotable.PlacementRunID, supersededBy)
	assert.Equal(t, "completed", targetSuccessor)
	assert.Equal(t, int64(1), promotionEvents)
	assert.Equal(t, int64(1), supersededEvents)
}

func TestSubmissionReplacementSerializesWithHoldExpiry(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-replacement-expiry-race-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-replacement-expiry-race-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-replacement-expiry-race-search", 3, "exact", "")
	repo := NewLedgerRepository(appDB, rls)
	target := createHeldSubmissionForTest(t, ctx, repo, teamID, ownerID, "submission-replacement-expiry-race-target")

	var expiresAt time.Time
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT expires_at FROM submission_holds WHERE team_id = ?::uuid AND placement_run_id = (SELECT placement_run_id FROM placement_runs WHERE team_id = ?::uuid AND ingest_id = ?::uuid)`, teamID, teamID, target.IngestID).Row().Scan(&expiresAt)
	}))

	replacementInput := CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "submission-replacement-expiry-race-successor",
		RequestHash: sha256Hex("submission-replacement-expiry-race-successor"), ReplacesSubmissionID: target.IngestID,
		Evidence: []EvidenceInput{{Content: "Orion links Vega."}, {Content: "Vega links Lyra."}},
	}
	start := make(chan struct{})
	type replacementResult struct {
		ingest *CreateIngestResult
		err    error
	}
	type expiryResult struct {
		result ExpireSubmissionHoldsResult
		err    error
	}
	replacementDone := make(chan replacementResult, 1)
	expiryDone := make(chan expiryResult, 1)
	go func() {
		<-start
		ingest, err := repo.CreateIngest(ctx, replacementInput)
		replacementDone <- replacementResult{ingest: ingest, err: err}
	}()
	go func() {
		<-start
		result, err := repo.ExpireSubmissionHolds(ctx, ExpireSubmissionHoldsInput{TeamID: teamID, Now: expiresAt})
		expiryDone <- expiryResult{result: result, err: err}
	}()
	close(start)
	replacement := <-replacementDone
	require.NoError(t, replacement.err)
	require.NotNil(t, replacement.ingest)
	expiry := <-expiryDone
	require.NoError(t, expiry.err)
	assert.Equal(t, int64(1), expiry.result.Expired)

	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assessment := persistSubmissionAssessment(t, ctx, repo, *claimed)
	committed, err := repo.CommitSubmissionAssessment(ctx, submissionAssessmentCommitFixture(*claimed, replacement.ingest, assessment.AssessmentID, false))
	require.NoError(t, err)
	assert.Equal(t, "accepted", committed.Status)

	var targetState, successorStatus string
	var expiryEvents, promotionEvents, supersededEvents int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT semantic_hold_state FROM placement_runs WHERE team_id = ?::uuid AND placement_run_id = ?::uuid`, teamID, target.PlacementRunID).Row().Scan(&targetState); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT status FROM placement_runs WHERE team_id = ?::uuid AND placement_run_id = ?::uuid`, teamID, replacement.ingest.PlacementRunID).Row().Scan(&successorStatus); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT count(*) FROM placement_outcomes WHERE team_id = ?::uuid AND placement_run_id = ?::uuid AND outcome_kind = 'submission_hold_expired'`, teamID, target.PlacementRunID).Row().Scan(&expiryEvents); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT count(*) FROM placement_outcomes WHERE team_id = ?::uuid AND placement_run_id = ?::uuid AND outcome_kind = 'submission_replacement_promoted'`, teamID, replacement.ingest.PlacementRunID).Row().Scan(&promotionEvents); err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*) FROM placement_outcomes WHERE team_id = ?::uuid AND placement_run_id = ?::uuid AND outcome_kind = 'submission_hold_superseded'`, teamID, target.PlacementRunID).Row().Scan(&supersededEvents)
	}))
	assert.Equal(t, "superseded", targetState)
	assert.Equal(t, "completed", successorStatus)
	assert.Equal(t, int64(1), expiryEvents)
	assert.Equal(t, int64(1), promotionEvents)
	assert.Equal(t, int64(1), supersededEvents)
}

func createHeldSubmissionForTest(t *testing.T, ctx context.Context, repo *LedgerRepositoryImpl, teamID, ownerID, key string) *CreateIngestResult {
	t.Helper()
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, key)
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	persistSubmissionAssessment(t, ctx, repo, *claimed)
	_, err = repo.CompleteSubmissionAssessment(ctx, CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
			PlacementRunID: ingest.PlacementRunID, WorkerID: "submission-worker", ExpectedAttempts: claimed.Attempts,
		},
		OutcomeKind: "submission_assessment_terminal", Status: "review_required", Category: "candidate",
		Payload: map[string]any{"failure_stage": "policy_review"},
	})
	require.NoError(t, err)
	return ingest
}
