//go:build integration

package postgres

import (
	"context"
	"database/sql"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestPrivateMemoryManifestMatchesCatalog(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	repo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, repo.Prepare(context.Background()))
	require.Len(t, repo.ordered, len(PrivateMemoryErasureManifest()))
	require.Contains(t, PrivateMemoryErasureManifest(), "evidence_conflict_cases")
	require.Contains(t, PrivateMemoryErasureManifest(), "evidence_conflict_positions")
	require.Contains(t, PrivateMemoryErasureManifest(), "evidence_conflict_events")
	require.Contains(t, PrivateMemoryErasureManifest(), "dream_path_evaluation_run_links")
	for _, table := range []string{"community_topic_versions", "community_topic_dependencies", "community_topic_work"} {
		require.Contains(t, PrivateMemoryErasureManifest(), table)
	}
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
	repo := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})

	created, err := repo.CreateIngestForTest(ctx, CreateIngestInput{
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
	credentialRepo := accesspostgres.NewCredentialRepository(appDB, rls, nil)
	eligible := createOwnedCredential(t, credentialRepo, teamID, ownerID, "eligible")
	held := createOwnedCredential(t, credentialRepo, teamID, ownerID, "held")
	fresh := createOwnedCredential(t, credentialRepo, teamID, ownerID, "fresh")
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
		IdempotencyScopeHash: Hash("retention-run", now.String()),
		RequestHash:          Hash("retention-run", "30", now.String()),
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

	scopeHash, requestHash := storedPrivateMemoryOperationHashes(t, adminDB, rls, operations[0].ID)
	require.Equal(t, Hash("retention-operation", run.ID.String(), eligible.MemorySpaceID.String()), scopeHash)
	require.Equal(t, Hash(string(domain.PrivateMemoryRetentionPurge), eligible.MemorySpaceID.String(), "1"), requestHash)

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
	credentialRepo := accesspostgres.NewCredentialRepository(appDB, rls, nil)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "fenced")
	seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, target.MemorySpaceID, "fenced private content")

	repo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, repo.Prepare(ctx))
	operation, created, err := repo.RequestCredentialErasure(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: target.ID, CredentialID: target.ID,
		IdempotencyScopeHash: Hash("fence", target.ID.String()),
		RequestHash:          Hash("fence-request", target.ID.String()),
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
	credentialRepo := accesspostgres.NewCredentialRepository(appDB, rls, nil)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "retry-target")
	seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, target.MemorySpaceID, "retry private content")

	repo := NewPrivateMemoryRepository(appDB, rls)
	operation, created, err := repo.RequestCredentialErasure(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: target.ID, CredentialID: target.ID,
		IdempotencyScopeHash: Hash("retry", target.ID.String()),
		RequestHash:          Hash("retry-request", target.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.NoError(t, err)
	require.True(t, created)

	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	wantRetryDelay := []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for attempt := 1; attempt <= MaximumAttempts; attempt++ {
		repo.now = func() time.Time { return now }
		claim, err := repo.ClaimNext(ctx, "retry-worker", time.Minute)
		require.NoError(t, err)
		require.NotNil(t, claim)
		require.Equal(t, operation.ID, claim.ID)
		require.Equal(t, attempt, claim.AttemptCount)
		require.NoError(t, repo.ReleaseClaim(ctx, claim.ID, claim.WorkerID, claim.Fence, "manifest_mismatch"))

		stored, err := repo.GetOperation(ctx, operation.ID)
		require.NoError(t, err)
		if attempt == MaximumAttempts {
			require.Equal(t, domain.PrivateMemoryErasureFailed, stored.Status)
			require.Nil(t, stored.NextAttemptAt)
			require.NotNil(t, stored.CompletedAt)
			break
		}
		require.Equal(t, domain.PrivateMemoryErasureQueued, stored.Status)
		require.NotNil(t, stored.NextAttemptAt)
		require.Equal(t, now.Add(wantRetryDelay[attempt]), *stored.NextAttemptAt)
		immediate, err := repo.ClaimNext(ctx, "early-worker", time.Minute)
		require.NoError(t, err)
		require.Nil(t, immediate)
		now = *stored.NextAttemptAt
	}

	repo.now = func() time.Time { return now.Add(24 * time.Hour) }
	claim, err := repo.ClaimNext(ctx, "late-worker", time.Minute)
	require.NoError(t, err)
	require.Nil(t, claim)

	superseding, created, err := repo.DisableSSOCredential(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: target.ID,
		IdempotencyScopeHash: Hash("retry-incompatible-retire", target.ID.String()),
		RequestHash:          Hash("retry-incompatible-retire-request", target.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, domain.PrivateMemoryRetireCredential, superseding.Action)
	require.Equal(t, target.ID, *superseding.TargetCredentialID)
	require.Equal(t, operation.TargetGeneration, superseding.TargetGeneration)
	require.True(t, superseding.RetireSpace)
	var credentialStatus string
	require.NoError(t, adminDB.Raw(`SELECT status FROM credentials WHERE id = ?`, target.ID).Row().Scan(&credentialStatus))
	require.Equal(t, "disabled", credentialStatus)

	original, err := repo.GetOperation(ctx, operation.ID)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureFailed, original.Status)

	claim, err = repo.ClaimNext(ctx, "retirement-recovery-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.Equal(t, superseding.ID, claim.ID)
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
	require.Equal(t, domain.MemorySpaceRetired, lifecycle)
	require.Equal(t, *operation.TargetGeneration+1, generation)

	retireOwnerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	retireTarget := createOwnedCredential(t, credentialRepo, teamID, retireOwnerID, "retry-retire-target")
	seedPrivateMemoryIngest(t, adminDB, rls, teamID, retireTarget.ID, retireTarget.MemorySpaceID, "retry retire private content")
	retirement, created, err := repo.DisableSSOCredential(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: retireOwnerID, CredentialID: retireTarget.ID,
		IdempotencyScopeHash: Hash("retry-retire", retireTarget.ID.String()),
		RequestHash:          Hash("retry-retire-request", retireTarget.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.True(t, retirement.RetireSpace)

	for attempt := 1; attempt <= MaximumAttempts; attempt++ {
		claim, err = repo.ClaimNext(ctx, "retirement-worker", time.Minute)
		require.NoError(t, err)
		require.NotNil(t, claim)
		require.Equal(t, retirement.ID, claim.ID)
		require.NoError(t, repo.ReleaseClaim(ctx, claim.ID, claim.WorkerID, claim.Fence, "database_error"))
		if attempt < MaximumAttempts {
			stored, getErr := repo.GetOperation(ctx, retirement.ID)
			require.NoError(t, getErr)
			require.NotNil(t, stored.NextAttemptAt)
			repo.now = func() time.Time { return *stored.NextAttemptAt }
		}
	}

	retirementRecovery, created, err := repo.RequestControlErasure(
		ctx,
		retireTarget.MemorySpaceID,
		Hash("retry-retire-control", retireTarget.ID.String()),
		Hash("retry-retire-control-request", retireTarget.ID.String()),
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
	credentialRepo := accesspostgres.NewCredentialRepository(appDB, rls, nil)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "missing-target")
	seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, target.MemorySpaceID, "missing target content")

	repo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, repo.Prepare(ctx))
	operation, created, err := repo.DisableSSOCredential(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: target.ID,
		IdempotencyScopeHash: Hash("missing-credential", target.ID.String()),
		RequestHash:          Hash("missing-credential-request", target.ID.String()),
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
	credentialRepo := accesspostgres.NewCredentialRepository(appDB, rls, nil)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "target")
	other := createOwnedCredential(t, credentialRepo, teamID, ownerID, "other")
	sharedSpaceID, err := credentialRepo.GetTeamSharedSpaceID(ctx, teamID)
	require.NoError(t, err)

	targetIngest := seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, target.MemorySpaceID, "target private content")
	otherIngest := seedPrivateMemoryIngest(t, adminDB, rls, teamID, other.ID, other.MemorySpaceID, "other private content")
	seedSessionErasureFixture(t, ctx, adminDB, rls, teamID, target.ID, target.MemorySpaceID)
	sharedIngest := seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, sharedSpaceID, "shared content")
	seedPrivateMemoryEvidenceConflict(t, adminDB, rls, teamID, target, targetIngest)
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
		IdempotencyScopeHash: Hash("owner", target.ID.String(), "key-1"),
		RequestHash:          Hash("erase", target.ID.String()),
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
	conflicting.RequestHash = Hash("different")
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
	require.Contains(t, completed.DeletedCounts, "evidence_conflict_cases")
	require.Contains(t, completed.DeletedCounts, "evidence_conflict_positions")
	require.Contains(t, completed.DeletedCounts, "evidence_conflict_events")
	for _, table := range []string{"session_submissions", "session_events", "session_extraction_checkpoints", "session_submission_receipts", "session_submission_diagnostics"} {
		require.Equal(t, int64(1), completed.DeletedCounts[table], table)
	}
	require.Equal(t, int64(1), completed.DeletedCounts["knowledge_ingests"])
	require.Equal(t, int64(1), completed.DeletedCounts["evidence_conflict_cases"])
	require.Equal(t, int64(2), completed.DeletedCounts["evidence_conflict_positions"])
	require.Equal(t, int64(1), completed.DeletedCounts["evidence_conflict_events"])

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

func TestPrivateMemoryErasureRemovesInboundCrossSpaceReferences(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-erasure-inbound-cross-reference"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := accesspostgres.NewCredentialRepository(appDB, rls, nil)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "inbound-target")
	sharedSpaceID, err := credentialRepo.GetTeamSharedSpaceID(ctx, teamID)
	require.NoError(t, err)
	privateIngestID := seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, target.MemorySpaceID, "private relationship target")
	sharedIngestID := seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, sharedSpaceID, "shared relationship source")
	privateSubjectID, privateObjectID := uuid.New(), uuid.New()
	sharedSubjectID, sharedObjectID := uuid.New(), uuid.New()
	targetRelationshipID, sourceRelationshipID := uuid.New(), uuid.New()
	sourceObservationID := uuid.New()
	verificationEventID, crossReferenceID := uuid.New(), uuid.New()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, teamID.String()); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO entity_records (team_id, entity_id, entity_kind, space_id)
			VALUES
				(?, ?, 'project', ?), (?, ?, 'product', ?),
				(?, ?, 'project', ?), (?, ?, 'product', ?)
		`, teamID, privateSubjectID, target.MemorySpaceID, teamID, privateObjectID, target.MemorySpaceID,
			teamID, sharedSubjectID, sharedSpaceID, teamID, sharedObjectID, sharedSpaceID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO relationship_records (
				team_id, relationship_id, owner_profile_id, semantic_group_key,
				subject_entity_id, predicate_key, predicate_version, object_entity_id,
				relationship_kind, current_cardinality, status, support_count,
				source_group_count, space_id
			) VALUES
				(?, ?, ?, 'private-target', ?, 'uses', 1, ?, 'state', 'many', 'active', 0, 0, ?),
				(?, ?, ?, 'shared-source', ?, 'uses', 1, ?, 'state', 'many', 'active', 0, 0, ?)
		`, teamID, targetRelationshipID, target.ID, privateSubjectID, privateObjectID, target.MemorySpaceID,
			teamID, sourceRelationshipID, target.ID, sharedSubjectID, sharedObjectID, sharedSpaceID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO relationship_observations (
				team_id, observation_id, relationship_id, ingest_id, owner_profile_id,
				subject_ref, original_predicate, object_ref, subject_entity_id,
				predicate_key, predicate_version, object_entity_id, evidence, space_id
			) VALUES (?, ?, ?, ?, ?, 'Shared source', 'uses', 'Private target', ?, 'uses', 1, ?, '[]'::jsonb, ?)
		`, teamID, sourceObservationID, sourceRelationshipID, sharedIngestID, target.ID,
			sharedSubjectID, sharedObjectID, sharedSpaceID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO verification_events (
				team_id, verification_event_id, observation_id, owner_profile_id, evidence_verdict, rationale
			) VALUES (?, ?, ?, ?, 'entailed', 'private erasure inbound cross-reference regression')
		`, teamID, verificationEventID, sourceObservationID, target.ID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO relationship_cross_references (
				team_id, cross_reference_id, author_profile_id, source_relationship_id,
				source_relationship_version, target_relationship_id, target_relationship_version,
				kind, verification_event_id, metadata, space_id
			) VALUES (?, ?, ?, ?, 1, ?, 1, 'challenges', ?, '{}'::jsonb, ?)
		`, teamID, crossReferenceID, target.ID, sourceRelationshipID, targetRelationshipID,
			verificationEventID, sharedSpaceID).Error
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var inboundCount int64
		if err := tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_cross_references AS cross_reference
			JOIN relationship_records AS target_relationship
			  ON target_relationship.team_id = cross_reference.team_id
			 AND target_relationship.relationship_id = cross_reference.target_relationship_id
			WHERE target_relationship.space_id = ?
			  AND cross_reference.space_id IS DISTINCT FROM ?
		`, target.MemorySpaceID, target.MemorySpaceID).Scan(&inboundCount).Error; err != nil {
			return err
		}
		require.Equal(t, int64(1), inboundCount)
		return nil
	}))

	repo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, repo.Prepare(ctx))
	operation, created, err := repo.RequestCredentialErasure(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: target.ID, CredentialID: target.ID,
		IdempotencyScopeHash: Hash("inbound-cross-reference", target.ID.String()),
		RequestHash:          Hash("erase-inbound-cross-reference", target.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.NoError(t, err)
	require.True(t, created)
	claim, err := repo.ClaimNext(ctx, "inbound-cross-reference-worker", time.Minute)
	require.NoError(t, err)
	require.Equal(t, operation.ID, claim.ID)
	completed, err := repo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureCompleted, completed.Status)
	require.Equal(t, int64(1), completed.DeletedCounts["relationship_cross_references_inbound"])

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var privateRelationshipCount, inboundReferenceCount, sourceRelationshipCount, sharedIngestCount int64
		if err := tx.Raw(`SELECT COUNT(*) FROM relationship_records WHERE relationship_id = ?`, targetRelationshipID).Scan(&privateRelationshipCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`SELECT COUNT(*) FROM relationship_cross_references WHERE cross_reference_id = ?`, crossReferenceID).Scan(&inboundReferenceCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`SELECT COUNT(*) FROM relationship_records WHERE relationship_id = ?`, sourceRelationshipID).Scan(&sourceRelationshipCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`SELECT COUNT(*) FROM knowledge_ingests WHERE ingest_id = ?`, sharedIngestID).Scan(&sharedIngestCount).Error; err != nil {
			return err
		}
		require.Equal(t, int64(0), privateRelationshipCount)
		require.Equal(t, int64(0), inboundReferenceCount)
		require.Equal(t, int64(1), sourceRelationshipCount)
		require.Equal(t, int64(1), sharedIngestCount)
		var privateIngestCount int64
		if err := tx.Raw(`SELECT COUNT(*) FROM knowledge_ingests WHERE ingest_id = ?`, privateIngestID).Scan(&privateIngestCount).Error; err != nil {
			return err
		}
		require.Equal(t, int64(0), privateIngestCount)
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
	credentialRepo := accesspostgres.NewCredentialRepository(appDB, rls, nil)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "concurrent")
	repo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, repo.Prepare(ctx))
	request := PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: target.ID,
		IdempotencyScopeHash: Hash("concurrent", teamID.String(), target.ID.String()),
		RequestHash:          Hash("erase", target.ID.String()),
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

