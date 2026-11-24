package topics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	audit "github.com/markhuangai/dense-mem/internal/audit/contract"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	access "github.com/markhuangai/dense-mem/internal/service/access"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	contract "github.com/markhuangai/dense-mem/internal/topics/contract"
)

type MaintenanceConfigSource interface {
	TopicMaintenanceRuntimeConfig(context.Context) (domain.TopicMaintenanceConfig, error)
}

type MaintenanceAuditPreparer interface {
	Prepare(context.Context, access.AuditLogEntry) (audit.Entry, error)
}

type MaintenanceDependencies struct {
	Repository      contract.MaintenanceRepository
	Config          MaintenanceConfigSource
	Organizer       func(string, int64, assessment.AttemptAccounting) *Service
	Assign          func(context.Context, contract.MaintenanceClaim, domain.TopicMaintenanceConfig, assessment.AttemptAccounting) (contract.OrganizationResult, error)
	Communities     contract.CommunityRepository
	Placement       CommunityAssessor
	DefaultModel    string
	ProviderTimeout time.Duration
	DecisionTimeout time.Duration
	Audit           MaintenanceAuditPreparer
	Now             func() time.Time
	Logger          *slog.Logger
	Diagnostics     MaintenanceDiagnosticCapture
}

type MaintenanceDiagnosticCapture struct {
	Observe       assessment.Observer
	NewRecorder   func() modelprovider.SnapshotRecorder
	RecordFailure func(context.Context, contract.MaintenanceClaim, contract.OrganizationResult, []modelprovider.ProviderExchange) error
}

type MaintenanceService struct{ deps MaintenanceDependencies }

func NewMaintenanceService(deps MaintenanceDependencies) *MaintenanceService {
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	if deps.ProviderTimeout <= 0 {
		deps.ProviderTimeout = time.Minute
	}
	return &MaintenanceService{deps: deps}
}

func (s *MaintenanceService) policy(ctx context.Context) (domain.TopicMaintenanceConfig, error) {
	if _, ok := requestctx.ActorFromContext(ctx); ok {
		return domain.TopicMaintenanceConfig{}, contract.ErrUnauthorized
	}
	if s == nil || s.deps.Repository == nil || s.deps.Config == nil {
		return domain.TopicMaintenanceConfig{}, &OrganizationError{Code: "configuration_invalid"}
	}
	policy, err := s.deps.Config.TopicMaintenanceRuntimeConfig(ctx)
	if err != nil {
		return policy, err
	}
	if policy.Model == "" {
		policy.Model = s.deps.DefaultModel
	}
	return policy, nil
}

func (s *MaintenanceService) Status(ctx context.Context) (contract.MaintenanceStatus, error) {
	policy, err := s.policy(ctx)
	if err != nil {
		return contract.MaintenanceStatus{}, err
	}
	status, err := s.deps.Repository.MaintenanceStatus(ctx, s.deps.Now())
	status.PendingPolicy = policy
	active := policy
	if status.Window != nil && status.ObservedAt.Before(status.Window.EndsAt) {
		active = status.Window.Policy
	}
	if s.validatePolicy(active) != nil {
		status.ConfigurationError = "configuration_invalid"
		status.CoverageComplete = false
	}
	return status, err
}

func (s *MaintenanceService) Runs(ctx context.Context, cursor string, limit int) (contract.MaintenanceRunPage, error) {
	if _, err := s.policy(ctx); err != nil {
		return contract.MaintenanceRunPage{}, err
	}
	return s.deps.Repository.ListMaintenanceRuns(ctx, cursor, limit)
}

func MaintenanceErrorCode(err error) string {
	switch {
	case errors.Is(err, contract.ErrInvalid):
		return "invalid_input"
	case errors.Is(err, contract.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, contract.ErrNotFound):
		return "not_found"
	case errors.Is(err, contract.ErrConflict):
		return "conflict"
	default:
		return contract.MaintenanceFailureCode(err)
	}
}

