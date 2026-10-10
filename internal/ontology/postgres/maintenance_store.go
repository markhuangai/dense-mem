package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func (s *Store) withMaintenanceSystem(ctx context.Context, fn func(*gorm.DB) error) error {
	if err := requireAutomatic(ctx); err != nil {
		return err
	}
	if s == nil || s.db == nil || s.rls == nil {
		return fmt.Errorf("%w: database and RLS required", ontology.ErrAccounting)
	}
	return s.rls.WithSystemTx(ctx, s.db, fn)
}

func (s *Store) readMaintenanceSystem(ctx context.Context, fn func(*gorm.DB) error) error {
	if err := requireAutomatic(ctx); err != nil {
		return err
	}
	if s == nil || s.db == nil || s.rls == nil {
		return fmt.Errorf("%w: database and RLS required", ontology.ErrAccounting)
	}
	return s.rls.WithSystemReadOnlyRepeatableTx(ctx, s.db, fn)
}

const maintenanceWindowColumns = `window_id::text,starts_at,ends_at,policy,charged_input,charged_output,reported_input,reported_output,reserved_input,reserved_output,overrun`
const maintenanceRunColumns = `run_id::text,COALESCE(window_id::text,''),kind,status,operation_key,max_batches,completed_batches,failure_code,created_at,updated_at`

type maintenanceScanner interface{ Scan(...any) error }

func scanMaintenanceWindow(row maintenanceScanner) (*ontology.MaintenanceWindow, error) {
	result := &ontology.MaintenanceWindow{}
	var policy []byte
	err := row.Scan(&result.ID, &result.StartsAt, &result.EndsAt, &policy, &result.ChargedInput, &result.ChargedOutput, &result.ReportedInput, &result.ReportedOutput, &result.ReservedInput, &result.ReservedOutput, &result.Overrun)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(policy, &result.Policy); err != nil {
		return nil, err
	}
	return result, nil
}

func scanMaintenanceRun(row maintenanceScanner) (ontology.MaintenanceRun, error) {
	var value ontology.MaintenanceRun
	err := row.Scan(&value.ID, &value.WindowID, &value.Kind, &value.Status, &value.OperationKey, &value.MaxBatches, &value.CompletedBatches, &value.FailureCode, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}

func maintenanceEnabled(tx *gorm.DB) (bool, error) {
	var value string
	err := tx.Raw(`SELECT value FROM app_config WHERE key=? FOR SHARE`, domain.AppConfigOntologyEnabled).Row().Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return value == "true", err
}

func maintenanceState(tx *gorm.DB) (bool, error) {
	var paused bool
	err := tx.Raw(`SELECT paused FROM ontology_maintenance_state WHERE singleton FOR UPDATE`).Row().Scan(&paused)
	return paused, err
}

func (s *Store) EnsureMaintenanceWindow(ctx context.Context, policy domain.OntologyMaintenanceConfig, now time.Time) (*ontology.MaintenanceWindow, error) {
	var result *ontology.MaintenanceWindow
	err := s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		if _, err := maintenanceState(tx); err != nil {
			return err
		}
		var version string
		if err := tx.Raw(`SELECT value FROM app_config WHERE key=? FOR SHARE`, domain.AppConfigUpdateTimeKey).Row().Scan(&version); err != nil {
			return err
		}
		if policy.SettingsVersion == "" || version != policy.SettingsVersion {
			return ontology.ErrConflict
		}
		previous, err := scanMaintenanceWindow(tx.Raw(`SELECT ` + maintenanceWindowColumns + ` FROM ontology_maintenance_windows ORDER BY starts_at DESC LIMIT 1`).Row())
		if err != nil {
			return err
		}
		if previous != nil && now.Before(previous.EndsAt) {
			result = previous
			return nil
		}
		if !policy.Enabled {
			return nil
		}
		start, end, err := ontology.MaintenanceWindowBounds(policy, now, previous)
		if err != nil {
			return err
		}
		if start.IsZero() {
			return nil
		}
		encoded, err := json.Marshal(policy)
		if err != nil {
			return err
		}
		result = &ontology.MaintenanceWindow{ID: uuid.NewString(), StartsAt: start, EndsAt: end, Policy: policy}
		if err := tx.Exec(`INSERT INTO ontology_maintenance_windows(window_id,starts_at,ends_at,policy) VALUES(?::uuid,?,?,?::jsonb)`, result.ID, start, end, string(encoded)).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE ontology_maintenance_runs SET status='incomplete',failure_code='window_closed',updated_at=? WHERE window_id<>?::uuid AND status IN ('pending','running')`, now, result.ID).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO ontology_maintenance_runs(run_id,window_id,operation_key,request_hash,command,kind,status,max_batches,created_at,updated_at)
		 VALUES(?::uuid,?::uuid,?,'scheduled','{}','scheduled','pending',0,?,?)`, uuid.NewString(), result.ID, "scheduled:"+result.ID, now, now).Error
	})
	return result, err
}

