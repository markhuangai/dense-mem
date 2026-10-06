package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func (s *Store) MaintenanceCommand(ctx context.Context, input domain.OntologyMaintenanceCommand, now time.Time) (ontology.MaintenanceRun, error) {
	input, err := ontology.PrepareMaintenanceCommand(input)
	if err != nil {
		return ontology.MaintenanceRun{}, err
	}
	hash, err := ontology.MaintenanceCommandHash(input)
	if err != nil {
		return ontology.MaintenanceRun{}, err
	}
	var result ontology.MaintenanceRun
	err = s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		paused, err := maintenanceState(tx)
		if err != nil {
			return err
		}
		var previousHash string
		err = tx.Raw(`SELECT request_hash FROM ontology_maintenance_runs WHERE operation_key=?`, input.OperationKey).Row().Scan(&previousHash)
		if err == nil {
			if previousHash != hash {
				return ontology.ErrConflict
			}
			result, err = scanMaintenanceRun(tx.Raw(`SELECT `+maintenanceRunColumns+` FROM ontology_maintenance_runs WHERE operation_key=?`, input.OperationKey).Row())
			if err != nil {
				return err
			}
			retryable, err := maintenanceRetryableRuns(tx, []string{result.ID})
			result.Retryable = retryable[result.ID]
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		result = ontology.MaintenanceRun{ID: uuid.NewString(), Kind: input.Action, OperationKey: input.OperationKey, MaxBatches: input.MaxBatches, CreatedAt: now, UpdatedAt: now, Status: "completed"}
		if input.Action == "pause" || input.Action == "resume" {
			if err = tx.Exec(`UPDATE ontology_maintenance_state SET paused=? WHERE singleton`, input.Action == "pause").Error; err != nil {
				return err
			}
		} else {
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
			window, err := scanMaintenanceWindow(tx.Raw(`SELECT `+maintenanceWindowColumns+` FROM ontology_maintenance_windows WHERE starts_at<=? AND ends_at>? ORDER BY starts_at DESC LIMIT 1`, now, now).Row())
			if err != nil {
				return err
			}
			if window == nil || window.Overrun {
				return ontology.ErrBudgetDeferred
			}
			result.WindowID = window.ID
			result.Status = "pending"
			if input.Action == "retry" {
				var exists bool
				if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM ontology_maintenance_runs WHERE run_id=?::uuid AND kind IN ('scheduled','run','retry'))`, input.RetryRunID).Row().Scan(&exists); err != nil {
					return err
				}
				if !exists {
					return ontology.ErrNotFound
				}
				retryable, err := maintenanceRetryableRuns(tx, []string{input.RetryRunID})
				if err != nil {
					return err
				}
				if !retryable[input.RetryRunID] {
					return ontology.ErrInvalid
				}
			}
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		var windowID any
		if result.WindowID != "" {
			windowID = result.WindowID
		}
		if err := tx.Exec(`INSERT INTO ontology_maintenance_runs(run_id,window_id,operation_key,request_hash,command,kind,status,max_batches,created_at,updated_at) VALUES(?::uuid,?::uuid,?,?,?::jsonb,?,?,?,?,?)`, result.ID, windowID, input.OperationKey, hash, string(encoded), result.Kind, result.Status, result.MaxBatches, now, now).Error; err != nil {
			return err
		}
		result, err = scanMaintenanceRun(tx.Raw(`SELECT `+maintenanceRunColumns+` FROM ontology_maintenance_runs WHERE run_id=?::uuid`, result.ID).Row())
		return err
	})
	return result, err
}

const maintenanceActiveSourceJoin = ` FROM ontology_maintenance_sources AS source
 JOIN teams AS team ON team.id=source.team_id AND team.status='active' AND team.deleted_at IS NULL
 JOIN memory_spaces AS space ON space.team_id=source.team_id AND space.id=source.shared_space_id AND space.generation=source.space_generation AND space.lifecycle_state='active'`

func maintenanceRetryableRuns(tx *gorm.DB, runIDs []string) (map[string]bool, error) {
	result := make(map[string]bool)
	if len(runIDs) == 0 {
		return result, nil
	}
	var matches []string
	if err := tx.Raw(`SELECT DISTINCT source.last_run_id::text`+maintenanceActiveSourceJoin+` WHERE source.eligible AND source.status='failed' AND source.last_run_id=ANY(?::uuid[])`, pq.Array(runIDs)).Scan(&matches).Error; err != nil {
		return nil, err
	}
	for _, id := range matches {
		result[id] = true
	}
	return result, nil
}

func (s *Store) RecordMaintenanceFailure(ctx context.Context, runID, code string, now time.Time) error {
	if len(code) > 128 {
		return ontology.ErrInvalid
	}
	return s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE ontology_maintenance_runs SET status='incomplete',failure_code=?,updated_at=? WHERE run_id=?::uuid`, code, now, runID).Error
	})
}

