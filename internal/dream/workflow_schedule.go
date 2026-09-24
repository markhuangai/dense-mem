package dream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
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
	if !isDueAt(windowAt, cfg) {
		return &RunCycleResult{TeamID: teamID, RunDate: runDate, Status: "skipped"}, nil
	}
	if !cfg.Enabled {
		return s.runTeamCycle(ctx, teamID, "", cfg, RunCycleRequest{}, true, windowAt)
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
	metricStarted := time.Now()
	claimed, err := s.deps.ScheduledStore.ClaimRecoverableScheduledDreamCycle(ctx, dreamcontract.DreamCycleRecoveryClaimInput{
		TeamID:      teamID,
		LeaseToken:  uuid.NewString(),
		LeaseUntil:  started.Add(s.cycleLease(true)),
		MaxAttempts: scheduledRecoveryAttempts,
	})
	if err != nil {
		s.recordDreamRecovery("dream_graph", "attempted")
		s.recordDreamRecovery("dream_graph", dreamRecoveryOutcome(ctx, nil, err))
		return nil, translateDreamRepositoryError(err)
	}
	if claimed == nil {
		return nil, nil
	}
	s.recordDreamRecovery("dream_graph", "attempted")
	recordRecovery := func(result *RunCycleResult, err error) {
		s.recordDreamCycleMetrics(ctx, string(domain.DreamLaneGraph), metricStarted, result, err)
		s.recordDreamRecovery("dream_graph", dreamRecoveryOutcome(ctx, result, err))
	}
	result := cycleRunResult(claimed)
	if result == nil {
		err := errors.New("recover scheduled dreaming cycle: missing claimed run")
		recordRecovery(nil, err)
		return nil, err
	}
	result.StartedAt = started
	result.Status = "running"
	if !cfg.Enabled {
		result.CompletedAt = s.now().UTC()
		result.Status = "cancelled"
		result.OutcomeSummary = map[string]int{"disabled_before_recovery": 1}
		appendRunDiagnosticPhase(result, "target", "cancelled", "dreaming_disabled", map[string]any{"enabled": false})
		if err := s.completeTeamCycle(ctx, true, dreamcontract.DreamCycleCompleteInput{
			TeamID:         teamID,
			RunID:          claimed.RunID,
			LeaseToken:     claimed.LeaseToken,
			Status:         "cancelled",
			OutcomeSummary: result.OutcomeSummary,
		}); err != nil {
			result.Status = "error"
			result.Error = err.Error()
			s.recordRunDiagnosticAfterCompletion(ctx, result, err)
			recordRecovery(result, err)
			return result, err
		}
		s.recordRunDiagnostic(ctx, result)
		recordRecovery(result, nil)
		return result, nil
	}
	result, err = s.runClaimedTeamCycle(ctx, teamID, "", cfg, RunCycleRequest{}, true, result, claimed)
	recordRecovery(result, err)
	return result, err
}

func (s *service) recordMissedScheduledCycle(ctx context.Context, teamID, runDate string) (*RunCycleResult, error) {
	metricStarted := time.Now()
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
	scheduledFor, hasScheduledFor := scheduledWindowAtTime(s.now(), cfg)
	run, err := s.deps.ScheduledStore.RecordMissedScheduledDreamCycle(ctx, dreamcontract.DreamCycleClaimInput{
		TeamID:       teamID,
		RunDate:      runDate,
		WindowKey:    runDate,
		ScheduledFor: optionalScheduledFor(scheduledFor, hasScheduledFor),
	})
	if err != nil {
		return nil, translateDreamRepositoryError(err)
	}
	result := cycleRunResult(run)
	if result != nil && run != nil && run.Claimed {
		appendRunDiagnosticPhase(result, "target", "missed", "scheduled_window_missed", map[string]any{"run_date": runDate})
		s.recordRunDiagnostic(ctx, result)
		s.recordDreamCycleMetrics(ctx, string(domain.DreamLaneGraph), metricStarted, result, nil)
	}
	return result, nil
}

func optionalScheduledFor(scheduledFor time.Time, ok bool) *time.Time {
	if !ok {
		return nil
	}
	return &scheduledFor
}

func normalizeScheduledRunDate(runDate string) (string, error) {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(runDate))
	if err != nil {
		return "", fmt.Errorf("dreaming cycle: invalid scheduled run date: %w", err)
	}
	return parsed.Format("2006-01-02"), nil
}
