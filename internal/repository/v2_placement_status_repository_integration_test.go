package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestV2PlacementStatusReadsCurrentOwnerScopedItemVersions(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-status-team")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamID, "owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamID, "owner-b")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)

	ingest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerA,
		"placement-status", "Placement status should expose current item version.")
	status, err := ledgerRepo.GetPlacementRun(ctx, V2GetPlacementRunInput{
		TeamID:         teamID,
		OwnerProfileID: ownerA,
		IngestID:       ingest.IngestID,
	})
	require.NoError(t, err)
	require.Len(t, status.Items, 1)
	assert.Equal(t, ingest.PlacementRunID, status.PlacementRunID)
	assert.Equal(t, "queued", status.Status)
	assert.Equal(t, 1, status.Items[0].Version)
	assert.Equal(t, "queued", status.Items[0].Status)

	_, err = ledgerRepo.GetPlacementRun(ctx, V2GetPlacementRunInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		IngestID:       ingest.IngestID,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2PlacementNotFound), err)

	_, err = ledgerRepo.ResolvePlacementReview(ctx, V2ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerA,
		Action:               "reject",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		Message:              "not durable memory",
		IdempotencyKey:       "reject-status",
	})
	require.NoError(t, err)

	status, err = ledgerRepo.GetPlacementRun(ctx, V2GetPlacementRunInput{
		TeamID:         teamID,
		OwnerProfileID: ownerA,
		IngestID:       ingest.IngestID,
	})
	require.NoError(t, err)
	require.Len(t, status.Items, 1)
	assert.Equal(t, "completed", status.Status)
	assert.Equal(t, 2, status.Items[0].Version)
	assert.Equal(t, "completed", status.Items[0].Status)
	assert.Equal(t, "candidate", status.Items[0].Category)
}
