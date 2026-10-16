//go:build integration

package postgres

import (
	"context"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/audit"
	auditpostgres "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	privacycontract "github.com/markhuangai/dense-mem/internal/privacy/contract"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
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

	repo := NewCredentialRepository(appDB, rls, privacypostgres.NewCredentialDeletionRepository(appDB, rls))
	profileA := createOwnedCredential(t, repo, teamID, ownerID, "profile-a")
	profileB := createOwnedCredential(t, repo, teamID, ownerID, "profile-b")
	credentialA := createOwnedCredential(t, repo, teamID, ownerID, "credential-a")
	credentialB := createOwnedCredential(t, repo, teamID, ownerID, "credential-b")
	shared := createOwnedCredential(t, repo, teamID, ownerID, "shared")

	owned, err := repo.ListSSOOwnedCredentials(ctx, teamID, ownerID)
	require.NoError(t, err)
	require.Len(t, owned, 5)
	_, err = repo.GetSSOOwnedCredential(ctx, teamID, ownerID)
	require.ErrorContains(t, err, "multiple sso-owned api credentials")
	require.NotEqual(t, profileA.MemorySpaceID, profileB.MemorySpaceID)
	require.NotEqual(t, credentialA.MemorySpaceID, credentialB.MemorySpaceID)
	sharedLoaded := findCredentialByID(owned, shared.ID)
	require.NotNil(t, sharedLoaded)
	require.NotEqual(t, sharedLoaded.TeamSharedSpaceID, sharedLoaded.MemorySpaceID)

	var privateSharedCount int
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*)
			FROM memory_spaces
			WHERE team_id = ? AND owner_membership_id = ?
		`, teamID, shared.MembershipID).Scan(&privateSharedCount).Error
	}))
	require.Equal(t, 1, privateSharedCount)

	rotatedPrefix := "dm_" + strings.ReplaceAll(credentialA.ID.String(), "-", "")[:20]
	rotation, err := repo.RotateForTeam(ctx, teamID, credentialA.ID, "rotated-hash", rotatedPrefix, "rot8ed", nil)
	require.NoError(t, err)
	require.NotNil(t, rotation)
	require.Equal(t, credentialA.MemorySpaceID, rotation.MemorySpaceID)
	rotated, err := repo.GetByIDForTeam(ctx, teamID, credentialA.ID)
	require.NoError(t, err)
	require.Equal(t, credentialA.ID, rotated.ID)
	require.Equal(t, credentialA.MembershipID, rotated.MembershipID)
	require.Equal(t, credentialA.MemorySpaceID, rotated.MemorySpaceID)

	rows, err := repo.RevokeForTeam(ctx, teamID, profileB.ID)
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
	credentialRepo := NewCredentialRepository(appDB, rls, privacypostgres.NewCredentialDeletionRepository(appDB, rls))
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "promoted")
	ingestID := seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, target.MemorySpaceID, "promoted private content")

	rows, err := credentialRepo.UpdateRoleForTeam(ctx, teamID, target.ID, "manager", []string{"read", "write"})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	rows, err = credentialRepo.DeleteForTeam(ctx, teamID, target.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	privateMemoryRepo := privacypostgres.NewPrivateMemoryRepository(appDB, rls)
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

func TestDeleteForTeamWithAuditCommitsBeforePrivateErasureQueue(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "canonical-delete-audit-order"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls, privacypostgres.NewCredentialDeletionRepository(appDB, rls))
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "audit-order")
	rollbackTarget := createOwnedCredential(t, credentialRepo, teamID, ownerID, "audit-rollback")
	seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, target.MemorySpaceID, "audit-order private content")

	rows, err := credentialRepo.DeleteForTeamWithAudit(ctx, teamID, target.ID, privacycontract.CredentialDeletionAuditInput{
		ActorRole:     "manager",
		ClientIP:      "127.0.0.1",
		CorrelationID: "corr-delete-audit-order",
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	var queuedCount, auditCount int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT COUNT(*)
			FROM private_memory_erasure_operations
			WHERE team_id = ? AND target_credential_id = ?
		`, teamID, target.ID).Row().Scan(&queuedCount); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT COUNT(*)
			FROM audit_log
			WHERE team_id = ? AND entity_type = 'api_key' AND entity_id = ? AND operation = 'DELETE'
		`, teamID, target.ID.String()).Row().Scan(&auditCount)
	}))
	require.Equal(t, int64(1), queuedCount)
	require.Equal(t, int64(1), auditCount)

	privateMemoryRepo := privacypostgres.NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, privateMemoryRepo.Prepare(ctx))
	claim, err := privateMemoryRepo.ClaimNext(ctx, "delete-audit-order-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)
	_, err = privateMemoryRepo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.NoError(t, err)

	var beforePayload, afterPayload, metadata []byte
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT before_payload, after_payload, metadata
			FROM audit_log
			WHERE team_id = ? AND entity_type = 'api_key' AND entity_id = ? AND operation = 'DELETE'
		`, teamID, target.ID.String()).Row().Scan(&beforePayload, &afterPayload, &metadata)
	}))
	require.Nil(t, beforePayload)
	require.Nil(t, afterPayload)
	require.JSONEq(t, privateMemoryAuditMetadataJSON, string(metadata))

	_, err = credentialRepo.DeleteForTeamWithAudit(ctx, teamID, rollbackTarget.ID, privacycontract.CredentialDeletionAuditInput{
		ActorRole: "manager",
		ClientIP:  "not-an-ip",
	})
	require.Error(t, err)
	var status string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT status FROM credentials WHERE id = ?`, rollbackTarget.ID).Row().Scan(&status)
	}))
	require.Equal(t, "active", status)

	t.Run("credential-private queue failure rolls back credential membership audit and queue", func(t *testing.T) {
		queueFailureTarget := createOwnedCredential(t, credentialRepo, teamID, ownerID, "audit-queue-rollback")
		require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			return tx.Exec(`
				UPDATE memory_spaces
				SET lifecycle_state = 'retired', retired_at = NOW()
				WHERE id = ? AND team_id = ? AND owner_membership_id = ?
			`, queueFailureTarget.MemorySpaceID, teamID, queueFailureTarget.MembershipID).Error
		}))

		_, err := credentialRepo.DeleteForTeamWithAudit(ctx, teamID, queueFailureTarget.ID, privacycontract.CredentialDeletionAuditInput{
			ActorRole:     "manager",
			ClientIP:      "127.0.0.1",
			CorrelationID: "corr-delete-queue-rollback",
		})
		require.Error(t, err)

		var status, membershipStatus string
		var auditCount, queuedCount int64
		require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			if err := tx.Raw(`SELECT status FROM credentials WHERE id = ?`, queueFailureTarget.ID).Row().Scan(&status); err != nil {
				return err
			}
			if err := tx.Raw(`
				SELECT status
				FROM team_memberships
				WHERE team_id = ? AND actor_identity_id = ?
			`, teamID, queueFailureTarget.ID).Row().Scan(&membershipStatus); err != nil {
				return err
			}
			if err := tx.Raw(`
				SELECT COUNT(*)
				FROM audit_log
				WHERE team_id = ? AND entity_type = 'api_key' AND entity_id = ? AND operation = 'DELETE'
			`, teamID, queueFailureTarget.ID.String()).Row().Scan(&auditCount); err != nil {
				return err
			}
			return tx.Raw(`
				SELECT COUNT(*)
				FROM private_memory_erasure_operations
				WHERE team_id = ? AND target_credential_id = ?
			`, teamID, queueFailureTarget.ID).Row().Scan(&queuedCount)
		}))
		require.Equal(t, "active", status)
		require.Equal(t, "active", membershipStatus)
		require.Zero(t, auditCount)
		require.Zero(t, queuedCount)
	})
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
		if err := tx.Exec(`INSERT INTO sso_group_mappings(provider_id,team_id,group_id,scopes) VALUES(?,?,'test-group',ARRAY['read','write'])`, providerID, teamID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO sso_entitlement_cache(provider_id,subject,groups,status,checked_at,expires_at) VALUES(?,?,ARRAY['test-group'],'active',now(),now()+interval '1 hour')`, providerID, "subject-"+identityID.String()).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO membership_grants (membership_id, grant_name, source)
			VALUES (?, 'read', 'explicit'), (?, 'write', 'explicit')
		`, membershipID, membershipID).Error
	}))
	return identityID
}

func createOwnedCredential(t *testing.T, repo *CredentialRepositoryImpl, teamID, ownerID uuid.UUID, name string) *domain.Credential {
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

	repo := NewCredentialRepository(appDB, rls, privacypostgres.NewCredentialDeletionRepository(appDB, rls))
	key, err := repo.GetActiveByPrefix(ctx, prefix)
	require.NoError(t, err)
	require.NotNil(t, key)
	require.Equal(t, []string{"read", "write"}, key.Scopes)

	auditService := audit.New(auditpostgres.NewStore(appDB, rls))
	teamService := accessservice.NewTeamService(NewTeamRepository(appDB, rls), auditService, nil)
	service := accessservice.NewCredentialService(repo, teamService, auditService, nil)
	teamUUID, credentialID := uuid.MustParse(teamID), uuid.MustParse(profileID)
	expiredAt := time.Now().Add(-time.Hour)
	expired, raw, err := service.CreateCredential(ctx, teamUUID, accessservice.CreateCredentialRequest{
		Name: "expired-grant-response", Role: "member", Scopes: []string{"read", "write"}, ExpiresAt: &expiredAt,
	}, nil, "control", "127.0.0.1", "expired-create")
	require.NoError(t, err)
	require.NotEmpty(t, raw)
	assert.False(t, expired.MemoryCapabilities().Private.Read)
	assert.False(t, expired.MemoryCapabilities().Private.Write)
	assert.False(t, expired.MemoryCapabilities().TeamShared.Read)
	assert.False(t, expired.MemoryCapabilities().TeamShared.Write)
	require.Empty(t, expired.KeyHash)
	storedExpired, err := service.GetByIDForTeam(ctx, teamUUID, expired.ID)
	require.NoError(t, err)
	require.Empty(t, storedExpired.EffectiveMemoryGrants)
	rotated, raw, err := service.RotateForTeam(ctx, teamUUID, expired.ID, accessservice.CreateCredentialRequest{}, nil, "control", "127.0.0.1", "expired-rotate")
	require.NoError(t, err)
	require.NotEmpty(t, raw)
	assert.True(t, rotated.MemoryCapabilities().Private.Read)
	assert.True(t, rotated.MemoryCapabilities().Private.Write)
	assert.True(t, rotated.MemoryCapabilities().TeamShared.Read)
	assert.True(t, rotated.MemoryCapabilities().TeamShared.Write)
	require.Equal(t, expired.ID, rotated.ID)
	require.Equal(t, expired.MembershipID, rotated.MembershipID)
	require.Equal(t, expired.MemorySpaceID, rotated.MemorySpaceID)
	require.Equal(t, expired.MemorySpaceGeneration, rotated.MemorySpaceGeneration)
	require.Nil(t, rotated.ExpiresAt)
	require.Empty(t, rotated.KeyHash)

	updated, err := service.UpdateScopesForTeam(ctx, teamUUID, credentialID, []string{"read"}, nil, "control", "127.0.0.1", "scope-downgrade")
	require.NoError(t, err)
	require.True(t, updated.MemoryCapabilities().Private.Write)
	require.False(t, updated.MemoryCapabilities().TeamShared.Write)
	require.Empty(t, updated.KeyHash)

	updated, err = service.UpdateRoleForTeam(ctx, teamUUID, credentialID, "manager", nil, "control", "127.0.0.1", "role-promotion")
	require.NoError(t, err)
	require.True(t, updated.MemoryCapabilities().Private.Write)
	require.True(t, updated.MemoryCapabilities().TeamShared.Write)
	require.Equal(t, "manager", updated.Role)
	require.Empty(t, updated.KeyHash)

	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamUUID)
	owned := createOwnedCredential(t, repo, teamUUID, ownerID, "read-only-owner-key")
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE team_memberships SET maximum_grants = ARRAY['read']::text[] WHERE team_id = ? AND actor_identity_id = ?`, teamUUID, ownerID).Error
	}))
	updated, err = service.UpdateScopesForTeam(ctx, teamUUID, owned.ID, []string{"read"}, nil, "control", "127.0.0.1", "owned-scope-downgrade")
	require.NoError(t, err)
	updated, err = service.UpdateScopesForTeam(ctx, teamUUID, owned.ID, []string{"read", "write"}, nil, "control", "127.0.0.1", "owned-scope-upgrade")
	require.NoError(t, err)
	require.True(t, updated.MemoryCapabilities().Private.Write)
	require.False(t, updated.MemoryCapabilities().TeamShared.Write)
	updated, err = service.UpdateRoleForTeam(ctx, teamUUID, owned.ID, "manager", nil, "control", "127.0.0.1", "owned-role-promotion")
	require.NoError(t, err)
	require.True(t, updated.MemoryCapabilities().Private.Write)
	require.False(t, updated.MemoryCapabilities().TeamShared.Write)
	require.Equal(t, []string{"read", "write"}, updated.Scopes)
	require.Equal(t, []string{"read"}, updated.EffectiveMemoryGrants)

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