func (s *MaintenanceService) Command(ctx context.Context, input domain.TopicMaintenanceCommand, clientIP, correlationID string) (contract.MaintenanceRun, error) {
	policy, err := s.policy(ctx)
	if err != nil {
		return contract.MaintenanceRun{}, err
	}
	input, err = contract.PrepareMaintenanceCommand(input)
	if err != nil {
		return contract.MaintenanceRun{}, err
	}
	if s.deps.Audit == nil {
		return contract.MaintenanceRun{}, &OrganizationError{Code: "audit_unavailable"}
	}
	if input.Action == "run" || input.Action == "retry" {
		if _, err := s.ensureWindow(ctx, policy, s.deps.Now()); err != nil {
			return contract.MaintenanceRun{}, err
		}
	}
	result, err := s.deps.Repository.MaintenanceCommand(ctx, input, s.deps.Now(), func(result contract.MaintenanceRun) (audit.Entry, error) {
		return s.deps.Audit.Prepare(ctx, access.AuditLogEntry{Operation: "TOPIC_MAINTENANCE_COMMAND", EntityType: "topic_maintenance", EntityID: result.ID, ActorRole: "control", ClientIP: clientIP, CorrelationID: correlationID, Metadata: map[string]any{"action": input.Action, "operation_key": input.OperationKey, "window_id": result.WindowID, "max_batches": result.MaxBatches}})
	})
	if errors.Is(err, contract.ErrAuditUnavailable) {
		return result, &OrganizationError{Code: "audit_unavailable", Cause: err}
	}
	return result, err
}

func (s *MaintenanceService) validatePolicy(policy domain.TopicMaintenanceConfig) error {
	if s.deps.Organizer == nil || policy.Model == "" {
		return contract.ErrConfiguration
	}
	organizer := s.deps.Organizer(policy.Model, policy.ContextWindowTokens, nil)
	if organizer == nil {
		return contract.ErrConfiguration
	}
	return organizer.provider.ConfigurationError()
}

func (s *MaintenanceService) ensureWindow(ctx context.Context, policy domain.TopicMaintenanceConfig, now time.Time) (*contract.MaintenanceWindow, error) {
	current, err := s.deps.Repository.CurrentMaintenanceWindow(ctx, now)
	if err != nil {
		return nil, err
	}
	if current != nil {
		if err := s.validatePolicy(current.Policy); err != nil {
			return nil, err
		}
	} else if err := s.validatePolicy(policy); err != nil {
		return nil, err
	}
	return s.deps.Repository.EnsureMaintenanceWindow(ctx, policy, now)
}

type maintenanceWork struct {
	claim     contract.MaintenanceClaim
	policy    domain.TopicMaintenanceConfig
	community *contract.CommunityInput
}

func (s *MaintenanceService) RunTurn(ctx context.Context) (bool, error) {
	work, progress, err := s.prepareTurn(ctx)
	if err != nil || work == nil {
		return progress, err
	}
	return true, s.executeBatch(ctx, *work)
}

func (s *MaintenanceService) prepareTurn(ctx context.Context) (work *maintenanceWork, progress bool, runErr error) {
	started := time.Now()
	policy, err := s.policy(ctx)
	if err != nil {
		return nil, false, err
	}
	if !policy.Enabled {
		return nil, false, nil
	}
	if s.deps.Organizer == nil || policy.Model == "" {
		return nil, false, &OrganizationError{Code: "configuration_invalid"}
	}
	window, err := s.ensureWindow(ctx, policy, s.deps.Now())
	if err != nil {
		return nil, false, err
	}
	if window == nil {
		return nil, false, nil
	}
	lease := 3*s.deps.ProviderTimeout + 15*time.Second
	turn, claimErr := s.deps.Repository.ClaimMaintenanceTurn(ctx, window.ID, s.deps.Now(), lease)
	if turn == nil {
		return nil, false, claimErr
	}
	defer func() { s.logMaintenancePreparation(ctx, *turn, work, time.Since(started), runErr) }()
	defer func() {
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if claimErr == nil && runErr != nil {
			code := contract.MaintenanceFailureCode(runErr)
			var organizationErr *OrganizationError
			if code == "" && errors.As(runErr, &organizationErr) {
				code = organizationErr.Code
			}
			if code == "" {
				code = "maintenance_failure"
			}
			runErr = errors.Join(runErr, s.deps.Repository.RecordMaintenanceFailure(finishCtx, turn.RunID, code, s.deps.Now()))
		}
		if work == nil {
			runErr = errors.Join(runErr, s.deps.Repository.ReleaseMaintenanceTurn(finishCtx, *turn))
		}
	}()
	if claimErr != nil {
		return nil, false, claimErr
	}
	turnCtx, cancel := context.WithTimeout(ctx, lease-5*time.Second)
	defer cancel()
	discovery, err := s.deps.Repository.DiscoverMaintenance(turnCtx, *turn, contract.MaintenancePageSize)
	s.logMaintenanceDiscovery(ctx, *turn, discovery, err)
	if err != nil {
		return nil, true, err
	}
	claim, err := s.deps.Repository.ClaimMaintenanceBatch(turnCtx, *turn, window.ID, s.deps.Now())
	if err != nil {
		return nil, true, err
	}
	if claim == nil {
		if s.deps.Communities == nil || turn.RetryRunID != "" {
			return nil, true, nil
		}
		community, placementClaim, err := s.deps.Communities.ClaimCommunity(turnCtx, *turn, window.ID, s.deps.Now())
		if err != nil || community == nil {
			return nil, true, err
		}
		return &maintenanceWork{claim: *placementClaim, policy: window.Policy, community: community}, true, nil
	}
	return &maintenanceWork{claim: *claim, policy: window.Policy}, true, nil
}

