package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func (s *Store) MaintenanceCommand(ctx context.Context, input domain.TopicMaintenanceCommand, now time.Time, prepareAudit topic.MaintenanceAudit) (topic.MaintenanceRun, error) {
	input, err := topic.PrepareMaintenanceCommand(input)
	if err != nil {
		return topic.MaintenanceRun{}, err
	}
	var result topic.MaintenanceRun
	err = s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		var err error
		result, err = maintenanceCommandTx(ctx, tx, input, now, prepareAudit)
		return err
	})
	return result, err
}

func maintenanceCommandTx(ctx context.Context, tx *gorm.DB, input domain.TopicMaintenanceCommand, now time.Time, prepareAudit topic.MaintenanceAudit) (topic.MaintenanceRun, error) {
	hash, err := topic.MaintenanceCommandHash(input)
	if err != nil {
		return topic.MaintenanceRun{}, err
	}
	var result topic.MaintenanceRun
	err = func() error {
		paused, err := maintenanceState(tx)
		if err != nil {
			return err
		}
		var previousHash string
		err = tx.Raw(`SELECT request_hash FROM topic_maintenance_runs WHERE operation_key=?`, input.OperationKey).Row().Scan(&previousHash)
		if err == nil {
			if previousHash != hash {
				return topic.ErrConflict
			}
			result, err = scanMaintenanceRun(tx.Raw(`SELECT `+maintenanceRunColumns+` FROM topic_maintenance_runs WHERE operation_key=?`, input.OperationKey).Row())
			if err != nil {
				return err
			}
			retryable, err := maintenanceRetryableRuns(tx, []string{result.ID})
			result.Retryable = retryable[result.ID]
			if err != nil {
				return err
			}
			return appendMaintenanceAuditTx(ctx, tx, result, prepareAudit)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		result = topic.MaintenanceRun{ID: uuid.NewString(), Kind: input.Action, OperationKey: input.OperationKey, MaxBatches: input.MaxBatches, CreatedAt: now, UpdatedAt: now, Status: "completed"}
		if input.Action == "pause" || input.Action == "resume" {
			if err = tx.Exec(`UPDATE topic_maintenance_state SET paused=? WHERE singleton`, input.Action == "pause").Error; err != nil {
				return err
			}
		} else {
			if paused {
				return topic.ErrMaintenancePaused
			}
			enabled, err := maintenanceEnabled(tx)
			if err != nil {
				return err
			}
			if !enabled {
				return topic.ErrMaintenanceDisabled
			}
			window, err := currentMaintenanceWindow(tx, now)
			if err != nil {
				return err
			}
			if window == nil {
				return topic.ErrWindowClosed
			}
			result.WindowID = window.ID
			result.Status = "pending"
			if input.Action == "retry" {
				var exists bool
				if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM topic_maintenance_runs WHERE run_id=?::uuid AND kind IN ('scheduled','run','retry','reassess'))`, input.RetryRunID).Row().Scan(&exists); err != nil {
					return err
				}
				if !exists {
					return topic.ErrNotFound
				}
				retryable, err := maintenanceRetryableRuns(tx, []string{input.RetryRunID})
				if err != nil {
					return err
				}
				if !retryable[input.RetryRunID] {
					return topic.ErrInvalid
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
		if err := tx.Exec(`INSERT INTO topic_maintenance_runs(run_id,window_id,operation_key,request_hash,command,kind,status,max_batches,created_at,updated_at) VALUES(?::uuid,?::uuid,?,?,?::jsonb,?,?,?,?,?)`, result.ID, windowID, input.OperationKey, hash, string(encoded), result.Kind, result.Status, result.MaxBatches, now, now).Error; err != nil {
			return err
		}
		if input.Action == "reassess" {
			if err := queueUncategorizedReassessment(ctx, tx, input, result.ID); err != nil {
				return err
			}
		}
		result, err = scanMaintenanceRun(tx.Raw(`SELECT `+maintenanceRunColumns+` FROM topic_maintenance_runs WHERE run_id=?::uuid`, result.ID).Row())
		if err != nil {
			return err
		}
		return appendMaintenanceAuditTx(ctx, tx, result, prepareAudit)
	}()
	return result, err
}

func queueUncategorizedReassessment(ctx context.Context, tx *gorm.DB, input domain.TopicMaintenanceCommand, runID string) error {
	fence, err := sharedScope(ctx, tx, input.TeamID)
	if errors.Is(err, sql.ErrNoRows) {
		return topic.ErrInvalid
	}
	if err != nil {
		return err
	}
	for _, id := range input.EvidenceIDs {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?::text,25022))`, fence.SpaceID+":"+id).Error; err != nil {
			return err
		}
	}
	for _, id := range input.EvidenceIDs {
		handle := topic.SourceHandle{Kind: topic.EvidenceSource, ID: id, Version: 1}
		if _, err := readSource(tx, fence, handle); err != nil {
			if errors.Is(err, topic.ErrSourceStale) {
				return topic.ErrInvalid
			}
			return err
		}
		var status string
		err := tx.Raw(`SELECT status FROM topic_assignment_outcomes WHERE team_id=?::uuid AND shared_space_id=?::uuid
			AND space_generation=? AND evidence_id=?::uuid ORDER BY created_at DESC,outcome_id LIMIT 1`,
			fence.TeamID, fence.SpaceID, fence.Generation, id).Row().Scan(&status)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "uncategorized") {
			return topic.ErrInvalid
		}
		if err != nil {
			return err
		}
	}
	for _, id := range input.EvidenceIDs {
		handle := topic.SourceHandle{Kind: topic.EvidenceSource, ID: id, Version: 1}
		if err := refreshMaintenanceSource(tx, fence, handle, false); err != nil {
			return err
		}
		updated := tx.Exec(`UPDATE topic_maintenance_sources SET status='pending',reason='operator_reassessment',
			force_reassessment=true,revision=revision+1,pending_at=clock_timestamp(),last_run_id=?::uuid,updated_at=clock_timestamp()
			WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_kind='evidence' AND source_id=? AND eligible`,
			runID, fence.TeamID, fence.SpaceID, fence.Generation, id)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return topic.ErrInvalid
		}
	}
	return nil
}

