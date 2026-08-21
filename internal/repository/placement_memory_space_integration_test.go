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

func TestLedgerPlacementClaimSkipsSealedPrivateGeneration(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "placement-claim-private-generation"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	privateCredential := createOwnedCredential(t, credentialRepo, teamID, ownerID, "private-claim", domain.CredentialBindingCredentialPrivate)
	repo := NewLedgerRepository(appDB, rls)

	_, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:          teamID.String(),
		OwnerProfileID:  privateCredential.ID.String(),
		SpaceID:         privateCredential.MemorySpaceID.String(),
		SpaceGeneration: privateCredential.MemorySpaceGeneration,
		Evidence:        []EvidenceInput{{Content: "A sealed private placement must not be claimed."}},
	})
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE memory_spaces
			SET lifecycle_state = 'sealed', generation = generation + 1, sealed_at = now(), updated_at = now()
			WHERE id = ?::uuid AND team_id = ?::uuid AND lifecycle_state = 'active'
		`, privateCredential.MemorySpaceID, teamID).Error
	}))

	shared, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID.String(),
		OwnerProfileID: privateCredential.ID.String(),
		Evidence:       []EvidenceInput{{Content: "A current shared placement remains claimable."}},
	})
	require.NoError(t, err)
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID.String(), "private-generation-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, shared.PlacementRunID, claimed.PlacementRunID)
}

func TestLedgerPlacementOutcomesInheritRunMemorySpace(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "placement-outcome-private-space"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	privateCredential := createOwnedCredential(t, credentialRepo, teamID, ownerID, "private-outcome", domain.CredentialBindingCredentialPrivate)
	repo := NewLedgerRepository(appDB, rls)
	created, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:          teamID.String(),
		OwnerProfileID:  privateCredential.ID.String(),
		SpaceID:         privateCredential.MemorySpaceID.String(),
		SpaceGeneration: privateCredential.MemorySpaceGeneration,
		Evidence:        []EvidenceInput{{Content: "Placement outcomes inherit the referenced run space."}},
	})
	require.NoError(t, err)

	outcomeID, err := repo.AppendPlacementOutcome(ctx, PlacementOutcomeInput{
		TeamID:          teamID.String(),
		OwnerProfileID:  privateCredential.ID.String(),
		PlacementRunID:  created.PlacementRunID,
		PlacementItemID: created.Items[0].PlacementItemID,
		OutcomeKind:     "private_space_regression",
		Status:          "recorded",
		Payload:         map[string]any{"verified": true},
	})
	require.NoError(t, err)
	var outcomeSpaceID string
	var outcomeGeneration int64
	require.NoError(t, adminDB.Raw(`
		SELECT space_id::text, space_generation
		FROM placement_outcomes
		WHERE team_id = ?::uuid AND outcome_id = ?::uuid
	`, teamID, outcomeID).Row().Scan(&outcomeSpaceID, &outcomeGeneration))
	assert.Equal(t, privateCredential.MemorySpaceID.String(), outcomeSpaceID)
	assert.Equal(t, privateCredential.MemorySpaceGeneration, outcomeGeneration)

	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID.String(), "private-outcome-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	_, err = repo.FinishPlacementRun(ctx, teamID.String(), claimed.PlacementRunID, "private-outcome-worker", "completed", "")
	require.NoError(t, err)
	var markerSpaceID string
	var markerGeneration int64
	require.NoError(t, adminDB.Raw(`
		SELECT space_id::text, space_generation
		FROM placement_outcomes
		WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		  AND outcome_kind = 'telemetry_first_disposition'
	`, teamID, created.PlacementRunID).Row().Scan(&markerSpaceID, &markerGeneration))
	assert.Equal(t, privateCredential.MemorySpaceID.String(), markerSpaceID)
	assert.Equal(t, privateCredential.MemorySpaceGeneration, markerGeneration)
}
