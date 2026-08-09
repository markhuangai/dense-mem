package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const reconciliationBatchLimit = 500

var _ EmbeddingReconciliationRepository = (*SearchRepositoryImpl)(nil)

func (r *SearchRepositoryImpl) GetSearchConvergence(ctx context.Context, input SearchConvergenceInput) (*SearchConvergence, error) {
	input = normalizeSearchConvergenceInput(input)
	if err := validateSearchConvergenceInput(input); err != nil {
		return nil, err
	}
	contract, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	if input.EmbeddingContractID != "" && input.EmbeddingContractID != contract.EmbeddingContractID {
		return nil, fmt.Errorf("%w: requested convergence contract is not active", ErrSearchContractMismatch)
	}
	stats, err := r.GetEmbeddingQueueStats(ctx, EmbeddingQueueStatsInput{
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions,
		ActiveTeamsOnly:     true,
	})
	if err != nil {
		return nil, err
	}
	convergence := &SearchConvergence{
		ObservedAt:       time.Now().UTC(),
		Status:           "converged",
		Contract:         contract,
		Queued:           stats.Queued,
		Processing:       stats.Processing,
		Failed:           stats.Failed,
		ExpiredLeases:    stats.ExpiredLeases,
		OldestPendingAge: stats.OldestPendingAge,
	}
	err = r.withSystemTx(ctx, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT source_kind, failure_class, failure_code, count(*)
			FROM embedding_jobs AS job
			JOIN teams AS team
			  ON team.id = job.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE job.embedding_contract_id = ?::uuid
			  AND job.embedding_dimensions = ?
			  AND job.status = 'failed'
			GROUP BY source_kind, failure_class, failure_code
			ORDER BY source_kind, failure_class, failure_code
		`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item EmbeddingFailureCount
			if err := rows.Scan(&item.SourceKind, &item.FailureClass, &item.FailureCode, &item.Count); err != nil {
				return err
			}
			convergence.Failures = append(convergence.Failures, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		var oldestFailureSeconds float64
		if err := tx.WithContext(ctx).Raw(`
			SELECT COALESCE(EXTRACT(EPOCH FROM (clock_timestamp() - min(COALESCE(job.last_failed_at, job.updated_at)))), 0)
			FROM embedding_jobs AS job
			JOIN teams AS team
			  ON team.id = job.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE job.embedding_contract_id = ?::uuid
			  AND job.embedding_dimensions = ?
			  AND job.status = 'failed'
		`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Scan(&oldestFailureSeconds).Error; err != nil {
			return err
		}
		convergence.OldestFailureAge = time.Duration(oldestFailureSeconds * float64(time.Second))
		if err := tx.WithContext(ctx).Raw(`
			SELECT count(DISTINCT job.team_id)
			FROM embedding_jobs AS job
			JOIN teams AS team
			  ON team.id = job.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE job.embedding_contract_id = ?::uuid
			  AND job.embedding_dimensions = ?
			  AND job.status = 'failed'
		`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Scan(&convergence.AffectedTeamCount).Error; err != nil {
			return err
		}
		rows, err = tx.WithContext(ctx).Raw(`
			SELECT incident.incident_id::text, incident.team_id::text,
			       COALESCE(team.name, ''), incident.embedding_contract_id::text,
			       incident.embedding_dimensions, incident.source_kind,
			       incident.failure_class, incident.failure_code, incident.status,
			       incident.affected_job_count, incident.first_seen_at,
			       incident.last_seen_at, incident.recovering_at, incident.resolved_at
			FROM embedding_failure_incidents AS incident
			JOIN teams AS team
			  ON team.id = incident.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE incident.embedding_contract_id = ?::uuid
			  AND incident.embedding_dimensions = ?
			  AND incident.status IN ('open', 'recovering')
			ORDER BY incident.last_seen_at DESC, incident.incident_id ASC
		`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item EmbeddingFailureIncident
			var recoveringAt, resolvedAt sql.NullTime
			if err := rows.Scan(
				&item.IncidentID, &item.TeamID, &item.TeamName,
				&item.EmbeddingContractID, &item.EmbeddingDimensions,
				&item.SourceKind, &item.FailureClass, &item.FailureCode,
				&item.Status, &item.AffectedJobCount, &item.FirstSeenAt,
				&item.LastSeenAt, &recoveringAt, &resolvedAt,
			); err != nil {
				return err
			}
			if recoveringAt.Valid {
				value := recoveringAt.Time.UTC()
				item.RecoveringAt = &value
			}
			if resolvedAt.Valid {
				value := resolvedAt.Time.UTC()
				item.ResolvedAt = &value
			}
			item.Age = time.Since(item.LastSeenAt)
			item.Guidance = embeddingFailureGuidance(item.FailureCode)
			convergence.Incidents = append(convergence.Incidents, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("search: convergence projection: %w", err)
	}
	convergence.LatestRun, err = r.latestEmbeddingReconciliationRun(ctx, contract.EmbeddingContractID, contract.EmbeddingDimensions)
	if err != nil {
		return nil, err
	}
	if convergence.Queued > 0 || convergence.Processing > 0 || convergence.Failed > 0 || len(convergence.Incidents) > 0 {
		convergence.Status = "attention_required"
		if convergence.Queued > 0 || convergence.Processing > 0 || hasRecoveringIncident(convergence.Incidents) {
			convergence.Status = "recovering"
		}
	}
	return convergence, nil
}

func (r *SearchRepositoryImpl) ReserveEmbeddingReconciliationRun(ctx context.Context, input ReserveEmbeddingReconciliationRunInput) (*EmbeddingReconciliationRun, bool, error) {
	input = normalizeReserveEmbeddingReconciliationRunInput(input)
	if err := validateReserveEmbeddingReconciliationRunInput(input); err != nil {
		return nil, false, err
	}
	run := &EmbeddingReconciliationRun{}
	claimed := false
	localRunDate := reconciliationLocalDate(input.LocalRunDate)
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO embedding_reconciliation_runs (
				embedding_contract_id, embedding_dimensions, local_run_date,
				candidate_cutoff, status
			) VALUES (?::uuid, ?, ?, ?, 'reserved')
			ON CONFLICT (embedding_contract_id, embedding_dimensions, local_run_date) DO NOTHING
		`, input.EmbeddingContractID, input.EmbeddingDimensions, localRunDate, input.Now).Error; err != nil {
			return err
		}
		var leaseUntil sql.NullTime
		var leaseToken sql.NullString
		var canaryJobID sql.NullString
		var startedAt, completedAt sql.NullTime
		var localDate time.Time
		err := tx.WithContext(ctx).Raw(`
			SELECT reconciliation_run_id::text, embedding_contract_id::text,
			       embedding_dimensions, local_run_date, status, candidate_cutoff,
			       worker_id, lease_token::text, lease_until, canary_job_id::text,
			       canary_attempted_at, canary_outcome, canary_failure_class,
			       canary_failure_code, requeued_count, recovered_count,
			       last_error, started_at, completed_at, updated_at
			FROM embedding_reconciliation_runs
			WHERE embedding_contract_id = ?::uuid
			  AND embedding_dimensions = ?
			  AND local_run_date = ?
			FOR UPDATE
		`, input.EmbeddingContractID, input.EmbeddingDimensions, localRunDate).Row().Scan(
			&run.RunID, &run.EmbeddingContractID, &run.EmbeddingDimensions,
			&localDate, &run.Status, &run.CandidateCutoff, &run.WorkerID,
			&leaseToken, &leaseUntil, &canaryJobID, &run.CanaryAttemptedAt,
			&run.CanaryOutcome, &run.CanaryFailureClass, &run.CanaryFailureCode,
			&run.RequeuedCount, &run.RecoveredCount, &run.LastError,
			&startedAt, &completedAt, &run.UpdatedAt,
		)
		if err != nil {
			return err
		}
		run.LocalRunDate = localDate
		if leaseToken.Valid {
			run.LeaseToken = leaseToken.String
		}
		if canaryJobID.Valid {
			run.CanaryJobID = canaryJobID.String
		}
		if leaseUntil.Valid {
			value := leaseUntil.Time.UTC()
			run.LeaseUntil = &value
		}
		if startedAt.Valid {
			value := startedAt.Time.UTC()
			run.StartedAt = &value
		}
		if completedAt.Valid {
			value := completedAt.Time.UTC()
			run.CompletedAt = &value
		}
		if run.Status == string(domain.EmbeddingReconciliationCompleted) || run.Status == string(domain.EmbeddingReconciliationDeferred) || run.Status == string(domain.EmbeddingReconciliationAmbiguous) {
			return nil
		}
		if run.LeaseUntil != nil && run.LeaseUntil.After(input.Now) {
			return nil
		}
		newToken := uuid.New()
		leaseUntilValue := input.Now.Add(input.Lease)
		result := tx.WithContext(ctx).Exec(`
			UPDATE embedding_reconciliation_runs
			SET status = 'running', worker_id = ?, lease_token = ?::uuid,
			    lease_until = ?, started_at = COALESCE(started_at, ?),
			    updated_at = now()
			WHERE reconciliation_run_id = ?::uuid
		`, input.WorkerID, newToken.String(), leaseUntilValue, input.Now, run.RunID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("reconciliation run lease lost")
		}
		run.Status = string(domain.EmbeddingReconciliationRunning)
		run.WorkerID = input.WorkerID
		run.LeaseToken = newToken.String()
		run.LeaseUntil = &leaseUntilValue
		claimed = true
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("search: reserve reconciliation run: %w", err)
	}
	return run, claimed, nil
}

