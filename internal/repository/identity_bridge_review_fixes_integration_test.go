package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestIdentityBridgeSynchronizesNewAndUpdatedSSOIdentities(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	providerID := uuid.NewString()
	identityID := uuid.NewString()

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO sso_providers (id, name, kind, issuer_url, client_id)
			VALUES (?::uuid, ?, 'generic_oidc', 'https://issuer.example', 'client')
		`, providerID, "bridge-provider").Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO sso_identities (id, provider_id, subject, external_id, email, display_name, active)
			VALUES (?::uuid, ?::uuid, 'subject-1', 'external-1', 'person@example.com', 'Person One', true)
		`, identityID, providerID).Error
	}))

	var actorProvider, actorSubject, actorName string
	var actorActive bool
	require.NoError(t, adminDB.Raw(`
		SELECT provider, subject, display_name, active
		FROM actor_identities
		WHERE id = ?::uuid
	`, identityID).Row().Scan(&actorProvider, &actorSubject, &actorName, &actorActive))
	require.Equal(t, providerID, actorProvider)
	require.Equal(t, "subject-1", actorSubject)
	require.Equal(t, "Person One", actorName)
	require.True(t, actorActive)

	var linkedIdentity string
	require.NoError(t, adminDB.Raw(`
		SELECT identity_id::text
		FROM identity_external_links
		WHERE provider = ? AND external_id = 'external-1'
	`, providerID).Row().Scan(&linkedIdentity))
	require.Equal(t, identityID, linkedIdentity)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE sso_identities
			SET subject = 'subject-2', external_id = 'external-2', display_name = 'Person Two', active = false
			WHERE id = ?::uuid
		`, identityID).Error
	}))

	require.NoError(t, adminDB.Raw(`
		SELECT provider, subject, display_name, active
		FROM actor_identities
		WHERE id = ?::uuid
	`, identityID).Row().Scan(&actorProvider, &actorSubject, &actorName, &actorActive))
	require.Equal(t, providerID, actorProvider)
	require.Equal(t, "subject-2", actorSubject)
	require.Equal(t, "Person Two", actorName)
	require.False(t, actorActive)
	var staleLinks, currentLinks int
	require.NoError(t, adminDB.Raw(`
		SELECT
			count(*) FILTER (WHERE external_id = 'external-1'),
			count(*) FILTER (WHERE external_id = 'external-2')
		FROM identity_external_links
		WHERE identity_id = ?::uuid
	`, identityID).Row().Scan(&staleLinks, &currentLinks))
	require.Zero(t, staleLinks)
	require.Equal(t, 1, currentLinks)

	teamID := createLedgerTeam(t, adminDB, rls, "sso-owner-team")
	profileID := uuid.NewString()
	keyPrefix := "dm_sso_owner_" + uuid.NewString()[:11]
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO team_profiles (
				id, team_id, key_hash, key_prefix, key_suffix, name, scopes, role, sso_owner_identity_id
			) VALUES (?::uuid, ?::uuid, 'hash-sso-owner', ?, 'suffix', 'sso-owner-key', ARRAY['read']::text[], 'member', ?::uuid)
		`, profileID, teamID, keyPrefix, identityID).Error
	}))
	var ownerIdentity string
	require.NoError(t, adminDB.Raw(`
		SELECT owner_identity_id::text
		FROM credentials
		WHERE legacy_profile_id = ?::uuid
	`, profileID).Row().Scan(&ownerIdentity))
	require.Equal(t, identityID, ownerIdentity)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM sso_identities WHERE id = ?::uuid`, identityID).Error
	}))
	require.NoError(t, adminDB.Raw(`
		SELECT COALESCE(owner_identity_id::text, '')
		FROM credentials
		WHERE legacy_profile_id = ?::uuid
	`, profileID).Row().Scan(&ownerIdentity))
	require.Empty(t, ownerIdentity)
}

func TestIdentityBridgeDeletesLegacyOwnershipAlias(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "identity-bridge-delete-alias")
	profileID := createLedgerProfile(t, adminDB, rls, teamID, "delete-alias-profile")

	var aliasCount int
	require.NoError(t, adminDB.Raw(`
		SELECT count(*)
		FROM ownership_aliases
		WHERE team_id = ?::uuid AND legacy_owner_id = ?::uuid
	`, teamID, profileID).Row().Scan(&aliasCount))
	require.Equal(t, 1, aliasCount)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM team_profiles WHERE id = ?::uuid`, profileID).Error
	}))

	require.NoError(t, adminDB.Raw(`
		SELECT count(*)
		FROM ownership_aliases
		WHERE team_id = ?::uuid AND legacy_owner_id = ?::uuid
	`, teamID, profileID).Row().Scan(&aliasCount))
	require.Zero(t, aliasCount)
}

