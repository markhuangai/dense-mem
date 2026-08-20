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

const (
	reconciliationBatchLimit = 500
)

var _ EmbeddingReconciliationRepository = (*SearchRepositoryImpl)(nil)

func (r *SearchRepositoryImpl) GetEmbeddingReconciliationTime(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw(`SELECT clock_timestamp()`).Scan(&now).Error
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("search: reconciliation clock: %w", err)
	}
	return now.UTC(), nil
}

// CheckSearchConvergence returns an error when the active search contract has
// any queued, processing, or failed jobs.
// It intentionally uses bounded existence checks for health probes instead of
// building the full operator convergence projection.
func (r *SearchRepositoryImpl) CheckSearchConvergence(ctx context.Context) error {
	contract, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return err
	}
	var attentionRequired bool
	err = r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw(`
			SELECT EXISTS (
			    SELECT 1
			    FROM embedding_jobs AS job
			    JOIN teams AS team
			      ON team.id = job.team_id
			     AND team.status = 'active'
			     AND team.deleted_at IS NULL
			    WHERE job.embedding_contract_id = ?::uuid
			      AND job.embedding_dimensions = ?
			      AND job.status IN ('queued', 'processing', 'failed')
				)
			`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Scan(&attentionRequired).Error
	})
	if err != nil {
		return fmt.Errorf("search: convergence health: %w", err)
	}
	if attentionRequired {
		return ErrSearchConvergenceAttentionRequired
	}
	return nil
}

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
	if input.EmbeddingDimensions > 0 && input.EmbeddingDimensions != contract.EmbeddingDimensions {
		return nil, fmt.Errorf("%w: requested convergence dimensions are not active", ErrSearchContractMismatch)
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
	var hasFailedGroup, hasRecoveringGroup bool
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
		groups, groupCount, failedGroup, recoveringGroup, groupsTruncated, err := readSearchConvergenceFailureGroups(ctx, tx, contract.EmbeddingContractID, contract.EmbeddingDimensions)
		if err != nil {
			return err
		}
		convergence.FailureGroups = groups
		convergence.FailureGroupCount = groupCount
		convergence.FailureGroupsTruncated = groupsTruncated
		hasFailedGroup = failedGroup
		hasRecoveringGroup = recoveringGroup
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("search: convergence projection: %w", err)
	}
	convergence.LatestRun, err = r.latestEmbeddingReconciliationRun(ctx, contract.EmbeddingContractID, contract.EmbeddingDimensions)
	if err != nil {
		return nil, err
	}
	if convergence.Failed > 0 || hasFailedGroup {
		convergence.Status = "attention_required"
	} else if convergence.Queued > 0 || convergence.Processing > 0 || hasRecoveringGroup {
		convergence.Status = "recovering"
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
		if input.CreateIfMissing {
			if err := tx.WithContext(ctx).Exec(`
				INSERT INTO embedding_reconciliation_runs (
				embedding_contract_id, embedding_dimensions, local_run_date,
				candidate_cutoff, status
			)
			SELECT ?::uuid, ?, ?, statement_timestamp(), 'reserved'
			WHERE EXISTS (
				SELECT 1
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
				 AND document.space_id = job.space_id
				 AND document.space_generation = job.space_generation
				JOIN teams AS team ON team.id = job.team_id
				 AND team.status = 'active' AND team.deleted_at IS NULL
				WHERE job.status = 'failed'
				  AND job.failure_class <> 'permanent'
				  AND job.embedding_contract_id = ?::uuid
				  AND job.embedding_dimensions = ?
				  AND COALESCE(job.last_failed_at, job.updated_at) <= statement_timestamp()
				  AND document.search_state = 'failed'
			)
			ON CONFLICT (embedding_contract_id, embedding_dimensions, local_run_date) DO NOTHING
				`, input.EmbeddingContractID, input.EmbeddingDimensions, localRunDate,
				input.EmbeddingContractID, input.EmbeddingDimensions).Error; err != nil {
				return err
			}
		}
		var databaseNow time.Time
		if err := tx.WithContext(ctx).Raw(`SELECT clock_timestamp()`).Scan(&databaseNow).Error; err != nil {
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
		if errors.Is(err, sql.ErrNoRows) {
			run = nil
			return nil
		}
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
		if run.Status == string(domain.EmbeddingReconciliationCompleted) || run.Status == string(domain.EmbeddingReconciliationDeferred) || run.Status == string(domain.EmbeddingReconciliationFailed) || run.Status == string(domain.EmbeddingReconciliationAmbiguous) {
			return nil
		}
		if run.LeaseUntil != nil && run.LeaseUntil.After(databaseNow) {
			return nil
		}
		newToken := uuid.New()
		leaseUntilValue := databaseNow.Add(input.Lease)
		result := tx.WithContext(ctx).Exec(`
			UPDATE embedding_reconciliation_runs
			SET status = 'running', worker_id = ?, lease_token = ?::uuid,
			    lease_until = ?, started_at = COALESCE(started_at, ?),
			    updated_at = now()
			WHERE reconciliation_run_id = ?::uuid
		`, input.WorkerID, newToken.String(), leaseUntilValue, databaseNow, run.RunID)
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
		if run.StartedAt == nil {
			run.StartedAt = &databaseNow
		}
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
			       job.space_id::text, job.space_generation,
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
				 AND document.space_id = job.space_id
				 AND document.space_generation = job.space_generation
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
			&value.OwnerProfileID, &value.SpaceID, &value.SpaceGeneration, &value.SourceKind, &value.SourceID,
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
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		var databaseNow time.Time
		if err := tx.WithContext(ctx).Raw(`SELECT clock_timestamp()`).Scan(&databaseNow).Error; err != nil {
			return err
		}
		leaseUntil := databaseNow.Add(input.Lease)
		result := tx.WithContext(ctx).Exec(`
			UPDATE embedding_jobs
			SET status = 'processing', attempts = 1,
			    total_attempts = total_attempts + 1,
			    worker_id = ?, lease_until = ?, completed_at = NULL,
			    error = '',
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND embedding_job_id = ?::uuid
			  AND status = 'failed'
		`, EmbeddingReconciliationWorkerIDPrefix+input.RunID, leaseUntil, input.TeamID, input.CanaryJobID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrEmbeddingReconciliationCanarySkipped
		}
		result = tx.WithContext(ctx).Exec(`
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
		`, input.CanaryJobID, databaseNow, leaseUntil, input.RunID, input.WorkerID, input.LeaseToken)
		if result.Error != nil || result.RowsAffected != 1 {
			if result.Error != nil {
				return result.Error
			}
			return errors.New("reconciliation canary marker already claimed or lease lost")
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
			SET canary_outcome = ?, canary_failure_class = ?, canary_failure_code = ?,
			    recovered_count = ?, updated_at = now()
			WHERE reconciliation_run_id = ?::uuid
			  AND status = 'running' AND worker_id = ? AND lease_token = ?::uuid
			  AND lease_until > clock_timestamp()
		`, outcome, input.FailureClass, input.FailureCode, input.RecoveredCount,
			input.RunID, input.WorkerID, input.LeaseToken)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("reconciliation canary lease lost")
		}
		return nil
	})
}

func (r *SearchRepositoryImpl) ResetEmbeddingReconciliationCanary(ctx context.Context, input ResetEmbeddingReconciliationCanaryInput) error {
	input = normalizeResetEmbeddingReconciliationCanaryInput(input)
	if err := validateResetEmbeddingReconciliationCanaryInput(input); err != nil {
		return err
	}
	return r.withSystemTx(ctx, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			UPDATE embedding_reconciliation_runs
			SET canary_job_id = NULL,
			    canary_attempted_at = NULL,
			    canary_outcome = '',
			    canary_failure_class = '',
			    canary_failure_code = '',
			    updated_at = now()
			WHERE reconciliation_run_id = ?::uuid
			  AND canary_job_id = ?::uuid
			  AND status = 'running'
			  AND worker_id = ?
			  AND lease_token = ?::uuid
			  AND lease_until > clock_timestamp()
			  AND canary_attempted_at IS NOT NULL
		`, input.RunID, input.CanaryJobID, input.WorkerID, input.LeaseToken)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("reconciliation canary lease lost")
		}
		return nil
	})
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
			    completed_at = clock_timestamp(), lease_until = NULL, updated_at = now()
			WHERE reconciliation_run_id = ?::uuid
			  AND status = 'running' AND worker_id = ? AND lease_token = ?::uuid
			  AND lease_until > clock_timestamp()
		`, input.Status, input.CanaryOutcome, input.FailureClass, input.FailureCode,
			input.RequeuedCount, input.RecoveredCount, input.LastError,
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
			ORDER BY created_at DESC, reconciliation_run_id DESC LIMIT 1
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
	input.TeamID = strings.TrimSpace(input.TeamID)
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
	for name, value := range map[string]string{"team_id": input.TeamID, "run_id": input.RunID, "canary_job_id": input.CanaryJobID, "lease_token": input.LeaseToken} {
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

func normalizeResetEmbeddingReconciliationCanaryInput(input ResetEmbeddingReconciliationCanaryInput) ResetEmbeddingReconciliationCanaryInput {
	input.RunID = strings.TrimSpace(input.RunID)
	input.CanaryJobID = strings.TrimSpace(input.CanaryJobID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
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
	if input.RecoveredCount < 0 {
		return errors.New("recovered_count must not be negative")
	}
	if input.Succeeded {
		if input.FailureClass != "" || input.FailureCode != "" {
			return errors.New("successful canary must not include failure metadata")
		}
	} else if !domain.EmbeddingFailureContractValid(input.FailureClass, input.FailureCode) {
		return errors.New("failed canary requires a valid failure class and code")
	}
	return nil
}

func validateResetEmbeddingReconciliationCanaryInput(input ResetEmbeddingReconciliationCanaryInput) error {
	for name, value := range map[string]string{
		"run_id": input.RunID, "canary_job_id": input.CanaryJobID, "lease_token": input.LeaseToken,
	} {
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
		if domain.EmbeddingFailureCodeValid(failureCode) {
			return "reconciliation failed: " + failureCode
		}
		return "reconciliation operation failed"
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
	if input.WorkerID == "" || input.Status == "" {
		return errors.New("worker_id and status are required")
	}
	if input.FailureClass != "" || input.FailureCode != "" {
		if !domain.EmbeddingFailureContractValid(input.FailureClass, input.FailureCode) {
			return errors.New("reconciliation run requires a valid failure class and code")
		}
	}
	return nil
}
