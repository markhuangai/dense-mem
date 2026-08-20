package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestCanonicalSSOOwnedCredentialCollectionAndSpaces(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "canonical-sso-multi-credential"))
	otherTeamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "canonical-sso-other-team"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	otherOwnerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	otherTeamOwnerID := createLedgerSSOIdentity(t, adminDB, rls, otherTeamID)

	repo := NewCredentialRepository(appDB, rls)
	profileA := createOwnedCredential(t, repo, teamID, ownerID, "profile-a", domain.CredentialBindingProfilePrivate)
	profileB := createOwnedCredential(t, repo, teamID, ownerID, "profile-b", domain.CredentialBindingProfilePrivate)
	credentialA := createOwnedCredential(t, repo, teamID, ownerID, "credential-a", domain.CredentialBindingCredentialPrivate)
	credentialB := createOwnedCredential(t, repo, teamID, ownerID, "credential-b", domain.CredentialBindingCredentialPrivate)
	shared := createOwnedCredential(t, repo, teamID, ownerID, "shared", domain.CredentialBindingSharedOnly)

	owned, err := repo.ListSSOOwnedCredentials(ctx, teamID, ownerID)
	require.NoError(t, err)
	require.Len(t, owned, 5)
	_, err = repo.GetSSOOwnedCredential(ctx, teamID, ownerID)
	require.ErrorContains(t, err, "multiple sso-owned api credentials")
	require.Equal(t, profileA.MemorySpaceID, profileB.MemorySpaceID)
	require.NotEqual(t, credentialA.MemorySpaceID, credentialB.MemorySpaceID)
	sharedLoaded := findCredentialByID(owned, shared.ID)
	require.NotNil(t, sharedLoaded)
	require.Equal(t, sharedLoaded.TeamSharedSpaceID, sharedLoaded.MemorySpaceID)

	var privateSharedCount int
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*)
			FROM memory_spaces
			WHERE team_id = ? AND owner_credential_id = ?
		`, teamID, shared.ID).Scan(&privateSharedCount).Error
	}))
	require.Zero(t, privateSharedCount)

	rotatedPrefix := "dm_" + strings.ReplaceAll(credentialA.ID.String(), "-", "")[:20]
	rows, err := repo.RotateForTeam(ctx, teamID, credentialA.ID, "rotated-hash", rotatedPrefix, "rot8ed", nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	rotated, err := repo.GetByIDForTeam(ctx, teamID, credentialA.ID)
	require.NoError(t, err)
	require.Equal(t, credentialA.ID, rotated.ID)
	require.Equal(t, credentialA.MemoryBinding, rotated.MemoryBinding)
	require.Equal(t, credentialA.MemorySpaceID, rotated.MemorySpaceID)

	rows, err = repo.RevokeForTeam(ctx, teamID, profileB.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	owned, err = repo.ListSSOOwnedCredentials(ctx, teamID, ownerID)
	require.NoError(t, err)
	require.Len(t, owned, 4)
	otherOwned, err := repo.ListSSOOwnedCredentials(ctx, teamID, otherOwnerID)
	require.NoError(t, err)
	require.Empty(t, otherOwned)
	missing, err := repo.GetSSOOwnedCredentialByID(ctx, teamID, otherOwnerID, credentialA.ID)
	require.NoError(t, err)
	require.Nil(t, missing)
	missing, err = repo.GetSSOOwnedCredentialByID(ctx, otherTeamID, otherTeamOwnerID, credentialA.ID)
	require.NoError(t, err)
	require.Nil(t, missing)

	var singletonIndexCount int
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*)
			FROM pg_indexes
			WHERE schemaname = 'public' AND indexname = 'idx_credentials_owner_team_active_unique'
		`).Scan(&singletonIndexCount).Error
	}))
	require.Zero(t, singletonIndexCount)
}