func seedMaintenanceTeams(tx *gorm.DB) error {
	return tx.Exec(`INSERT INTO ontology_maintenance_teams(team_id,shared_space_id,space_generation)
	 SELECT team.id,space.id,space.generation FROM teams AS team JOIN memory_spaces AS space
	 ON space.team_id=team.id AND space.kind='team_shared' AND space.lifecycle_state='active'
	 WHERE team.status='active' AND team.deleted_at IS NULL AND NOT EXISTS(
	 SELECT 1 FROM ontology_maintenance_teams AS state WHERE state.team_id=team.id AND state.shared_space_id=space.id AND state.space_generation=space.generation)
	 ORDER BY team.id LIMIT ? ON CONFLICT DO NOTHING`, ontology.MaintenancePageSize).Error
}

func (s *Store) ClaimMaintenanceTurn(ctx context.Context, windowID string, now time.Time, lease time.Duration) (*ontology.MaintenanceTurn, error) {
	if _, err := uuid.Parse(windowID); err != nil || lease <= 0 {
		return nil, ontology.ErrInvalid
	}
	var result *ontology.MaintenanceTurn
	err := s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		paused, err := maintenanceState(tx)
		if err != nil {
			return err
		}
		if paused {
			return ontology.ErrMaintenancePaused
		}
		enabled, err := maintenanceEnabled(tx)
		if err != nil {
			return err
		}
		if !enabled {
			return ontology.ErrMaintenanceDisabled
		}
		window, err := scanMaintenanceWindow(tx.Raw(`SELECT `+maintenanceWindowColumns+` FROM ontology_maintenance_windows WHERE window_id=?::uuid`, windowID).Row())
		if err != nil {
			return err
		}
		if window == nil || !now.Before(window.EndsAt) {
			return ontology.ErrBudgetDeferred
		}
		if window.Overrun {
			return ontology.ErrBudgetDeferred
		}
		if err = seedMaintenanceTeams(tx); err != nil {
			return err
		}
		if err = tx.Exec(`UPDATE ontology_maintenance_batches SET status='lost',failure_code='lease_lost' WHERE status='running' AND lease_until<=?`, now).Error; err != nil {
			return err
		}
		var active int
		if err = tx.Raw(`SELECT count(*) FROM ontology_maintenance_teams WHERE lease_until>?`, now).Row().Scan(&active); err != nil {
			return err
		}
		if active >= window.Policy.MaxConcurrency {
			return nil
		}
		candidate := &ontology.MaintenanceTurn{LeaseToken: uuid.NewString(), LeaseUntil: now.Add(lease)}
		err = tx.Raw(`SELECT run_id::text,COALESCE(command->>'retry_run_id','') FROM ontology_maintenance_runs AS run
		 WHERE window_id=?::uuid AND kind IN ('scheduled','run','retry') AND status IN ('pending','running')
		 AND (max_batches=0 OR completed_batches+(SELECT count(*) FROM ontology_maintenance_batches AS batch WHERE batch.run_id=run.run_id AND batch.status='running')<max_batches)
		 ORDER BY (kind='scheduled'),created_at,run_id LIMIT 1`, windowID).Row().Scan(&candidate.RunID, &candidate.RetryRunID)
		if errors.Is(err, sql.ErrNoRows) {
			if err := tx.Exec(`UPDATE ontology_maintenance_runs SET status='pending',updated_at=? WHERE window_id=?::uuid AND kind='scheduled' AND status IN ('completed','incomplete')`, now, windowID).Error; err != nil {
				return err
			}
			err = tx.Raw(`SELECT run_id::text,'' FROM ontology_maintenance_runs WHERE window_id=?::uuid AND kind='scheduled' AND status='pending'`, windowID).Row().Scan(&candidate.RunID, &candidate.RetryRunID)
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		err = tx.Raw(`SELECT state.team_id::text,state.shared_space_id::text,state.space_generation FROM ontology_maintenance_teams AS state
		 JOIN teams AS team ON team.id=state.team_id AND team.status='active' AND team.deleted_at IS NULL
		 JOIN memory_spaces AS space ON space.team_id=state.team_id AND space.id=state.shared_space_id AND space.generation=state.space_generation AND space.lifecycle_state='active'
		 WHERE (state.lease_until IS NULL OR state.lease_until<=?) AND (?='' OR EXISTS(SELECT 1 FROM ontology_maintenance_sources AS retry_source
		 WHERE retry_source.team_id=state.team_id AND retry_source.shared_space_id=state.shared_space_id AND retry_source.space_generation=state.space_generation AND retry_source.last_run_id=NULLIF(?,'')::uuid AND retry_source.eligible AND retry_source.status='failed'))
		 AND (?<>'' OR state.discovery_kind<4 OR EXISTS(
		 SELECT 1 FROM ontology_maintenance_markers AS marker WHERE marker.team_id=state.team_id AND marker.shared_space_id=state.shared_space_id AND marker.space_generation=state.space_generation)
		 OR EXISTS(SELECT 1 FROM ontology_maintenance_sources AS source WHERE source.team_id=state.team_id AND source.shared_space_id=state.shared_space_id AND source.space_generation=state.space_generation AND source.eligible AND
		 (source.status='pending' OR (source.status='budget_deferred' AND NOT EXISTS(SELECT 1 FROM ontology_maintenance_runs AS run WHERE run.run_id=source.last_run_id AND run.window_id=?::uuid)))))
		 ORDER BY state.last_turn,state.team_id LIMIT 1 FOR UPDATE OF state SKIP LOCKED`, now, candidate.RetryRunID, candidate.RetryRunID, candidate.RetryRunID, windowID).Row().Scan(&candidate.TeamID, &candidate.SpaceID, &candidate.Generation)
		if errors.Is(err, sql.ErrNoRows) {
			if active > 0 {
				return nil
			}
			return completeMaintenanceRun(tx, candidate.RunID, candidate.RetryRunID, candidate.RetryRunID == "", "", now)
		}
		if err != nil {
			return err
		}
		if err = tx.Exec(`UPDATE ontology_maintenance_state SET turn_sequence=turn_sequence+1 WHERE singleton`).Error; err != nil {
			return err
		}
		if err = tx.Exec(`UPDATE ontology_maintenance_teams SET last_turn=(SELECT turn_sequence FROM ontology_maintenance_state WHERE singleton),lease_token=?::uuid,lease_until=? WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, candidate.LeaseToken, candidate.LeaseUntil, candidate.TeamID, candidate.SpaceID, candidate.Generation).Error; err != nil {
			return err
		}
		result = candidate
		return nil
	})
	return result, err
}

func (s *Store) ReleaseMaintenanceTurn(ctx context.Context, turn ontology.MaintenanceTurn) error {
	return s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE ontology_maintenance_teams SET lease_token=NULL,lease_until=NULL WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND lease_token=?::uuid`, turn.TeamID, turn.SpaceID, turn.Generation, turn.LeaseToken).Error
	})
}

