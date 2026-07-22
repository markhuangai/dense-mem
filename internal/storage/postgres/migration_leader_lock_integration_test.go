//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrationLeaderLockRequiresReleaseBeforeSecondSessionAcquires(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	db, err := Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	first := NewMigrationLeaderLock(db)
	second := NewMigrationLeaderLock(db)

	firstLease, err := first.TryLock(ctx)
	require.NoError(t, err)
	require.NotNil(t, firstLease)

	blockedLease, err := second.TryLock(ctx)
	require.NoError(t, err)
	require.Nil(t, blockedLease)

	require.NoError(t, firstLease.Release(ctx))

	secondLease, err := second.TryLock(ctx)
	require.NoError(t, err)
	require.NotNil(t, secondLease)
	require.NoError(t, secondLease.Release(ctx))
}
