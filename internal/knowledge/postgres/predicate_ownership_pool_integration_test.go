//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPredicateOwnershipCountedSessionPreservesApplicationPool(t *testing.T) {
	_, appDB, _, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	statement := appDB.Statement
	pool := appDB.ConnPool
	sqlDB, err := appDB.DB()
	require.NoError(t, err)
	counters := &predicateOwnershipBenchmarkCounters{}
	counted := newPredicateOwnershipCountedDB(appDB, counters)
	require.Same(t, statement, appDB.Statement)
	require.NotSame(t, statement, counted.Statement)
	require.Same(t, pool, appDB.ConnPool)
	require.Same(t, pool, appDB.Statement.ConnPool)
	actualDB, err := appDB.DB()
	require.NoError(t, err)
	require.Same(t, sqlDB, actualDB)
	require.NoError(t, counted.Transaction(func(tx *gorm.DB) error {
		return tx.Exec("SELECT 1").Error
	}))
	require.Equal(t, predicateOwnershipBenchmarkCount{statements: 1, transactions: 1, commits: 1}, counters.snapshot())
	require.NoError(t, sqlDB.Close())
	require.Error(t, sqlDB.PingContext(context.Background()))
}
