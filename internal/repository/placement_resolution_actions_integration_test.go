package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestPlacementResolutionReviewActionsRecordOutcomeAndRequeue(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-actions-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "owner-b")
	ledgerRepo := NewLedgerRepository(appDB, rls)

	cases := []struct {
		name               string
		action             domain.ResolveAction
		outcomeStatus      string
		resultStatus       string
		checkAfter         int
		additionalEvidence int64
	}{
		{
			name:          "acknowledge",
			action:        domain.ResolveAcknowledge,
			outcomeStatus: "acknowledged",
			resultStatus:  "acknowledged",
		},
		{
			name:          "select_entity",
			action:        domain.ResolveSelectEntity,
			outcomeStatus: "entity_selected",
			resultStatus:  string(domain.PlacementRunQueued),
			checkAfter:    60,
		},
		{
			name:               "confirm_new_entity",
			action:             domain.ResolveConfirmNewEntity,
			outcomeStatus:      "new_entity_confirmed",
			resultStatus:       string(domain.PlacementRunQueued),
			checkAfter:         60,
			additionalEvidence: 1,
		},
		{
			name:               "accept",
			action:             domain.ResolveAccept,
			outcomeStatus:      "accepted",
			resultStatus:       string(domain.PlacementRunQueued),
			checkAfter:         60,
			additionalEvidence: 1,
		},
		{
			name:               "correct",
			action:             domain.ResolveCorrect,
			outcomeStatus:      "correction_submitted",
			resultStatus:       string(domain.PlacementRunQueued),
			checkAfter:         60,
			additionalEvidence: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerA,
				"placement-action-"+tc.name, "Placement action "+tc.name+".")
			input := validPlacementResolutionActionInput(tc.action, teamID, ownerA, ingest)

			ownerBInput := input
			ownerBInput.OwnerProfileID = ownerB
			ownerBInput.IdempotencyKey += "-owner-b"
			_, err := ledgerRepo.ResolvePlacementReview(ctx, ownerBInput)
			require.ErrorIs(t, err, ErrPlacementResolutionNotFound)

			if tc.action != domain.ResolveAcknowledge {
				withoutVersion := input
				withoutVersion.PlacementItemVersion = 0
				withoutVersion.IdempotencyKey += "-missing-version"
				_, err := ledgerRepo.ResolvePlacementReview(ctx, withoutVersion)
				require.ErrorContains(t, err, "placement_item_version is required")
			}

			result, err := ledgerRepo.ResolvePlacementReview(ctx, input)
			require.NoError(t, err)
			require.Equal(t, tc.resultStatus, result.Status)
			require.Equal(t, tc.checkAfter, result.CheckAfterSeconds)
			require.NotEmpty(t, result.DecisionID)

			retry, err := ledgerRepo.ResolvePlacementReview(ctx, input)
			require.NoError(t, err)
			require.True(t, retry.Existing)
			require.Equal(t, result.DecisionID, retry.DecisionID)

			if tc.action != domain.ResolveAcknowledge {
				stale := input
				stale.IdempotencyKey += "-stale"
				_, err = ledgerRepo.ResolvePlacementReview(ctx, stale)
				require.ErrorIs(t, err, ErrPlacementResolutionStale)
			}

			conflict := input
			conflict.Message = "same idempotency key with a different request"
			_, err = ledgerRepo.ResolvePlacementReview(ctx, conflict)
			require.ErrorIs(t, err, ErrIdempotencyConflict)

			assertPlacementResolutionActionState(t, ctx, appDB, rls, teamID, ownerA, ingest, result.DecisionID, tc.outcomeStatus, tc.resultStatus, string(tc.action), tc.additionalEvidence)
		})
	}
}

func TestPlacementResolutionReviewActionsBlockProcessingState(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-actions-processing-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)

	actions := []domain.ResolveAction{
		domain.ResolveAcknowledge,
		domain.ResolveSelectEntity,
		domain.ResolveConfirmNewEntity,
		domain.ResolveAccept,
		domain.ResolveCorrect,
	}
	for _, action := range actions {
		t.Run(string(action), func(t *testing.T) {
			ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
				"placement-processing-"+string(action), "Processing action "+string(action)+".")
			require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
				return tx.Exec(`
					UPDATE placement_runs
					SET status = 'processing',
					    worker_id = 'worker',
					    attempts = 1,
					    lease_until = now() + interval '1 hour'
					WHERE team_id = ?::uuid
					  AND placement_run_id = ?::uuid
				`, teamID, ingest.PlacementRunID).Error
			}))

			input := validPlacementResolutionActionInput(action, teamID, ownerID, ingest)
			input.IdempotencyKey += "-processing"
			_, err := ledgerRepo.ResolvePlacementReview(ctx, input)
			require.ErrorIs(t, err, ErrPlacementResolutionInvalidState)
		})
	}
}