func (r *SearchRepositoryImpl) SelectEmbeddingReconciliationCanary(ctx context.Context, input SelectEmbeddingReconciliationCanaryInput) (*EmbeddingJob, error) {
	input = normalizeSelectEmbeddingReconciliationCanaryInput(input)
	if err := validateSelectEmbeddingReconciliationCanaryInput(input); err != nil {
		return nil, err
	}
	var job *EmbeddingJob
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		var value EmbeddingJob
		err := tx.WithContext(ctx).Raw(`
			SELECT job.team_id::text, job.embedding_job_id::text,
			       job.search_document_id::text, job.owner_profile_id::text,
			       job.source_kind, job.source_id::text, job.source_version,
			       job.projection_format_version, COALESCE(job.projection_generation_id::text, ''),
			       job.document_version, job.embedding_contract_id::text,
			       job.embedding_dimensions, job.status, job.attempts,
			       job.total_attempts, job.recovery_count, job.failure_class,
			       job.failure_code, job.first_failed_at, job.last_failed_at,
			       job.lease_until, document.document_text
			FROM embedding_jobs AS job
			JOIN search_documents AS document
			  ON document.team_id = job.team_id
			 AND document.search_document_id = job.search_document_id
			 AND document.source_version = job.source_version
			 AND document.projection_format_version = job.projection_format_version
			 AND document.projection_generation_id IS NOT DISTINCT FROM job.projection_generation_id
			 AND document.document_version = job.document_version
			 AND document.embedding_contract_id = job.embedding_contract_id
			 AND document.embedding_dimensions = job.embedding_dimensions
		JOIN teams AS team ON team.id = job.team_id AND team.status = 'active' AND team.deleted_at IS NULL
		JOIN embedding_reconciliation_runs AS run
		  ON run.reconciliation_run_id = ?::uuid
		 AND run.embedding_contract_id = job.embedding_contract_id
		 AND run.embedding_dimensions = job.embedding_dimensions
		 AND run.status = 'running'
		 AND run.canary_attempted_at IS NULL
		WHERE job.status = 'failed'
		  AND job.failure_class <> 'permanent'
		  AND job.embedding_contract_id = ?::uuid
		  AND job.embedding_dimensions = ?
		  AND COALESCE(job.last_failed_at, job.updated_at) <= ?
		  AND document.search_state = 'failed'
		ORDER BY COALESCE(job.last_failed_at, job.updated_at), job.embedding_job_id
		LIMIT 1
		`, input.RunID, input.EmbeddingContractID, input.EmbeddingDimensions, input.CandidateCutoff).Row().Scan(
			&value.TeamID, &value.EmbeddingJobID, &value.SearchDocumentID,
			&value.OwnerProfileID, &value.SourceKind, &value.SourceID,
			&value.SourceVersion, &value.ProjectionFormat,
			&value.ProjectionGenerationID, &value.DocumentVersion,
			&value.EmbeddingContractID, &value.EmbeddingDimensions, &value.Status,
			&value.Attempts, &value.TotalAttempts, &value.RecoveryCount,
			&value.FailureClass, &value.FailureCode, &value.FirstFailedAt,
			&value.LastFailedAt, &value.LeaseUntil, &value.DocumentText,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		job = &value
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("search: select reconciliation canary: %w", err)
	}
	return job, nil
}

