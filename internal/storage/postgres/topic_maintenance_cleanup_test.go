package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestTopicMaintenanceUpgradeCleanupSummary(t *testing.T) {
	require.Zero(t, (TopicMaintenanceCleanupSummary{}).DiscardedTotal())
	summary := TopicMaintenanceCleanupSummary{
		DiscardedBatches: 1, ClearedTeamLeases: 2, ClearedCommunityLeases: 3,
		ClearedConsolidationLeases: 4, ReconciledAttempts: 5,
	}
	require.EqualValues(t, 15, summary.DiscardedTotal())
}

func TestTopicMaintenanceUpgradeCleanupFailure(t *testing.T) {
	for _, tc := range []struct {
		name, state, reason string
		cause               error
	}{
		{"lock timeout", "55P03", "table lock unavailable (SQLSTATE 55P03)",
			&pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout", Detail: "database-payload-canary"}},
		{"database error", "P0001", "database operation failed (SQLSTATE P0001)",
			&pgconn.PgError{Code: "P0001", Message: "database-payload-canary"}},
		{"invalid SQLSTATE", "", "database operation failed; invalid SQLSTATE omitted",
			&pgconn.PgError{Code: "database-payload-canary"}},
		{"timeout", "", "deadline_exceeded", context.DeadlineExceeded},
		{"other", "", "migration operation failed; database payload omitted", errors.New("database-payload-canary")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, failure := topicMaintenanceCleanupFailure(tc.cause)
			require.Equal(t, tc.state, state)
			require.ErrorIs(t, failure, tc.cause)
			require.Equal(t, "topic_maintenance_upgrade_cleanup: "+tc.reason, MigrationFailureReason(failure))
			require.Equal(t, "topic_maintenance_upgrade_cleanup_failed: "+tc.reason, failure.Error())
		})
	}
}
