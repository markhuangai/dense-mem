package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestPrivateMemoryCredentialRetirementUpgradesActiveErasure(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-erasure-active-retirement"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	repo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, repo.Prepare(ctx))

	queuedTarget := createOwnedCredential(t, credentialRepo, teamID, ownerID, "active-queued", domain.CredentialBindingCredentialPrivate)
	seedPrivateMemoryIngest(t, adminDB, rls, teamID, queuedTarget.ID, queuedTarget.MemorySpaceID, "active queued private content")
	queuedErase, created, err := repo.RequestCredentialErasure(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: queuedTarget.ID, CredentialID: queuedTarget.ID,
		IdempotencyScopeHash: privateMemoryHash("active-queued-erase", queuedTarget.ID.String()),
		RequestHash:          privateMemoryHash("active-queued-erase-request", queuedTarget.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, domain.PrivateMemoryErasureQueued, queuedErase.Status)

	queuedRetirement, created, err := repo.DisableSSOCredential(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: queuedTarget.ID,
		IdempotencyScopeHash: privateMemoryHash("active-queued-retirement", queuedTarget.ID.String()),
		RequestHash:          privateMemoryHash("active-queued-retirement-request", queuedTarget.ID.String()),
		ReasonCode:           "credential_deleted",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, queuedErase.ID, queuedRetirement.ID)
	require.Equal(t, domain.PrivateMemoryRetireCredential, queuedRetirement.Action)
	require.Equal(t, queuedTarget.ID, *queuedRetirement.TargetCredentialID)
	require.Equal(t, queuedErase.TargetGeneration, queuedRetirement.TargetGeneration)
	require.Equal(t, queuedErase.Fence, queuedRetirement.Fence)
	require.Equal(t, queuedErase.WorkerID, queuedRetirement.WorkerID)
	require.Equal(t, queuedErase.LeaseUntil, queuedRetirement.LeaseUntil)
	require.True(t, queuedRetirement.RetireSpace)

	queuedClaim, err := repo.ClaimNext(ctx, "active-queued-retirement-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, queuedClaim)
	require.Equal(t, queuedRetirement.ID, queuedClaim.ID)
	queuedCompleted, err := repo.ExecuteClaim(ctx, queuedClaim.ID, queuedClaim.WorkerID, queuedClaim.Fence)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryRetireCredential, queuedCompleted.Action)
	require.Equal(t, domain.PrivateMemoryErasureCompleted, queuedCompleted.Status)

	var lifecycle domain.MemorySpaceLifecycleState
	require.NoError(t, adminDB.Raw(`SELECT lifecycle_state FROM memory_spaces WHERE id = ?`, queuedTarget.MemorySpaceID).Row().Scan(&lifecycle))
	require.Equal(t, domain.MemorySpaceRetired, lifecycle)

	processingTarget := createOwnedCredential(t, credentialRepo, teamID, ownerID, "active-processing", domain.CredentialBindingCredentialPrivate)
	seedPrivateMemoryIngest(t, adminDB, rls, teamID, processingTarget.ID, processingTarget.MemorySpaceID, "active processing private content")
	processingErase, created, err := repo.RequestCredentialErasure(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: processingTarget.ID, CredentialID: processingTarget.ID,
		IdempotencyScopeHash: privateMemoryHash("active-processing-erase", processingTarget.ID.String()),
		RequestHash:          privateMemoryHash("active-processing-erase-request", processingTarget.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.NoError(t, err)
	require.True(t, created)
	processingClaim, err := repo.ClaimNext(ctx, "active-processing-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, processingClaim)
	require.Equal(t, processingErase.ID, processingClaim.ID)

	processingRetirement, created, err := repo.DisableSSOCredential(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: processingTarget.ID,
		IdempotencyScopeHash: privateMemoryHash("active-processing-retirement", processingTarget.ID.String()),
		RequestHash:          privateMemoryHash("active-processing-retirement-request", processingTarget.ID.String()),
		ReasonCode:           "credential_deleted",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, processingErase.ID, processingRetirement.ID)
	require.Equal(t, domain.PrivateMemoryRetireCredential, processingRetirement.Action)
	require.Equal(t, processingTarget.ID, *processingRetirement.TargetCredentialID)
	require.Equal(t, processingErase.TargetGeneration, processingRetirement.TargetGeneration)
	require.Equal(t, processingClaim.Fence, processingRetirement.Fence)
	require.Equal(t, processingClaim.WorkerID, processingRetirement.WorkerID)
	require.Equal(t, processingClaim.LeaseUntil, processingRetirement.LeaseUntil)
	require.Equal(t, processingClaim.AttemptCount, processingRetirement.AttemptCount)
	require.True(t, processingRetirement.RetireSpace)

	processingCompleted, err := repo.ExecuteClaim(ctx, processingClaim.ID, processingClaim.WorkerID, processingClaim.Fence)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryRetireCredential, processingCompleted.Action)
	require.Equal(t, domain.PrivateMemoryErasureCompleted, processingCompleted.Status)
	require.NoError(t, adminDB.Raw(`SELECT lifecycle_state FROM memory_spaces WHERE id = ?`, processingTarget.MemorySpaceID).Row().Scan(&lifecycle))
	require.Equal(t, domain.MemorySpaceRetired, lifecycle)
}