func TestIdentityCleanupPreflightRejectsDisabledBridgeTriggers(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	tests := []struct {
		name    string
		disable string
		enable  string
	}{
		{
			name:    "legacy profile trigger",
			disable: `ALTER TABLE team_profiles DISABLE TRIGGER team_profiles_identity_bridge`,
			enable:  `ALTER TABLE team_profiles ENABLE TRIGGER team_profiles_identity_bridge`,
		},
		{
			name:    "sso identity trigger",
			disable: `ALTER TABLE sso_identities DISABLE TRIGGER sso_identities_identity_bridge`,
			enable:  `ALTER TABLE sso_identities ENABLE TRIGGER sso_identities_identity_bridge`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
				return tx.Exec(tt.disable).Error
			}))
			defer func() {
				_ = rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
					return tx.Exec(tt.enable).Error
				})
			}()

			report, err := NewIdentityCleanupPreflightRepository(adminDB, rls).ReadIdentityCleanupPreflight(ctx)
			require.NoError(t, err)
			require.False(t, report.Ready)
			require.NotEmpty(t, report.Blockers)
			require.Equal(t, "identity_bridge_missing", report.Blockers[0].Code)
		})
	}
}

func TestIdentityCleanupPreflightBlocksStaleMembershipGrants(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "identity-cleanup-stale-grants")
	profileID := createLedgerProfile(t, adminDB, rls, teamID, "stale-grants-profile")

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE identity_compatibility_state
			SET state = 'reconciled', backup_checkpoint = 'checkpoint-stale-grants', deployment_fingerprint = 'release-stale-grants'
			WHERE singleton = true
		`).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO v2_compatibility_markers (marker_kind, version, status)
			VALUES ('identity_cutover', 'identity-bridge-stale-grants', 'compatible')
		`).Error
	}))

	preflight := NewIdentityCleanupPreflightRepository(adminDB, rls)
	initial, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.True(t, initial.Ready)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			DELETE FROM membership_grants
			WHERE membership_id = (SELECT id FROM team_memberships WHERE legacy_profile_id = ?::uuid)
			  AND grant_name = 'read'
		`, profileID).Error
	}))

	report, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.False(t, report.Ready)
	require.Equal(t, int64(1), report.UnresolvedCount)
}

func TestIdentityCleanupPreflightBlocksWrongOwnershipAliasTargets(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "identity-cleanup-stale-alias")
	profileID := createLedgerProfile(t, adminDB, rls, teamID, "stale-alias-profile")
	otherProfileID := createLedgerProfile(t, adminDB, rls, teamID, "other-alias-profile")

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE identity_compatibility_state
			SET state = 'reconciled', backup_checkpoint = 'checkpoint-stale-alias', deployment_fingerprint = 'release-stale-alias'
			WHERE singleton = true
		`).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO v2_compatibility_markers (marker_kind, version, status)
			VALUES ('identity_cutover', 'identity-bridge-stale-alias', 'compatible')
		`).Error
	}))

	preflight := NewIdentityCleanupPreflightRepository(adminDB, rls)
	initial, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.True(t, initial.Ready)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE ownership_aliases
			SET canonical_identity_id = ?::uuid, credential_id = ?::uuid
			WHERE team_id = ?::uuid AND legacy_owner_id = ?::uuid
		`, otherProfileID, otherProfileID, teamID, profileID).Error
	}))

	report, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.False(t, report.Ready)
	require.Equal(t, int64(1), report.UnresolvedCount)
}

