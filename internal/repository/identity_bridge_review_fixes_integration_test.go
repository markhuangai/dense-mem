package repository

import (
	"context"
	"testing"

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

func TestIdentityCleanupPreflightBlocksMissingMembershipsAndCredentials(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "identity-cleanup-required-rows")
	membershipProfileID := createLedgerProfile(t, adminDB, rls, teamID, "missing-membership-profile")
	credentialProfileID := createLedgerProfile(t, adminDB, rls, teamID, "missing-credential-profile")

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
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
	report, err = NewIdentityCleanupPreflightRepository(adminDB, rls).ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.False(t, report.Ready)
	require.Equal(t, int64(2), report.UnresolvedCount)
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
