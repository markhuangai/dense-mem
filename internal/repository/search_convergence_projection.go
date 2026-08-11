package repository

import (
	"context"
	"time"

	"gorm.io/gorm"
)

const searchConvergenceFailureGroupLimit = 100

func readSearchConvergenceFailureGroups(
	ctx context.Context,
	tx *gorm.DB,
	contractID string,
	dimensions int,
) ([]EmbeddingFailureGroup, int64, bool, bool, bool, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH failure_groups AS MATERIALIZED (
			SELECT job.team_id, COALESCE(team.name, '') AS team_name,
			       job.embedding_contract_id, job.embedding_dimensions,
			       job.source_kind, job.failure_class, job.failure_code,
			       count(*) FILTER (WHERE job.status = 'failed') AS failed_job_count,
			       count(*) FILTER (WHERE job.status = 'queued') AS queued_job_count,
			       count(*) FILTER (WHERE job.status = 'processing') AS processing_job_count,
			       count(*) AS affected_job_count,
			       min(job.first_failed_at) AS first_failed_at,
			       max(COALESCE(job.last_failed_at, job.first_failed_at)) AS last_failed_at
			FROM embedding_jobs AS job
			JOIN teams AS team
			  ON team.id = job.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE job.embedding_contract_id = ?::uuid
			  AND job.embedding_dimensions = ?
			  AND job.first_failed_at IS NOT NULL
			  AND job.status IN ('queued', 'processing', 'failed')
			GROUP BY job.team_id, team.name, job.embedding_contract_id,
			         job.embedding_dimensions, job.source_kind,
			         job.failure_class, job.failure_code
		), ranked AS (
			SELECT failure_groups.*, count(*) OVER () AS failure_group_count
			FROM failure_groups
		)
		SELECT team_id::text, team_name, embedding_contract_id::text,
		       embedding_dimensions, source_kind, failure_class, failure_code,
		       failed_job_count, queued_job_count, processing_job_count,
		       affected_job_count, first_failed_at, last_failed_at,
		       GREATEST(EXTRACT(EPOCH FROM (clock_timestamp() - last_failed_at)), 0),
		       failure_group_count
		FROM ranked
		ORDER BY last_failed_at DESC, team_id, source_kind, failure_class, failure_code
		LIMIT ?
	`, contractID, dimensions, searchConvergenceFailureGroupLimit).Rows()
	if err != nil {
		return nil, 0, false, false, false, err
	}
	defer rows.Close()

	groups := make([]EmbeddingFailureGroup, 0, searchConvergenceFailureGroupLimit)
	var groupCount int64
	var hasFailed, hasRecovering bool
	for rows.Next() {
		var item EmbeddingFailureGroup
		var ageSeconds float64
		if err := rows.Scan(
			&item.TeamID, &item.TeamName,
			&item.EmbeddingContractID, &item.EmbeddingDimensions,
			&item.SourceKind, &item.FailureClass, &item.FailureCode,
			&item.FailedJobCount, &item.QueuedJobCount, &item.ProcessingJobCount,
			&item.AffectedJobCount, &item.FirstFailedAt, &item.LastFailedAt,
			&ageSeconds, &groupCount,
		); err != nil {
			return nil, 0, false, false, false, err
		}
		item.Status = "recovering"
		if item.FailedJobCount > 0 {
			item.Status = "attention_required"
			hasFailed = true
		} else {
			hasRecovering = true
		}
		item.Age = time.Duration(ageSeconds * float64(time.Second))
		item.Guidance = embeddingFailureGuidance(item.FailureCode)
		groups = append(groups, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, false, false, err
	}
	return groups, groupCount, hasFailed, hasRecovering, groupCount > int64(len(groups)), nil
}