func TestCanonicalCredentialSecretMutationHydrationFailureRollsBack(t *testing.T) {
	for _, operation := range []string{"create", "rotate"} {
		t.Run(operation, func(t *testing.T) {
			adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
			defer cleanup()
			ctx := context.Background()
			teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "secret-projection-failure"))
			repo := NewCredentialRepository(appDB, rls, privacypostgres.NewCredentialDeletionRepository(appDB, rls))
			auditService := audit.New(auditpostgres.NewStore(appDB, rls))
			service := accessservice.NewCredentialService(repo, accessservice.NewTeamService(NewTeamRepository(appDB, rls), auditService, nil), auditService, nil)
			req := accessservice.CreateCredentialRequest{Name: "projection-failure", Role: "member", Scopes: []string{"read", "write"}}
			var existing *domain.Credential
			var oldHash, oldPrefix string
			if operation == "rotate" {
				var err error
				existing, _, err = service.CreateCredential(ctx, teamID, req, nil, "control", "127.0.0.1", "before-projection-failure")
				require.NoError(t, err)
				require.NoError(t, adminDB.Raw(`SELECT key_hash,key_prefix FROM credentials WHERE id=?`, existing.ID).Row().Scan(&oldHash, &oldPrefix))
			}

			// Ordinary API-client writes do not read SSO metadata, so this isolates the required projection read.
			require.NoError(t, adminDB.Exec(`
				CREATE FUNCTION test_credential_projection_failure() RETURNS trigger
				LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
				BEGIN
					EXECUTE format('REVOKE SELECT ON TABLE public.sso_identities FROM %I',session_user);
					RETURN NEW;
				END $$;
				CREATE TRIGGER test_credential_projection_failure AFTER INSERT OR UPDATE ON credentials
				FOR EACH ROW WHEN (NEW.name='projection-failure') EXECUTE FUNCTION test_credential_projection_failure();
			`).Error)
			defer func() {
				require.NoError(t, adminDB.Exec(`
					DROP TRIGGER test_credential_projection_failure ON credentials;
					DROP FUNCTION test_credential_projection_failure();
					GRANT SELECT ON sso_identities TO densemem_rls_test;
				`).Error)
			}()

			var result *domain.Credential
			var raw string
			var err error
			if operation == "create" {
				result, raw, err = service.CreateCredential(ctx, teamID, req, nil, "control", "127.0.0.1", "create-projection-failure")
			} else {
				result, raw, err = service.RotateForTeam(ctx, teamID, existing.ID, accessservice.CreateCredentialRequest{}, nil, "control", "127.0.0.1", "rotate-projection-failure")
			}
			require.ErrorContains(t, err, "permission denied")
			require.True(t, result == nil, "failed mutation returned a credential")
			require.True(t, raw == "", "failed mutation returned bearer material")

			if operation == "create" {
				var credentials, identities, spaces int
				require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
					return tx.Raw(`SELECT
						(SELECT count(*) FROM credentials WHERE team_id=? AND name=?),
						(SELECT count(*) FROM actor_identities WHERE team_id=? AND display_name=?),
						(SELECT count(*) FROM memory_spaces s JOIN team_memberships m ON m.id=s.owner_membership_id
						 JOIN actor_identities a ON a.id=m.actor_identity_id WHERE s.team_id=? AND a.display_name=? AND s.kind='private')
					`, teamID, req.Name, teamID, req.Name, teamID, req.Name).Row().Scan(&credentials, &identities, &spaces)
				}))
				assert.Zero(t, credentials, "failed creation committed an undisclosed credential")
				assert.Zero(t, identities, "failed creation committed an identity")
				assert.Zero(t, spaces, "failed creation committed a private space")
			} else {
				var currentHash, currentPrefix string
				require.NoError(t, adminDB.Raw(`SELECT key_hash,key_prefix FROM credentials WHERE id=?`, existing.ID).Row().Scan(&currentHash, &currentPrefix))
				assert.True(t, currentHash == oldHash && currentPrefix == oldPrefix, "failed rotation invalidated the existing secret")
			}
			var canRead bool
			require.NoError(t, adminDB.Raw(`SELECT has_table_privilege(?, 'public.sso_identities', 'SELECT')`, ledgerTestRole).Row().Scan(&canRead))
			assert.True(t, canRead, "projection failure did not roll back the write transaction")
			require.NoError(t, adminDB.Exec(`GRANT SELECT ON sso_identities TO densemem_rls_test`).Error)
			if operation == "rotate" {
				active, err := repo.GetActiveByPrefix(ctx, oldPrefix)
				require.NoError(t, err)
				assert.True(t, active != nil, "existing credential no longer authenticates after failed rotation")
			}
		})
	}
}