func checkMaintenanceTurn(tx *gorm.DB, turn ontology.MaintenanceTurn) error {
	var found bool
	if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM ontology_maintenance_teams WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND lease_token=?::uuid AND lease_until>clock_timestamp())`, turn.TeamID, turn.SpaceID, turn.Generation, turn.LeaseToken).Row().Scan(&found); err != nil {
		return err
	}
	if !found {
		return ontology.ErrLeaseLost
	}
	return nil
}

func maintenancePublicationFence(ctx context.Context, tx *gorm.DB, fence scope) error {
	claim, ok := ontology.MaintenanceClaimFromContext(ctx)
	if !ok {
		return nil
	}
	if claim.TeamID != fence.TeamID || claim.SpaceID != fence.SpaceID || claim.Generation != fence.Generation {
		return ontology.ErrUnauthorized
	}
	var status string
	err := tx.Raw(`SELECT status FROM ontology_maintenance_batches WHERE batch_id=?::uuid AND team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND lease_token=?::uuid AND lease_until>clock_timestamp() FOR UPDATE`, claim.ID, fence.TeamID, fence.SpaceID, fence.Generation, claim.LeaseToken).Row().Scan(&status)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "running") {
		return ontology.ErrLeaseLost
	}
	return err
}

var _ ontology.MaintenanceRepository = (*Store)(nil)