func appendMaintenanceAuditTx(ctx context.Context, tx *gorm.DB, run topic.MaintenanceRun, prepareAudit topic.MaintenanceAudit) error {
	if prepareAudit == nil {
		return nil
	}
	entry, err := prepareAudit(run)
	if err == nil {
		err = storagepostgres.InsertAuditEntryTx(ctx, tx, entry)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", topic.ErrAuditUnavailable, err)
	}
	return nil
}

const maintenanceActiveSourceJoin = ` FROM topic_maintenance_sources AS source
 JOIN teams AS team ON team.id=source.team_id AND team.status='active' AND team.deleted_at IS NULL
 JOIN memory_spaces AS space ON space.team_id=source.team_id AND space.id=source.shared_space_id AND space.generation=source.space_generation AND space.lifecycle_state='active'`

func completeMaintenanceRun(tx *gorm.DB, runID, retryRunID string, allSources bool, requiredFailure string) error {
	unresolved, err := maintenanceRunHasUnresolvedSources(tx, runID, retryRunID, allSources)
	if err != nil {
		return err
	}
	return writeMaintenanceRunOutcome(tx, runID, unresolved, requiredFailure)
}

func maintenanceRunHasUnresolvedSources(tx *gorm.DB, runID, retryRunID string, allSources bool) (bool, error) {
	var unresolved bool
	err := tx.Raw(`SELECT EXISTS(SELECT 1`+maintenanceActiveSourceJoin+`
	 WHERE source.eligible AND (
	 ((source.status IN ('failed','ambiguous') OR (NOT ? AND source.status='pending'))
	  AND (? OR source.last_run_id IN (?::uuid,NULLIF(?,'')::uuid))) OR
	 ((source.equivalence_status='failed' OR (NOT ? AND source.equivalence_status='pending'))
	  AND (? OR source.equivalence_last_run_id IN (?::uuid,NULLIF(?,'')::uuid)))))`,
		allSources, allSources, runID, retryRunID, allSources, allSources, runID, retryRunID).Row().Scan(&unresolved)
	return unresolved, err
}