func TestIdentityCleanupPreflightBlocksStaleCredentialOwnerIdentity(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	providerID := uuid.NewString()
	identityID := uuid.NewString()
	teamID := createLedgerTeam(t, adminDB, rls, "identity-cleanup-stale-owner")
	profileID := uuid.NewString()
	keyPrefix := "dm_stale_" + uuid.NewString()[:15]

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO sso_providers (id, name, kind, issuer_url, client_id)
			VALUES (?::uuid, 'stale-owner-provider', 'generic_oidc', 'https://stale-owner.example', 'stale-owner-client')
		`, providerID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO sso_identities (id, provider_id, subject, external_id, email, display_name, active)
			VALUES (?::uuid, ?::uuid, 'stale-owner-subject', 'stale-owner-external', 'stale-owner@example.com', 'Stale Owner', true)
		`, identityID, providerID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO team_profiles (
				id, team_id, key_hash, key_prefix, key_suffix, name, scopes, role, sso_owner_identity_id
			) VALUES (?::uuid, ?::uuid, 'hash-stale-owner', ?, 'suffix', 'stale-owner-key', ARRAY['read','write']::text[], 'member', ?::uuid)
		`, profileID, teamID, keyPrefix, identityID).Error
	}))

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE identity_compatibility_state
			SET state = 'reconciled', backup_checkpoint = 'checkpoint-stale-owner', deployment_fingerprint = 'release-stale-owner'
			WHERE singleton = true
		`).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO v2_compatibility_markers (marker_kind, version, status)
			VALUES ('identity_cutover', 'identity-bridge-stale-owner', 'compatible')
		`).Error
	}))

	preflight := NewIdentityCleanupPreflightRepository(adminDB, rls)
	initial, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.True(t, initial.Ready)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE credentials
			SET owner_identity_id = NULL
			WHERE legacy_profile_id = ?::uuid
		`, profileID).Error
	}))

	report, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.False(t, report.Ready)
	require.Equal(t, int64(1), report.UnresolvedCount)
}

