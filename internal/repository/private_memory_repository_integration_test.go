package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestPrivateMemoryManifestMatchesCatalog(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	repo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, repo.Prepare(context.Background()))
	require.Len(t, repo.ordered, len(PrivateMemoryErasureManifest()))
}

func TestPrivateMemoryManifestMismatchBlocksPrepare(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, adminDB.WithContext(ctx).Exec(`
		CREATE TABLE private_memory_manifest_mismatch (
			id UUID PRIMARY KEY,
			space_id UUID NULL
		);
		GRANT SELECT ON private_memory_manifest_mismatch TO densemem_rls_test
	`).Error)
	defer func() {
		require.NoError(t, adminDB.WithContext(ctx).Exec(`DROP TABLE private_memory_manifest_mismatch`).Error)
	}()

	repo := NewPrivateMemoryRepository(appDB, rls)
	err := repo.Prepare(ctx)
	require.ErrorIs(t, err, ErrPrivateMemoryManifest)
	require.ErrorContains(t, err, "unknown=[private_memory_manifest_mismatch]")
}

func TestPrivateMemoryGenerationTriggerAllowsAuthenticatedSharedWrites(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "private-generation-shared-write")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "shared-writer")
	repo := NewLedgerRepository(appDB, rls)

	created, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "private-generation-shared-write",
		RequestHash:    "private-generation-shared-write-hash",
		Evidence: []EvidenceInput{{
			Content: "A normal authenticated write captures the active shared-space generation.",
		}},
	})
	require.NoError(t, err)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var row struct {
			SpaceGeneration int64
			Generation      int64
			Kind            string
		}
		if err := tx.Raw(`
			SELECT ingest.space_generation, space.generation, space.kind
			FROM knowledge_ingests AS ingest
			JOIN memory_spaces AS space
			  ON space.team_id = ingest.team_id
			 AND space.id = ingest.space_id
			WHERE ingest.ingest_id = ?
		`, created.IngestID).Scan(&row).Error; err != nil {
			return err
		}
		require.Equal(t, "team_shared", row.Kind)
		require.Equal(t, row.Generation, row.SpaceGeneration)
		return nil
	}))
}

