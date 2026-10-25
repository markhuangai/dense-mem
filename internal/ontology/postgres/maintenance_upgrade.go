package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func maintenanceUpgradeState(tx *gorm.DB) (state ontology.MaintenanceUpgradeState, err error) {
	var installed bool
	if err = tx.Raw(`SELECT to_regclass('public.ontology_maintenance_state') IS NOT NULL`).Row().Scan(&installed); err != nil || !installed {
		return state, err
	}
	state.Paused, err = maintenanceState(tx)
	if err != nil {
		return state, err
	}
	if err = tx.Raw(`SELECT COALESCE((SELECT is_applied FROM goose_db_version WHERE version_id=? ORDER BY id DESC LIMIT 1),false)`, ontology.OntologyBudgetRetirementVersion).Row().Scan(&state.MigrationApplied); err != nil {
		return state, err
	}
	paused, err := maintenanceUpgradeCommandExists(tx, "pause", ontology.OntologyUpgradePauseKey)
	if err != nil {
		return state, err
	}
	resumed, err := maintenanceUpgradeCommandExists(tx, "resume", ontology.OntologyUpgradeResumeKey)
	if err != nil {
		return state, err
	}
	if resumed && (!paused || !state.MigrationApplied) {
		return state, ontology.ErrConflict
	}
	state.OwnsPause = paused && !resumed
	state.Required = !state.MigrationApplied || state.OwnsPause
	if state.OwnsPause && !state.Paused {
		return state, ontology.ErrConflict
	}
	return state, nil
}

func maintenanceUpgradeCommandExists(tx *gorm.DB, action, key string) (bool, error) {
	var id, hash, kind, status string
	err := tx.Raw(`SELECT run_id::text,request_hash,kind,status FROM ontology_maintenance_runs WHERE operation_key=?`, key).Row().Scan(&id, &hash, &kind, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	expected, err := ontology.MaintenanceCommandHash(domain.OntologyMaintenanceCommand{OperationKey: key, Action: action})
	if err != nil {
		return false, err
	}
	if hash != expected || kind != action || status != "completed" {
		return false, ontology.ErrConflict
	}
	var audited bool
	err = tx.Raw(`SELECT EXISTS(SELECT 1 FROM audit_log WHERE operation=? AND entity_type='ontology_maintenance'
	 AND entity_id=? AND actor_role='system' AND metadata->>'operation_key'=? AND metadata->>'action'=? AND metadata->>'migration_version'=?)`,
		ontology.OntologyUpgradeAuditOperation, id, key, action, "20261023020000").Row().Scan(&audited)
	if err != nil {
		return false, err
	}
	if !audited {
		return false, ontology.ErrConflict
	}
	return true, nil
}

func (s *Store) PrepareMaintenanceUpgrade(ctx context.Context, prepareAudit ontology.MaintenanceAudit) (state ontology.MaintenanceUpgradeState, err error) {
	err = s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		state, err = maintenanceUpgradeState(tx)
		if err != nil || !state.Required || state.MigrationApplied {
			return err
		}
		if prepareAudit == nil {
			return ontology.ErrAuditUnavailable
		}
		enabled, err := maintenanceEnabled(tx)
		if err != nil {
			return err
		}
		var now time.Time
		if err = tx.Raw(`SELECT clock_timestamp()`).Row().Scan(&now); err != nil {
			return err
		}
		if enabled && !state.Paused {
			if _, err = maintenanceCommandTx(ctx, tx, domain.OntologyMaintenanceCommand{OperationKey: ontology.OntologyUpgradePauseKey, Action: "pause"}, now, prepareAudit); err != nil {
				return err
			}
			state.Paused, state.OwnsPause = true, true
		}
		if err = tx.Raw(`SELECT
		 (SELECT count(*) FROM ontology_maintenance_batches WHERE status='running' AND lease_until>?),
		 (SELECT count(*) FROM ontology_maintenance_teams WHERE lease_until>?),
		 (SELECT count(*) FROM ontology_maintenance_attempts WHERE reconciled_at IS NULL)`, now, now).Row().Scan(&state.LiveBatches, &state.LiveTeamLeases, &state.UnreconciledAttempts); err != nil {
			return err
		}
		if state.Disposition() != "ready" {
			return nil
		}
		state.RecoveredBatches, err = expireMaintenanceBatches(tx, now)
		if err != nil {
			return err
		}
		state.ClosedRuns, err = closeMaintenanceUpgradeRuns(ctx, tx, now, prepareAudit)
		return err
	})
	return state, err
}

func closeMaintenanceUpgradeRuns(ctx context.Context, tx *gorm.DB, now time.Time, prepareAudit ontology.MaintenanceAudit) (int64, error) {
	var runs []ontology.MaintenanceRun
	if err := tx.Raw(`SELECT run_id::text AS id,kind,operation_key FROM ontology_maintenance_runs
	 WHERE kind IN ('scheduled','run','retry') AND status IN ('pending','running') ORDER BY run_id`).Scan(&runs).Error; err != nil {
		return 0, err
	}
	for _, run := range runs {
		if err := recordMaintenanceFailureTx(tx, run.ID, "window_closed", now); err != nil {
			return 0, err
		}
		run.Status, run.FailureCode, run.UpdatedAt = "incomplete", "window_closed", now
		if err := appendMaintenanceAuditTx(ctx, tx, run, prepareAudit); err != nil {
			return 0, err
		}
	}
	return int64(len(runs)), nil
}

func (s *Store) CompleteMaintenanceUpgrade(ctx context.Context, prepareAudit ontology.MaintenanceAudit) (state ontology.MaintenanceUpgradeState, err error) {
	err = s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		state, err = maintenanceUpgradeState(tx)
		if err != nil || !state.Required {
			return err
		}
		if !state.MigrationApplied {
			return ontology.ErrMaintenanceUpgradeBlocked
		}
		if prepareAudit == nil {
			return ontology.ErrAuditUnavailable
		}
		var now time.Time
		if err = tx.Raw(`SELECT clock_timestamp()`).Row().Scan(&now); err != nil {
			return err
		}
		if _, err = maintenanceCommandTx(ctx, tx, domain.OntologyMaintenanceCommand{OperationKey: ontology.OntologyUpgradeResumeKey, Action: "resume"}, now, prepareAudit); err != nil {
			return err
		}
		state.Paused, state.OwnsPause = false, false
		return nil
	})
	return state, err
}

var _ ontology.MaintenanceUpgradeRepository = (*Store)(nil)
