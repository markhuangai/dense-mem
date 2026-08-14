package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestIdentityCleanupPreflightAllowsRevokedSSOMembershipWithActiveActor(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	providerID := uuid.NewString()
	identityID := uuid.NewString()
	teamOneID := createLedgerTeam(t, adminDB, rls, "sso-membership-revoked-one")
	teamTwoID := createLedgerTeam(t, adminDB, rls, "sso-membership-active-two")
	profileOneID := uuid.NewString()
	profileTwoID := uuid.NewString()

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO sso_providers (id, name, kind, issuer_url, client_id)
			VALUES (?::uuid, 'shared-sso-provider', 'generic_oidc', 'https://shared-sso.example', 'shared-sso-client')
		`, providerID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO sso_identities (id, provider_id, subject, external_id, email, display_name, active)
			VALUES (?::uuid, ?::uuid, 'shared-sso-subject', 'shared-sso-external', 'shared-sso@example.com', 'Shared SSO User', true)
		`, identityID, providerID).Error; err != nil {
			return err
		}
		for _, profile := range []struct {
			id     string
			teamID string
			name   string
		}{
			{profileOneID, teamOneID, "shared-sso-one"},
			{profileTwoID, teamTwoID, "shared-sso-two"},
		} {
			if err := tx.Exec(`
				INSERT INTO team_profiles (
					id, team_id, key_hash, key_prefix, key_suffix, name, scopes, role, auth_source,
					sso_identity_id, sso_provider_id, sso_subject, sso_email, sso_group_id, sso_entitlement_status
				) VALUES (?::uuid, ?::uuid, NULL, NULL, NULL, ?, ARRAY['read']::text[], 'member', 'sso',
					?::uuid, ?::uuid, 'shared-sso-subject', 'shared-sso@example.com', 'shared-sso-group', 'active')
			`, profile.id, profile.teamID, profile.name, identityID, providerID).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec(`
			UPDATE identity_compatibility_state
			SET state = 'reconciled', backup_checkpoint = 'checkpoint-sso-membership', deployment_fingerprint = 'release-sso-membership'
			WHERE singleton = true
		`).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO v2_compatibility_markers (marker_kind, version, status)
			VALUES ('identity_cutover', 'identity-bridge-sso-membership', 'compatible')
		`).Error
	}))

	preflight := NewIdentityCleanupPreflightRepository(adminDB, rls)
	initial, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.True(t, initial.Ready)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE team_profiles
			SET revoked_at = now()
			WHERE id = ?::uuid
		`, profileOneID).Error
	}))

	var active bool
	require.NoError(t, adminDB.Raw(`
		SELECT active FROM actor_identities WHERE id = ?::uuid
	`, identityID).Row().Scan(&active))
	require.True(t, active)
	report, err := preflight.ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.True(t, report.Ready)
	require.Zero(t, report.UnresolvedCount)
}

func TestIdentityCleanupPreflightAllowsNaturallyExpiredCredentialStatus(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "identity-cleanup-expired-status")
	profileID := createLedgerProfile(t, adminDB, rls, teamID, "expired-status-profile")

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE team_profiles
			SET expires_at = now() - interval '1 hour'
			WHERE id = ?::uuid
		`, profileID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE credentials
			SET status = 'active'
			WHERE legacy_profile_id = ?::uuid
		`, profileID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE identity_compatibility_state
			SET state = 'reconciled', backup_checkpoint = 'checkpoint-expired-status', deployment_fingerprint = 'release-expired-status'
			WHERE singleton = true
		`).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO v2_compatibility_markers (marker_kind, version, status)
			VALUES ('identity_cutover', 'identity-bridge-expired-status', 'compatible')
		`).Error
	}))

	report, err := NewIdentityCleanupPreflightRepository(adminDB, rls).ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.True(t, report.Ready)
	require.Zero(t, report.UnresolvedCount)
}

func TestIdentityCleanupPreflightBlocksWrongCredentialKind(t *testing.T) {
	adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "identity-cleanup-credential-kind")
	profileID := createLedgerProfile(t, adminDB, rls, teamID, "credential-kind-profile")

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE identity_compatibility_state
			SET state = 'reconciled', backup_checkpoint = 'checkpoint-credential-kind', deployment_fingerprint = 'release-credential-kind'
			WHERE singleton = true
		`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO v2_compatibility_markers (marker_kind, version, status)
			VALUES ('identity_cutover', 'identity-bridge-credential-kind', 'compatible')
		`).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE credentials
			SET kind = 'oauth'
			WHERE legacy_profile_id = ?::uuid
		`, profileID).Error
	}))

	report, err := NewIdentityCleanupPreflightRepository(adminDB, rls).ReadIdentityCleanupPreflight(ctx)
	require.NoError(t, err)
	require.False(t, report.Ready)
	require.Equal(t, int64(1), report.UnresolvedCount)
}