func TestPrivateMemoryRetentionSelectsExpiredSpacesAndExcludesLegalHolds(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-retention"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	eligible := createOwnedCredential(t, credentialRepo, teamID, ownerID, "eligible", domain.CredentialBindingCredentialPrivate)
	held := createOwnedCredential(t, credentialRepo, teamID, ownerID, "held", domain.CredentialBindingCredentialPrivate)
	fresh := createOwnedCredential(t, credentialRepo, teamID, ownerID, "fresh", domain.CredentialBindingCredentialPrivate)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	seedPrivateMemoryIngestAt(t, adminDB, rls, teamID, eligible.ID, eligible.MemorySpaceID, "expired private content", now.AddDate(0, 0, -60))
	seedPrivateMemoryIngestAt(t, adminDB, rls, teamID, held.ID, held.MemorySpaceID, "held private content", now.AddDate(0, 0, -60))
	seedPrivateMemoryIngestAt(t, adminDB, rls, teamID, fresh.ID, fresh.MemorySpaceID, "fresh private content", now.AddDate(0, 0, -1))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE memory_spaces
			SET private_content_at = NULL
			WHERE id = ?
		`, eligible.MemorySpaceID).Error
	}))

	repo := NewPrivateMemoryRepository(appDB, rls)
	_, created, err := repo.PlaceLegalHold(ctx, held.MemorySpaceID, "retention_case")
	require.NoError(t, err)
	require.True(t, created)
	run, created, err := repo.RunRetention(ctx, PrivateMemoryRetentionRequest{
		ActorClass:           domain.PrivateMemoryActorControl,
		IdempotencyScopeHash: privateMemoryHash("retention-run", now.String()),
		RequestHash:          privateMemoryHash("retention-run", "30", now.String()),
		RetentionDays:        30,
		Now:                  now,
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, 1, run.QueuedCount)

	operations, err := repo.ListOperations(ctx, 100, 0)
	require.NoError(t, err)
	require.Len(t, operations, 1)
	require.NotNil(t, operations[0].SpaceID)
	require.Equal(t, eligible.MemorySpaceID, *operations[0].SpaceID)
	require.Equal(t, domain.PrivateMemoryRetentionPurge, operations[0].Action)
	require.Equal(t, domain.PrivateMemoryActorRetention, operations[0].ActorClass)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		states := map[uuid.UUID]string{}
		rows, err := tx.Raw(`
			SELECT id, lifecycle_state
			FROM memory_spaces
			WHERE id = ANY(?)
		`, pq.Array([]uuid.UUID{eligible.MemorySpaceID, held.MemorySpaceID, fresh.MemorySpaceID})).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			var state string
			if err := rows.Scan(&id, &state); err != nil {
				return err
			}
			states[id] = state
		}
		require.Equal(t, "sealed", states[eligible.MemorySpaceID])
		require.Equal(t, "active", states[held.MemorySpaceID])
		require.Equal(t, "active", states[fresh.MemorySpaceID])
		return rows.Err()
	}))
}

func TestPrivateMemoryExpiredLeaseIsReclaimedWithNewFence(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-erasure-fence"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "fenced", domain.CredentialBindingCredentialPrivate)
	seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, target.MemorySpaceID, "fenced private content")

	repo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, repo.Prepare(ctx))
	operation, created, err := repo.RequestCredentialErasure(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: target.ID, CredentialID: target.ID,
		IdempotencyScopeHash: privateMemoryHash("fence", target.ID.String()),
		RequestHash:          privateMemoryHash("fence-request", target.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.NoError(t, err)
	require.True(t, created)

	claimTime := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return claimTime }
	first, err := repo.ClaimNext(ctx, "worker-a", time.Minute)
	require.NoError(t, err)
	require.Equal(t, operation.ID, first.ID)

	repo.now = func() time.Time { return claimTime.Add(2 * time.Minute) }
	second, err := repo.ClaimNext(ctx, "worker-b", time.Minute)
	require.NoError(t, err)
	require.Equal(t, operation.ID, second.ID)
	require.Greater(t, second.Fence, first.Fence)
	require.Equal(t, "worker-b", second.WorkerID)

	_, err = repo.ExecuteClaim(ctx, first.ID, first.WorkerID, first.Fence)
	require.ErrorIs(t, err, ErrPrivateMemoryClaimLost)
	completed, err := repo.ExecuteClaim(ctx, second.ID, second.WorkerID, second.Fence)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureCompleted, completed.Status)
}

func TestPrivateMemoryFailureRetriesAreBackedOffAndBounded(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-erasure-retries"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "retry-target", domain.CredentialBindingCredentialPrivate)
	seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, target.MemorySpaceID, "retry private content")

	repo := NewPrivateMemoryRepository(appDB, rls)
	operation, created, err := repo.RequestCredentialErasure(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: target.ID, CredentialID: target.ID,
		IdempotencyScopeHash: privateMemoryHash("retry", target.ID.String()),
		RequestHash:          privateMemoryHash("retry-request", target.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.NoError(t, err)
	require.True(t, created)

	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	for attempt := 1; attempt <= privateMemoryMaximumAttempts; attempt++ {
		repo.now = func() time.Time { return now }
		claim, err := repo.ClaimNext(ctx, "retry-worker", time.Minute)
		require.NoError(t, err)
		require.NotNil(t, claim)
		require.Equal(t, operation.ID, claim.ID)
		require.Equal(t, attempt, claim.AttemptCount)
		require.NoError(t, repo.ReleaseClaim(ctx, claim.ID, claim.WorkerID, claim.Fence, "manifest_mismatch"))

		stored, err := repo.GetOperation(ctx, operation.ID)
		require.NoError(t, err)
		if attempt == privateMemoryMaximumAttempts {
			require.Equal(t, domain.PrivateMemoryErasureFailed, stored.Status)
			require.Nil(t, stored.NextAttemptAt)
			require.NotNil(t, stored.CompletedAt)
			break
		}
		require.Equal(t, domain.PrivateMemoryErasureQueued, stored.Status)
		require.NotNil(t, stored.NextAttemptAt)
		require.Equal(t, now.Add(privateMemoryRetryDelay(attempt)), *stored.NextAttemptAt)
		immediate, err := repo.ClaimNext(ctx, "early-worker", time.Minute)
		require.NoError(t, err)
		require.Nil(t, immediate)
		now = *stored.NextAttemptAt
	}

	repo.now = func() time.Time { return now.Add(24 * time.Hour) }
	claim, err := repo.ClaimNext(ctx, "late-worker", time.Minute)
	require.NoError(t, err)
	require.Nil(t, claim)

	_, _, err = repo.DisableSSOCredential(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: target.ID,
		IdempotencyScopeHash: privateMemoryHash("retry-incompatible-retire", target.ID.String()),
		RequestHash:          privateMemoryHash("retry-incompatible-retire-request", target.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.ErrorIs(t, err, ErrPrivateMemoryOperationConflict)
	var credentialStatus string
	require.NoError(t, adminDB.Raw(`SELECT status FROM credentials WHERE id = ?`, target.ID).Row().Scan(&credentialStatus))
	require.Equal(t, "active", credentialStatus)

	recovery, created, err := repo.RequestCredentialErasure(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: target.ID, CredentialID: target.ID,
		IdempotencyScopeHash: privateMemoryHash("retry-recovery", target.ID.String()),
		RequestHash:          privateMemoryHash("retry-recovery-request", target.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, operation.ID, recovery.ID)
	require.Equal(t, domain.PrivateMemoryErasureQueued, recovery.Status)
	require.Equal(t, operation.Action, recovery.Action)
	require.Equal(t, operation.TargetGeneration, recovery.TargetGeneration)
	require.Equal(t, operation.RetireSpace, recovery.RetireSpace)

	original, err := repo.GetOperation(ctx, operation.ID)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureFailed, original.Status)

	claim, err = repo.ClaimNext(ctx, "recovery-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.Equal(t, recovery.ID, claim.ID)
	completed, err := repo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureCompleted, completed.Status)

	var lifecycle domain.MemorySpaceLifecycleState
	var generation int64
	require.NoError(t, adminDB.Raw(`
		SELECT lifecycle_state, generation
		FROM memory_spaces
		WHERE id = ?
	`, target.MemorySpaceID).Row().Scan(&lifecycle, &generation))
	require.Equal(t, domain.MemorySpaceActive, lifecycle)
	require.Equal(t, *operation.TargetGeneration+1, generation)

	retireOwnerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	retireTarget := createOwnedCredential(t, credentialRepo, teamID, retireOwnerID, "retry-retire-target", domain.CredentialBindingCredentialPrivate)
	seedPrivateMemoryIngest(t, adminDB, rls, teamID, retireTarget.ID, retireTarget.MemorySpaceID, "retry retire private content")
	retirement, created, err := repo.DisableSSOCredential(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: retireOwnerID, CredentialID: retireTarget.ID,
		IdempotencyScopeHash: privateMemoryHash("retry-retire", retireTarget.ID.String()),
		RequestHash:          privateMemoryHash("retry-retire-request", retireTarget.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.True(t, retirement.RetireSpace)

	for attempt := 1; attempt <= privateMemoryMaximumAttempts; attempt++ {
		claim, err = repo.ClaimNext(ctx, "retirement-worker", time.Minute)
		require.NoError(t, err)
		require.NotNil(t, claim)
		require.Equal(t, retirement.ID, claim.ID)
		require.NoError(t, repo.ReleaseClaim(ctx, claim.ID, claim.WorkerID, claim.Fence, "database_error"))
		if attempt < privateMemoryMaximumAttempts {
			stored, getErr := repo.GetOperation(ctx, retirement.ID)
			require.NoError(t, getErr)
			require.NotNil(t, stored.NextAttemptAt)
			repo.now = func() time.Time { return *stored.NextAttemptAt }
		}
	}

	retirementRecovery, created, err := repo.RequestControlErasure(
		ctx,
		retireTarget.MemorySpaceID,
		privateMemoryHash("retry-retire-control", retireTarget.ID.String()),
		privateMemoryHash("retry-retire-control-request", retireTarget.ID.String()),
		"operator_retry",
	)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, domain.PrivateMemoryRetireCredential, retirementRecovery.Action)
	require.Equal(t, retirement.TargetCredentialID, retirementRecovery.TargetCredentialID)
	require.Equal(t, retirement.TargetGeneration, retirementRecovery.TargetGeneration)
	require.True(t, retirementRecovery.RetireSpace)

	claim, err = repo.ClaimNext(ctx, "retirement-recovery-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)
	retired, err := repo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureCompleted, retired.Status)
	require.NoError(t, adminDB.Raw(`
		SELECT lifecycle_state, generation
		FROM memory_spaces
		WHERE id = ?
	`, retireTarget.MemorySpaceID).Row().Scan(&lifecycle, &generation))
	require.Equal(t, domain.MemorySpaceRetired, lifecycle)
	require.Equal(t, *retirement.TargetGeneration+1, generation)
}

func TestPrivateMemoryMissingRetiredCredentialLosesClaim(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-erasure-missing-credential"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "missing-target", domain.CredentialBindingCredentialPrivate)
	seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, target.MemorySpaceID, "missing target content")

	repo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, repo.Prepare(ctx))
	operation, created, err := repo.DisableSSOCredential(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: target.ID,
		IdempotencyScopeHash: privateMemoryHash("missing-credential", target.ID.String()),
		RequestHash:          privateMemoryHash("missing-credential-request", target.ID.String()),
		ReasonCode:           "credential_deleted",
	})
	require.NoError(t, err)
	require.True(t, created)
	claim, err := repo.ClaimNext(ctx, "missing-credential-worker", time.Minute)
	require.NoError(t, err)
	require.Equal(t, operation.ID, claim.ID)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE private_memory_erasure_operations
			SET target_credential_id = ?
			WHERE id = ?
		`, uuid.New(), operation.ID).Error
	}))
	_, err = repo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.ErrorIs(t, err, ErrPrivateMemoryClaimLost)
}