func TestCanonicalCredentialLifecycleRetainsDisabledIdentity(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "canonical-key-lifecycle"))
	keyID := uuid.New()
	prefix := "dm_lifecycle_" + keyID.String()[:11]
	repo := NewCredentialRepository(appDB, rls, privacypostgres.NewCredentialDeletionRepository(appDB, rls))
	require.NoError(t, repo.CreateCredential(ctx, &domain.Credential{
		ID: keyID, TeamID: teamID, Name: "lifecycle-key", KeyHash: "lifecycle-hash",
		KeyPrefix: prefix, KeySuffix: "suffix", Scopes: []string{"read", "write"},
	}))

	active, err := repo.GetActiveByPrefix(ctx, prefix)
	require.NoError(t, err)
	require.NotNil(t, active)
	require.Equal(t, []string{"read", "write"}, active.Scopes)
	require.NotEqual(t, uuid.Nil, active.MemorySpaceID)
	require.NotEqual(t, active.MemorySpaceID, active.TeamSharedSpaceID)

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
	require.Equal(t, active.MemorySpaceID, loaded.MemorySpaceID)
	require.Equal(t, active.TeamSharedSpaceID, loaded.TeamSharedSpaceID)

	listed, err := repo.ListByTeam(ctx, teamID, 100, 0)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, active.MemorySpaceID, listed[0].MemorySpaceID)
	require.Equal(t, active.TeamSharedSpaceID, listed[0].TeamSharedSpaceID)

	rotatedPrefix := "dm_rotated_" + keyID.String()[:13]
	rotation, err := repo.RotateForTeam(ctx, teamID, keyID, "rotated-hash", rotatedPrefix, "rotate", nil)
	require.NoError(t, err)
	require.NotNil(t, rotation)
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
	rotation, err = repo.RotateForTeam(ctx, teamID, keyID, "restored-hash", rotatedPrefix, "stored", nil)
	require.NoError(t, err)
	require.Nil(t, rotation)

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

	repo := NewCredentialRepository(appDB, rls, privacypostgres.NewCredentialDeletionRepository(appDB, rls))
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

	rotation, err := repo.RotateForTeam(ctx, teamA, keyA, "shared-actor-hash-a-restored", "dm_"+keyA.String()[:20], "restor", nil)
	require.NoError(t, err)
	require.Nil(t, rotation)
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

