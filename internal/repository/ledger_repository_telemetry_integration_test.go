package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestLedgerCreateIngestQuarantineRecordsFirstDispositionOnce(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-first-disposition")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-first-disposition")
	repo := NewLedgerRepository(appDB, rls)
	input := CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "first-disposition-quarantine",
		RequestHash:    "first-disposition-quarantine-hash",
		Status:         string(domain.PlacementRunQuarantined),
		Evidence: []EvidenceInput{{
			Content: "This evidence was quarantined before semantic assessment.",
		}},
	}

	created, err := repo.CreateIngest(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, created.FirstDisposition)
	assert.Equal(t, string(domain.PlacementRunQuarantined), created.FirstDisposition.Status)
	assert.False(t, created.FirstDisposition.CreatedAt.IsZero())
	assert.False(t, created.FirstDisposition.CompletedAt.IsZero())

	replayed, err := repo.CreateIngest(ctx, input)
	require.NoError(t, err)
	assert.True(t, replayed.Existing)
	assert.Nil(t, replayed.FirstDisposition)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		var count int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
			  AND outcome_kind = 'telemetry_first_disposition'
			  AND status = 'quarantined'
		`, teamID, created.PlacementRunID).Scan(&count).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), count)
		return nil
	})
	require.NoError(t, err)
}

func TestLedgerFinishPlacementRunRecordsFirstDispositionOnce(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-finish-first-disposition")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-finish-first-disposition")
	repo := NewLedgerRepository(appDB, rls)

	created, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "finish-first-disposition",
		RequestHash:    "finish-first-disposition-hash",
		Evidence: []EvidenceInput{{
			Content: "A worker completion records the first terminal placement disposition.",
		}},
	})
	require.NoError(t, err)
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "telemetry-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	disposition, err := repo.FinishPlacementRun(ctx, teamID, created.PlacementRunID, "telemetry-worker", string(domain.PlacementRunCompleted), "")
	require.NoError(t, err)
	require.NotNil(t, disposition)
	assert.Equal(t, string(domain.PlacementRunCompleted), disposition.Status)
	assert.False(t, disposition.CreatedAt.IsZero())
	assert.False(t, disposition.CompletedAt.IsZero())

	_, err = repo.FinishPlacementRun(ctx, teamID, created.PlacementRunID, "telemetry-worker", string(domain.PlacementRunCompleted), "")
	require.ErrorIs(t, err, ErrPlacementLeaseConflict)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		var count int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
			  AND outcome_kind = 'telemetry_first_disposition'
			  AND status = 'completed'
		`, teamID, created.PlacementRunID).Scan(&count).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), count)
		return nil
	})
	require.NoError(t, err)
}
