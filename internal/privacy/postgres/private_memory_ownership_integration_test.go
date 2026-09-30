//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestPrivateMemoryStaleReleaseCannotRequeueReclaimedLease(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-erasure-stale-release"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := accesspostgres.NewCredentialRepository(appDB, rls, nil)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "stale-release", domain.CredentialBindingCredentialPrivate)
	repo := NewPrivateMemoryRepository(appDB, rls)
	operation, created, err := repo.RequestCredentialErasure(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: target.ID, CredentialID: target.ID,
		IdempotencyScopeHash: Hash("stale-release", target.ID.String()),
		RequestHash:          Hash("stale-release-request", target.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.NoError(t, err)
	require.True(t, created)

	claimedAt := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	repo.SetNow(func() time.Time { return claimedAt })
	first, err := repo.ClaimNext(ctx, "worker-a", time.Minute)
	require.NoError(t, err)
	require.Equal(t, operation.ID, first.ID)
	require.Equal(t, 1, first.AttemptCount)

	reclaimedAt := claimedAt.Add(2 * time.Minute)
	repo.SetNow(func() time.Time { return reclaimedAt })
	second, err := repo.ClaimNext(ctx, "worker-b", time.Minute)
	require.NoError(t, err)
	require.Equal(t, operation.ID, second.ID)
	require.Equal(t, 2, second.AttemptCount)
	require.Greater(t, second.Fence, first.Fence)

	err = repo.ReleaseClaim(ctx, first.ID, first.WorkerID, first.Fence, "stale_error")
	require.ErrorIs(t, err, ErrPrivateMemoryClaimLost)
	stored, err := repo.GetOperation(ctx, operation.ID)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureProcessing, stored.Status)
	require.Equal(t, second.WorkerID, stored.WorkerID)
	require.Equal(t, second.Fence, stored.Fence)
	require.Equal(t, 2, stored.AttemptCount)
	require.Empty(t, stored.LastErrorCode)

	require.NoError(t, repo.ReleaseClaim(ctx, second.ID, second.WorkerID, second.Fence, "manifest_mismatch"))
	stored, err = repo.GetOperation(ctx, operation.ID)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureQueued, stored.Status)
	require.Equal(t, "manifest_mismatch", stored.LastErrorCode)
	require.NotNil(t, stored.NextAttemptAt)
	require.Equal(t, reclaimedAt.Add(2*time.Second), *stored.NextAttemptAt)
}

func TestPrivateMemoryCredentialRetirementOperationHashesStayStable(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-credential-delete-hashes"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := accesspostgres.NewCredentialRepository(appDB, rls, NewCredentialDeletionRepository(appDB, rls))
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "delete-hashes", domain.CredentialBindingCredentialPrivate)

	rows, err := credentialRepo.DeleteForTeam(ctx, teamID, target.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	operations, err := NewPrivateMemoryRepository(appDB, rls).ListOperations(ctx, 10, 0)
	require.NoError(t, err)
	require.Len(t, operations, 1)
	require.Equal(t, domain.PrivateMemoryRetireCredential, operations[0].Action)
	require.Equal(t, target.ID, *operations[0].TargetCredentialID)

	scopeHash, requestHash := storedPrivateMemoryOperationHashes(t, adminDB, rls, operations[0].ID)
	require.Equal(t, Hash("team-credential-delete", teamID.String(), target.ID.String()), scopeHash)
	require.Equal(t, Hash("retire-credential", teamID.String(), target.ID.String()), requestHash)
}

func storedPrivateMemoryOperationHashes(t *testing.T, db *gorm.DB, rls *storagepostgres.RLS, operationID uuid.UUID) (string, string) {
	t.Helper()
	var scopeHash, requestHash, mappedRequestHash string
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT operation.idempotency_scope_hash, operation.request_hash, mapping.request_hash
			FROM private_memory_erasure_operations AS operation
			JOIN private_memory_erasure_idempotency_keys AS mapping
			  ON mapping.idempotency_scope_hash = operation.idempotency_scope_hash
			 AND mapping.operation_id = operation.id
			WHERE operation.id = ?
		`, operationID).Row().Scan(&scopeHash, &requestHash, &mappedRequestHash)
	}))
	require.Equal(t, requestHash, mappedRequestHash)
	return scopeHash, requestHash
}
