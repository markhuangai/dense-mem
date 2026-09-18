//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrationControlRetirementAppliesPendingRuntimeMigrationsFirst(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
	require.False(t, columnExists(t, ctx, sqlDB, "usage_metric_buckets", "mcp_tool_calls"))
	require.False(t, columnExists(t, ctx, sqlDB, "usage_metric_buckets", "mcp_tool_failures"))
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)

	require.NoError(t, NewMigratorWithDB(sqlDB).RunMigrationControlRetirement(ctx))

	require.True(t, columnExists(t, ctx, sqlDB, "usage_metric_buckets", "mcp_tool_calls"))
	require.True(t, columnExists(t, ctx, sqlDB, "usage_metric_buckets", "mcp_tool_failures"))
	require.True(t, migrationControlRetirementApplied(t, ctx, sqlDB))
}

func TestMigrationControlRetirementRejectsPendingRuntimePredecessor(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)

	err := runMigrationControlRetirementOnly(t, ctx, sqlDB)
	require.Error(t, err)
	require.Contains(t, err.Error(), "required runtime migration 20260913020001 is not applied")
	require.False(t, migrationControlRetirementApplied(t, ctx, sqlDB))
	for _, table := range migrationControlRetirementTables {
		require.True(t, tableExists(t, ctx, sqlDB, table), "%s must remain after predecessor rejection", table)
	}
}
