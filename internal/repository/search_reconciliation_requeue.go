package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

const embeddingReconciliationLockedCandidateRetryInterval = time.Second

type embeddingReconciliationCursor struct {
	failedAt time.Time
	jobID    string
}

type embeddingReconciliationCandidate struct {
	teamID   string
	jobID    string
	failedAt time.Time
}

type embeddingReconciliationBatchResult struct {
	requeued  int64
	cursor    *embeddingReconciliationCursor
	remaining bool
}

func (r *SearchRepositoryImpl) RequeueEmbeddingReconciliationJobs(ctx context.Context, input RequeueEmbeddingReconciliationJobsInput) (int64, error) {
	input = normalizeRequeueEmbeddingReconciliationJobsInput(input)
	if err := validateRequeueEmbeddingReconciliationJobsInput(input); err != nil {
		return 0, err
	}
	var total int64
	var cursor *embeddingReconciliationCursor
	for {
		batch, err := r.requeueEmbeddingReconciliationBatch(ctx, input, cursor)
		if err != nil {
			return total, fmt.Errorf("search: requeue reconciliation jobs: %w", err)
		}
		total += batch.requeued
		if batch.cursor != nil {
			cursor = batch.cursor
			continue
		}
		if !batch.remaining {
			break
		}
		cursor = nil
		timer := time.NewTimer(embeddingReconciliationLockedCandidateRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return total, fmt.Errorf("search: requeue reconciliation jobs: %w", ctx.Err())
		case <-timer.C:
		}
	}
	return total, nil
}

