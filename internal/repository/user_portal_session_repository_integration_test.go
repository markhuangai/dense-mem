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

func TestUserPortalSessionsEnforceSystemRLSAndProfileLifecycle(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "portal-session-rls-team-a")
	profileA := createLedgerProfile(t, adminDB, rls, teamA, "portal-session-rls-profile-a")
	teamB := createLedgerTeam(t, adminDB, rls, "portal-session-rls-team-b")
	profileB := createLedgerProfile(t, adminDB, rls, teamB, "portal-session-rls-profile-b")

	repo := NewUserPortalSessionRepository(appDB, rls)
	createdAt := time.Now().UTC()
	sessionA := &domain.UserPortalSession{
		SessionHash: "portal-session-hash-a-" + uuid.NewString(),
		KeyID:       uuid.MustParse(profileA),
		CSRFHash:    "portal-csrf-hash-a",
		ExpiresAt:   createdAt.Add(time.Hour),
		CreatedAt:   createdAt,
	}
	sessionB := &domain.UserPortalSession{
		SessionHash: "portal-session-hash-b-" + uuid.NewString(),
		KeyID:       uuid.MustParse(profileB),
		CSRFHash:    "portal-csrf-hash-b",
		ExpiresAt:   createdAt.Add(time.Hour),
		CreatedAt:   createdAt,
	}
	require.NoError(t, repo.CreateSession(ctx, sessionA))
	require.NoError(t, repo.CreateSession(ctx, sessionB))

	loadedA, err := repo.GetSession(ctx, sessionA.SessionHash)
	require.NoError(t, err)
	require.NotNil(t, loadedA)
	require.Equal(t, sessionA.KeyID, loadedA.KeyID)

	loadedB, err := repo.GetSession(ctx, sessionB.SessionHash)
	require.NoError(t, err)
	require.NotNil(t, loadedB)
	require.Equal(t, sessionB.KeyID, loadedB.KeyID)

	for _, actor := range []struct {
		teamID    string
		profileID string
	}{
		{teamID: teamA, profileID: profileA},
		{teamID: teamB, profileID: profileB},
	} {
		require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, actor.teamID, actor.profileID, func(tx *gorm.DB) error {
			var visible int64
			if err := tx.Raw(`
				SELECT count(*)
				FROM user_portal_sessions
				WHERE session_hash IN (?, ?)
			`, sessionA.SessionHash, sessionB.SessionHash).Scan(&visible).Error; err != nil {
				return err
			}
			require.Zero(t, visible, "non-system contexts must not read portal sessions")

			updated := tx.Exec(`
				UPDATE user_portal_sessions
				SET csrf_hash = 'cross-context-update'
				WHERE session_hash IN (?, ?)
			`, sessionA.SessionHash, sessionB.SessionHash)
			require.NoError(t, updated.Error)
			require.Zero(t, updated.RowsAffected, "non-system contexts must not update portal sessions")

			deleted := tx.Exec(`
				DELETE FROM user_portal_sessions
				WHERE session_hash IN (?, ?)
			`, sessionA.SessionHash, sessionB.SessionHash)
			require.NoError(t, deleted.Error)
			require.Zero(t, deleted.RowsAffected, "non-system contexts must not delete portal sessions")
			return nil
		}))
	}

	require.NoError(t, repo.DeleteSession(ctx, sessionA.SessionHash))
	deletedA, err := repo.GetSession(ctx, sessionA.SessionHash)
	require.NoError(t, err)
	require.Nil(t, deletedA)
	remainingB, err := repo.GetSession(ctx, sessionB.SessionHash)
	require.NoError(t, err)
	require.NotNil(t, remainingB)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM team_profiles WHERE id = ?::uuid`, profileB).Error
	}))
	cascadedB, err := repo.GetSession(ctx, sessionB.SessionHash)
	require.NoError(t, err)
	require.Nil(t, cascadedB)
}