func TestDeleteForTeamRetiresPromotedSSOCredentialPrivateSpace(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "canonical-promoted-sso-delete"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "promoted", domain.CredentialBindingCredentialPrivate)
	ingestID := seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, target.MemorySpaceID, "promoted private content")

	rows, err := credentialRepo.UpdateRoleForTeam(ctx, teamID, target.ID, "manager", []string{"read", "write"})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	rows, err = credentialRepo.DeleteForTeam(ctx, teamID, target.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	privateMemoryRepo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, privateMemoryRepo.Prepare(ctx))
	operations, err := privateMemoryRepo.ListOperations(ctx, 100, 0)
	require.NoError(t, err)
	require.Len(t, operations, 1)
	require.Equal(t, domain.PrivateMemoryRetireCredential, operations[0].Action)
	require.Equal(t, domain.PrivateMemoryActorControl, operations[0].ActorClass)
	require.Equal(t, target.ID, *operations[0].TargetCredentialID)

	claim, err := privateMemoryRepo.ClaimNext(ctx, "promoted-sso-delete-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)
	completed, err := privateMemoryRepo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureCompleted, completed.Status)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var exists bool
		if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM knowledge_ingests WHERE ingest_id = ?)`, ingestID).Row().Scan(&exists); err != nil {
			return err
		}
		require.False(t, exists)

		var lifecycle, status string
		if err := tx.Raw(`SELECT lifecycle_state FROM memory_spaces WHERE id = ?`, target.MemorySpaceID).Row().Scan(&lifecycle); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT status FROM credentials WHERE id = ?`, target.ID).Row().Scan(&status); err != nil {
			return err
		}
		require.Equal(t, "retired", lifecycle)
		require.Equal(t, "disabled", status)
		return nil
	}))
}

