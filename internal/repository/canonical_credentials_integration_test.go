package repository

import (
	"context"
	"testing"

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