func writeMaintenanceRunOutcome(tx *gorm.DB, runID string, unresolved bool, requiredFailure string) error {
	var previousFailure string
	if err := tx.Raw(`SELECT failure_code FROM topic_maintenance_runs WHERE run_id=?::uuid`, runID).Row().Scan(&previousFailure); err != nil {
		return err
	}
	if requiredFailure != "" {
		previousFailure = requiredFailure
	}
	status, reason := topic.MaintenanceCompletion(unresolved || requiredFailure != "", previousFailure)
	return tx.Exec(`UPDATE topic_maintenance_runs SET status=?,failure_code=?,updated_at=clock_timestamp() WHERE run_id=?::uuid`, status, reason, runID).Error
}

func maintenanceRetryableRuns(tx *gorm.DB, runIDs []string) (map[string]bool, error) {
	result := make(map[string]bool)
	if len(runIDs) == 0 {
		return result, nil
	}
	var matches []string
	if err := tx.Raw(`SELECT DISTINCT run_id FROM (
	 SELECT source.last_run_id::text AS run_id`+maintenanceActiveSourceJoin+` WHERE source.eligible AND source.status='failed' AND source.last_run_id=ANY(?::uuid[])
	 UNION ALL
	 SELECT source.equivalence_last_run_id::text AS run_id`+maintenanceActiveSourceJoin+` WHERE source.eligible AND source.equivalence_status='failed' AND source.equivalence_last_run_id=ANY(?::uuid[])
	) AS retryable`, pq.Array(runIDs), pq.Array(runIDs)).Scan(&matches).Error; err != nil {
		return nil, err
	}
	for _, id := range matches {
		result[id] = true
	}
	return result, nil
}

func (s *Store) MaintenanceRunRetryable(ctx context.Context, runID string) (bool, error) {
	var retryable bool
	err := s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		values, err := maintenanceRetryableRuns(tx, []string{runID})
		if err == nil {
			retryable = values[runID]
		}
		return err
	})
	return retryable, err
}

func (s *Store) RecordMaintenanceFailure(ctx context.Context, runID, code string, now time.Time) error {
	if len(code) > 128 {
		return topic.ErrInvalid
	}
	return s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		return recordMaintenanceFailureTx(tx, runID, code, now)
	})
}

func recordMaintenanceFailureTx(tx *gorm.DB, runID, code string, now time.Time) error {
	return tx.Exec(`UPDATE topic_maintenance_runs SET status='incomplete',failure_code=?,updated_at=? WHERE run_id=?::uuid`, code, now, runID).Error
}