func (s *MaintenanceService) executeBatch(ctx context.Context, work maintenanceWork) (runErr error) {
	if work.community != nil {
		return s.placeCommunity(ctx, work)
	}
	claim := work.claim
	turnCtx, cancel, accounting := s.maintenanceAdmission(ctx, claim, s.deps.Assign != nil)
	defer cancel()
	claimCtx := contract.WithMaintenanceClaim(turnCtx, claim)
	var result contract.OrganizationResult
	var recorder modelprovider.SnapshotRecorder
	if s.deps.Diagnostics.NewRecorder != nil {
		recorder = s.deps.Diagnostics.NewRecorder()
		claimCtx = modelprovider.WithExchangeRecorder(claimCtx, recorder)
	}
	claimCtx = observability.WithExchangeScope(claimCtx, observability.ExchangeScope{TeamID: claim.TeamID, RunID: claim.RunID, BatchID: claim.ID})
	defer func() {
		if runErr != nil && errors.Is(context.Cause(turnCtx), contract.ErrLeaseLost) {
			runErr = errors.Join(contract.ErrLeaseLost, runErr)
		}
		failure := contract.MaintenanceFailureCode(runErr)
		if failure == "" {
			failure = result.FailureCode
		}
		if runErr != nil && failure == "" {
			failure = "organization_failed"
		}
		finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer finishCancel()
		runErr = errors.Join(runErr, s.deps.Repository.CompleteMaintenanceBatch(finishCtx, claim, result, failure, s.deps.Now()))
		if runErr != nil {
			if failure == "" {
				failure = "maintenance_failure"
			}
			runErr = errors.Join(runErr, s.deps.Repository.RecordMaintenanceFailure(finishCtx, claim.RunID, failure, s.deps.Now()))
		}
		if (runErr != nil || result.FailureCode != "") && s.deps.Diagnostics.RecordFailure != nil {
			var exchanges []modelprovider.ProviderExchange
			if recorder != nil {
				exchanges = recorder.Snapshot()
			}
			diagnosticCtx, diagnosticCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer diagnosticCancel()
			captureStarted := time.Now()
			if result.FailureCode == "" {
				result.FailureCode = failure
			}
			if err := s.deps.Diagnostics.RecordFailure(diagnosticCtx, claim, result, exchanges); err != nil && s.deps.Logger != nil {
				reason := "capture_failed"
				if errors.Is(diagnosticCtx.Err(), context.DeadlineExceeded) {
					reason = "deadline_exceeded"
				} else if diagnosticCtx.Err() != nil {
					reason = "canceled"
				}
				s.deps.Logger.LogAttrs(diagnosticCtx, slog.LevelWarn, "topic_failure_diagnostics_unavailable", slog.String("team_id", claim.TeamID), slog.String("run_id", claim.RunID), slog.String("batch_id", claim.ID), slog.String("assessment_id", result.AssessmentID), slog.String("reason", reason), slog.Int64("duration_ms", time.Since(captureStarted).Milliseconds()))
			}
		}
	}()
	organizer := s.deps.Organizer(work.policy.Model, work.policy.ContextWindowTokens, accounting)
	organizer.policy = &work.policy
	assessmentCtx := observability.WithAIOperation(claimCtx, observability.AIOperationLegacyOrganization, len(claim.Sources))
	if s.deps.Assign != nil {
		equivalenceCtx := observability.WithAIOperation(claimCtx, observability.AIOperationEvidenceEquivalence, len(claim.Sources))
		result, runErr = organizer.AssessMaintenanceEquivalence(equivalenceCtx, claim)
		if runErr == nil {
			assessmentCtx = observability.WithAIOperation(claimCtx, observability.AIOperationTopicAssignment, len(claim.Sources))
			result, runErr = s.deps.Assign(assessmentCtx, claim, work.policy, accounting)
		}
	} else {
		result, runErr = organizer.Organize(assessmentCtx, claim.TeamID, contract.OrganizationInput{OperationKey: "maintenance:" + claim.ID, Sources: claim.Sources})
	}
	s.logFreshnessRejection(ctx, claim, result)
	if s.deps.Logger != nil {
		for _, attempt := range result.Attempts {
			fields := []slog.Attr{slog.String("team_id", claim.TeamID), slog.String("batch_id", claim.ID), slog.String("run_id", claim.RunID),
				slog.String("assessment_id", result.AssessmentID), slog.Int("attempt", attempt.Number),
				slog.String("failure_code", attempt.FailureCode), slog.Bool("reported_usage_available", attempt.ReportedUsageAvailable)}
			if attempt.Diagnostics.Valid() {
				fields = append(fields, slog.Any("diagnostics", attempt.Diagnostics.LogFields()))
			} else {
				fields = append(fields, slog.Bool("diagnostics_invalid", true))
			}
			if attempt.Validation != nil {
				if attempt.Validation.Valid() && attempt.FailureCode == "response_invalid" {
					validation := map[string]any{"stage": attempt.Validation.Stage, "code": attempt.Validation.Code, "field_family": attempt.Validation.FieldFamily}
					if attempt.Validation.ExpectedCount != nil {
						validation["expected_count"] = *attempt.Validation.ExpectedCount
						validation["actual_count"] = *attempt.Validation.ActualCount
					}
					fields = append(fields, slog.Any("validation", validation))
				} else {
					fields = append(fields, slog.Bool("validation_invalid", true))
				}
			}
			s.deps.Logger.LogAttrs(ctx, slog.LevelInfo, "topic maintenance assessment attempt", fields...)
		}
	}
	return runErr
}