func TestIdentityCleanupPreflightBlocksWrongCredentialAndMembershipActors(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "identity-cleanup-stale-actors")
	profileID := createLedgerProfile(t, adminDB, rls, teamID, "stale-actors-profile")
	fakeActorID := uuid.NewString()

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE identity_compatibility_state
			SET state = 'reconciled', backup_checkpoint = 'checkpoint-stale-actors', deployment_fingerprint = 'release-stale-actors'
			WHERE singleton = true
		`).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO v2_compatibility_markers (marker_kind, version, status)
			VALUES ('identity_cutover', 'identity-bridge-stale-actors', 'compatible')
		`).Error
	}))

	preflight := NewIdentityCleanupPreflightRepository(adminDB, rls)
	initial, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.True(t, initial.Ready)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO actor_identities (id, kind, team_id, display_name)
			VALUES (?::uuid, 'api_client', ?::uuid, 'Fake Actor')
		`, fakeActorID, teamID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE team_memberships
			SET actor_identity_id = ?::uuid
			WHERE legacy_profile_id = ?::uuid
		`, fakeActorID, profileID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE credentials
			SET actor_identity_id = ?::uuid
			WHERE legacy_profile_id = ?::uuid
		`, fakeActorID, profileID).Error
	}))

	report, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.False(t, report.Ready)
	require.Equal(t, int64(1), report.UnresolvedCount)
}

func TestIdentityCleanupPreflightBlocksStaleProfileActorMetadata(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "identity-cleanup-stale-actor-metadata")
	profileID := createLedgerProfile(t, adminDB, rls, teamID, "stale-actor-metadata-profile")

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE identity_compatibility_state
			SET state = 'reconciled', backup_checkpoint = 'checkpoint-stale-actor-metadata', deployment_fingerprint = 'release-stale-actor-metadata'
			WHERE singleton = true
		`).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO v2_compatibility_markers (marker_kind, version, status)
			VALUES ('identity_cutover', 'identity-bridge-stale-actor-metadata', 'compatible')
		`).Error
	}))

	preflight := NewIdentityCleanupPreflightRepository(adminDB, rls)
	initial, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.True(t, initial.Ready)

	for _, tt := range []struct {
		name    string
		column  string
		value   string
		restore string
	}{
		{name: "kind", column: "kind", value: "human", restore: "api_client"},
		{name: "display name", column: "display_name", value: "stale actor name", restore: "stale-actor-metadata-profile"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
				return tx.Exec("UPDATE actor_identities SET "+tt.column+" = ? WHERE id = ?::uuid", tt.value, profileID).Error
			}))

			report, err := preflight.ReadIdentityCleanupPreflight(ctx)
			require.NoError(t, err)
			require.False(t, report.Ready)
			require.Equal(t, int64(1), report.UnresolvedCount)

			require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
				return tx.Exec("UPDATE actor_identities SET "+tt.column+" = ? WHERE id = ?::uuid", tt.restore, profileID).Error
			}))
		})
	}
}

func TestIdentityCleanupPreflightUsesLiveBridgeCounts(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "identity-cleanup-live-counts")
	createLedgerProfile(t, adminDB, rls, teamID, "first-profile")

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE identity_compatibility_state
			SET state = 'reconciled', backup_checkpoint = 'checkpoint-1', deployment_fingerprint = 'release-1'
			WHERE singleton = true
		`).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO v2_compatibility_markers (marker_kind, version, status)
			VALUES ('identity_cutover', 'identity-bridge-test', 'compatible')
			ON CONFLICT (marker_kind, version) DO UPDATE SET status = EXCLUDED.status
		`).Error
	}))

	preflight := NewIdentityCleanupPreflightRepository(adminDB, rls)
	first, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.True(t, first.Ready)
	require.Zero(t, first.UnresolvedCount)

	createLedgerProfile(t, adminDB, rls, teamID, "second-profile")
	second, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.True(t, second.Ready)
	require.Equal(t, first.LegacyProfileCount+1, second.LegacyProfileCount)
	require.Equal(t, first.IdentityCount+1, second.IdentityCount)
	require.Equal(t, first.MembershipCount+1, second.MembershipCount)
	require.Equal(t, first.CredentialCount+1, second.CredentialCount)
	require.Equal(t, first.AliasCount+1, second.AliasCount)
	require.Equal(t, int64(0), second.UnresolvedCount)
}

func TestIdentityCleanupPreflightBlocksStaleCanonicalAuthorization(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "identity-cleanup-stale-authorization")
	profileID := createLedgerProfile(t, adminDB, rls, teamID, "stale-authorization-profile")

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE identity_compatibility_state
			SET state = 'reconciled', backup_checkpoint = 'checkpoint-stale-authorization', deployment_fingerprint = 'release-stale-authorization'
			WHERE singleton = true
		`).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO v2_compatibility_markers (marker_kind, version, status)
			VALUES ('identity_cutover', 'identity-bridge-stale-authorization', 'compatible')
		`).Error
	}))

	preflight := NewIdentityCleanupPreflightRepository(adminDB, rls)
	initial, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.True(t, initial.Ready)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE team_memberships
			SET status = 'revoked', team_admin = true, maximum_grants = ARRAY['read']::text[]
			WHERE legacy_profile_id = ?::uuid
		`, profileID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE credentials
			SET key_hash = 'stale-hash', scopes = ARRAY['read']::text[], rate_limit = 999,
			    status = 'revoked', revoked_at = now()
			WHERE legacy_profile_id = ?::uuid
		`, profileID).Error
	}))

	report, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.False(t, report.Ready)
	require.Equal(t, int64(1), report.UnresolvedCount)
}

func TestIdentityCleanupPreflightBlocksStaleSSOExternalLink(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	providerID := uuid.NewString()
	identityID := uuid.NewString()

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO sso_providers (id, name, kind, issuer_url, client_id)
			VALUES (?::uuid, 'stale-link-provider', 'generic_oidc', 'https://stale-link.example', 'stale-link-client')
		`, providerID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO sso_identities (id, provider_id, subject, external_id, email, display_name, active)
			VALUES (?::uuid, ?::uuid, 'stale-link-subject', 'stale-link-external', 'stale-link@example.com', 'Stale Link', true)
		`, identityID, providerID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE identity_compatibility_state
			SET state = 'reconciled', backup_checkpoint = 'checkpoint-stale-link', deployment_fingerprint = 'release-stale-link'
			WHERE singleton = true
		`).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO v2_compatibility_markers (marker_kind, version, status)
			VALUES ('identity_cutover', 'identity-bridge-stale-link', 'compatible')
		`).Error
	}))

	preflight := NewIdentityCleanupPreflightRepository(adminDB, rls)
	initial, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.True(t, initial.Ready)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE identity_external_links
			SET external_id = 'stale-link-external-value'
			WHERE identity_id = ?::uuid
		`, identityID).Error
	}))

	report, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.False(t, report.Ready)
	require.Equal(t, int64(1), report.UnresolvedCount)
}

