package ontology

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	audit "github.com/markhuangai/dense-mem/internal/audit/contract"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	access "github.com/markhuangai/dense-mem/internal/service/access"
)

type MaintenanceConfigSource interface {
	OntologyMaintenanceRuntimeConfig(context.Context) (domain.OntologyMaintenanceConfig, error)
}

type MaintenanceAuditPreparer interface {
	Prepare(context.Context, access.AuditLogEntry) (audit.Entry, error)
}

type MaintenanceDependencies struct {
	Repository      contract.MaintenanceRepository
	Config          MaintenanceConfigSource
	Organizer       func(string, assessment.AttemptAccounting) *Service
	DefaultModel    string
	ProviderTimeout time.Duration
	Audit           MaintenanceAuditPreparer
	Now             func() time.Time
	Projections     ProjectionRunner
}

type ProjectionRunner interface {
	RunProjectionTurn(context.Context) (bool, error)
}

func (s *MaintenanceService) SetProjectionRunner(runner ProjectionRunner) {
	s.deps.Projections = runner
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

func (s *MaintenanceService) policy(ctx context.Context) (domain.OntologyMaintenanceConfig, error) {
	if _, ok := requestctx.ActorFromContext(ctx); ok {
		return domain.OntologyMaintenanceConfig{}, contract.ErrUnauthorized
	}
	if s == nil || s.deps.Repository == nil || s.deps.Config == nil {
		return domain.OntologyMaintenanceConfig{}, &OrganizationError{Code: "configuration_invalid"}
	}
	policy, err := s.deps.Config.OntologyMaintenanceRuntimeConfig(ctx)
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

func (s *MaintenanceService) Command(ctx context.Context, input domain.OntologyMaintenanceCommand, clientIP, correlationID string) (contract.MaintenanceRun, error) {
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
		if _, err := s.deps.Repository.EnsureMaintenanceWindow(ctx, policy, s.deps.Now()); err != nil {
			return contract.MaintenanceRun{}, err
		}
	}
	result, err := s.deps.Repository.MaintenanceCommand(ctx, input, s.deps.Now(), func(result contract.MaintenanceRun) (audit.Entry, error) {
		return s.deps.Audit.Prepare(ctx, access.AuditLogEntry{Operation: "ONTOLOGY_MAINTENANCE_COMMAND", EntityType: "ontology_maintenance", EntityID: result.ID, ActorRole: "control", ClientIP: clientIP, CorrelationID: correlationID, Metadata: map[string]any{"action": input.Action, "operation_key": input.OperationKey, "window_id": result.WindowID, "max_batches": result.MaxBatches}})
	})
	if errors.Is(err, contract.ErrAuditUnavailable) {
		return result, &OrganizationError{Code: "audit_unavailable", Cause: err}
	}
	return result, err
}

func (s *MaintenanceService) RunTurn(ctx context.Context) (progress bool, runErr error) {
	policy, err := s.policy(ctx)
	if err != nil {
		return false, err
	}
	if !policy.Enabled {
		return false, nil
	}
	if s.deps.Organizer == nil || policy.Model == "" {
		return false, &OrganizationError{Code: "configuration_invalid"}
	}
	window, err := s.deps.Repository.EnsureMaintenanceWindow(ctx, policy, s.deps.Now())
	if err != nil {
		return false, err
	}
	if window == nil {
		return false, nil
	}
	lease := 3*s.deps.ProviderTimeout + 15*time.Second
	turn, err := s.deps.Repository.ClaimMaintenanceTurn(ctx, window.ID, s.deps.Now(), lease)
	if err != nil || turn == nil {
		return false, err
	}
	defer func() {
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if runErr != nil {
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
		runErr = errors.Join(runErr, s.deps.Repository.ReleaseMaintenanceTurn(finishCtx, *turn))
	}()
	turnCtx, cancel := context.WithTimeout(ctx, lease-5*time.Second)
	defer cancel()
	if err = s.deps.Repository.DiscoverMaintenance(turnCtx, *turn, contract.MaintenancePageSize); err != nil {
		return true, err
	}
	claim, err := s.deps.Repository.ClaimMaintenanceBatch(turnCtx, *turn, window.ID, s.deps.Now())
	if err != nil || claim == nil {
		return true, err
	}
	claimCtx := contract.WithMaintenanceClaim(turnCtx, *claim)
	var result contract.OrganizationResult
	defer func() {
		failure := contract.MaintenanceFailureCode(runErr)
		if failure == "" {
			failure = result.FailureCode
		}
		if runErr != nil && failure == "" {
			failure = "organization_failed"
		}
		finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(turnCtx), 5*time.Second)
		defer finishCancel()
		runErr = errors.Join(runErr, s.deps.Repository.CompleteMaintenanceBatch(finishCtx, *claim, result, failure, s.deps.Now()))
	}()
	page, err := s.deps.Repository.ListRecords(claimCtx, turn.TeamID, contract.EntityClass, "", 1)
	if err != nil {
		return true, err
	}
	if len(page.Records) == 0 {
		_, err = s.deps.Repository.SeedDefinitions(claimCtx, turn.TeamID, contract.SeedInput{OperationKey: "maintenance-seed:" + turn.LeaseToken, ExpectedRevision: page.Revision, Limit: 20})
		if err != nil {
			return true, err
		}
	}
	accounting := &maintenanceAttemptAccounting{repository: s.deps.Repository, claim: *claim, now: s.deps.Now, admitted: map[int]bool{}}
	organizer := s.deps.Organizer(window.Policy.Model, accounting)
	assessmentCtx := observability.WithAIOperation(claimCtx, observability.AIOperationOntologyOrganization, len(claim.Sources))
	result, err = organizer.Organize(assessmentCtx, claim.TeamID, contract.OrganizationInput{OperationKey: "maintenance:" + claim.ID, Sources: claim.Sources})
	return true, err
}

type maintenanceAttemptAccounting struct {
	repository contract.MaintenanceRepository
	claim      contract.MaintenanceClaim
	now        func() time.Time
	mu         sync.Mutex
	admitted   map[int]bool
}

func (a *maintenanceAttemptAccounting) BeforeAttempt(ctx context.Context, id string, number, input, output int) (context.Context, error) {
	return modelprovider.WithAdmissionCallback(ctx, func(dispatchCtx context.Context) error {
		if err := a.repository.ReserveMaintenanceAttempt(dispatchCtx, a.claim, id, number, input, output, a.now()); err != nil {
			return err
		}
		a.mu.Lock()
		a.admitted[number] = true
		a.mu.Unlock()
		return nil
	}), nil
}

func (a *maintenanceAttemptAccounting) AfterAttempt(ctx context.Context, id string, attempt contract.AssessmentAttempt, success bool) error {
	a.mu.Lock()
	admitted := a.admitted[attempt.Number]
	a.mu.Unlock()
	if !admitted {
		if success {
			return fmt.Errorf("%w: transport omitted durable admission", contract.ErrAccounting)
		}
		return nil
	}
	return a.repository.ReconcileMaintenanceAttempt(ctx, a.claim, id, attempt)
}