type maintenanceAttemptKey struct {
	id     string
	number int
}

type maintenanceAttemptAccounting struct {
	repository    contract.MaintenanceRepository
	claim         contract.MaintenanceClaim
	now           func() time.Time
	attemptLease  time.Duration
	decisionLease time.Duration
	extend        func(time.Time)
	mu            sync.Mutex
	admitted      map[maintenanceAttemptKey]bool
}

func (s *MaintenanceService) maintenanceAdmission(ctx context.Context, claim contract.MaintenanceClaim, renewable bool) (context.Context, context.CancelFunc, *maintenanceAttemptAccounting) {
	accounting := &maintenanceAttemptAccounting{repository: s.deps.Repository, claim: claim, now: s.deps.Now, admitted: map[maintenanceAttemptKey]bool{}}
	deadline := claim.LeaseUntil.Add(-5 * time.Second)
	if !renewable {
		turnCtx, cancel := context.WithDeadline(ctx, deadline)
		return turnCtx, cancel, accounting
	}
	turnCtx, cancel := context.WithCancelCause(ctx)
	timer := time.AfterFunc(time.Until(deadline), func() { cancel(contract.ErrLeaseLost) })
	accounting.attemptLease = 3*s.deps.ProviderTimeout + 15*time.Second
	accounting.decisionLease = s.deps.DecisionTimeout + 15*time.Second
	accounting.extend = func(leaseUntil time.Time) {
		next := leaseUntil.Add(-5 * time.Second)
		// Once the local lease has expired, let cancellation finish the turn.
		if next.After(deadline) && timer.Stop() {
			deadline = next
			timer.Reset(time.Until(deadline))
		}
	}
	return turnCtx, func() {
		timer.Stop()
		cancel(nil)
	}, accounting
}

func (a *maintenanceAttemptAccounting) BeforeAttempt(ctx context.Context, id string, number, input, output int) (context.Context, error) {
	claim := a.claim
	if a.extend != nil {
		claim.AttemptLease = a.attemptLease
		if modelprovider.DecisionAccountingFromContext(ctx) != nil {
			claim.AttemptLease = max(claim.AttemptLease, a.decisionLease)
		}
	}
	return modelprovider.WithAdmissionCallback(ctx, func(dispatchCtx context.Context) error {
		now := a.now()
		if err := a.repository.AdmitMaintenanceAttempt(dispatchCtx, claim, id, number, input, output, now); err != nil {
			return err
		}
		a.mu.Lock()
		a.admitted[maintenanceAttemptKey{id, number}] = true
		if a.extend != nil {
			a.extend(now.Add(claim.AttemptLease))
		}
		a.mu.Unlock()
		return nil
	}), nil
}

func (a *maintenanceAttemptAccounting) AfterAttempt(ctx context.Context, id string, attempt contract.AssessmentAttempt, success bool) error {
	a.mu.Lock()
	admitted := a.admitted[maintenanceAttemptKey{id, attempt.Number}]
	a.mu.Unlock()
	if !admitted {
		if success {
			return fmt.Errorf("%w: transport omitted durable admission", contract.ErrAccounting)
		}
		return nil
	}
	return a.repository.ReconcileMaintenanceAttempt(ctx, a.claim, id, attempt)
}