func createLedgerSSOIdentity(t *testing.T, db *gorm.DB, rls *storagepostgres.RLS, teamID uuid.UUID) uuid.UUID {
	t.Helper()
	providerID := uuid.New()
	identityID := uuid.New()
	membershipID := uuid.New()
	now := time.Now().UTC()
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO sso_providers (id, name, kind, issuer_url, client_id)
			VALUES (?, ?, 'generic_oidc', 'https://issuer.example.test', ?)
		`, providerID, "provider-"+providerID.String(), "client-"+providerID.String()).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO sso_identities (id, provider_id, subject, email, display_name)
			VALUES (?, ?, ?, ?, ?)
		`, identityID, providerID, "subject-"+identityID.String(), "user-"+identityID.String()+"@example.test", "SSO test user").Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO actor_identities (id, kind, team_id, provider, subject, display_name, active, created_at, updated_at)
			VALUES (?, 'human', NULL, ?, ?, 'SSO test user', true, ?, ?)
		`, identityID, providerID.String(), "subject-"+identityID.String(), now, now).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO team_memberships (
				id, actor_identity_id, team_id, status, team_admin, maximum_grants,
				sso_provider_id, sso_group_id, sso_entitlement_status, created_at, updated_at
			) VALUES (?, ?, ?, 'active', false, ARRAY['read','write']::text[], ?, 'test-group', 'active', ?, ?)
		`, membershipID, identityID, teamID, providerID, now, now).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO membership_grants (membership_id, grant_name, source)
			VALUES (?, 'read', 'explicit'), (?, 'write', 'explicit')
		`, membershipID, membershipID).Error
	}))
	return identityID
}

func createOwnedCredential(t *testing.T, repo *CredentialRepositoryImpl, teamID, ownerID uuid.UUID, name string, binding domain.CredentialMemoryBinding) *domain.Credential {
	t.Helper()
	id := uuid.New()
	prefix := "dm_" + strings.ReplaceAll(id.String(), "-", "")[:20]
	credential := &domain.Credential{
		ID:              id,
		TeamID:          teamID,
		Name:            name,
		KeyHash:         "hash-" + id.String(),
		KeyPrefix:       prefix,
		KeySuffix:       "suffix",
		Scopes:          []string{"read", "write"},
		RateLimit:       60,
		OwnerIdentityID: &ownerID,
		MemoryBinding:   binding,
	}
	require.NoError(t, repo.CreateCredential(context.Background(), credential))
	return credential
}

func findCredentialByID(credentials []*domain.Credential, id uuid.UUID) *domain.Credential {
	for _, credential := range credentials {
		if credential.ID == id {
			return credential
		}
	}
	return nil
}

func TestCanonicalCredentialScopesRespectMembershipGrants(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "canonical-grant-scope")
	profileID := createLedgerProfile(t, adminDB, rls, teamID, "canonical-grant-scope-key")

	var prefix string
	require.NoError(t, adminDB.Raw(`
		SELECT key_prefix
		FROM credentials
		WHERE id = ?::uuid
	`, profileID).Row().Scan(&prefix))

	repo := NewCredentialRepository(appDB, rls)
	key, err := repo.GetActiveByPrefix(ctx, prefix)
	require.NoError(t, err)
	require.NotNil(t, key)
	require.Equal(t, []string{"read", "write"}, key.Scopes)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			DELETE FROM membership_grants
			WHERE membership_id = (
				SELECT id FROM team_memberships WHERE actor_identity_id = ?::uuid AND team_id = ?::uuid
			)
			  AND grant_name = 'write'
		`, profileID, teamID).Error
	}))

	key, err = repo.GetActiveByPrefix(ctx, prefix)
	require.NoError(t, err)
	require.NotNil(t, key)
	require.Equal(t, []string{"read"}, key.Scopes)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE actor_identities
			SET active = false
			WHERE id = (SELECT actor_identity_id FROM credentials WHERE key_prefix = ?)
		`, prefix).Error
	}))
	key, err = repo.GetActiveByPrefix(ctx, prefix)
	require.NoError(t, err)
	require.Nil(t, key)
}

func TestCanonicalCredentialLifecycleRetainsDisabledIdentity(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "canonical-key-lifecycle"))
	keyID := uuid.New()
	prefix := "dm_lifecycle_" + keyID.String()[:11]
	repo := NewCredentialRepository(appDB, rls)
	require.NoError(t, repo.CreateCredential(ctx, &domain.Credential{
		ID: keyID, TeamID: teamID, Name: "lifecycle-key", KeyHash: "lifecycle-hash",
		KeyPrefix: prefix, KeySuffix: "suffix", Scopes: []string{"read", "write"},
	}))

	active, err := repo.GetActiveByPrefix(ctx, prefix)
	require.NoError(t, err)
	require.NotNil(t, active)
	require.Equal(t, []string{"read", "write"}, active.Scopes)
	require.Equal(t, domain.CredentialBindingSharedOnly, active.MemoryBinding)
	require.NotEqual(t, uuid.Nil, active.MemorySpaceID)
	require.Equal(t, active.MemorySpaceID, active.TeamSharedSpaceID)

	rows, err := repo.UpdateNameForTeam(ctx, teamID, keyID, "renamed-key")
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	rows, err = repo.UpdateRoleForTeam(ctx, teamID, keyID, "manager", []string{"read", "feedback:read"})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	loaded, err := repo.GetByIDForTeam(ctx, teamID, keyID)
	require.NoError(t, err)
	require.Equal(t, "renamed-key", loaded.Name)
	require.Equal(t, "manager", loaded.Role)
	require.Equal(t, []string{"read", "feedback:read"}, loaded.Scopes)
	require.Equal(t, domain.CredentialBindingSharedOnly, loaded.MemoryBinding)
	require.Equal(t, active.MemorySpaceID, loaded.MemorySpaceID)
	require.Equal(t, active.TeamSharedSpaceID, loaded.TeamSharedSpaceID)

	listed, err := repo.ListByTeam(ctx, teamID, 100, 0)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, domain.CredentialBindingSharedOnly, listed[0].MemoryBinding)
	require.Equal(t, active.MemorySpaceID, listed[0].MemorySpaceID)
	require.Equal(t, active.TeamSharedSpaceID, listed[0].TeamSharedSpaceID)

	rotatedPrefix := "dm_rotated_" + keyID.String()[:13]
	rows, err = repo.RotateForTeam(ctx, teamID, keyID, "rotated-hash", rotatedPrefix, "rotate", nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	require.NoError(t, repo.TouchLastUsedBatch(ctx, []LastUsedUpdate{{ID: keyID, At: time.Now().UTC()}}))
	active, err = repo.GetActiveByPrefix(ctx, rotatedPrefix)
	require.NoError(t, err)
	require.NotNil(t, active)
	require.Equal(t, "rotated-hash", active.KeyHash)
	require.NotNil(t, active.LastUsedAt)

	rows, err = repo.RevokeForTeam(ctx, teamID, keyID)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	active, err = repo.GetActiveByPrefix(ctx, rotatedPrefix)
	require.NoError(t, err)
	require.Nil(t, active)
	rows, err = repo.RotateForTeam(ctx, teamID, keyID, "restored-hash", rotatedPrefix, "stored", nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), rows)

	rows, err = repo.DeleteForTeam(ctx, teamID, keyID)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	count, err := repo.CountByTeam(ctx, teamID)
	require.NoError(t, err)
	require.Zero(t, count)

	var status string
	var aliasCount int
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT status FROM credentials WHERE id = ?`, keyID).Scan(&status).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT count(*) FROM ownership_aliases
			WHERE team_id = ? AND legacy_owner_id = ? AND credential_id = ?
		`, teamID, keyID, keyID).Scan(&aliasCount).Error
	}))
	require.Equal(t, "disabled", status)
	require.Equal(t, 1, aliasCount)
	var actorActive bool
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT active FROM actor_identities WHERE id = ?`, keyID).Scan(&actorActive).Error
	}))
	require.False(t, actorActive)
}