func (r *SearchRepositoryImpl) MarkEmbeddingReconciliationCanaryAttempt(ctx context.Context, input MarkEmbeddingReconciliationCanaryAttemptInput) error {
	input = normalizeMarkEmbeddingReconciliationCanaryAttemptInput(input)
	if err := validateMarkEmbeddingReconciliationCanaryAttemptInput(input); err != nil {
		return err
	}
	leaseUntil := input.AttemptedAt.Add(input.Lease)
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			UPDATE embedding_reconciliation_runs AS run
			SET canary_job_id = ?::uuid,
			    canary_attempted_at = ?,
			    lease_until = ?,
			    updated_at = now()
			WHERE run.reconciliation_run_id = ?::uuid
			  AND run.status = 'running'
			  AND run.worker_id = ?
			  AND run.lease_token = ?::uuid
			  AND run.lease_until > clock_timestamp()
			  AND run.canary_attempted_at IS NULL
		`, input.CanaryJobID, input.AttemptedAt, leaseUntil, input.RunID, input.WorkerID, input.LeaseToken)
		if result.Error != nil || result.RowsAffected != 1 {
			if result.Error != nil {
				return result.Error
			}
			return errors.New("reconciliation canary marker already claimed or lease lost")
		}
		result = tx.WithContext(ctx).Exec(`
			UPDATE embedding_jobs
			SET status = 'processing', attempts = 1,
			    total_attempts = total_attempts + 1,
			    worker_id = ?, lease_until = ?, completed_at = NULL,
			    error = '',
			    updated_at = now()
			WHERE embedding_job_id = ?::uuid
			  AND status = 'failed'
		`, EmbeddingReconciliationWorkerIDPrefix+input.RunID, leaseUntil, input.CanaryJobID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("reconciliation canary job was already claimed")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("search: mark reconciliation canary: %w", err)
	}
	return nil
}

func (r *SearchRepositoryImpl) CompleteEmbeddingReconciliationCanary(ctx context.Context, input CompleteEmbeddingReconciliationCanaryInput) error {
	input = normalizeCompleteEmbeddingReconciliationCanaryInput(input)
	if err := validateCompleteEmbeddingReconciliationCanaryInput(input); err != nil {
		return err
	}
	outcome := "failed"
	if input.Succeeded {
		outcome = "succeeded"
	}
	return r.withSystemTx(ctx, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			UPDATE embedding_reconciliation_runs
			SET canary_outcome = ?, canary_failure_class = ?, canary_failure_code = ?, updated_at = now()
			WHERE reconciliation_run_id = ?::uuid
			  AND status = 'running' AND worker_id = ? AND lease_token = ?::uuid
		`, outcome, input.FailureClass, input.FailureCode, input.RunID, input.WorkerID, input.LeaseToken)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("reconciliation canary lease lost")
		}
		return nil
	})
}