func validPlacementResolutionActionInput(
	action domain.ResolveAction,
	teamID string,
	ownerID string,
	ingest *CreateIngestResult,
) ResolvePlacementReviewInput {
	input := ResolvePlacementReviewInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Action:         string(action),
		IngestID:       ingest.IngestID,
		IdempotencyKey: "resolve-" + string(action),
	}
	if action != domain.ResolveAcknowledge {
		input.PlacementItemID = ingest.Items[0].PlacementItemID
		input.PlacementItemVersion = ingest.Items[0].Version
	}
	switch action {
	case domain.ResolveSelectEntity:
		input.EntityRef = "Mark"
		input.CandidateEntityID = uuid.NewString()
	case domain.ResolveConfirmNewEntity:
		input.EntityRef = "New Mark"
		input.Evidence = []EvidenceInput{{
			Content: "Reviewer confirmed this mention is a distinct entity.",
		}}
	case domain.ResolveAccept:
		input.Evidence = []EvidenceInput{{
			Content: "Reviewer accepted this placement with supporting evidence.",
		}}
	case domain.ResolveCorrect:
		input.Evidence = []EvidenceInput{{
			Content: "Reviewer submitted corrected placement evidence.",
		}}
	}
	return input
}

func assertPlacementResolutionActionState(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithTeamProfileTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error
	},
	teamID string,
	ownerID string,
	ingest *CreateIngestResult,
	outcomeID string,
	wantOutcomeStatus string,
	wantResultStatus string,
	wantAction string,
	additionalEvidence int64,
) {
	t.Helper()
	var outcomeStatus, payloadAction, payloadResultStatus string
	var payloadEvidenceCount, fragmentCount int64
	err := rls.WithTeamProfileTx(ctx, db, teamID, ownerID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT status,
			       payload->>'action',
			       payload->>'result_status',
			       CASE
			           WHEN jsonb_typeof(payload->'evidence_fragment_ids') = 'array'
			           THEN jsonb_array_length(payload->'evidence_fragment_ids')
			           ELSE 0
			       END
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND outcome_id = ?::uuid
		`, teamID, ownerID, outcomeID).Row().Scan(
			&outcomeStatus,
			&payloadAction,
			&payloadResultStatus,
			&payloadEvidenceCount,
		))
		return tx.Raw(`
			SELECT COUNT(*)
			FROM evidence_fragments
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND ingest_id = ?::uuid
		`, teamID, ownerID, ingest.IngestID).Scan(&fragmentCount).Error
	})
	require.NoError(t, err)
	assert.Equal(t, wantOutcomeStatus, outcomeStatus)
	assert.Equal(t, wantAction, payloadAction)
	assert.Equal(t, wantResultStatus, payloadResultStatus)
	assert.Equal(t, additionalEvidence, payloadEvidenceCount)
	assert.Equal(t, int64(1)+additionalEvidence, fragmentCount)

	var runStatus, itemStatus, itemCategory, itemAction string
	var itemVersion int
	err = rls.WithTeamProfileTx(ctx, db, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT run.status,
			       item.status,
			       item.category,
			       COALESCE(item.result->>'action', ''),
			       item.version
			FROM placement_runs AS run
			JOIN placement_items AS item
			  ON item.team_id = run.team_id
			 AND item.placement_run_id = run.placement_run_id
			WHERE run.team_id = ?::uuid
			  AND run.owner_profile_id = ?::uuid
			  AND run.placement_run_id = ?::uuid
			  AND item.placement_item_id = ?::uuid
		`, teamID, ownerID, ingest.PlacementRunID, ingest.Items[0].PlacementItemID).Row().Scan(
			&runStatus,
			&itemStatus,
			&itemCategory,
			&itemAction,
			&itemVersion,
		)
	})
	require.NoError(t, err)
	assert.Equal(t, string(domain.PlacementRunQueued), runStatus)
	assert.Equal(t, "queued", itemStatus)
	assert.Equal(t, "pending", itemCategory)
	if wantAction == string(domain.ResolveAcknowledge) {
		assert.Equal(t, 1, itemVersion)
		assert.Empty(t, itemAction)
		return
	}
	assert.Equal(t, 2, itemVersion)
	assert.Equal(t, wantAction, itemAction)
}