func TestPrivateMemoryCredentialErasureIsHeldIdempotentAndExact(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-erasure"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "target", domain.CredentialBindingCredentialPrivate)
	other := createOwnedCredential(t, credentialRepo, teamID, ownerID, "other", domain.CredentialBindingCredentialPrivate)
	sharedSpaceID, err := credentialRepo.GetTeamSharedSpaceID(ctx, teamID)
	require.NoError(t, err)

	targetIngest := seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, target.MemorySpaceID, "target private content")
	otherIngest := seedPrivateMemoryIngest(t, adminDB, rls, teamID, other.ID, other.MemorySpaceID, "other private content")
	sharedIngest := seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, sharedSpaceID, "shared content")
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO audit_log (
				team_id, operation, entity_type, entity_id, before_payload, after_payload,
				metadata, memory_space_id
			) VALUES (?, 'PRIVATE_WRITE', 'memory', ?, '{"content":"before"}'::jsonb,
			          '{"content":"after"}'::jsonb, '{"source":"test"}'::jsonb, ?)
		`, teamID, targetIngest.String(), target.MemorySpaceID).Error
	}))

	repo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, repo.Prepare(ctx))
	hold, created, err := repo.PlaceLegalHold(ctx, target.MemorySpaceID, "investigation")
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, target.MemorySpaceID, hold.SpaceID)

	request := PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: target.ID, CredentialID: target.ID,
		IdempotencyScopeHash: privateMemoryHash("owner", target.ID.String(), "key-1"),
		RequestHash:          privateMemoryHash("erase", target.ID.String()),
		ReasonCode:           "owner_request",
	}
	_, _, err = repo.RequestCredentialErasure(ctx, request)
	require.ErrorIs(t, err, ErrPrivateMemoryLegalHold)

	_, released, err := repo.ReleaseLegalHold(ctx, target.MemorySpaceID)
	require.NoError(t, err)
	require.True(t, released)
	operation, created, err := repo.RequestCredentialErasure(ctx, request)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, domain.PrivateMemoryErasureQueued, operation.Status)
	require.NotNil(t, operation.TargetGeneration)
	ownerIdentityOperation, err := repo.GetOwnerOperation(ctx, teamID, operation.ID, &ownerID, nil)
	require.NoError(t, err)
	require.NotNil(t, ownerIdentityOperation)
	require.Equal(t, operation.ID, ownerIdentityOperation.ID)
	ownerCredentialOperation, err := repo.GetOwnerOperation(ctx, teamID, operation.ID, nil, &target.ID)
	require.NoError(t, err)
	require.NotNil(t, ownerCredentialOperation)
	require.Equal(t, operation.ID, ownerCredentialOperation.ID)
	nonOwnerIdentityID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	nonOwnerIdentityOperation, err := repo.GetOwnerOperation(ctx, teamID, operation.ID, &nonOwnerIdentityID, nil)
	require.NoError(t, err)
	require.Nil(t, nonOwnerIdentityOperation)
	nonOwnerCredentialOperation, err := repo.GetOwnerOperation(ctx, teamID, operation.ID, nil, &other.ID)
	require.NoError(t, err)
	require.Nil(t, nonOwnerCredentialOperation)
	foreignTeamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-erasure-foreign"))
	foreignOperation, err := repo.GetOwnerOperation(ctx, foreignTeamID, operation.ID, &ownerID, nil)
	require.NoError(t, err)
	require.Nil(t, foreignOperation)

	replay, created, err := repo.RequestCredentialErasure(ctx, request)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, operation.ID, replay.ID)
	conflicting := request
	conflicting.RequestHash = privateMemoryHash("different")
	_, _, err = repo.RequestCredentialErasure(ctx, conflicting)
	require.ErrorIs(t, err, ErrPrivateMemoryIdempotency)

	claim, err := repo.ClaimNext(ctx, "private-test-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.Equal(t, operation.ID, claim.ID)
	_, _, err = repo.PlaceLegalHold(ctx, target.MemorySpaceID, "late_hold")
	require.NoError(t, err)
	_, err = repo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.ErrorIs(t, err, ErrPrivateMemoryLegalHold)
	require.NoError(t, repo.ReleaseClaim(ctx, claim.ID, claim.WorkerID, claim.Fence, "legal_hold"))
	_, released, err = repo.ReleaseLegalHold(ctx, target.MemorySpaceID)
	require.NoError(t, err)
	require.True(t, released)

	retrying, err := repo.GetOperation(ctx, operation.ID)
	require.NoError(t, err)
	require.NotNil(t, retrying)
	require.NotNil(t, retrying.NextAttemptAt)
	repo.now = func() time.Time { return *retrying.NextAttemptAt }
	claim, err = repo.ClaimNext(ctx, "private-test-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)
	completed, err := repo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureCompleted, completed.Status)
	require.Contains(t, completed.DeletedCounts, "knowledge_ingests")
	require.Equal(t, int64(1), completed.DeletedCounts["knowledge_ingests"])

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		for _, fixture := range []struct {
			id      uuid.UUID
			exists  bool
			message string
		}{
			{targetIngest, false, "target private ingest survived"},
			{otherIngest, true, "other private ingest was deleted"},
			{sharedIngest, true, "team-shared ingest was deleted"},
		} {
			var exists bool
			if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM knowledge_ingests WHERE ingest_id = ?)`, fixture.id).Scan(&exists).Error; err != nil {
				return err
			}
			require.Equal(t, fixture.exists, exists, fixture.message)
		}
		var state string
		var generation int64
		if err := tx.Raw(`SELECT lifecycle_state, generation FROM memory_spaces WHERE id = ?`, target.MemorySpaceID).Row().Scan(&state, &generation); err != nil {
			return err
		}
		require.Equal(t, "active", state)
		require.Equal(t, int64(2), generation)
		var credentialStatus string
		if err := tx.Raw(`SELECT status FROM credentials WHERE id = ?`, target.ID).Row().Scan(&credentialStatus); err != nil {
			return err
		}
		require.Equal(t, "active", credentialStatus)
		var beforePayload, afterPayload any
		var metadata string
		if err := tx.Raw(`
			SELECT before_payload, after_payload, metadata::text
			FROM audit_log WHERE memory_space_id = ?
		`, target.MemorySpaceID).Row().Scan(&beforePayload, &afterPayload, &metadata); err != nil {
			return err
		}
		require.Nil(t, beforePayload)
		require.Nil(t, afterPayload)
		require.JSONEq(t, privateMemoryAuditMetadataJSON, metadata)
		return nil
	}))
}

