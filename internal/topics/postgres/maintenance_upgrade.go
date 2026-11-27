package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

// Pre-retirement databases still have the original relation names. These invoker views
// are scoped to this migration transaction and are removed before returning the connection.
func (s *Store) withMaintenanceUpgradeSchema(ctx context.Context, fn func(*gorm.DB) error) error {
	return s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		var legacy bool
		if err := tx.Raw(`SELECT to_regclass('public.ontology_maintenance_state') IS NOT NULL`).Row().Scan(&legacy); err != nil {
			return err
		}
		if !legacy {
			return fn(tx)
		}
		relations := []string{"state", "runs", "sources", "batches", "teams", "attempts"}
		for _, name := range relations {
			if err := tx.Exec("CREATE TEMP VIEW topic_maintenance_" + name + " WITH (security_invoker=true) AS SELECT * FROM public.ontology_maintenance_" + name).Error; err != nil {
				return err
			}
		}
		if err := fn(tx); err != nil {
			return err
		}
		for _, name := range relations {
			if err := tx.Exec("DROP VIEW pg_temp.topic_maintenance_" + name).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func maintenanceUpgradeState(tx *gorm.DB) (state topic.MaintenanceUpgradeState, err error) {
	var installed bool
	if err = tx.Raw(`SELECT to_regclass('topic_maintenance_state') IS NOT NULL`).Row().Scan(&installed); err != nil || !installed {
		return state, err
	}
	state.Paused, err = maintenanceState(tx)
	if err != nil {
		return state, err
	}
	if err = tx.Raw(`SELECT COALESCE((SELECT is_applied FROM goose_db_version WHERE version_id=? ORDER BY id DESC LIMIT 1),false)`, topic.OntologyBudgetRetirementVersion).Row().Scan(&state.MigrationApplied); err != nil {
		return state, err
	}
	paused, err := maintenanceUpgradeCommandExists(tx, "pause", topic.OntologyUpgradePauseKey)
	if err != nil {
		return state, err
	}
	resumed, err := maintenanceUpgradeCommandExists(tx, "resume", topic.OntologyUpgradeResumeKey)
	if err != nil {
		return state, err
	}
	if resumed && (!paused || !state.MigrationApplied) {
		return state, topic.ErrConflict
	}
	state.OwnsPause = paused && !resumed
	state.Required = !state.MigrationApplied || state.OwnsPause
	if state.OwnsPause && !state.Paused {
		return state, topic.ErrConflict
	}
	return state, nil
}

func maintenanceUpgradeCommandExists(tx *gorm.DB, action, key string) (bool, error) {
	var id, hash, kind, status string
	err := tx.Raw(`SELECT run_id::text,request_hash,kind,status FROM topic_maintenance_runs WHERE operation_key=?`, key).Row().Scan(&id, &hash, &kind, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	expected, err := topic.MaintenanceCommandHash(domain.TopicMaintenanceCommand{OperationKey: key, Action: action})
	if err != nil {
		return false, err
	}
	if hash != expected || kind != action || status != "completed" {
		return false, topic.ErrConflict
	}
	var audited bool
	err = tx.Raw(`SELECT EXISTS(SELECT 1 FROM audit_log WHERE operation=? AND entity_type='ontology_maintenance'
	 AND entity_id=? AND actor_role='system' AND metadata->>'operation_key'=? AND metadata->>'action'=? AND metadata->>'migration_version'=?)`,
		topic.OntologyUpgradeAuditOperation, id, key, action, "20261023020000").Row().Scan(&audited)
	if err != nil {
		return false, err
	}
	if !audited {
		return false, topic.ErrConflict
	}
	return true, nil
}

func (s *Store) PrepareMaintenanceUpgrade(ctx context.Context, prepareAudit topic.MaintenanceAudit) (state topic.MaintenanceUpgradeState, err error) {
	err = s.withMaintenanceUpgradeSchema(ctx, func(tx *gorm.DB) error {
		state, err = maintenanceUpgradeState(tx)
		if err != nil || !state.Required || state.MigrationApplied {
			return err
		}
		if prepareAudit == nil {
			return topic.ErrAuditUnavailable
		}
		var enabled bool
		if err = tx.Raw(`SELECT COALESCE((SELECT value='true' FROM app_config WHERE key='ONTOLOGY_MAINTENANCE_ENABLED' FOR SHARE),false)`).Row().Scan(&enabled); err != nil {
			return err
		}
		var now time.Time
		if err = tx.Raw(`SELECT clock_timestamp()`).Row().Scan(&now); err != nil {
			return err
		}
		if enabled && !state.Paused {
			if _, err = maintenanceCommandTx(ctx, tx, domain.TopicMaintenanceCommand{OperationKey: topic.OntologyUpgradePauseKey, Action: "pause"}, now, prepareAudit); err != nil {
				return err
			}
			state.Paused, state.OwnsPause = true, true
		}
		if err = tx.Raw(`SELECT
		 (SELECT count(*) FROM topic_maintenance_batches WHERE status='running' AND lease_until>?),
		 (SELECT count(*) FROM topic_maintenance_teams WHERE lease_until>?),
		 (SELECT count(*) FROM topic_maintenance_attempts WHERE reconciled_at IS NULL)`, now, now).Row().Scan(&state.LiveBatches, &state.LiveTeamLeases, &state.UnreconciledAttempts); err != nil {
			return err
		}
		if state.Disposition() != "ready" {
			return nil
		}
		state.RecoveredBatches, err = expireMaintenanceBatchRecords(tx, now)
		if err != nil {
			return err
		}
		state.ClosedRuns, err = closeMaintenanceUpgradeRuns(ctx, tx, now, prepareAudit)
		return err
	})
	return state, err
}

func closeMaintenanceUpgradeRuns(ctx context.Context, tx *gorm.DB, now time.Time, prepareAudit topic.MaintenanceAudit) (int64, error) {
	var runs []topic.MaintenanceRun
	if err := tx.Raw(`SELECT run_id::text AS id,kind,operation_key FROM topic_maintenance_runs
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

func (s *Store) CompleteMaintenanceUpgrade(ctx context.Context, prepareAudit topic.MaintenanceAudit) (state topic.MaintenanceUpgradeState, err error) {
	err = s.withMaintenanceUpgradeSchema(ctx, func(tx *gorm.DB) error {
		state, err = maintenanceUpgradeState(tx)
		if err != nil || !state.Required {
			return err
		}
		if !state.MigrationApplied {
			return topic.ErrMaintenanceUpgradeBlocked
		}
		if prepareAudit == nil {
			return topic.ErrAuditUnavailable
		}
		var now time.Time
		if err = tx.Raw(`SELECT clock_timestamp()`).Row().Scan(&now); err != nil {
			return err
		}
		if _, err = maintenanceCommandTx(ctx, tx, domain.TopicMaintenanceCommand{OperationKey: topic.OntologyUpgradeResumeKey, Action: "resume"}, now, prepareAudit); err != nil {
			return err
		}
		state.Paused, state.OwnsPause = false, false
		return nil
	})
	return state, err
}

var _ topic.MaintenanceUpgradeRepository = (*Store)(nil)
