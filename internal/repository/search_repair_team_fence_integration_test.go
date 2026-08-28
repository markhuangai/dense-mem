package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSearchRepairActiveTeamFenceBlocksConcurrentSoftDelete(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-team-lock")
	locked := make(chan struct{})
	release := make(chan struct{})
	lockErr := make(chan error, 1)
	go func() {
		lockErr <- rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
			active, err := lockSearchRepairActiveTeam(ctx, tx, teamID)
			if err != nil {
				return err
			}
			if !active {
				return gorm.ErrRecordNotFound
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
			return tx.Exec(`UPDATE teams SET status = 'deleted', deleted_at = clock_timestamp() WHERE id = ?::uuid`, teamID).Error
		})
	}()
	select {
	case err := <-deleteDone:
		t.Fatalf("soft delete completed while repair held its active-team fence: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-lockErr)
	require.NoError(t, <-deleteDone)
}
