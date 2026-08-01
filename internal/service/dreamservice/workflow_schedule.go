package dreamservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func (s *service) runCycle(ctx context.Context, req RunCycleRequest) (*RunCycleResult, error) {
	teamID, actorProfileID, err := dreamActor(ctx)
	if err != nil {
		return nil, err
	}
	cfg, err := s.effectiveConfigForTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	return s.runTeamCycle(ctx, teamID, actorProfileID, cfg, req, false, time.Time{})
}

func (s *service) runScheduledCycle(ctx context.Context, teamID string, windowAt time.Time) (*RunCycleResult, error) {
	teamID, err := normalizeDreamTeamID(teamID)
	if err != nil {
		return nil, err
	}
	cfg, err := s.effectiveConfigForTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	windowAt = windowAt.UTC()
	runDate := localRunDate(windowAt, cfg)
	if !cfg.Enabled {
		return &RunCycleResult{TeamID: teamID, RunDate: runDate, Status: "skipped"}, nil
	}
	if !isDueAt(windowAt, cfg) {
		return &RunCycleResult{TeamID: teamID, RunDate: runDate, Status: "skipped"}, nil
	}
	return s.runTeamCycle(ctx, teamID, "", cfg, RunCycleRequest{}, true, windowAt)
}

func (s *service) recoverScheduledCycle(ctx context.Context, teamID string) (*RunCycleResult, error) {
	teamID, err := normalizeDreamTeamID(teamID)
	if err != nil {
		return nil, err
	}
	cfg, err := s.effectiveConfigForTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	started := s.now().UTC()
	claimed, err := s.deps.ScheduledStore.ClaimRecoverableScheduledDreamCycle(ctx, repository.DreamCycleRecoveryClaimInput{
		TeamID:      teamID,
		LeaseToken:  uuid.NewString(),
		LeaseUntil:  started.Add(scheduledDreamCycleLease),
		MaxAttempts: scheduledRecoveryAttempts,
	})
	if err != nil {
		return nil, translateDreamRepositoryError(err)
	}
	if claimed == nil {
		return nil, nil
	}
	result := cycleRunResult(claimed)
	if result == nil {
		return nil, errors.New("recover scheduled dreaming cycle: missing claimed run")
	}
	result.StartedAt = started
	result.Status = "running"
	if !cfg.Enabled {
		result.CompletedAt = s.now().UTC()
		result.Status = "cancelled"
		result.OutcomeSummary = map[string]int{"disabled_before_recovery": 1}
		if err := s.completeTeamCycle(ctx, true, repository.DreamCycleCompleteInput{
			TeamID:         teamID,
			RunID:          claimed.RunID,
			LeaseToken:     claimed.LeaseToken,
			Status:         "cancelled",
			OutcomeSummary: result.OutcomeSummary,
		}); err != nil {
			return result, err
		}
		return result, nil
	}
	return s.runClaimedTeamCycle(ctx, teamID, "", cfg, RunCycleRequest{}, true, result, claimed)
}

func (s *service) recordMissedScheduledCycle(ctx context.Context, teamID, runDate string) (*RunCycleResult, error) {
	teamID, err := normalizeDreamTeamID(teamID)
	if err != nil {
		return nil, err
	}
	runDate, err = normalizeScheduledRunDate(runDate)
	if err != nil {
		return nil, err
	}
	cfg, err := s.effectiveConfigForTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return &RunCycleResult{TeamID: teamID, RunDate: runDate, Status: "skipped"}, nil
	}
	scheduledFor, _ := scheduledWindowAtTime(s.now(), cfg)
	run, err := s.deps.ScheduledStore.RecordMissedScheduledDreamCycle(ctx, repository.DreamCycleClaimInput{
		TeamID:       teamID,
		RunDate:      runDate,
		WindowKey:    runDate,
		ScheduledFor: &scheduledFor,
	})
	if err != nil {
		return nil, translateDreamRepositoryError(err)
	}
	return cycleRunResult(run), nil
}

func normalizeScheduledRunDate(runDate string) (string, error) {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(runDate))
	if err != nil {
		return "", fmt.Errorf("dreaming cycle: invalid scheduled run date: %w", err)
	}
	return parsed.Format("2006-01-02"), nil
}
