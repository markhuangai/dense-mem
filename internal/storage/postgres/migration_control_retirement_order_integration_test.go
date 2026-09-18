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
