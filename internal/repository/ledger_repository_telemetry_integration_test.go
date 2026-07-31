package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
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

func TestLedgerFirstDispositionMarkerAllowsUserRequeue(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-first-disposition-requeue")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-first-disposition-requeue")
	repo := NewLedgerRepository(appDB, rls)

	created, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "first-disposition-requeue",
		RequestHash:    "first-disposition-requeue-hash",
		Evidence: []EvidenceInput{{
			Content: "A reviewer can requeue this placement after its first disposition.",
		}},
	})
	require.NoError(t, err)
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "first-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	first, err := repo.FinishPlacementRun(ctx, teamID, created.PlacementRunID, "first-worker", string(domain.PlacementRunCompleted), "")
	require.NoError(t, err)
	require.NotNil(t, first)

	resolved, err := repo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerID,
		Action:               string(domain.ResolveSelectEntity),
		IngestID:             created.IngestID,
		PlacementItemID:      created.Items[0].PlacementItemID,
		PlacementItemVersion: created.Items[0].Version,
		EntityRef:            "reviewed entity",
		CandidateEntityID:    uuid.NewString(),
		IdempotencyKey:       "first-disposition-requeue-resolution",
	})
	require.NoError(t, err)
	assert.Equal(t, string(domain.PlacementRunQueued), resolved.Status)
	assert.Nil(t, resolved.FirstDisposition)

	claimed, err = repo.ClaimNextPlacementRun(ctx, teamID, "second-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, created.PlacementRunID, claimed.PlacementRunID)

	second, err := repo.FinishPlacementRun(ctx, teamID, created.PlacementRunID, "second-worker", string(domain.PlacementRunCompleted), "")
	require.NoError(t, err)
	assert.Nil(t, second)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		var count int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
			  AND outcome_kind = 'telemetry_first_disposition'
		`, teamID, created.PlacementRunID).Scan(&count).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), count)
		return nil
	})
	require.NoError(t, err)
}

func TestPlacementResolutionQuarantineRecordsFirstDisposition(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-resolution-first-disposition-quarantine")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-resolution-first-disposition-quarantine")
	repo := NewLedgerRepository(appDB, rls)

	created, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "resolution-first-disposition-quarantine",
		RequestHash:    "resolution-first-disposition-quarantine-hash",
		Evidence: []EvidenceInput{{
			Content: "This placement starts queued before reviewer evidence arrives.",
		}},
	})
	require.NoError(t, err)

	resolved, err := repo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerID,
		Action:               string(domain.ResolveAccept),
		IngestID:             created.IngestID,
		PlacementItemID:      created.Items[0].PlacementItemID,
		PlacementItemVersion: created.Items[0].Version,
		IdempotencyKey:       "resolution-first-disposition-quarantine-action",
		Evidence: []EvidenceInput{{
			Content: "Reviewer evidence requires quarantine.",
			InitialEvent: &SecurityEventDraft{
				EventKind:      "deterministic_scan",
				Decision:       "quarantine",
				ScanPolicyHash: "policy",
				Reason:         "deterministic quarantine",
			},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, resolved.FirstDisposition)
	assert.Equal(t, string(domain.PlacementRunQuarantined), resolved.FirstDisposition.Status)

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
