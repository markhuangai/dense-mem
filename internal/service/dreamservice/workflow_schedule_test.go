package dreamservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
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

func TestRecoverScheduledCycleFencesExpiredLeaseThroughCompletion(t *testing.T) {
	teamID := uuid.New()
	now := time.Date(2026, 7, 17, 3, 16, 0, 0, time.UTC)
	store := &dreamRepositoryStub{}
	scheduledStore := &recoveryScheduledStoreStub{
		dreamRepositoryStub: store,
		recovered: &repository.DreamCycleRun{
			TeamID:       teamID.String(),
			RunID:        uuid.NewString(),
			RunDate:      "2026-07-17",
			WindowKey:    "2026-07-17",
			LeaseToken:   uuid.NewString(),
			AttemptCount: 1,
		},
	}
	svc := New(Dependencies{
		Store:          store,
		ScheduledStore: scheduledStore,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{
			Enabled:        true,
			StartTimeLocal: "03:00",
			Timezone:       "UTC",
			MaxOutputs:     5,
		}},
		Now: func() time.Time { return now },
	})

	recoverer, ok := svc.(scheduledRecoveryService)
	require.True(t, ok)
	result, err := recoverer.RecoverScheduledCycle(context.Background(), teamID.String())
	require.NoError(t, err)
	require.NotNil(t, result)
	assertRecoveryClaim(t, teamID, now, scheduledStore.recoveryInput)
	require.NotEmpty(t, result.RunID)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, 2, result.AttemptCount)
	require.Equal(t, scheduledStore.recoveryInput.LeaseToken, store.completeInput.LeaseToken)
	require.Equal(t, "completed", store.completeInput.Status)
}

func TestRecoverScheduledCycleCancelsDisabledRunAndSurfacesClaimFailure(t *testing.T) {
	teamID := uuid.New()
	now := time.Date(2026, 7, 17, 3, 16, 0, 0, time.UTC)

	t.Run("cancels a recovered run when dreaming is disabled", func(t *testing.T) {
		store := &dreamRepositoryStub{}
		scheduledStore := &recoveryScheduledStoreStub{
			dreamRepositoryStub: store,
			recovered: &repository.DreamCycleRun{
				TeamID:       teamID.String(),
				RunID:        uuid.NewString(),
				RunDate:      "2026-07-17",
				LeaseToken:   uuid.NewString(),
				AttemptCount: 1,
			},
		}
		svc := New(Dependencies{
			Store:          store,
			ScheduledStore: scheduledStore,
			AppConfig:      cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: false}},
			Now:            func() time.Time { return now },
		})

		recoverer, ok := svc.(scheduledRecoveryService)
		require.True(t, ok)
		result, err := recoverer.RecoverScheduledCycle(context.Background(), teamID.String())
		require.NoError(t, err)
		require.Equal(t, "cancelled", result.Status)
		require.Equal(t, map[string]int{"disabled_before_recovery": 1}, result.OutcomeSummary)
		require.Equal(t, "cancelled", store.completeInput.Status)
		require.Equal(t, scheduledStore.recoveryInput.LeaseToken, store.completeInput.LeaseToken)
	})

	t.Run("returns the recovery claim failure", func(t *testing.T) {
		store := &dreamRepositoryStub{}
		scheduledStore := &recoveryScheduledStoreStub{
			dreamRepositoryStub: store,
			recoveryErr:         errors.New("recovery claim failed"),
		}
		svc := New(Dependencies{
			Store:          store,
			ScheduledStore: scheduledStore,
			AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{
				Enabled:        true,
				StartTimeLocal: "03:00",
				Timezone:       "UTC",
				MaxOutputs:     5,
			}},
		})

		recoverer, ok := svc.(scheduledRecoveryService)
		require.True(t, ok)
		result, err := recoverer.RecoverScheduledCycle(context.Background(), teamID.String())
		require.Nil(t, result)
		require.ErrorContains(t, err, "recovery claim failed")
	})
}

func assertRecoveryClaim(
	t *testing.T,
	teamID uuid.UUID,
	now time.Time,
	input repository.DreamCycleRecoveryClaimInput,
) {
	t.Helper()
	require.Equal(t, teamID.String(), input.TeamID)
	require.NotEmpty(t, input.LeaseToken)
	require.Equal(t, scheduledRecoveryAttempts, input.MaxAttempts)
	require.Equal(t, now.Add(scheduledDreamCycleLease), input.LeaseUntil)
}

type recoveryScheduledStoreStub struct {
	*dreamRepositoryStub
	recovered     *repository.DreamCycleRun
	recoveryInput repository.DreamCycleRecoveryClaimInput
	recoveryErr   error
}

func (s *recoveryScheduledStoreStub) ClaimRecoverableScheduledDreamCycle(
	_ context.Context,
	input repository.DreamCycleRecoveryClaimInput,
) (*repository.DreamCycleRun, error) {
	s.recoveryInput = input
	if s.recoveryErr != nil {
		return nil, s.recoveryErr
	}
	if s.recovered == nil {
		return nil, nil
	}
	run := *s.recovered
	run.LeaseToken = input.LeaseToken
	run.AttemptCount++
	return &run, nil
}
