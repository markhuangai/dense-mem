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

func TestV2PlacementResolutionReviewActionsRecordOutcomeAndRequeue(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-actions-team")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamID, "owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamID, "owner-b")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)

	cases := []struct {
		name               string
		action             domain.V2ResolveAction
		outcomeStatus      string
		resultStatus       string
		checkAfter         int
		additionalEvidence int64
	}{
		{
			name:          "acknowledge",
			action:        domain.V2ResolveAcknowledge,
			outcomeStatus: "acknowledged",
			resultStatus:  "acknowledged",
		},
		{
			name:          "select_entity",
			action:        domain.V2ResolveSelectEntity,
			outcomeStatus: "entity_selected",
			resultStatus:  string(domain.V2PlacementRunQueued),
			checkAfter:    60,
		},
		{
			name:               "confirm_new_entity",
			action:             domain.V2ResolveConfirmNewEntity,
			outcomeStatus:      "new_entity_confirmed",
			resultStatus:       string(domain.V2PlacementRunQueued),
			checkAfter:         60,
			additionalEvidence: 1,
		},
		{
			name:               "accept",
			action:             domain.V2ResolveAccept,
			outcomeStatus:      "accepted",
			resultStatus:       string(domain.V2PlacementRunQueued),
			checkAfter:         60,
			additionalEvidence: 1,
		},
		{
			name:               "correct",
			action:             domain.V2ResolveCorrect,
			outcomeStatus:      "correction_submitted",
			resultStatus:       string(domain.V2PlacementRunQueued),
			checkAfter:         60,
			additionalEvidence: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ingest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerA,
				"placement-action-"+tc.name, "Placement action "+tc.name+".")
			input := validV2PlacementResolutionActionInput(tc.action, teamID, ownerA, ingest)

			ownerBInput := input
			ownerBInput.OwnerProfileID = ownerB
			ownerBInput.IdempotencyKey += "-owner-b"
			_, err := ledgerRepo.ResolvePlacementReview(ctx, ownerBInput)
			require.ErrorIs(t, err, ErrV2PlacementResolutionNotFound)

			if tc.action != domain.V2ResolveAcknowledge {
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

			if tc.action != domain.V2ResolveAcknowledge {
				stale := input
				stale.IdempotencyKey += "-stale"
				_, err = ledgerRepo.ResolvePlacementReview(ctx, stale)
				require.ErrorIs(t, err, ErrV2PlacementResolutionStale)
			}

			conflict := input
			conflict.Message = "same idempotency key with a different request"
			_, err = ledgerRepo.ResolvePlacementReview(ctx, conflict)
			require.ErrorIs(t, err, ErrV2IdempotencyConflict)

			assertV2PlacementResolutionActionState(t, ctx, appDB, rls, teamID, ownerA, ingest, result.DecisionID, tc.outcomeStatus, tc.resultStatus, string(tc.action), tc.additionalEvidence)
		})
	}
}

func TestV2PlacementResolutionReviewActionsBlockProcessingState(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-actions-processing-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)

	actions := []domain.V2ResolveAction{
		domain.V2ResolveAcknowledge,
		domain.V2ResolveSelectEntity,
		domain.V2ResolveConfirmNewEntity,
		domain.V2ResolveAccept,
		domain.V2ResolveCorrect,
	}
	for _, action := range actions {
		t.Run(string(action), func(t *testing.T) {
			ingest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
				"placement-processing-"+string(action), "Processing action "+string(action)+".")
			require.NoError(t, rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
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

			input := validV2PlacementResolutionActionInput(action, teamID, ownerID, ingest)
			input.IdempotencyKey += "-processing"
			_, err := ledgerRepo.ResolvePlacementReview(ctx, input)
			require.ErrorIs(t, err, ErrV2PlacementResolutionInvalidState)
		})
	}
}

func validV2PlacementResolutionActionInput(
	action domain.V2ResolveAction,
	teamID string,
	ownerID string,
	ingest *V2CreateIngestResult,
) V2ResolvePlacementReviewInput {
	input := V2ResolvePlacementReviewInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Action:         string(action),
		IngestID:       ingest.IngestID,
		IdempotencyKey: "resolve-" + string(action),
	}
	if action != domain.V2ResolveAcknowledge {
		input.PlacementItemID = ingest.Items[0].PlacementItemID
		input.PlacementItemVersion = ingest.Items[0].Version
	}
	switch action {
	case domain.V2ResolveSelectEntity:
		input.EntityRef = "Mark"
		input.CandidateEntityID = uuid.NewString()
	case domain.V2ResolveConfirmNewEntity:
		input.EntityRef = "New Mark"
		input.Evidence = []V2EvidenceInput{{
			Content: "Reviewer confirmed this mention is a distinct entity.",
		}}
	case domain.V2ResolveAccept:
		input.Evidence = []V2EvidenceInput{{
			Content: "Reviewer accepted this placement with supporting evidence.",
		}}
	case domain.V2ResolveCorrect:
		input.Evidence = []V2EvidenceInput{{
			Content: "Reviewer submitted corrected placement evidence.",
		}}
	}
	return input
}

func assertV2PlacementResolutionActionState(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithTeamProfileTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error
	},
	teamID string,
	ownerID string,
	ingest *V2CreateIngestResult,
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
	assert.Equal(t, string(domain.V2PlacementRunQueued), runStatus)
	assert.Equal(t, "queued", itemStatus)
	assert.Equal(t, "pending", itemCategory)
	if wantAction == string(domain.V2ResolveAcknowledge) {
		assert.Equal(t, 1, itemVersion)
		assert.Empty(t, itemAction)
		return
	}
	assert.Equal(t, 2, itemVersion)
	assert.Equal(t, wantAction, itemAction)
}
