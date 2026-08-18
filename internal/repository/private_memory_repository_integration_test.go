package repository

import (
	"context"
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
	seedPrivateMemoryIngest(t, adminDB, rls, teamID, eligible.ID, eligible.MemorySpaceID, "expired private content")
	seedPrivateMemoryIngest(t, adminDB, rls, teamID, held.ID, held.MemorySpaceID, "held private content")
	seedPrivateMemoryIngest(t, adminDB, rls, teamID, fresh.ID, fresh.MemorySpaceID, "fresh private content")

	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE memory_spaces
			SET private_content_at = CASE id
				WHEN ? THEN ?::timestamptz
				WHEN ? THEN ?::timestamptz
				WHEN ? THEN ?::timestamptz
			END
			WHERE id = ANY(?)
		`, eligible.MemorySpaceID, now.AddDate(0, 0, -60),
			held.MemorySpaceID, now.AddDate(0, 0, -60),
			fresh.MemorySpaceID, now.AddDate(0, 0, -1),
			pq.Array([]uuid.UUID{eligible.MemorySpaceID, held.MemorySpaceID, fresh.MemorySpaceID})).Error
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
	require.Equal(t, operation.ID, claim.ID)
	_, _, err = repo.PlaceLegalHold(ctx, target.MemorySpaceID, "late_hold")
	require.NoError(t, err)
	_, err = repo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.ErrorIs(t, err, ErrPrivateMemoryLegalHold)
	require.NoError(t, repo.ReleaseClaim(ctx, claim.ID, claim.WorkerID, claim.Fence, "legal_hold"))
	_, released, err = repo.ReleaseLegalHold(ctx, target.MemorySpaceID)
	require.NoError(t, err)
	require.True(t, released)

	claim, err = repo.ClaimNext(ctx, "private-test-worker", time.Minute)
	require.NoError(t, err)
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
		for _, status := range statuses {
			require.Equal(t, "disabled", status)
		}
		return nil
	}))
}

func seedPrivateMemoryIngest(t *testing.T, db *gorm.DB, rls interface {
	WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
}, teamID, ownerID, spaceID uuid.UUID, content string) uuid.UUID {
	t.Helper()
	ingestID := uuid.New()
	err := rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, request_hash, source_summary,
				status, proposal, metadata, space_id
			) VALUES (?, ?, ?, ?, ?, 'queued', '{}'::jsonb, '{}'::jsonb, ?)
		`, teamID, ingestID, ownerID, "hash-"+ingestID.String(), content, spaceID).Error
	})
	require.NoError(t, err)
	return ingestID
}