func TestPrivateMemoryIdempotencyConcurrentReplay(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-erasure-concurrent-idempotency"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "concurrent", domain.CredentialBindingCredentialPrivate)
	repo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, repo.Prepare(ctx))
	request := PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: target.ID,
		IdempotencyScopeHash: privateMemoryHash("concurrent", teamID.String(), target.ID.String()),
		RequestHash:          privateMemoryHash("erase", target.ID.String()),
		ReasonCode:           "owner_request",
	}

	start := make(chan struct{})
	results := make(chan struct {
		operation *domain.PrivateMemoryErasureOperation
		created   bool
		err       error
	}, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			operation, created, err := repo.RequestCredentialErasure(ctx, request)
			results <- struct {
				operation *domain.PrivateMemoryErasureOperation
				created   bool
				err       error
			}{operation: operation, created: created, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)

	var operationID uuid.UUID
	createdCount := 0
	for result := range results {
		require.NoError(t, result.err)
		require.NotNil(t, result.operation)
		if result.created {
			createdCount++
		}
		if operationID == uuid.Nil {
			operationID = result.operation.ID
		}
		require.Equal(t, operationID, result.operation.ID)
	}
	require.Equal(t, 1, createdCount)
}

func TestPrivateMemorySSOCredentialDeleteRetiresOnlyCredentialPrivateSpace(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "sso-credential-private-erasure"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "isolated", domain.CredentialBindingCredentialPrivate)
	profile := createOwnedCredential(t, credentialRepo, teamID, ownerID, "profile", domain.CredentialBindingProfilePrivate)
	shared := createOwnedCredential(t, credentialRepo, teamID, ownerID, "shared", domain.CredentialBindingSharedOnly)
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
		IdempotencyScopeHash: privateMemoryHash("sso-delete", ownerID.String(), "isolated-key"),
		RequestHash:          privateMemoryHash("delete", target.ID.String()),
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
	require.Equal(t, "active", membershipStatus)

	for _, credentialID := range []uuid.UUID{profile.ID, shared.ID} {
		preserveRequest := PrivateMemoryErasureRequest{
			TeamID: teamID, OwnerID: ownerID, CredentialID: credentialID,
			IdempotencyScopeHash: privateMemoryHash("sso-delete", ownerID.String(), credentialID.String()),
			RequestHash:          privateMemoryHash("delete", credentialID.String()),
			ReasonCode:           "credential_deleted",
		}
		preservedOperation, wasCreated, err := repo.DisableSSOCredential(ctx, preserveRequest)
		require.NoError(t, err)
		require.True(t, wasCreated)
		require.Equal(t, domain.PrivateMemoryErasureCompleted, preservedOperation.Status)
		require.Nil(t, preservedOperation.SpaceID)
	}

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
		var statuses []string
		if err := tx.Raw(`SELECT status FROM credentials WHERE id = ANY(?) ORDER BY id`, pq.Array([]uuid.UUID{target.ID, profile.ID, shared.ID})).Scan(&statuses).Error; err != nil {
			return err
		}
		require.Len(t, statuses, 3)
		for _, status := range statuses {
			require.Equal(t, "disabled", status)
		}
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
}

