package contract

import (
	"context"
	"errors"
)

const OntologyBudgetRetirementVersion int64 = 20261023020000

const OntologyUpgradePauseKey = "ontology-upgrade:20261023020000:pause"
const OntologyUpgradeResumeKey = "ontology-upgrade:20261023020000:resume"
const OntologyUpgradeAuditOperation = "ONTOLOGY_UPGRADE_COMMAND"
const OntologyBudgetRetirementAuditOperation = "ONTOLOGY_BUDGET_RETIREMENT"

var ErrMaintenanceUpgradeBlocked = errors.New("ontology upgrade blocked")

type MaintenanceUpgradeState struct {
	Required             bool
	MigrationApplied     bool
	Paused               bool
	OwnsPause            bool
	LiveBatches          int64
	LiveTeamLeases       int64
	UnreconciledAttempts int64
	RecoveredBatches     int64
	ClosedRuns           int64
}

func (s MaintenanceUpgradeState) Disposition() string {
	switch {
	case !s.Required:
		return "not_required"
	case s.UnreconciledAttempts > 0:
		return "blocked"
	case s.LiveBatches > 0 || s.LiveTeamLeases > 0:
		return "waiting"
	default:
		return "ready"
	}
}

type MaintenanceUpgradeRepository interface {
	PrepareMaintenanceUpgrade(context.Context, MaintenanceAudit) (MaintenanceUpgradeState, error)
	CompleteMaintenanceUpgrade(context.Context, MaintenanceAudit) (MaintenanceUpgradeState, error)
}