func (s *Store) MaintenanceStatus(ctx context.Context, now time.Time) (topic.MaintenanceStatus, error) {
	result := topic.MaintenanceStatus{ObservedAt: now}
	err := s.readMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT state.paused,
			GREATEST(state.last_successful_progress,(SELECT max(team_state.last_successful_progress) FROM topic_maintenance_teams AS team_state))
			FROM topic_maintenance_state AS state WHERE state.singleton`).Row().Scan(&result.Paused, &result.LastSuccessfulProgress); err != nil {
			return err
		}
		var enabled string
		err := tx.Raw(`SELECT value FROM app_config WHERE key=?`, domain.AppConfigTopicMaintenanceEnabled).Row().Scan(&enabled)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		result.Enabled = enabled == "true"
		if err := tx.Raw(`SELECT count(*) FILTER(WHERE source.eligible),count(*) FILTER(WHERE source.eligible AND source.status='organized'),count(*) FILTER(WHERE source.eligible AND source.status='pending'),
		 count(*) FILTER(WHERE source.eligible AND source.status='ambiguous'),count(*) FILTER(WHERE source.eligible AND source.status='failed'),
		 count(*) FILTER(WHERE source.eligible AND source.status='uncategorized'),
		 min(source.pending_at) FILTER(WHERE source.eligible AND source.status='pending'),
		 count(*) FILTER(WHERE source.eligible AND source.equivalence_status='pending'),
		 count(*) FILTER(WHERE source.eligible AND source.equivalence_status='completed'),
		 count(*) FILTER(WHERE source.eligible AND source.equivalence_status='failed')`+maintenanceActiveSourceJoin).Row().Scan(&result.Counts.Eligible, &result.Counts.Organized, &result.Counts.Pending, &result.Counts.Ambiguous, &result.Counts.Failed, &result.Counts.Uncategorized, &result.OldestPendingAt, &result.Equivalence.Pending, &result.Equivalence.Completed, &result.Equivalence.Failed); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT NOT EXISTS(SELECT 1 FROM teams AS team JOIN memory_spaces AS space ON space.team_id=team.id AND space.kind='team_shared' AND space.lifecycle_state='active'
		 LEFT JOIN topic_maintenance_teams AS state ON state.team_id=team.id AND state.shared_space_id=space.id AND state.space_generation=space.generation
		 WHERE team.status='active' AND team.deleted_at IS NULL AND (state.team_id IS NULL OR state.discovery_kind<1 OR EXISTS(
		 SELECT 1 FROM topic_maintenance_markers AS marker WHERE marker.team_id=team.id AND marker.shared_space_id=space.id AND marker.space_generation=space.generation)))`).Row().Scan(&result.DiscoveryComplete); err != nil {
			return err
		}
		if err := tx.Raw(`WITH active_topics AS (
			SELECT current.team_id,current.shared_space_id,current.space_generation,current.topic_id,
				current.definition_version,current.placement,current.manager_authored
			FROM topics AS current JOIN teams AS team ON team.id=current.team_id AND team.status='active' AND team.deleted_at IS NULL
			JOIN memory_spaces AS space ON space.team_id=current.team_id AND space.id=current.shared_space_id
				AND space.generation=current.space_generation AND space.lifecycle_state='active' AND space.kind='team_shared'
			WHERE NOT current.retired
		), evaluated AS (
			SELECT current.placement,latest.status AS latest_status,
				CASE WHEN current.placement='declared' OR latest.status='failed' THEN `+communityEligible+` ELSE false END AS eligible
			FROM active_topics AS current `+latestCommunityOutcome+`
		)
		SELECT count(*) FILTER (WHERE placement='placed'),
			count(*) FILTER (WHERE placement='standalone'),
			count(*) FILTER (WHERE placement='declared' AND latest_status IS DISTINCT FROM 'failed' AND eligible),
			count(*) FILTER (WHERE latest_status='failed' AND eligible)
		FROM evaluated`).Row().Scan(&result.Communities.Placed, &result.Communities.Standalone, &result.Communities.Pending, &result.Communities.Failed); err != nil {
			return err
		}
		result.CoverageComplete = result.DiscoveryComplete && result.Counts.Pending == 0 && result.Counts.Failed == 0 && result.Counts.Ambiguous == 0 && result.Communities.Pending == 0 && result.Communities.Failed == 0 && result.Equivalence.Pending == 0 && result.Equivalence.Failed == 0
		result.Window, err = currentMaintenanceWindow(tx, now)
		if err != nil {
			return err
		}
		result.ActiveWork, err = activeMaintenanceWork(tx, now)
		if err != nil {
			return err
		}
		windowID := ""
		if result.Window != nil {
			windowID = result.Window.ID
		}
		windowUsage, err := maintenanceUsageSummary(tx, windowID, &result.Usage, &result.HistoricalUsage)
		if err != nil {
			return err
		}
		if result.Window != nil {
			result.Window.Usage = windowUsage
		}
		result.CoverageComplete = result.CoverageComplete && result.ActiveWork == 0 && result.Usage.UnreconciledAttempts == 0

		run, err := scanMaintenanceRun(tx.Raw(`SELECT ` + maintenanceRunColumns + ` FROM topic_maintenance_runs ORDER BY created_at DESC,run_id DESC LIMIT 1`).Row())
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