func (r *SearchRepositoryImpl) requeueEmbeddingReconciliationBatch(
	ctx context.Context,
	input RequeueEmbeddingReconciliationJobsInput,
	cursor *embeddingReconciliationCursor,
) (embeddingReconciliationBatchResult, error) {
	var batch embeddingReconciliationBatchResult
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := renewEmbeddingReconciliationLease(ctx, tx, input); err != nil {
			return err
		}
		query := `
			SELECT job.team_id::text, job.embedding_job_id::text,
			       COALESCE(job.last_failed_at, job.updated_at) AS failed_at
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
			JOIN teams AS team
			  ON team.id = job.team_id AND team.status = 'active' AND team.deleted_at IS NULL
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
		`
		args := []any{
			input.RunID, input.WorkerID, input.LeaseToken,
			input.EmbeddingContractID, input.EmbeddingDimensions, input.CandidateCutoff,
		}
		if cursor != nil {
			query += `
			  AND (COALESCE(job.last_failed_at, job.updated_at), job.embedding_job_id) > (?::timestamptz, ?::uuid)
			`
			args = append(args, cursor.failedAt, cursor.jobID)
		}
		query += `
			ORDER BY COALESCE(job.last_failed_at, job.updated_at), job.embedding_job_id
			LIMIT ?
			FOR UPDATE OF document SKIP LOCKED
		`
		args = append(args, input.BatchSize)
		rows, err := tx.WithContext(ctx).Raw(query, args...).Rows()
		if err != nil {
			return err
		}
		candidates := make([]embeddingReconciliationCandidate, 0, input.BatchSize)
		for rows.Next() {
			var candidate embeddingReconciliationCandidate
			if err := rows.Scan(&candidate.teamID, &candidate.jobID, &candidate.failedAt); err != nil {
				_ = rows.Close()
				return err
			}
			candidates = append(candidates, candidate)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(candidates) == 0 {
			remaining, err := embeddingReconciliationCandidatesRemain(ctx, tx, input)
			batch.remaining = remaining
			return err
		}
		lastCandidate := candidates[len(candidates)-1]
		batch.cursor = &embeddingReconciliationCursor{failedAt: lastCandidate.failedAt, jobID: lastCandidate.jobID}
		candidateTeamIDs := make([]string, 0, len(candidates))
		candidateJobIDs := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			candidateTeamIDs = append(candidateTeamIDs, candidate.teamID)
			candidateJobIDs = append(candidateJobIDs, candidate.jobID)
		}

		rows, err = tx.WithContext(ctx).Raw(`
			SELECT job.team_id::text, job.embedding_job_id::text,
			       job.source_kind, job.failure_class, job.failure_code,
			       job.embedding_contract_id::text, job.embedding_dimensions
			FROM unnest(?::uuid[], ?::uuid[]) WITH ORDINALITY AS candidate(team_id, embedding_job_id, position)
			JOIN embedding_jobs AS job
			  ON job.team_id = candidate.team_id
			 AND job.embedding_job_id = candidate.embedding_job_id
			JOIN search_documents AS document
			  ON document.team_id = job.team_id
			 AND document.search_document_id = job.search_document_id
			 AND document.source_version = job.source_version
			 AND document.projection_format_version = job.projection_format_version
			 AND document.projection_generation_id IS NOT DISTINCT FROM job.projection_generation_id
			 AND document.document_version = job.document_version
			 AND document.embedding_contract_id = job.embedding_contract_id
			 AND document.embedding_dimensions = job.embedding_dimensions
			WHERE job.status = 'failed'
			  AND job.failure_class <> 'permanent'
			  AND job.embedding_contract_id = ?::uuid
			  AND job.embedding_dimensions = ?
			  AND COALESCE(job.last_failed_at, job.updated_at) <= ?
			  AND document.search_state = 'failed'
			ORDER BY candidate.position
			FOR UPDATE OF job SKIP LOCKED
		`, pq.Array(candidateTeamIDs), pq.Array(candidateJobIDs),
			input.EmbeddingContractID, input.EmbeddingDimensions, input.CandidateCutoff).Rows()
		if err != nil {
			return err
		}
		jobTeamIDs := make([]string, 0, input.BatchSize)
		jobIDs := make([]string, 0, input.BatchSize)
		identitiesByTeam := make(map[string]map[embeddingFailureIncidentIdentity]struct{})
		for rows.Next() {
			var teamID, jobID string
			var identity embeddingFailureIncidentIdentity
			if err := rows.Scan(&teamID, &jobID, &identity.sourceKind, &identity.failureClass,
				&identity.failureCode, &identity.contractID, &identity.dimensions); err != nil {
				_ = rows.Close()
				return err
			}
			jobTeamIDs = append(jobTeamIDs, teamID)
			jobIDs = append(jobIDs, jobID)
			if identitiesByTeam[teamID] == nil {
				identitiesByTeam[teamID] = make(map[embeddingFailureIncidentIdentity]struct{})
			}
			identitiesByTeam[teamID][identity] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(jobIDs) == 0 {
			return nil
		}
		teamIDs := make([]string, 0, len(identitiesByTeam))
		for teamID := range identitiesByTeam {
			teamIDs = append(teamIDs, teamID)
		}
		sort.Strings(teamIDs)
		for _, teamID := range teamIDs {
			identities := make([]embeddingFailureIncidentIdentity, 0, len(identitiesByTeam[teamID]))
			for identity := range identitiesByTeam[teamID] {
				identities = append(identities, identity)
			}
			if err := lockEmbeddingFailureIncidentIdentities(ctx, tx, teamID, identities); err != nil {
				return err
			}
		}

		var documentCount, incidentCount int64
		if err := tx.WithContext(ctx).Raw(`
			WITH selected AS MATERIALIZED (
				SELECT *
				FROM unnest(?::uuid[], ?::uuid[]) AS item(team_id, embedding_job_id)
			), requeued AS (
				UPDATE embedding_jobs AS job
				SET status = 'queued', attempts = 0, available_at = now(),
				    lease_until = NULL, worker_id = '', completed_at = NULL,
				    error = '', recovery_count = recovery_count + 1,
				    last_recovered_at = now(), updated_at = now()
				FROM selected
				WHERE job.team_id = selected.team_id
				  AND job.embedding_job_id = selected.embedding_job_id
				  AND job.status = 'failed'
				  AND job.failure_class <> 'permanent'
				  AND job.embedding_contract_id = ?::uuid
				  AND job.embedding_dimensions = ?
				  AND COALESCE(job.last_failed_at, job.updated_at) <= ?
				  AND EXISTS (
				      SELECT 1
				      FROM embedding_reconciliation_runs AS run
				      WHERE run.reconciliation_run_id = ?::uuid
				        AND run.status = 'running'
				        AND run.worker_id = ?
				        AND run.lease_token = ?::uuid
				  )
				RETURNING job.team_id, job.embedding_job_id, job.search_document_id, job.source_kind,
				          job.failure_class, job.failure_code,
				          job.source_version, job.projection_format_version,
				          job.projection_generation_id, job.document_version,
				          job.embedding_contract_id, job.embedding_dimensions
			), documents AS (
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
				  AND NOT EXISTS (
				      SELECT 1
				      FROM embedding_jobs AS remaining_failure
				      JOIN search_documents AS document
				        ON document.team_id = remaining_failure.team_id
				       AND document.search_document_id = remaining_failure.search_document_id
				       AND document.source_version = remaining_failure.source_version
				       AND document.projection_format_version = remaining_failure.projection_format_version
				       AND document.projection_generation_id IS NOT DISTINCT FROM remaining_failure.projection_generation_id
				       AND document.document_version = remaining_failure.document_version
				       AND document.embedding_contract_id = remaining_failure.embedding_contract_id
				       AND document.embedding_dimensions = remaining_failure.embedding_dimensions
				      JOIN teams AS team
				        ON team.id = remaining_failure.team_id AND team.status = 'active' AND team.deleted_at IS NULL
				      WHERE remaining_failure.team_id = incident.team_id
				        AND remaining_failure.embedding_contract_id = incident.embedding_contract_id
				        AND remaining_failure.embedding_dimensions = incident.embedding_dimensions
				        AND remaining_failure.source_kind = incident.source_kind
				        AND remaining_failure.failure_class = incident.failure_class
				        AND remaining_failure.failure_code = incident.failure_code
				        AND remaining_failure.status = 'failed'
				        AND NOT EXISTS (
				            SELECT 1
				            FROM requeued
				            WHERE requeued.team_id = remaining_failure.team_id
				              AND requeued.embedding_job_id = remaining_failure.embedding_job_id
				        )
				  )
				RETURNING 1
			)
			SELECT (SELECT count(*) FROM requeued),
			       (SELECT count(*) FROM documents),
			       (SELECT count(*) FROM incidents)
		`, pq.Array(jobTeamIDs), pq.Array(jobIDs),
			input.EmbeddingContractID, input.EmbeddingDimensions, input.CandidateCutoff,
			input.RunID, input.WorkerID, input.LeaseToken,
			input.RunID).Row().Scan(&batch.requeued, &documentCount, &incidentCount); err != nil {
			return err
		}
		if batch.requeued == 0 {
			return nil
		}
		result := tx.WithContext(ctx).Exec(`
			UPDATE embedding_reconciliation_runs
			SET requeued_count = requeued_count + ?, updated_at = now()
			WHERE reconciliation_run_id = ?::uuid
			  AND status = 'running' AND worker_id = ? AND lease_token = ?::uuid
			  AND lease_until > clock_timestamp()
		`, batch.requeued, input.RunID, input.WorkerID, input.LeaseToken)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("reconciliation run lease lost")
		}
		return nil
	})
	return batch, err
}

func embeddingReconciliationCandidatesRemain(
	ctx context.Context,
	tx *gorm.DB,
	input RequeueEmbeddingReconciliationJobsInput,
) (bool, error) {
	var remaining bool
	err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
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
			JOIN teams AS team
			  ON team.id = job.team_id AND team.status = 'active' AND team.deleted_at IS NULL
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
		)
	`, input.RunID, input.WorkerID, input.LeaseToken,
		input.EmbeddingContractID, input.EmbeddingDimensions, input.CandidateCutoff,
	).Scan(&remaining).Error
	return remaining, err
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
