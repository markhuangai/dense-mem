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
	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

const (
	manualDreamCycleLease     = 30 * time.Second
	scheduledDreamCycleLease  = 15 * time.Minute
	scheduledRecoveryAttempts = 3
)

type service struct {
	deps Dependencies
	now  func() time.Time
}

var _ Service = (*service)(nil)

func New(deps Dependencies) Service {
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if deps.Generator == nil {
		deps.Generator = unavailableGenerator{}
	}
	if deps.ScheduledStore == nil {
		if store, ok := deps.Store.(dreamcontract.ScheduledDreamRepository); ok {
			deps.ScheduledStore = store
		}
	}
	return &service{deps: deps, now: now}
}

func (s *service) cycleLease(scheduled bool) time.Duration {
	lease := manualDreamCycleLease
	if scheduled {
		lease = scheduledDreamCycleLease
	}
	if s.deps.ProviderCycleLease > lease {
		return s.deps.ProviderCycleLease
	}
	return lease
}

func (s *service) evidenceCycleLease() time.Duration {
	lease := scheduledDreamCycleLease
	if s.deps.ProviderCycleLease > 0 {
		lease = s.deps.ProviderCycleLease * evidenceDiscoveryTargetLimit * evidenceDiscoveryPassLimit * evidenceDiscoveryRegenerationLimit
		if lease < scheduledDreamCycleLease {
			lease = scheduledDreamCycleLease
		}
	}
	return lease
}

func (s *service) RunCycle(ctx context.Context, _ string, req RunCycleRequest) (*RunCycleResult, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("dreaming cycle: dream repository is required")
	}
	return s.runCycle(ctx, req)
}

func (s *service) RunScheduledCycle(ctx context.Context, teamID string, windowAt time.Time) (*RunCycleResult, error) {
	if s.deps.ScheduledStore == nil {
		return nil, fmt.Errorf("scheduled dreaming cycle: scheduled dream repository is required")
	}
	return s.runScheduledCycle(ctx, teamID, windowAt)
}

// RunScheduledEvidenceCycle runs one current UTC-hour evidence-discovery
// window. It is exposed on the concrete service for the scheduler so the
// daily Service contract remains backward compatible.
func (s *service) RunScheduledEvidenceCycle(ctx context.Context, teamID string, windowAt time.Time) (*RunCycleResult, error) {
	if s.deps.ScheduledStore == nil {
		return nil, fmt.Errorf("scheduled evidence dreaming cycle: scheduled dream repository is required")
	}
	started := time.Now()
	result, err := s.runScheduledEvidenceCycle(ctx, teamID, windowAt)
	s.recordDreamCycleMetrics(ctx, string(domain.DreamLaneEvidenceDiscovery), started, result, err)
	return result, err
}

func (s *service) RecoverScheduledCycle(ctx context.Context, teamID string) (*RunCycleResult, error) {
	if s.deps.ScheduledStore == nil {
		return nil, fmt.Errorf("recover scheduled dreaming cycle: scheduled dream repository is required")
	}
	return s.recoverScheduledCycle(ctx, teamID)
}

func (s *service) RecordMissedScheduledCycle(ctx context.Context, teamID, runDate string) (*RunCycleResult, error) {
	if s.deps.ScheduledStore == nil {
		return nil, fmt.Errorf("record missed scheduled dreaming cycle: scheduled dream repository is required")
	}
	return s.recordMissedScheduledCycle(ctx, teamID, runDate)
}

func (s *service) List(ctx context.Context, _ string, opts ListOptions) ([]*domain.Dream, string, error) {
	if s.deps.Store == nil {
		return nil, "", fmt.Errorf("dream list: dream repository is required")
	}
	return s.listDreams(ctx, opts)
}

func (s *service) Get(ctx context.Context, _ string, dreamID string) (*domain.Dream, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("dream get: dream repository is required")
	}
	return s.getDream(ctx, dreamID)
}

func (s *service) ListRuns(ctx context.Context, _ string, limit int) ([]*RunCycleResult, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("dream cycle runs: dream repository is required")
	}
	return s.listRuns(ctx, limit)
}

func (s *service) Recall(ctx context.Context, _ string, query string, limit int) ([]*domain.Dream, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("dream recall: dream repository is required")
	}
	return s.recallDreams(ctx, query, limit)
}

func (s *service) ResolveFeedback(ctx context.Context, _ string, req ResolveFeedbackRequest) (*ResolveFeedbackResult, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("resolve dream feedback: dream repository is required")
	}
	started := time.Now()
	result, err := s.resolveFeedback(ctx, req)
	operation, classification := "dream_feedback", "feedback"
	decision := strings.ToLower(strings.TrimSpace(req.Decision))
	if decision == "confirm_true" || decision == "confirm_false" || decision == "promote_candidate" {
		operation, classification = "dream_confirmation", "confirmation"
	}
	outcome := "completed"
	if err != nil {
		outcome = "failed"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			outcome = "cancelled"
		}
	} else if operation == "dream_confirmation" && result != nil && result.Memory != nil && result.Memory.Terminal != nil &&
		result.Memory.Terminal.ProcessingState == string(rememberapp.TerminalProcessingFailed) {
		outcome = "failed"
	}
	observability.RecordLogicalOperation(s.deps.Metrics, operation, classification, outcome, time.Since(started))
	return result, err
}

func (s *service) Status(ctx context.Context, _ string) (*StatusResult, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("dream status: dream repository is required")
	}
	return s.status(ctx)
}

func (s *service) recordDreamFeedback(ctx context.Context, decision string, dream *domain.Dream, outcome string) {
	fromStatus := ""
	if dream != nil {
		fromStatus = string(dream.Status)
	}
	observability.RecordDreamFeedback(ctx, s.deps.Metrics, observability.DreamFeedback{
		Decision:   decision,
		Outcome:    outcome,
		FromStatus: fromStatus,
	})
}

func (s *service) recordDreamCycleMetrics(ctx context.Context, lane string, started time.Time, result *RunCycleResult, err error) {
	status := "failed"
	if result != nil {
		status = result.Status
	}
	if dreamExecutionCancelled(ctx, err, status) {
		status = "cancelled"
	}
	switch status {
	case "running", "completed", "failed", "skipped", "cancelled", "missed":
	case "error":
		status = "failed"
	default:
		status = "failed"
	}
	observability.RecordDreamCycle(s.deps.Metrics, lane, status, time.Since(started))
}

func (s *service) recordDreamRecovery(operation, outcome string) {
	observability.RecordLogicalRecovery(s.deps.Metrics, operation, outcome)
}

func dreamRecoveryOutcome(ctx context.Context, result *RunCycleResult, err error) string {
	status := ""
	if result != nil {
		status = result.Status
	}
	if dreamExecutionCancelled(ctx, err, status) {
		return "cancelled"
	}
	if err != nil || result == nil {
		return "failed"
	}
	switch status {
	case "cancelled":
		return "cancelled"
	case "failed", "error":
		return "failed"
	default:
		return "succeeded"
	}
}

func dreamExecutionCancelled(ctx context.Context, err error, status string) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if ctx == nil || ctx.Err() == nil {
		return false
	}
	switch status {
	case "completed", "skipped", "missed":
		return false
	default:
		return true
	}
}

func localRunDate(now time.Time, cfg EffectiveConfig) string {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return now.In(loc).Format("2006-01-02")
}

func parseTeamID(teamID string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(teamID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("dreaming config: invalid team id: %w", err)
	}
	return parsed, nil
}
