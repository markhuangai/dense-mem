package repository

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

type SemanticEmbeddingJob struct {
	TeamID           string
	JobID            string
	SearchDocumentID string
	SourceType       string
	SourceID         string
	DocumentVersion  int64
	DocumentText     string
	Attempts         int
}

func (r *SemanticRepositoryImpl) ClaimSemanticEmbeddingJobs(ctx context.Context, batchSize int) ([]SemanticEmbeddingJob, error) {
	if batchSize <= 0 {
		batchSize = 1
	}
	jobs := []SemanticEmbeddingJob{}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH ready_team AS (
				SELECT team_id
					FROM semantic_embedding_jobs j
				WHERE (status = 'queued' AND available_at <= now())
				   OR (status = 'processing' AND lease_until <= now())
				ORDER BY CASE
				           WHEN status = 'processing' THEN lease_until
				           ELSE available_at
				         END ASC,
				         created_at ASC
				LIMIT 1
			),
			next AS (
				SELECT j.team_id, j.job_id
					FROM semantic_embedding_jobs j
				JOIN ready_team rt ON rt.team_id = j.team_id
				WHERE (status = 'queued' AND available_at <= now())
				   OR (status = 'processing' AND lease_until <= now())
				ORDER BY CASE
				           WHEN status = 'processing' THEN lease_until
				           ELSE available_at
				         END ASC,
				         created_at ASC
				LIMIT ?
				FOR UPDATE SKIP LOCKED
			),
			claimed AS (
			UPDATE semantic_embedding_jobs j
			SET status = 'processing',
			    attempts = attempts + 1,
			    lease_until = now() + interval '5 minutes',
			    updated_at = now()
			FROM next
			WHERE j.team_id = next.team_id
			  AND j.job_id = next.job_id
			RETURNING j.team_id::text, j.job_id::text, j.search_document_id::text,
			          j.source_type, j.source_id::text, j.document_version, j.attempts
			)
			SELECT c.team_id, c.job_id, c.search_document_id, c.source_type,
			       c.source_id, c.document_version, c.attempts, d.document_text
			FROM claimed c
			JOIN semantic_search_documents d
			  ON d.team_id = c.team_id::uuid
			 AND d.search_document_id = c.search_document_id::uuid
			 AND d.document_version = c.document_version
			ORDER BY c.job_id
		`, batchSize).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var job SemanticEmbeddingJob
			if err := rows.Scan(&job.TeamID, &job.JobID, &job.SearchDocumentID, &job.SourceType, &job.SourceID, &job.DocumentVersion, &job.Attempts, &job.DocumentText); err != nil {
				return err
			}
			jobs = append(jobs, job)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("semantic embedding: claim jobs: %w", err)
	}
	return jobs, nil
}

func (r *SemanticRepositoryImpl) CompleteSemanticEmbeddingJob(ctx context.Context, job SemanticEmbeddingJob, vec []float32, model string) error {
	vectorLiteral := semanticVectorLiteral(vec)
	contractID := strings.TrimSpace(model) + ":" + strconv.Itoa(len(vec)) + ":semantic_search_document_v1"
	return r.withSystemTx(ctx, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			UPDATE semantic_embedding_jobs
			SET status = 'completed',
			    last_error = '',
			    lease_until = NULL,
			    updated_at = now(),
			    completed_at = now()
			WHERE team_id = ?
			  AND job_id = ?::uuid
			  AND status = 'processing'
			  AND attempts = ?
		`, job.TeamID, job.JobID, job.Attempts)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		if err := tx.WithContext(ctx).Exec(`
			UPDATE semantic_search_documents
			SET embedding = ?::vector,
			    embedding_model = ?,
			    embedding_contract_id = ?,
			    search_state = 'current',
			    last_error = '',
			    updated_at = now()
			WHERE team_id = ?
			  AND search_document_id = ?::uuid
			  AND document_version = ?
		`, vectorLiteral, model, contractID, job.TeamID, job.SearchDocumentID, job.DocumentVersion).Error; err != nil {
			return err
		}
		if err := updateSemanticSourceSearchState(ctx, tx, job, "current", contractID); err != nil {
			return err
		}
		return nil
	})
}

func (r *SemanticRepositoryImpl) FailSemanticEmbeddingJob(ctx context.Context, job SemanticEmbeddingJob, cause error) error {
	msg := strings.TrimSpace(fmt.Sprint(cause))
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	nextStatus := "queued"
	availableExpr := "now() + interval '30 seconds'"
	if job.Attempts >= 3 {
		nextStatus = "failed"
		availableExpr = "now()"
	}
	return r.withSystemTx(ctx, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			UPDATE semantic_embedding_jobs
			SET status = ?,
			    last_error = ?,
			    lease_until = NULL,
			    available_at = `+availableExpr+`,
			    updated_at = now()
			WHERE team_id = ?
			  AND job_id = ?::uuid
			  AND status = 'processing'
			  AND attempts = ?
		`, nextStatus, msg, job.TeamID, job.JobID, job.Attempts)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		if nextStatus == "failed" {
			if err := updateSemanticSourceSearchState(ctx, tx, job, "failed", ""); err != nil {
				return err
			}
			if err := tx.WithContext(ctx).Exec(`
				UPDATE semantic_search_documents
				SET search_state = 'failed',
				    last_error = ?,
				    updated_at = now()
				WHERE team_id = ? AND search_document_id = ?::uuid AND document_version = ?
			`, msg, job.TeamID, job.SearchDocumentID, job.DocumentVersion).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func updateSemanticSourceSearchState(ctx context.Context, tx *gorm.DB, job SemanticEmbeddingJob, state string, contractID string) error {
	switch job.SourceType {
	case "evidence":
		return tx.WithContext(ctx).Exec(`
			UPDATE semantic_evidence_fragments
			SET search_state = ?,
			    embedding_contract_id = ?,
			    updated_at = now()
			WHERE team_id = ? AND fragment_id = ?::uuid
		`, state, contractID, job.TeamID, job.SourceID).Error
	case "entity":
		return tx.WithContext(ctx).Exec(`
			UPDATE semantic_entities
			SET search_state = ?,
			    updated_at = now()
			WHERE team_id = ? AND entity_id = ?::uuid
		`, state, job.TeamID, job.SourceID).Error
	case "relationship":
		return tx.WithContext(ctx).Exec(`
			UPDATE semantic_relationship_records
			SET search_state = ?,
			    embedding_contract_id = ?,
			    updated_at = now()
			WHERE team_id = ? AND relationship_id = ?::uuid
		`, state, contractID, job.TeamID, job.SourceID).Error
	default:
		return fmt.Errorf("semantic embedding: unsupported source_type %q", job.SourceType)
	}
}