func TestIdentityCleanupPreflightBlocksStaleSSOActor(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	providerID := uuid.NewString()
	identityID := uuid.NewString()

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO sso_providers (id, name, kind, issuer_url, client_id)
			VALUES (?::uuid, 'stale-actor-provider', 'generic_oidc', 'https://stale-actor.example', 'stale-actor-client')
		`, providerID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO sso_identities (id, provider_id, subject, external_id, email, display_name, active)
			VALUES (?::uuid, ?::uuid, 'stale-actor-subject', 'stale-actor-external', 'stale-actor@example.com', 'Stale Actor', true)
		`, identityID, providerID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE identity_compatibility_state
			SET state = 'reconciled', backup_checkpoint = 'checkpoint-stale-actor', deployment_fingerprint = 'release-stale-actor'
			WHERE singleton = true
		`).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO v2_compatibility_markers (marker_kind, version, status)
			VALUES ('identity_cutover', 'identity-bridge-stale-actor', 'compatible')
		`).Error
	}))

	preflight := NewIdentityCleanupPreflightRepository(adminDB, rls)
	initial, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.True(t, initial.Ready)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE actor_identities
			SET provider = 'stale-provider', subject = 'stale-subject', display_name = 'Stale Name', active = false
			WHERE id = ?::uuid
		`, identityID).Error
	}))

	report, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.False(t, report.Ready)
	require.Equal(t, int64(1), report.UnresolvedCount)
}

func TestIdentityBridgeLastUsedUpdateOnlyTouchesCanonicalCredential(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "identity-bridge-last-used")
	profileID := createLedgerProfile(t, adminDB, rls, teamID, "last-used-profile")
	var grantCreatedAt, membershipUpdatedAt string
	require.NoError(t, adminDB.Raw(`
		SELECT to_char(g.created_at, 'YYYY-MM-DD"T"HH24:MI:SS.USOF'), to_char(m.updated_at, 'YYYY-MM-DD"T"HH24:MI:SS.USOF')
		FROM team_memberships m
		JOIN membership_grants g ON g.membership_id = m.id
		WHERE m.legacy_profile_id = ?::uuid
		ORDER BY g.grant_name
		LIMIT 1
	`, profileID).Row().Scan(&grantCreatedAt, &membershipUpdatedAt))

	lastUsed := "2026-08-14T12:00:00Z"
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE team_profiles
			SET last_used_at = ?::timestamptz
			WHERE id = ?::uuid
		`, lastUsed, profileID).Error
	}))

	var canonicalLastUsed time.Time
	require.NoError(t, adminDB.Raw(`
		SELECT last_used_at
		FROM credentials
		WHERE legacy_profile_id = ?::uuid
	`, profileID).Row().Scan(&canonicalLastUsed))
	var nextGrantCreatedAt, nextMembershipUpdatedAt string
	require.NoError(t, adminDB.Raw(`
		SELECT to_char(g.created_at, 'YYYY-MM-DD"T"HH24:MI:SS.USOF'), to_char(m.updated_at, 'YYYY-MM-DD"T"HH24:MI:SS.USOF')
		FROM team_memberships m
		JOIN membership_grants g ON g.membership_id = m.id
		WHERE m.legacy_profile_id = ?::uuid
		ORDER BY g.grant_name
		LIMIT 1
	`, profileID).Row().Scan(&nextGrantCreatedAt, &nextMembershipUpdatedAt))
	require.True(t, canonicalLastUsed.Equal(time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)))
	require.Equal(t, grantCreatedAt, nextGrantCreatedAt)
	require.Equal(t, membershipUpdatedAt, nextMembershipUpdatedAt)
}