func (r *SearchRepositoryImpl) RequeueEmbeddingReconciliationJobs(ctx context.Context, input RequeueEmbeddingReconciliationJobsInput) (int64, error) {
	input = normalizeRequeueEmbeddingReconciliationJobsInput(input)
	if err := validateRequeueEmbeddingReconciliationJobsInput(input); err != nil {
		return 0, err
	}
	var total int64
	for {
		count, err := r.requeueEmbeddingReconciliationBatch(ctx, input)
		if err != nil {
			return total, fmt.Errorf("search: requeue reconciliation jobs: %w", err)
		}
		if count == 0 {
			break
		}
		total += count
		if count < int64(input.BatchSize) {
			break
		}
	}
	return total, nil
}

func (r *SearchRepositoryImpl) requeueEmbeddingReconciliationBatch(ctx context.Context, input RequeueEmbeddingReconciliationJobsInput) (int64, error) {
	var count int64
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := renewEmbeddingReconciliationLease(ctx, tx, input); err != nil {
			return err
		}
		return tx.WithContext(ctx).Raw(`
				WITH candidates AS MATERIALIZED (
					SELECT job.team_id, job.embedding_job_id, job.search_document_id,
					       job.source_version, job.projection_format_version,
					       job.projection_generation_id, job.document_version,
					       job.embedding_contract_id, job.embedding_dimensions
					FROM embedding_jobs AS job
					JOIN search_documents AS document
					  ON document.team_id = job.team_id
					 AND document.search_document_id = job.search_document_id
					 AND document.source_version = job.source_version
					 AND document.projection_format_version = job.projection_format_version
					 AND document.projection_generation_id IS NOT DISTINCT FROM job.projection_generation_id
					 AND document.document_version = job.document_version
					 AND document.embedding_contract_id = job.embedding_contract_id
					 AND document.embedding_dimensions = job.embedding_dimensions
					JOIN teams AS team ON team.id = job.team_id AND team.status = 'active' AND team.deleted_at IS NULL
					JOIN embedding_reconciliation_runs AS run
					  ON run.reconciliation_run_id = ?::uuid
					 AND run.status = 'running'
					 AND run.worker_id = ?
					 AND run.lease_token = ?::uuid
					WHERE job.status = 'failed'
					  AND job.failure_class <> 'permanent'
					  AND job.embedding_contract_id = ?::uuid
					  AND job.embedding_dimensions = ?
					  AND COALESCE(job.last_failed_at, job.updated_at) <= ?
					  AND document.search_state = 'failed'
					ORDER BY COALESCE(job.last_failed_at, job.updated_at), job.embedding_job_id
					LIMIT ?
					FOR UPDATE OF job SKIP LOCKED
				), requeued AS (
					UPDATE embedding_jobs AS job
					SET status = 'queued', attempts = 0, available_at = now(),
					    lease_until = NULL, worker_id = '', completed_at = NULL,
					    error = '', recovery_count = recovery_count + 1,
					    last_recovered_at = now(), updated_at = now()
					FROM candidates
					WHERE job.team_id = candidates.team_id
					  AND job.embedding_job_id = candidates.embedding_job_id
					RETURNING job.team_id, job.search_document_id, job.source_kind,
					          job.failure_class, job.failure_code,
					          job.source_version, job.projection_format_version,
					          job.projection_generation_id, job.document_version,
					          job.embedding_contract_id, job.embedding_dimensions
				)
				, documents AS (
					UPDATE search_documents AS document
				SET search_state = 'pending', embedding_error = '', updated_at = now()
				FROM requeued
				WHERE document.team_id = requeued.team_id
				  AND document.search_document_id = requeued.search_document_id
				  AND document.source_version = requeued.source_version
				  AND document.projection_format_version = requeued.projection_format_version
				  AND document.projection_generation_id IS NOT DISTINCT FROM requeued.projection_generation_id
				  AND document.document_version = requeued.document_version
				  AND document.embedding_contract_id = requeued.embedding_contract_id
				  AND document.embedding_dimensions = requeued.embedding_dimensions
				RETURNING 1
				), incidents AS (
					UPDATE embedding_failure_incidents AS incident
					SET status = 'recovering',
					    recovering_at = COALESCE(incident.recovering_at, now()),
					    last_reconciliation_run_id = ?::uuid,
					    updated_at = now()
					WHERE incident.status = 'open'
					  AND EXISTS (
					      SELECT 1
					      FROM requeued
					      WHERE requeued.team_id = incident.team_id
					        AND requeued.embedding_contract_id = incident.embedding_contract_id
					        AND requeued.embedding_dimensions = incident.embedding_dimensions
					        AND requeued.source_kind = incident.source_kind
					        AND requeued.failure_class = incident.failure_class
					        AND requeued.failure_code = incident.failure_code
					  )
					RETURNING 1
				)
				SELECT count(*) FROM requeued
			`, input.RunID, input.WorkerID, input.LeaseToken, input.EmbeddingContractID, input.EmbeddingDimensions, input.CandidateCutoff, input.BatchSize, input.RunID).Scan(&count).Error
	})
	return count, err
}