func TestPrivateMemorySSOCredentialDeletePreservesSharedActorMembership(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "sso-delete-shared-actor"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := accesspostgres.NewCredentialRepository(appDB, rls, nil)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "shared-actor-target")
	sibling := createOwnedCredential(t, credentialRepo, teamID, ownerID, "sibling")
	siblingID := sibling.ID
	require.NotEqual(t, target.MembershipID, sibling.MembershipID)

	repo := NewPrivateMemoryRepository(appDB, rls)
	_, created, err := repo.DisableSSOCredential(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: target.ID,
		IdempotencyScopeHash: Hash("shared-actor-target", target.ID.String()),
		RequestHash:          Hash("shared-actor-target-request", target.ID.String()),
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
		`, sibling.ActorIdentityID, teamID).Row().Scan(&membershipStatus)
	}))
	require.Equal(t, "active", membershipStatus)

	_, created, err = repo.DisableSSOCredential(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: siblingID,
		IdempotencyScopeHash: Hash("shared-actor-sibling", siblingID.String()),
		RequestHash:          Hash("shared-actor-sibling-request", siblingID.String()),
		ReasonCode:           "credential_deleted",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status
			FROM team_memberships
			WHERE actor_identity_id = ? AND team_id = ?
		`, sibling.ActorIdentityID, teamID).Row().Scan(&membershipStatus)
	}))
	require.Equal(t, "revoked", membershipStatus)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT status FROM team_memberships WHERE actor_identity_id=? AND team_id=?`, ownerID, teamID).Row().Scan(&membershipStatus)
	}))
	require.Equal(t, "active", membershipStatus, "deleting both issued keys preserves their SSO user's membership")
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

func TestPrivateMigrationSessionOnlyRetentionPreservesAge(t *testing.T) {
	f := legacyPrivateMigrationFixture(t)
	ctx := context.Background()
	owner, member, submission := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	latest := now.AddDate(0, 0, -45)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO actor_identities(id,kind,team_id,active) VALUES(?,'api_client',?,true)`, []any{owner, f.team}},
			{`INSERT INTO team_memberships(id,team_id,actor_identity_id,status,maximum_grants,sso_entitlement_status) VALUES(?,?,?,'active',ARRAY['read','write'],'active')`, []any{member, f.team, owner}},
			{`INSERT INTO membership_grants(membership_id,grant_name,source) VALUES(?,'read','explicit'),(?,'write','explicit')`, []any{member, member}},
			{`INSERT INTO credentials(id,team_id,actor_identity_id,owner_identity_id,key_hash,key_prefix,key_suffix,name,scopes,memory_binding,memory_space_id) VALUES(?,?,?,?,'synthetic-test-hash',?,'test01','session-only writer',ARRAY['read','write'],'profile_private',?)`, []any{owner, f.team, owner, f.user, owner.String()[:24], f.legacy}},
			{`INSERT INTO ownership_aliases(team_id,legacy_owner_id,canonical_identity_id,credential_id) VALUES(?,?,?,?)`, []any{f.team, owner, owner, owner}},
			{`INSERT INTO session_submissions(team_id,owner_profile_id,space_id,space_generation,submission_id,idempotency_key,request_hash,intake,prior_context,new_event_indices,accepted_event_count,duplicate_event_count,result,created_at) VALUES(?,?,?,1,?,'session-only-retention',?,'{}','[]','[0,1]',2,0,'{"processing_state":"completed"}',?)`, []any{f.team, owner, f.legacy, submission, "sha256:" + strings.Repeat("a", 64), latest}},
		} {
			if err := tx.Exec(statement.query, statement.args...).Error; err != nil {
				return err
			}
		}
		for index, age := range []time.Time{now.AddDate(0, 0, -60), latest} {
			if err := tx.Exec(`INSERT INTO session_events(team_id,owner_profile_id,space_id,space_generation,identity_hash,submission_id,event_index,framework,app_name,user_id,session_id,event_id,body,result,created_at) VALUES(?,?,?,1,?,?,?,'generic','automation','session-only','retention',?,'{"text":"private automation history"}','{}',?)`, f.team, owner, f.legacy, "sha256:"+strings.Repeat(string(rune('b'+index)), 64), submission, index, uuid.NewString(), age).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	report, err := f.repo.InspectPrivateMemoryMigration(ctx)
	require.NoError(t, err)
	migrator, err := storage.NewMigrator(f.db)
	require.NoError(t, err)
	require.NoError(t, migrator.PreparePrivateMemoryMigration(ctx))
	require.NoError(t, f.repo.MigratePrivateMemory(ctx, report.SourceFingerprint))
	require.NoError(t, migrator.RunUp(ctx))
	repo := NewPrivateMemoryRepository(f.applicationDB(t), f.rls)
	var destination uuid.UUID
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT id FROM memory_spaces WHERE team_id=? AND owner_membership_id=? AND NOT migration_archive`, f.team, member).Row().Scan(&destination); err != nil {
			return err
		}
		var ingests, events int
		if err := tx.Raw(`SELECT count(*) FROM knowledge_ingests WHERE team_id=? AND space_id=?`, f.team, destination).Row().Scan(&ingests); err != nil {
			return err
		}
		require.Zero(t, ingests)
		if err := tx.Raw(`SELECT count(*) FROM session_events WHERE team_id=? AND space_id=?`, f.team, destination).Row().Scan(&events); err != nil {
			return err
		}
		require.Equal(t, 2, events)
		return nil
	}))
	_, _, err = repo.PlaceLegalHold(ctx, destination, "session-retention-hold")
	require.NoError(t, err)
	run, created, err := repo.RunRetention(ctx, PrivateMemoryRetentionRequest{ActorClass: domain.PrivateMemoryActorControl, IdempotencyScopeHash: Hash("session-only", "held"), RequestHash: Hash("session-only-request", "held"), RetentionDays: 30, Now: now})
	require.NoError(t, err)
	require.True(t, created)
	assert.Zero(t, run.QueuedCount)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.db, func(tx *gorm.DB) error {
		var age sql.NullTime
		if err := tx.Raw(`SELECT private_content_at FROM memory_spaces WHERE id=?`, destination).Row().Scan(&age); err != nil {
			return err
		}
		assert.True(t, age.Valid, "session-only migration must participate in retention age backfill")
		if age.Valid {
			assert.WithinDuration(t, latest, age.Time, time.Microsecond, "retention uses the newest original session event, not migration time or other writers")
		}
		return nil
	}))
	_, _, err = repo.ReleaseLegalHold(ctx, destination)
	require.NoError(t, err)
	run, created, err = repo.RunRetention(ctx, PrivateMemoryRetentionRequest{ActorClass: domain.PrivateMemoryActorControl, IdempotencyScopeHash: Hash("session-only", "released"), RequestHash: Hash("session-only-request", "released"), RetentionDays: 30, Now: now})
	require.NoError(t, err)
	require.True(t, created)
	assert.Equal(t, 1, run.QueuedCount, "expired session-only private history must be queued without another write")
	operations, err := repo.ListOperations(ctx, 100, 0)
	require.NoError(t, err)
	if assert.Len(t, operations, 1) {
		require.NotNil(t, operations[0].SpaceID)
		assert.Equal(t, destination, *operations[0].SpaceID)
	}
}
