package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
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
		WHERE id = ?::uuid
	`, profileID).Row().Scan(&prefix))

	repo := NewAPIKeyRepository(appDB, rls)
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

func TestCanonicalAPIKeyLifecycleRetainsDisabledIdentity(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "canonical-key-lifecycle"))
	keyID := uuid.New()
	prefix := "dm_lifecycle_" + keyID.String()[:11]
	repo := NewAPIKeyRepository(appDB, rls)
	require.NoError(t, repo.CreateStandardKey(ctx, &domain.APIKey{
		ID: keyID, TeamID: teamID, Name: "lifecycle-key", KeyHash: "lifecycle-hash",
		KeyPrefix: prefix, KeySuffix: "suffix", Scopes: []string{"read", "write"},
	}))

	active, err := repo.GetActiveByPrefix(ctx, prefix)
	require.NoError(t, err)
	require.NotNil(t, active)
	require.Equal(t, []string{"read", "write"}, active.Scopes)

	rows, err := repo.UpdateNameForProfile(ctx, teamID, keyID, "renamed-key")
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	rows, err = repo.UpdateRoleForProfile(ctx, teamID, keyID, "manager", []string{"read", "feedback:read"})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	loaded, err := repo.GetByIDForProfile(ctx, teamID, keyID)
	require.NoError(t, err)
	require.Equal(t, "renamed-key", loaded.Name)
	require.Equal(t, "manager", loaded.Role)
	require.Equal(t, []string{"read", "feedback:read"}, loaded.Scopes)

	rotatedPrefix := "dm_rotated_" + keyID.String()[:13]
	rows, err = repo.RotateForProfile(ctx, teamID, keyID, "rotated-hash", rotatedPrefix, "rotate", nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	require.NoError(t, repo.TouchLastUsedBatch(ctx, []LastUsedUpdate{{ID: keyID, At: time.Now().UTC()}}))
	active, err = repo.GetActiveByPrefix(ctx, rotatedPrefix)
	require.NoError(t, err)
	require.NotNil(t, active)
	require.Equal(t, "rotated-hash", active.KeyHash)
	require.NotNil(t, active.LastUsedAt)

	rows, err = repo.RevokeForProfile(ctx, teamID, keyID)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	active, err = repo.GetActiveByPrefix(ctx, rotatedPrefix)
	require.NoError(t, err)
	require.Nil(t, active)
	rows, err = repo.RotateForProfile(ctx, teamID, keyID, "restored-hash", rotatedPrefix, "stored", nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	rows, err = repo.DeleteForProfile(ctx, teamID, keyID)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	count, err := repo.CountByProfile(ctx, teamID)
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

	repo := NewAPIKeyRepository(appDB, rls)
	rows, err := repo.RevokeForProfile(ctx, teamA, keyA)
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

	rows, err = repo.RotateForProfile(ctx, teamA, keyA, "shared-actor-hash-a-restored", "dm_"+keyA.String()[:20], "restor", nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	rows, err = repo.DeleteForProfile(ctx, teamA, keyA)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT active FROM actor_identities WHERE id = ?`, keyA).Row().Scan(&actorActive)
	}))
	require.True(t, actorActive)
}