func maintenanceUsageSummary(tx *gorm.DB, windowID string, overall, historical *topic.MaintenanceUsage) (topic.MaintenanceUsage, error) {
	var windowUsage topic.MaintenanceUsage
	const unavailablePred = `reconciled_at IS NOT NULL AND COALESCE(reported_input,0)=0 AND COALESCE(reported_output,0)=0 AND COALESCE(reported_total,0)=0`
	const partialPred = `reconciled_at IS NOT NULL AND (COALESCE(reported_input,0)>0 OR COALESCE(reported_output,0)>0 OR COALESCE(reported_total,0)>0) AND (COALESCE(reported_input,0)=0 OR COALESCE(reported_output,0)=0)`
	const histPred = `estimated_input_tokens IS NULL`
	const winPred = `(?<>'' AND window_id=NULLIF(?,'')::uuid)`
	err := tx.Raw(`SELECT
		count(*),
		count(*) FILTER(WHERE reconciled_at IS NULL),
		count(*) FILTER(WHERE `+unavailablePred+`),
		count(*) FILTER(WHERE `+partialPred+`),
		COALESCE(sum(reported_input),0),
		COALESCE(sum(reported_output),0),
		COALESCE(sum(reported_total),0),
		count(*) FILTER(WHERE `+histPred+`),
		count(*) FILTER(WHERE `+histPred+` AND reconciled_at IS NULL),
		count(*) FILTER(WHERE `+histPred+` AND `+unavailablePred+`),
		count(*) FILTER(WHERE `+histPred+` AND `+partialPred+`),
		COALESCE(sum(reported_input) FILTER(WHERE `+histPred+`),0),
		COALESCE(sum(reported_output) FILTER(WHERE `+histPred+`),0),
		COALESCE(sum(reported_total) FILTER(WHERE `+histPred+`),0),
		count(*) FILTER(WHERE `+winPred+`),
		count(*) FILTER(WHERE `+winPred+` AND reconciled_at IS NULL),
		count(*) FILTER(WHERE `+winPred+` AND `+unavailablePred+`),
		count(*) FILTER(WHERE `+winPred+` AND `+partialPred+`),
		COALESCE(sum(reported_input) FILTER(WHERE `+winPred+`),0),
		COALESCE(sum(reported_output) FILTER(WHERE `+winPred+`),0),
		COALESCE(sum(reported_total) FILTER(WHERE `+winPred+`),0)
		FROM topic_maintenance_attempts`,
		windowID, windowID, windowID, windowID, windowID, windowID, windowID, windowID,
		windowID, windowID, windowID, windowID, windowID, windowID).Row().Scan(
		&overall.Attempts, &overall.UnreconciledAttempts, &overall.UnavailableUsageAttempts, &overall.PartialUsageAttempts,
		&overall.ReportedInputTokens, &overall.ReportedOutputTokens, &overall.ReportedTotalTokens,
		&historical.Attempts, &historical.UnreconciledAttempts, &historical.UnavailableUsageAttempts, &historical.PartialUsageAttempts,
		&historical.ReportedInputTokens, &historical.ReportedOutputTokens, &historical.ReportedTotalTokens,
		&windowUsage.Attempts, &windowUsage.UnreconciledAttempts, &windowUsage.UnavailableUsageAttempts, &windowUsage.PartialUsageAttempts,
		&windowUsage.ReportedInputTokens, &windowUsage.ReportedOutputTokens, &windowUsage.ReportedTotalTokens,
	)
	return windowUsage, err
}

func (s *Store) ListMaintenanceRuns(ctx context.Context, cursor string, limit int) (topic.MaintenanceRunPage, error) {
	page := topic.MaintenanceRunPage{Runs: []topic.MaintenanceRun{}}
	if limit < 1 || limit > topic.MaxPageSize {
		return page, topic.ErrInvalid
	}
	if cursor != "" {
		if _, err := uuid.Parse(cursor); err != nil {
			return page, topic.ErrInvalid
		}
	}
	err := s.readMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		var after sql.NullTime
		if cursor != "" {
			err := tx.Raw(`SELECT created_at FROM topic_maintenance_runs WHERE run_id=?::uuid`, cursor).Row().Scan(&after)
			if errors.Is(err, sql.ErrNoRows) {
				return topic.ErrInvalid
			}
			if err != nil {
				return err
			}
		}
		rows, err := tx.Raw(`SELECT `+maintenanceRunColumns+` FROM topic_maintenance_runs WHERE (?::timestamptz IS NULL OR (created_at,run_id)<(?::timestamptz,NULLIF(?,'')::uuid)) ORDER BY created_at DESC,run_id DESC LIMIT ?`, after, after, cursor, limit+1).Rows()
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