func renewEmbeddingReconciliationLease(ctx context.Context, tx *gorm.DB, input RequeueEmbeddingReconciliationJobsInput) error {
	result := tx.WithContext(ctx).Exec(`
		UPDATE embedding_reconciliation_runs
		SET lease_until = clock_timestamp() + (? * interval '1 millisecond'),
		    updated_at = now()
		WHERE reconciliation_run_id = ?::uuid
		  AND status = 'running'
		  AND worker_id = ?
		  AND lease_token = ?::uuid
		  AND lease_until > clock_timestamp()
	`, input.Lease.Milliseconds(), input.RunID, input.WorkerID, input.LeaseToken)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("reconciliation run lease lost")
	}
	return nil
}

func (r *SearchRepositoryImpl) CompleteEmbeddingReconciliationRun(ctx context.Context, input CompleteEmbeddingReconciliationRunInput) error {
	input = normalizeCompleteEmbeddingReconciliationRunInput(input)
	if err := validateCompleteEmbeddingReconciliationRunInput(input); err != nil {
		return err
	}
	return r.withSystemTx(ctx, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			UPDATE embedding_reconciliation_runs
			SET status = ?, canary_outcome = ?, canary_failure_class = ?, canary_failure_code = ?,
			    requeued_count = ?, recovered_count = ?, last_error = ?,
			    completed_at = ?, lease_until = NULL, updated_at = now()
			WHERE reconciliation_run_id = ?::uuid
			  AND status = 'running' AND worker_id = ? AND lease_token = ?::uuid
		`, input.Status, input.CanaryOutcome, input.FailureClass, input.FailureCode,
			input.RequeuedCount, input.RecoveredCount, input.LastError, input.CompletedAt,
			input.RunID, input.WorkerID, input.LeaseToken)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("reconciliation run lease lost")
		}
		return nil
	})
}

func (r *SearchRepositoryImpl) latestEmbeddingReconciliationRun(ctx context.Context, contractID string, dimensions int) (*EmbeddingReconciliationRun, error) {
	var run EmbeddingReconciliationRun
	var localDate time.Time
	var leaseToken, canaryJobID sql.NullString
	var leaseUntil, attemptedAt, startedAt, completedAt sql.NullTime
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw(`
			SELECT reconciliation_run_id::text, embedding_contract_id::text,
			       embedding_dimensions, local_run_date, status, candidate_cutoff,
			       worker_id, lease_token::text, lease_until, canary_job_id::text,
			       canary_attempted_at, canary_outcome, canary_failure_class,
			       canary_failure_code, requeued_count, recovered_count, last_error,
			       started_at, completed_at, updated_at
			FROM embedding_reconciliation_runs
			WHERE embedding_contract_id = ?::uuid AND embedding_dimensions = ?
			ORDER BY local_run_date DESC, created_at DESC LIMIT 1
		`, contractID, dimensions).Row().Scan(
			&run.RunID, &run.EmbeddingContractID, &run.EmbeddingDimensions,
			&localDate, &run.Status, &run.CandidateCutoff, &run.WorkerID,
			&leaseToken, &leaseUntil, &canaryJobID, &attemptedAt,
			&run.CanaryOutcome, &run.CanaryFailureClass, &run.CanaryFailureCode,
			&run.RequeuedCount, &run.RecoveredCount, &run.LastError,
			&startedAt, &completedAt, &run.UpdatedAt,
		)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("search: latest reconciliation run: %w", err)
	}
	run.LocalRunDate = localDate
	if leaseToken.Valid {
		run.LeaseToken = leaseToken.String
	}
	if canaryJobID.Valid {
		run.CanaryJobID = canaryJobID.String
	}
	if leaseUntil.Valid {
		value := leaseUntil.Time.UTC()
		run.LeaseUntil = &value
	}
	if attemptedAt.Valid {
		value := attemptedAt.Time.UTC()
		run.CanaryAttemptedAt = &value
	}
	if startedAt.Valid {
		value := startedAt.Time.UTC()
		run.StartedAt = &value
	}
	if completedAt.Valid {
		value := completedAt.Time.UTC()
		run.CompletedAt = &value
	}
	return &run, nil
}

func hasRecoveringIncident(items []EmbeddingFailureIncident) bool {
	for _, item := range items {
		if item.Status == "recovering" {
			return true
		}
	}
	return false
}

func embeddingFailureGuidance(code string) string {
	switch code {
	case "provider_quota_exhausted":
		return "Add provider credit or repair billing before the next daily canary."
	case "provider_authentication_failed":
		return "Replace the provider credential, then preserve the active model and dimensions."
	case "provider_permission_denied":
		return "Restore provider access or change to a compatible provider endpoint."
	case "provider_contract_rejected", "provider_response_invalid":
		return "Verify provider compatibility while preserving the active model and dimensions."
	case "embedding_input_rejected", "embedding_contract_mismatch", "unknown_embedding_failure":
		return "Inspect application or data compatibility; this failure is blocked from automatic retry."
	default:
		return "Recovery is automatic; inspect the provider and wait for the next daily canary."
	}
}

func normalizeSearchConvergenceInput(input SearchConvergenceInput) SearchConvergenceInput {
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	return input
}

func validateSearchConvergenceInput(input SearchConvergenceInput) error {
	if input.EmbeddingContractID != "" {
		if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
			return fmt.Errorf("embedding_contract_id is invalid: %w", err)
		}
	}
	if input.EmbeddingDimensions < 0 {
		return errors.New("embedding_dimensions must not be negative")
	}
	return nil
}

func normalizeReserveEmbeddingReconciliationRunInput(input ReserveEmbeddingReconciliationRunInput) ReserveEmbeddingReconciliationRunInput {
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	if input.Lease <= 0 {
		input.Lease = 10 * time.Minute
	}
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	}
	return input
}

// reconciliationLocalDate preserves the calendar date in the configured
// scheduler location. Converting it to UTC before extracting the date can
// move an evening local run into the following UTC date.
func reconciliationLocalDate(value time.Time) string {
	return fmt.Sprintf("%04d-%02d-%02d", value.Year(), value.Month(), value.Day())
}

func validateReserveEmbeddingReconciliationRunInput(input ReserveEmbeddingReconciliationRunInput) error {
	if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
		return fmt.Errorf("embedding_contract_id is required: %w", err)
	}
	if input.EmbeddingDimensions <= 0 {
		return errors.New("embedding_dimensions must be positive")
	}
	if input.LocalRunDate.IsZero() {
		return errors.New("local_run_date is required")
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	if input.Lease < time.Second {
		return errors.New("lease must be at least one second")
	}
	return nil
}

func normalizeSelectEmbeddingReconciliationCanaryInput(input SelectEmbeddingReconciliationCanaryInput) SelectEmbeddingReconciliationCanaryInput {
	input.RunID = strings.TrimSpace(input.RunID)
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	return input
}

func validateSelectEmbeddingReconciliationCanaryInput(input SelectEmbeddingReconciliationCanaryInput) error {
	if _, err := uuid.Parse(input.RunID); err != nil {
		return fmt.Errorf("run_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
		return fmt.Errorf("embedding_contract_id is required: %w", err)
	}
	if input.EmbeddingDimensions <= 0 || input.CandidateCutoff.IsZero() {
		return errors.New("reconciliation canary contract and cutoff are required")
	}
	return nil
}

func normalizeMarkEmbeddingReconciliationCanaryAttemptInput(input MarkEmbeddingReconciliationCanaryAttemptInput) MarkEmbeddingReconciliationCanaryAttemptInput {
	input.RunID = strings.TrimSpace(input.RunID)
	input.CanaryJobID = strings.TrimSpace(input.CanaryJobID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	if input.Lease <= 0 {
		input.Lease = 10 * time.Minute
	}
	return input
}

func validateMarkEmbeddingReconciliationCanaryAttemptInput(input MarkEmbeddingReconciliationCanaryAttemptInput) error {
	for name, value := range map[string]string{"run_id": input.RunID, "canary_job_id": input.CanaryJobID, "lease_token": input.LeaseToken} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", name, err)
		}
	}
	if input.WorkerID == "" || input.AttemptedAt.IsZero() || input.Lease < time.Second {
		return errors.New("worker_id, attempted_at, and lease are required")
	}
	return nil
}

func normalizeCompleteEmbeddingReconciliationCanaryInput(input CompleteEmbeddingReconciliationCanaryInput) CompleteEmbeddingReconciliationCanaryInput {
	input.RunID = strings.TrimSpace(input.RunID)
	input.CanaryJobID = strings.TrimSpace(input.CanaryJobID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	input.FailureClass = strings.TrimSpace(input.FailureClass)
	input.FailureCode = strings.TrimSpace(input.FailureCode)
	return input
}

func validateCompleteEmbeddingReconciliationCanaryInput(input CompleteEmbeddingReconciliationCanaryInput) error {
	for name, value := range map[string]string{"run_id": input.RunID, "canary_job_id": input.CanaryJobID, "lease_token": input.LeaseToken} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", name, err)
		}
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	return nil
}

func normalizeRequeueEmbeddingReconciliationJobsInput(input RequeueEmbeddingReconciliationJobsInput) RequeueEmbeddingReconciliationJobsInput {
	input.RunID = strings.TrimSpace(input.RunID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	if input.BatchSize <= 0 || input.BatchSize > reconciliationBatchLimit {
		input.BatchSize = reconciliationBatchLimit
	}
	if input.Lease <= 0 {
		input.Lease = 10 * time.Minute
	}
	return input
}

func validateRequeueEmbeddingReconciliationJobsInput(input RequeueEmbeddingReconciliationJobsInput) error {
	if _, err := uuid.Parse(input.RunID); err != nil {
		return fmt.Errorf("run_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.LeaseToken); err != nil {
		return fmt.Errorf("lease_token is required: %w", err)
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
		return fmt.Errorf("embedding_contract_id is required: %w", err)
	}
	if input.EmbeddingDimensions <= 0 || input.CandidateCutoff.IsZero() {
		return errors.New("reconciliation contract and cutoff are required")
	}
	if input.Lease < time.Second {
		return errors.New("reconciliation lease must be at least one second")
	}
	return nil
}

func normalizeCompleteEmbeddingReconciliationRunInput(input CompleteEmbeddingReconciliationRunInput) CompleteEmbeddingReconciliationRunInput {
	input.RunID = strings.TrimSpace(input.RunID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	input.Status = strings.TrimSpace(input.Status)
	input.CanaryOutcome = strings.TrimSpace(input.CanaryOutcome)
	input.FailureClass = strings.TrimSpace(input.FailureClass)
	input.FailureCode = strings.TrimSpace(input.FailureCode)
	input.LastError = boundedReconciliationError(input.LastError, input.FailureCode)
	return input
}

func boundedReconciliationError(message, failureCode string) string {
	message = strings.Join(strings.Fields(strings.TrimSpace(message)), " ")
	if message == "" {
		return ""
	}
	// Reconciliation messages are operator state, not a provider-error sink.
	// Keep only the closed failure code or a short generic operation marker.
	if failureCode != "" {
		return "reconciliation failed: " + failureCode
	}
	const limit = 128
	if len(message) > limit {
		return "reconciliation operation failed"
	}
	switch message {
	case "canary selection failed",
		"daily embedding canary failure persistence was ambiguous",
		"daily embedding canary completion was ambiguous",
		"reconciliation backlog release failed",
		"daily embedding canary failed":
		return message
	default:
		return "reconciliation operation failed"
	}
}

func validateCompleteEmbeddingReconciliationRunInput(input CompleteEmbeddingReconciliationRunInput) error {
	for name, value := range map[string]string{"run_id": input.RunID, "lease_token": input.LeaseToken} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", name, err)
		}
	}
	if input.WorkerID == "" || input.Status == "" || input.CompletedAt.IsZero() {
		return errors.New("worker_id, status, and completed_at are required")
	}
	return nil
}
