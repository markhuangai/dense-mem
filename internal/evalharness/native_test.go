package evalharness

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDurableUsageUnknownDoesNotBecomeZeroOrAnUnfinishedAttempt(t *testing.T) {
	now := time.Now()
	attempt := NativeAttempt{OperationKey: "maintenance:" + uuid.NewString(), AssessmentID: uuid.NewString(), Number: 1, Terminal: true, ReconciledAt: &now}
	measurement, err := MeasureAttempts([]NativeAttempt{attempt})
	require.NoError(t, err)
	require.Equal(t, 1, measurement.UnknownUsage)
	require.Zero(t, measurement.Unfinished)
	_, err = MeasureAttempts([]NativeAttempt{attempt, attempt})
	require.ErrorContains(t, err, "duplicated")
	attempt.ReconciledAt = nil
	measurement, err = MeasureAttempts([]NativeAttempt{attempt})
	require.NoError(t, err)
	require.Equal(t, 1, measurement.Unfinished)
}

func TestRunnerGuardRejectsLocalExecutionAndReruns(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	require.ErrorContains(t, RequireRunner(), "local execution")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("RUNNER_ENVIRONMENT", "github-hosted")
	t.Setenv("GITHUB_RUN_ID", "42")
	t.Setenv("GITHUB_RUN_ATTEMPT", "1")
	require.NoError(t, RequireRunner())
	t.Setenv("GITHUB_RUN_ATTEMPT", "2")
	require.ErrorContains(t, RequireRunner(), "reruns")
}
