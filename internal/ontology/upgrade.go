package ontology

import (
	"context"
	"errors"
	"fmt"
	"time"

	audit "github.com/markhuangai/dense-mem/internal/audit/contract"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	access "github.com/markhuangai/dense-mem/internal/service/access"
)

const MaintenanceUpgradeWaitLimit = 5 * time.Minute

type MaintenanceUpgradeService struct {
	repository contract.MaintenanceUpgradeRepository
	audit      MaintenanceAuditPreparer
}

func NewMaintenanceUpgradeService(repository contract.MaintenanceUpgradeRepository, audit MaintenanceAuditPreparer) *MaintenanceUpgradeService {
	return &MaintenanceUpgradeService{repository: repository, audit: audit}
}

func (s *MaintenanceUpgradeService) validate(ctx context.Context) error {
	if _, ok := requestctx.ActorFromContext(ctx); ok {
		return contract.ErrUnauthorized
	}
	if s == nil || s.repository == nil {
		return contract.ErrConfiguration
	}
	if s.audit == nil {
		return contract.ErrAuditUnavailable
	}
	return nil
}

func (s *MaintenanceUpgradeService) prepareAudit(ctx context.Context) contract.MaintenanceAudit {
	return func(run contract.MaintenanceRun) (audit.Entry, error) {
		operation := contract.OntologyUpgradeAuditOperation
		metadata := map[string]any{"action": run.Kind, "operation_key": run.OperationKey, "migration_version": contract.OntologyBudgetRetirementVersion}
		if run.FailureCode == "window_closed" {
			operation = contract.OntologyBudgetRetirementAuditOperation
			metadata = map[string]any{"migration_version": contract.OntologyBudgetRetirementVersion, "outcome": run.FailureCode}
		}
		return s.audit.Prepare(ctx, access.AuditLogEntry{
			Operation: operation, EntityType: "ontology_maintenance", EntityID: run.ID,
			ActorRole: "system", Metadata: metadata,
		})
	}
}

func (s *MaintenanceUpgradeService) Prepare(ctx context.Context, observe func(string, contract.MaintenanceUpgradeState)) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, MaintenanceUpgradeWaitLimit)
	defer cancel()
	var previous contract.MaintenanceUpgradeState
	for {
		state, err := s.repository.PrepareMaintenanceUpgrade(ctx, s.prepareAudit(ctx))
		phase := state.Disposition()
		if err != nil {
			if observe != nil {
				observe("blocked", previous)
			}
			if ctx.Err() != nil {
				return errors.Join(contract.ErrMaintenanceUpgradeBlocked, err, ctx.Err())
			}
			return err
		}
		if observe != nil && state != previous {
			observe(phase, state)
		}
		previous = state
		switch phase {
		case "not_required", "ready":
			return nil
		case "blocked":
			return fmt.Errorf("%w: %d unreconciled attempts require terminal evidence", contract.ErrMaintenanceUpgradeBlocked, state.UnreconciledAttempts)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			if observe != nil {
				observe("blocked", state)
			}
			return errors.Join(contract.ErrMaintenanceUpgradeBlocked, ctx.Err())
		case <-timer.C:
		}
	}
}

func (s *MaintenanceUpgradeService) Complete(ctx context.Context) (contract.MaintenanceUpgradeState, error) {
	if err := s.validate(ctx); err != nil {
		return contract.MaintenanceUpgradeState{}, err
	}
	return s.repository.CompleteMaintenanceUpgrade(ctx, s.prepareAudit(ctx))
}