func TestIdentityCleanupPreflightBlocksMissingMembershipsAndCredentials(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "identity-cleanup-required-rows")
	membershipProfileID := createLedgerProfile(t, adminDB, rls, teamID, "missing-membership-profile")
	credentialProfileID := createLedgerProfile(t, adminDB, rls, teamID, "missing-credential-profile")
	providerID := uuid.NewString()
	identityID := uuid.NewString()

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO sso_providers (id, name, kind, issuer_url, client_id)
			VALUES (?::uuid, 'cleanup-provider', 'generic_oidc', 'https://cleanup.example', 'cleanup-client')
		`, providerID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO sso_identities (id, provider_id, subject, external_id, email, display_name, active)
			VALUES (?::uuid, ?::uuid, 'cleanup-subject', 'cleanup-external', 'cleanup@example.com', 'Cleanup Person', true)
		`, identityID, providerID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE identity_compatibility_state
			SET state = 'reconciled', backup_checkpoint = 'checkpoint-required-rows', deployment_fingerprint = 'release-required-rows'
			WHERE singleton = true
		`).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO v2_compatibility_markers (marker_kind, version, status)
			VALUES ('identity_cutover', 'identity-bridge-required-rows', 'compatible')
			ON CONFLICT (marker_kind, version) DO UPDATE SET status = EXCLUDED.status
		`).Error
	}))

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM team_memberships WHERE legacy_profile_id = ?::uuid`, membershipProfileID).Error
	}))
	report, err := NewIdentityCleanupPreflightRepository(adminDB, rls).ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.False(t, report.Ready)
	require.Equal(t, int64(1), report.UnresolvedCount)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM credentials WHERE legacy_profile_id = ?::uuid`, credentialProfileID).Error
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM identity_external_links WHERE identity_id = ?::uuid`, identityID).Error
	}))
	report, err = NewIdentityCleanupPreflightRepository(adminDB, rls).ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.False(t, report.Ready)
	require.Equal(t, int64(3), report.UnresolvedCount)
}

func TestIdentityCleanupPreflightUsesLatestCutoverMarker(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "identity-cleanup-marker-order")
	createLedgerProfile(t, adminDB, rls, teamID, "marker-profile")

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE identity_compatibility_state
			SET state = 'reconciled', backup_checkpoint = 'checkpoint-marker', deployment_fingerprint = 'release-marker'
			WHERE singleton = true
		`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO v2_compatibility_markers (marker_kind, version, status, created_at)
			VALUES ('identity_cutover', 'identity-bridge-compatible', 'compatible', now() - interval '1 minute')
		`).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO v2_compatibility_markers (marker_kind, version, status, created_at)
			VALUES ('identity_cutover', 'identity-bridge-incompatible', 'incompatible', now())
		`).Error
	}))

	report, err := NewIdentityCleanupPreflightRepository(adminDB, rls).ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.False(t, report.Ready)
	found := false
	for _, blocker := range report.Blockers {
		if blocker.Code == "identity_cutover_marker_missing" {
			found = true
			break
		}
	}
	require.True(t, found)
}

func TestIdentityCleanupPreflightRejectsMissingBridgeRelations(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`DROP TABLE membership_grants, identity_external_links`).Error
	}))

	report, err := NewIdentityCleanupPreflightRepository(adminDB, rls).ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.False(t, report.Ready)
	require.Len(t, report.Blockers, 1)
	require.Equal(t, "identity_bridge_missing", report.Blockers[0].Code)
}
