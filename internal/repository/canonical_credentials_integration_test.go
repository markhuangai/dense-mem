package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

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
		WHERE legacy_profile_id = ?::uuid
	`, profileID).Row().Scan(&prefix))

	repo := NewAPIKeyRepository(appDB, rls)
	key, err := repo.GetActiveByPrefix(ctx, prefix)
	require.NoError(t, err)
	require.NotNil(t, key)
	require.Equal(t, []string{"read", "write"}, key.Scopes)

	// SSO-backed profiles keep the legacy profile as the credential actor while
	// the membership points at the stable SSO identity. Authentication must
	// continue to resolve the credential through its legacy ownership alias.
	ssoIdentityID := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO actor_identities (id, kind, team_id, display_name)
			VALUES (?::uuid, 'human', ?::uuid, 'SSO owner')
		`, ssoIdentityID, teamID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE team_memberships
			SET actor_identity_id = ?::uuid
			WHERE legacy_profile_id = ?::uuid
		`, ssoIdentityID, profileID).Error
	}))
	key, err = repo.GetActiveByPrefix(ctx, prefix)
	require.NoError(t, err)
	require.NotNil(t, key)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			DELETE FROM membership_grants
			WHERE membership_id = (SELECT id FROM team_memberships WHERE legacy_profile_id = ?::uuid)
			  AND grant_name = 'write'
		`, profileID).Error
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