func (s *Store) MaintenanceStatus(ctx context.Context, now time.Time) (ontology.MaintenanceStatus, error) {
	result := ontology.MaintenanceStatus{ObservedAt: now}
	err := s.readMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT paused,last_successful_progress FROM ontology_maintenance_state WHERE singleton`).Row().Scan(&result.Paused, &result.LastSuccessfulProgress); err != nil {
			return err
		}
		var enabled string
		err := tx.Raw(`SELECT value FROM app_config WHERE key=?`, domain.AppConfigOntologyEnabled).Row().Scan(&enabled)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		result.Enabled = enabled == "true"
		if err := tx.Raw(`SELECT count(*) FILTER(WHERE source.eligible),count(*) FILTER(WHERE source.eligible AND source.status='organized'),count(*) FILTER(WHERE source.eligible AND source.status='pending'),
		 count(*) FILTER(WHERE source.eligible AND source.status='ambiguous'),count(*) FILTER(WHERE source.eligible AND source.status='failed'),count(*) FILTER(WHERE source.eligible AND source.status='budget_deferred'),
		 min(source.pending_at) FILTER(WHERE source.eligible AND source.status IN ('pending','budget_deferred'))`+maintenanceActiveSourceJoin).Row().Scan(&result.Counts.Eligible, &result.Counts.Organized, &result.Counts.Pending, &result.Counts.Ambiguous, &result.Counts.Failed, &result.Counts.BudgetDeferred, &result.OldestPendingAt); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT NOT EXISTS(SELECT 1 FROM teams AS team JOIN memory_spaces AS space ON space.team_id=team.id AND space.kind='team_shared' AND space.lifecycle_state='active'
		 LEFT JOIN ontology_maintenance_teams AS state ON state.team_id=team.id AND state.shared_space_id=space.id AND state.space_generation=space.generation
		 WHERE team.status='active' AND team.deleted_at IS NULL AND (state.team_id IS NULL OR state.discovery_kind<4 OR EXISTS(
		 SELECT 1 FROM ontology_maintenance_markers AS marker WHERE marker.team_id=team.id AND marker.shared_space_id=space.id AND marker.space_generation=space.generation)))`).Row().Scan(&result.DiscoveryComplete); err != nil {
			return err
		}
		result.CoverageComplete = result.DiscoveryComplete && result.Counts.Pending == 0 && result.Counts.BudgetDeferred == 0 && result.Counts.Failed == 0 && result.Counts.Ambiguous == 0
		result.Window, err = scanMaintenanceWindow(tx.Raw(`SELECT ` + maintenanceWindowColumns + ` FROM ontology_maintenance_windows ORDER BY starts_at DESC LIMIT 1`).Row())
		if err != nil {
			return err
		}
		if result.Window != nil && (result.Window.Overrun || result.Window.ChargedInput >= result.Window.Policy.InputTokens || result.Window.ChargedOutput >= result.Window.Policy.OutputTokens) {
			result.CoverageComplete = false
		}
		run, err := scanMaintenanceRun(tx.Raw(`SELECT ` + maintenanceRunColumns + ` FROM ontology_maintenance_runs ORDER BY created_at DESC,run_id DESC LIMIT 1`).Row())
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			retryable, err := maintenanceRetryableRuns(tx, []string{run.ID})
			if err != nil {
				return err
			}
			run.Retryable = retryable[run.ID]
			result.LatestRun = &run
		}
		return nil
	})
	return result, err
}

func (s *Store) ListMaintenanceRuns(ctx context.Context, cursor string, limit int) (ontology.MaintenanceRunPage, error) {
	page := ontology.MaintenanceRunPage{Runs: []ontology.MaintenanceRun{}}
	if limit < 1 || limit > ontology.MaxPageSize {
		return page, ontology.ErrInvalid
	}
	if cursor != "" {
		if _, err := uuid.Parse(cursor); err != nil {
			return page, ontology.ErrInvalid
		}
	}
	err := s.readMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		var after sql.NullTime
		if cursor != "" {
			err := tx.Raw(`SELECT created_at FROM ontology_maintenance_runs WHERE run_id=?::uuid`, cursor).Row().Scan(&after)
			if errors.Is(err, sql.ErrNoRows) {
				return ontology.ErrInvalid
			}
			if err != nil {
				return err
			}
		}
		rows, err := tx.Raw(`SELECT `+maintenanceRunColumns+` FROM ontology_maintenance_runs WHERE (?::timestamptz IS NULL OR (created_at,run_id)<(?::timestamptz,NULLIF(?,'')::uuid)) ORDER BY created_at DESC,run_id DESC LIMIT ?`, after, after, cursor, limit+1).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			run, err := scanMaintenanceRun(rows)
			if err != nil {
				return err
			}
			page.Runs = append(page.Runs, run)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Runs) > limit {
			page.Runs = page.Runs[:limit]
			page.NextCursor = page.Runs[limit-1].ID
		}
		ids := make([]string, len(page.Runs))
		for i, run := range page.Runs {
			ids[i] = run.ID
		}
		retryable, err := maintenanceRetryableRuns(tx, ids)
		if err != nil {
			return err
		}
		for i := range page.Runs {
			page.Runs[i].Retryable = retryable[page.Runs[i].ID]
		}
		return nil
	})
	return page, err
}