type canonicalSSORefreshGroups struct {
	calls  int
	groups []string
	err    error
}

func (r *canonicalSSORefreshGroups) ResolveGroups(context.Context, domain.SSOProvider, string, string) ([]string, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	if r.groups != nil {
		return r.groups, nil
	}
	return []string{"test-group"}, nil
}
func TestCanonicalSSOCredentialRefreshPreservesAuthorizationCeilings(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	team := uuid.MustParse(createLedgerTeam(t, admin, rls, "cache-refresh-auth"))
	owner := createLedgerSSOIdentity(t, admin, rls, team)
	repo := NewCredentialRepository(app, rls, nil)
	credential := createOwnedCredential(t, repo, team, owner, "cache-refresh-key")
	ssoRepo := NewSSORepository(app, rls)
	for _, lookup := range []string{"prefix", "portal_id"} {
		t.Run(lookup, func(t *testing.T) {
			require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
				return tx.Exec(`UPDATE sso_entitlement_cache SET expires_at=now()-interval '1 second' WHERE subject=?`, "subject-"+owner.String()).Error
			}))
			metadata, err := repo.GetByIDForTeam(ctx, team, credential.ID)
			require.NoError(t, err)
			require.Empty(t, metadata.EffectiveMemoryGrants, "metadata remains conservative before entitlement refresh")
			var loaded *domain.Credential
			if lookup == "prefix" {
				loaded, err = repo.GetActiveByPrefix(ctx, credential.KeyPrefix)
			} else {
				loaded, err = repo.GetActiveByID(ctx, credential.ID)
			}
			require.NoError(t, err)
			require.NotNil(t, loaded)
			resolver := &canonicalSSORefreshGroups{}
			validator := accessservice.NewSSOService(ssoRepo, accessservice.SSOConfig{GroupResolver: resolver, EntitlementCacheTTL: time.Minute})
			validated, err := validator.ValidateCredential(ctx, loaded)
			assert.NoError(t, err, "a successful refresh must not intersect with expired-cache empty scopes")
			if assert.NotNil(t, validated) {
				assert.Equal(t, []string{"read", "write"}, validated.Scopes)
				assert.True(t, validated.MemoryCapabilities().Private.Write)
				assert.True(t, validated.MemoryCapabilities().TeamShared.Write)
			}
			assert.Equal(t, 1, resolver.calls)
			cache, err := ssoRepo.GetEntitlementCache(ctx, *loaded.SSOProviderID, loaded.SSOSubject)
			require.NoError(t, err)
			require.NotNil(t, cache)
			assert.Equal(t, "active", cache.Status)
			assert.True(t, cache.ExpiresAt.After(time.Now()))
		})
	}
	for _, failure := range []string{"provider_unavailable", "unmapped_group"} {
		t.Run(failure, func(t *testing.T) {
			require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
				return tx.Exec(`UPDATE sso_entitlement_cache SET expires_at=now()-interval '1 second' WHERE subject=?`, "subject-"+owner.String()).Error
			}))
			loaded, err := repo.GetActiveByPrefix(ctx, credential.KeyPrefix)
			require.NoError(t, err)
			require.NotNil(t, loaded)
			resolver := &canonicalSSORefreshGroups{groups: []string{"unmapped"}}
			want := accessservice.ErrSSOAccessDenied
			if failure == "provider_unavailable" {
				resolver.err = accessservice.ErrSSOGroupRefreshUnavailable
				want = accessservice.ErrSSOEntitlementRefreshStale
			}
			validator := accessservice.NewSSOService(ssoRepo, accessservice.SSOConfig{GroupResolver: resolver, EntitlementCacheTTL: time.Minute})
			validated, err := validator.ValidateCredential(ctx, loaded)
			require.ErrorIs(t, err, want)
			require.Nil(t, validated)
		})
	}
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE sso_entitlement_cache SET expires_at=now()-interval '1 second' WHERE subject=?`, "subject-"+owner.String()).Error
	}))

	t.Run("owner_ceiling", func(t *testing.T) {
		require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
			return tx.Exec(`UPDATE team_memberships SET maximum_grants=ARRAY['read']::text[] WHERE team_id=? AND actor_identity_id=?`, team, owner).Error
		}))
		loaded, err := repo.GetActiveByPrefix(ctx, credential.KeyPrefix)
		require.NoError(t, err)
		require.NotNil(t, loaded)
		validator := accessservice.NewSSOService(ssoRepo, accessservice.SSOConfig{GroupResolver: &canonicalSSORefreshGroups{}, EntitlementCacheTTL: time.Minute})
		validated, err := validator.ValidateCredential(ctx, loaded)
		require.NoError(t, err)
		require.NotNil(t, validated)
		assert.Equal(t, []string{"read"}, validated.Scopes, "owner membership ceiling remains authoritative even when group mapping grants write")
		assert.True(t, validated.MemoryCapabilities().Private.Write)
		assert.False(t, validated.MemoryCapabilities().TeamShared.Write)
	})

	t.Run("fresh_projection_failure", func(t *testing.T) {
		loaded, err := repo.GetActiveByPrefix(ctx, credential.KeyPrefix)
		require.NoError(t, err)
		require.NotNil(t, loaded)
		require.NoError(t, admin.Exec("REVOKE SELECT ON credentials FROM "+ledgerTestRole).Error)
		defer func() { require.NoError(t, admin.Exec("GRANT SELECT ON credentials TO "+ledgerTestRole).Error) }()
		validator := accessservice.NewSSOService(ssoRepo, accessservice.SSOConfig{})
		validated, err := validator.ValidateCredential(ctx, loaded)
		require.ErrorContains(t, err, "permission denied for table credentials")
		require.Nil(t, validated)
	})
	t.Run("key_revoked_after_lookup", func(t *testing.T) {
		loaded, err := repo.GetActiveByPrefix(ctx, credential.KeyPrefix)
		require.NoError(t, err)
		require.NotNil(t, loaded)
		require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
			return tx.Exec(`UPDATE credentials SET status='revoked', revoked_at=now() WHERE id=?`, credential.ID).Error
		}))
		validator := accessservice.NewSSOService(ssoRepo, accessservice.SSOConfig{})
		validated, err := validator.ValidateCredential(ctx, loaded)
		require.ErrorIs(t, err, accessservice.ErrSSOAccessDenied)
		require.Nil(t, validated)
	})
}