func TestCanonicalCredentialRevocationPreservesSharedActorAcrossTeams(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamA := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "canonical-shared-actor-a"))
	teamB := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "canonical-shared-actor-b"))
	keyA := uuid.MustParse(createLedgerProfile(t, adminDB, rls, teamA.String(), "shared-actor-key-a"))
	keyB := uuid.New()
	prefixB := "dm_shared_" + keyB.String()[:12]
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE actor_identities
			SET team_id = NULL
			WHERE id = ?::uuid
		`, keyA).Error; err != nil {
			return err
		}
		var membershipID uuid.UUID
		if err := tx.Raw(`
			INSERT INTO team_memberships (
				actor_identity_id, team_id, status, team_admin, maximum_grants
			) VALUES (?, ?, 'active', false, ARRAY['read']::text[])
			RETURNING id
		`, keyA, teamB).Row().Scan(&membershipID); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO membership_grants (membership_id, grant_name, source)
			VALUES (?, 'read', 'legacy_scope')
		`, membershipID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO credentials (
				id, actor_identity_id, team_id, kind, key_hash, key_prefix, key_suffix,
				name, scopes, status
			) VALUES (?, ?, ?, 'api_key', 'shared-actor-hash-b', ?, 'suffix',
				'shared-actor-key-b', ARRAY['read']::text[], 'active')
		`, keyB, keyA, teamB, prefixB).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO ownership_aliases (
				team_id, legacy_owner_id, canonical_identity_id, credential_id, reason
			) VALUES (?, ?, ?, ?, 'credential')
		`, teamB, keyB, keyA, keyB).Error
	}))

	repo := NewCredentialRepository(appDB, rls)
	rows, err := repo.RevokeForTeam(ctx, teamA, keyA)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	var actorActive bool
	var teamBMembershipStatus string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT active FROM actor_identities WHERE id = ?`, keyA).Row().Scan(&actorActive); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT status
			FROM team_memberships
			WHERE actor_identity_id = ? AND team_id = ?
		`, keyA, teamB).Row().Scan(&teamBMembershipStatus)
	}))
	require.True(t, actorActive)
	require.Equal(t, "active", teamBMembershipStatus)
	activeB, err := repo.GetActiveByPrefix(ctx, prefixB)
	require.NoError(t, err)
	require.NotNil(t, activeB)

	rows, err = repo.RotateForTeam(ctx, teamA, keyA, "shared-actor-hash-a-restored", "dm_"+keyA.String()[:20], "restor", nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), rows)
	rows, err = repo.DeleteForTeam(ctx, teamA, keyA)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	var teamBCredentialStatus string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT active FROM actor_identities WHERE id = ?`, keyA).Row().Scan(&actorActive); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status
			FROM team_memberships
			WHERE actor_identity_id = ? AND team_id = ?
		`, keyA, teamB).Row().Scan(&teamBMembershipStatus); err != nil {
			return err
		}
		return tx.Raw(`SELECT status FROM credentials WHERE id = ?`, keyB).Row().Scan(&teamBCredentialStatus)
	}))
	require.True(t, actorActive)
	require.Equal(t, "active", teamBMembershipStatus)
	require.Equal(t, "active", teamBCredentialStatus)
}
