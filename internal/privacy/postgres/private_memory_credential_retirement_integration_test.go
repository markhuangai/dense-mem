//go:build integration

package postgres

import (
	"context"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestPrivateMemoryCredentialRetirementUpgradesActiveErasure(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-erasure-active-retirement"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := accesspostgres.NewCredentialRepository(appDB, rls, nil)
	repo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, repo.Prepare(ctx))

	queuedTarget := createOwnedCredential(t, credentialRepo, teamID, ownerID, "active-queued")
	seedPrivateMemoryIngest(t, adminDB, rls, teamID, queuedTarget.ID, queuedTarget.MemorySpaceID, "active queued private content")
	queuedErase, created, err := repo.RequestCredentialErasure(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: queuedTarget.ID, CredentialID: queuedTarget.ID,
		IdempotencyScopeHash: Hash("active-queued-erase", queuedTarget.ID.String()),
		RequestHash:          Hash("active-queued-erase-request", queuedTarget.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, domain.PrivateMemoryErasureQueued, queuedErase.Status)

	queuedRetirement, created, err := repo.DisableSSOCredential(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: queuedTarget.ID,
		IdempotencyScopeHash: Hash("active-queued-retirement", queuedTarget.ID.String()),
		RequestHash:          Hash("active-queued-retirement-request", queuedTarget.ID.String()),
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
	queuedRetirementReplay, replayCreated, err := repo.DisableSSOCredential(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: queuedTarget.ID,
		IdempotencyScopeHash: Hash("active-queued-retirement", queuedTarget.ID.String()),
		RequestHash:          Hash("active-queued-retirement-request", queuedTarget.ID.String()),
		ReasonCode:           "credential_deleted",
	})
	require.NoError(t, err)
	require.False(t, replayCreated)
	require.Equal(t, queuedRetirement.ID, queuedRetirementReplay.ID)
	originalEraseReplay, replayCreated, err := repo.RequestCredentialErasure(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: queuedTarget.ID, CredentialID: queuedTarget.ID,
		IdempotencyScopeHash: Hash("active-queued-erase", queuedTarget.ID.String()),
		RequestHash:          Hash("active-queued-erase-request", queuedTarget.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.NoError(t, err)
	require.False(t, replayCreated)
	require.Equal(t, queuedErase.ID, originalEraseReplay.ID)

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

	processingTarget := createOwnedCredential(t, credentialRepo, teamID, ownerID, "active-processing")
	seedPrivateMemoryIngest(t, adminDB, rls, teamID, processingTarget.ID, processingTarget.MemorySpaceID, "active processing private content")
	processingErase, created, err := repo.RequestCredentialErasure(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: processingTarget.ID, CredentialID: processingTarget.ID,
		IdempotencyScopeHash: Hash("active-processing-erase", processingTarget.ID.String()),
		RequestHash:          Hash("active-processing-erase-request", processingTarget.ID.String()),
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
		IdempotencyScopeHash: Hash("active-processing-retirement", processingTarget.ID.String()),
		RequestHash:          Hash("active-processing-retirement-request", processingTarget.ID.String()),
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
	processingRetirementReplay, replayCreated, err := repo.DisableSSOCredential(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: processingTarget.ID,
		IdempotencyScopeHash: Hash("active-processing-retirement", processingTarget.ID.String()),
		RequestHash:          Hash("active-processing-retirement-request", processingTarget.ID.String()),
		ReasonCode:           "credential_deleted",
	})
	require.NoError(t, err)
	require.False(t, replayCreated)
	require.Equal(t, processingRetirement.ID, processingRetirementReplay.ID)

	processingCompleted, err := repo.ExecuteClaim(ctx, processingClaim.ID, processingClaim.WorkerID, processingClaim.Fence)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryRetireCredential, processingCompleted.Action)
	require.Equal(t, domain.PrivateMemoryErasureCompleted, processingCompleted.Status)
	require.NoError(t, adminDB.Raw(`SELECT lifecycle_state FROM memory_spaces WHERE id = ?`, processingTarget.MemorySpaceID).Row().Scan(&lifecycle))
	require.Equal(t, domain.MemorySpaceRetired, lifecycle)
}

func TestPrivateMemorySSOCredentialDeleteRetiresOnlyCredentialPrivateSpace(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "sso-credential-private-erasure"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := accesspostgres.NewCredentialRepository(appDB, rls, nil)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "isolated")
	profile := createOwnedCredential(t, credentialRepo, teamID, ownerID, "profile")
	shared := createOwnedCredential(t, credentialRepo, teamID, ownerID, "shared")
	sharedSpaceID, err := credentialRepo.GetTeamSharedSpaceID(ctx, teamID)
	require.NoError(t, err)

	targetIngest := seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, target.MemorySpaceID, "isolated content")
	profileIngest := seedPrivateMemoryIngest(t, adminDB, rls, teamID, profile.ID, profile.MemorySpaceID, "profile content")
	sharedIngest := seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, sharedSpaceID, "shared content")

	repo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, repo.Prepare(ctx))
	_, createdHold, err := repo.PlaceLegalHold(ctx, target.MemorySpaceID, "credential_delete_hold")
	require.NoError(t, err)
	require.True(t, createdHold)
	request := PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: target.ID,
		IdempotencyScopeHash: Hash("sso-delete", ownerID.String(), "isolated-key"),
		RequestHash:          Hash("delete", target.ID.String()),
		ReasonCode:           "credential_deleted",
	}
	operation, created, err := repo.DisableSSOCredential(ctx, request)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, domain.PrivateMemoryErasureQueued, operation.Status)
	var disabledStatus string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT status FROM credentials WHERE id = ?`, target.ID).Row().Scan(&disabledStatus)
	}))
	require.Equal(t, "disabled", disabledStatus)
	claimWhileHeld, err := repo.ClaimNext(ctx, "sso-delete-held-worker", time.Minute)
	require.NoError(t, err)
	require.Nil(t, claimWhileHeld)
	_, releasedHold, err := repo.ReleaseLegalHold(ctx, target.MemorySpaceID)
	require.NoError(t, err)
	require.True(t, releasedHold)

	lockAcquired := make(chan error, 1)
	releaseLock := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockErr := rls.WithSystemTx(context.Background(), appDB, func(tx *gorm.DB) error {
			var lockedID uuid.UUID
			if err := tx.Raw(`
				SELECT id
				FROM private_memory_erasure_operations
				WHERE id = ?
				FOR UPDATE
			`, operation.ID).Row().Scan(&lockedID); err != nil {
				return err
			}
			lockAcquired <- nil
			<-releaseLock
			return nil
		})
		if lockErr != nil {
			select {
			case lockAcquired <- lockErr:
			default:
			}
		}
		lockDone <- lockErr
	}()
	require.NoError(t, <-lockAcquired)
	lockReleased := false
	defer func() {
		if !lockReleased {
			close(releaseLock)
			<-lockDone
		}
	}()

	replayCtx, cancelReplay := context.WithTimeout(ctx, 2*time.Second)
	defer cancelReplay()
	replay, created, err := repo.DisableSSOCredential(replayCtx, request)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, operation.ID, replay.ID)
	close(releaseLock)
	lockReleased = true
	require.NoError(t, <-lockDone)
	claim, err := repo.ClaimNext(ctx, "sso-delete-worker", time.Minute)
	require.NoError(t, err)
	completed, err := repo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureCompleted, completed.Status)
	var membershipStatus string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status
			FROM team_memberships
			WHERE actor_identity_id = ? AND team_id = ?
		`, target.ID, teamID).Row().Scan(&membershipStatus)
	}))
	require.Equal(t, "revoked", membershipStatus)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		for _, fixture := range []struct {
			id     uuid.UUID
			exists bool
		}{
			{targetIngest, false},
			{profileIngest, true},
			{sharedIngest, true},
		} {
			var exists bool
			if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM knowledge_ingests WHERE ingest_id = ?)`, fixture.id).Row().Scan(&exists); err != nil {
				return err
			}
			require.Equal(t, fixture.exists, exists)
		}
		var lifecycle string
		if err := tx.Raw(`SELECT lifecycle_state FROM memory_spaces WHERE id = ?`, target.MemorySpaceID).Row().Scan(&lifecycle); err != nil {
			return err
		}
		require.Equal(t, "retired", lifecycle)
		var profileLifecycle string
		if err := tx.Raw(`SELECT lifecycle_state FROM memory_spaces WHERE id = ?`, profile.MemorySpaceID).Row().Scan(&profileLifecycle); err != nil {
			return err
		}
		require.Equal(t, "active", profileLifecycle)
		var activeCredentials int
		if err := tx.Raw(`SELECT count(*) FROM credentials WHERE id = ANY(?) AND status='active'`, pq.Array([]uuid.UUID{profile.ID, shared.ID})).Row().Scan(&activeCredentials); err != nil {
			return err
		}
		require.Equal(t, 2, activeCredentials)

		if err := tx.Raw(`
			SELECT status
			FROM team_memberships
			WHERE actor_identity_id = ? AND team_id = ?
		`, target.ID, teamID).Row().Scan(&membershipStatus); err != nil {
			return err
		}
		require.Equal(t, "revoked", membershipStatus)
		return nil
	}))

	for _, credentialID := range []uuid.UUID{profile.ID, shared.ID} {
		deleteRequest := PrivateMemoryErasureRequest{
			TeamID: teamID, OwnerID: ownerID, CredentialID: credentialID,
			IdempotencyScopeHash: Hash("sso-delete", ownerID.String(), credentialID.String()),
			RequestHash:          Hash("delete", credentialID.String()),
			ReasonCode:           "credential_deleted",
		}
		deletedOperation, wasCreated, err := repo.DisableSSOCredential(ctx, deleteRequest)
		require.NoError(t, err)
		require.True(t, wasCreated)
		require.Equal(t, domain.PrivateMemoryErasureQueued, deletedOperation.Status)
		require.NotNil(t, deletedOperation.SpaceID)
		claim, err := repo.ClaimNext(ctx, "sso-delete-other-worker", time.Minute)
		require.NoError(t, err)
		require.NotNil(t, claim)
		require.Equal(t, deletedOperation.ID, claim.ID)
		completed, err := repo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
		require.NoError(t, err)
		require.Equal(t, domain.PrivateMemoryErasureCompleted, completed.Status)
	}

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var privateExists, sharedExists bool
		if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM knowledge_ingests WHERE ingest_id=?), EXISTS(SELECT 1 FROM knowledge_ingests WHERE ingest_id=?)`, profileIngest, sharedIngest).Row().Scan(&privateExists, &sharedExists); err != nil {
			return err
		}
		require.False(t, privateExists)
		require.True(t, sharedExists)
		return nil
	}))
}
