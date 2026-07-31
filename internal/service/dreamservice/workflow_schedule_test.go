package dreamservice

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestScheduledCycleKeepsSchedulerWindowAfterMinuteRollsOver(t *testing.T) {
	teamID := uuid.New()
	windowAt := time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC)
	repo := &dreamRepositoryStub{}
	svc := New(Dependencies{
		Store:          repo,
		ScheduledStore: repo,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{
			Enabled:        true,
			StartTimeLocal: "03:00",
			Timezone:       "UTC",
			MaxOutputs:     5,
		}},
		Now: func() time.Time { return windowAt.Add(time.Minute) },
	})

	result, err := svc.RunScheduledCycle(context.Background(), teamID.String(), windowAt)

	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, "2026-07-17", result.RunDate)
	require.Equal(t, "2026-07-17", repo.claimInput.RunDate)
	require.Equal(t, "2026-07-17", repo.claimInput.WindowKey)
	require.Empty(t, repo.claimInput.InitiatedByProfileID)
}

func TestRecordMissedScheduledCycleUsesDSTGapRunDate(t *testing.T) {
	teamID := uuid.New()
	repo := &dreamRepositoryStub{}
	svc := New(Dependencies{
		Store:          repo,
		ScheduledStore: repo,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{
			Enabled:        true,
			StartTimeLocal: "02:30",
			Timezone:       "America/New_York",
			MaxOutputs:     5,
		}},
	})

	result, err := svc.RecordMissedScheduledCycle(context.Background(), teamID.String(), "2026-03-08")

	require.NoError(t, err)
	require.Equal(t, "missed", result.Status)
	require.Equal(t, "2026-03-08", result.RunDate)
	require.Equal(t, "2026-03-08", repo.missedInput.RunDate)
	require.Equal(t, "2026-03-08", repo.missedInput.WindowKey)
}

func TestScheduledCycleSkipsDisabledAndNonDueWindows(t *testing.T) {
	teamID := uuid.New()
	windowAt := time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC)

	disabledRepo := &dreamRepositoryStub{}
	disabled := New(Dependencies{
		Store:          disabledRepo,
		ScheduledStore: disabledRepo,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{
			Enabled:        false,
			StartTimeLocal: "03:00",
			Timezone:       "UTC",
			MaxOutputs:     5,
		}},
	})
	result, err := disabled.RunScheduledCycle(context.Background(), teamID.String(), windowAt)
	require.NoError(t, err)
	require.Equal(t, "skipped", result.Status)
	require.Empty(t, disabledRepo.claimInput.TeamID)

	nonDueRepo := &dreamRepositoryStub{}
	nonDue := New(Dependencies{
		Store:          nonDueRepo,
		ScheduledStore: nonDueRepo,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{
			Enabled:        true,
			StartTimeLocal: "03:00",
			Timezone:       "UTC",
			MaxOutputs:     5,
		}},
	})
	result, err = nonDue.RunScheduledCycle(context.Background(), teamID.String(), windowAt.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, "skipped", result.Status)
	require.Empty(t, nonDueRepo.claimInput.TeamID)
}

func TestRecordMissedScheduledCycleRejectsInvalidDateAndSkipsDisabledSchedule(t *testing.T) {
	teamID := uuid.New()
	repo := &dreamRepositoryStub{}
	svc := New(Dependencies{
		Store:          repo,
		ScheduledStore: repo,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{
			Enabled: false,
		}},
	})

	_, err := svc.RecordMissedScheduledCycle(context.Background(), teamID.String(), "not-a-date")
	require.ErrorContains(t, err, "invalid scheduled run date")

	result, err := svc.RecordMissedScheduledCycle(context.Background(), teamID.String(), "2026-07-17")
	require.NoError(t, err)
	require.Equal(t, "skipped", result.Status)
	require.Empty(t, repo.missedInput.TeamID)
}