func TestPrivateMemorySSOCredentialDeletePreservesSharedActorMembership(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "sso-delete-shared-actor"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "shared-actor-target", domain.CredentialBindingSharedOnly)
	siblingID := uuid.New()
	siblingPrefix := "dm_" + siblingID.String()[:20]
	now := time.Now().UTC()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO credentials (
				id, actor_identity_id, owner_identity_id, team_id, kind, key_hash, key_prefix,
				key_suffix, name, scopes, rate_limit, status, memory_binding, memory_space_id,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, 'api_key', ?, ?, 'suffix', 'shared-actor-sibling',
				ARRAY['read','write']::text[], 60, 'active', 'shared_only',
				(SELECT id FROM memory_spaces WHERE team_id = ? AND kind = 'team_shared'), ?, ?)
		`, siblingID, target.ActorIdentityID, ownerID, teamID, "hash-"+siblingID.String(), siblingPrefix, teamID, now, now).Error
	}))

	repo := NewPrivateMemoryRepository(appDB, rls)
	_, created, err := repo.DisableSSOCredential(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: target.ID,
		IdempotencyScopeHash: privateMemoryHash("shared-actor-target", target.ID.String()),
		RequestHash:          privateMemoryHash("shared-actor-target-request", target.ID.String()),
		ReasonCode:           "credential_deleted",
	})
	require.NoError(t, err)
	require.True(t, created)
	var membershipStatus string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status
			FROM team_memberships
			WHERE actor_identity_id = ? AND team_id = ?
		`, target.ActorIdentityID, teamID).Row().Scan(&membershipStatus)
	}))
	require.Equal(t, "active", membershipStatus)

	_, created, err = repo.DisableSSOCredential(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: siblingID,
		IdempotencyScopeHash: privateMemoryHash("shared-actor-sibling", siblingID.String()),
		RequestHash:          privateMemoryHash("shared-actor-sibling-request", siblingID.String()),
		ReasonCode:           "credential_deleted",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status
			FROM team_memberships
			WHERE actor_identity_id = ? AND team_id = ?
		`, target.ActorIdentityID, teamID).Row().Scan(&membershipStatus)
	}))
	require.Equal(t, "revoked", membershipStatus)
}

func seedPrivateMemoryIngest(t *testing.T, db *gorm.DB, rls interface {
	WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
}, teamID, ownerID, spaceID uuid.UUID, content string) uuid.UUID {
	t.Helper()
	return seedPrivateMemoryIngestAt(t, db, rls, teamID, ownerID, spaceID, content, time.Now().UTC())
}

func seedPrivateMemoryIngestAt(t *testing.T, db *gorm.DB, rls interface {
	WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
}, teamID, ownerID, spaceID uuid.UUID, content string, createdAt time.Time) uuid.UUID {
	t.Helper()
	ingestID := uuid.New()
	err := rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, request_hash, source_summary,
				status, proposal, metadata, space_id, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, 'queued', '{}'::jsonb, '{}'::jsonb, ?, ?, ?)
		`, teamID, ingestID, ownerID, "hash-"+ingestID.String(), content, spaceID, createdAt, createdAt).Error
	})
	require.NoError(t, err)
	return ingestID
}
