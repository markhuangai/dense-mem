package repository

import (
	"context"
	"sort"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type embeddingFailureIncidentLockKey struct {
	failureClass string
	failureCode  string
}

type embeddingFailureIncidentIdentity struct {
	sourceKind   string
	contractID   string
	dimensions   int
	failureClass string
	failureCode  string
}

func lockEmbeddingFailureIncidentIdentities(ctx context.Context, tx *gorm.DB, teamID string, identities []embeddingFailureIncidentIdentity) error {
	sort.Slice(identities, func(i, j int) bool {
		if identities[i].failureClass != identities[j].failureClass {
			return identities[i].failureClass < identities[j].failureClass
		}
		if identities[i].failureCode != identities[j].failureCode {
			return identities[i].failureCode < identities[j].failureCode
		}
		if identities[i].sourceKind != identities[j].sourceKind {
			return identities[i].sourceKind < identities[j].sourceKind
		}
		if identities[i].contractID != identities[j].contractID {
			return identities[i].contractID < identities[j].contractID
		}
		return identities[i].dimensions < identities[j].dimensions
	})
	for _, identity := range identities {
		if err := lockEmbeddingFailureIncident(ctx, tx, teamID, identity.sourceKind, identity.contractID, identity.failureClass, identity.failureCode, identity.dimensions); err != nil {
			return err
		}
	}
	return nil
}

func lockEmbeddingFailureIncidentTransition(
	ctx context.Context,
	tx *gorm.DB,
	teamID, sourceKind, contractID string,
	dimensions int,
	previousClass, previousCode, nextClass, nextCode string,
) error {
	if previousClass == nextClass && previousCode == nextCode {
		return nil
	}
	keys := []embeddingFailureIncidentLockKey{
		{failureClass: previousClass, failureCode: previousCode},
		{failureClass: nextClass, failureCode: nextCode},
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].failureClass != keys[j].failureClass {
			return keys[i].failureClass < keys[j].failureClass
		}
		return keys[i].failureCode < keys[j].failureCode
	})
	for _, key := range keys {
		if err := lockEmbeddingFailureIncident(ctx, tx, teamID, sourceKind, contractID, key.failureClass, key.failureCode, dimensions); err != nil {
			return err
		}
	}
	return nil
}

func lockEmbeddingFailureIncident(
	ctx context.Context,
	tx *gorm.DB,
	teamID, sourceKind, contractID, failureClass, failureCode string,
	dimensions int,
) error {
	return tx.WithContext(ctx).Exec(`
		SELECT pg_advisory_xact_lock(hashtextextended(
			concat_ws('|', ?::uuid::text, ?::uuid::text, ?::integer::text, ?::text, ?::text, ?::text), 0
		))
	`, teamID, contractID, dimensions, sourceKind, failureClass, failureCode).Error
}

func resolveEmbeddingIncidentKey(
	ctx context.Context,
	tx *gorm.DB,
	teamID, sourceKind, contractID, failureClass, failureCode string,
	dimensions int,
) error {
	if err := lockEmbeddingFailureIncident(ctx, tx, teamID, sourceKind, contractID, failureClass, failureCode, dimensions); err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		WITH remaining AS (
			SELECT incident.team_id, incident.incident_id,
			       count(job.embedding_job_id) AS affected_job_count
			FROM embedding_failure_incidents AS incident
			LEFT JOIN embedding_jobs AS job
			  ON job.team_id = incident.team_id
			 AND job.embedding_contract_id = incident.embedding_contract_id
			 AND job.embedding_dimensions = incident.embedding_dimensions
			 AND job.source_kind = incident.source_kind
			 AND job.failure_class = incident.failure_class
			 AND job.failure_code = incident.failure_code
			 AND job.first_failed_at IS NOT NULL
			 AND job.status IN ('queued', 'processing', 'failed')
			WHERE incident.team_id = ?::uuid
			  AND incident.embedding_contract_id = ?::uuid
			  AND incident.embedding_dimensions = ?
			  AND incident.source_kind = ?
			  AND incident.failure_class = ?
			  AND incident.failure_code = ?
			  AND incident.status IN ('open', 'recovering')
			GROUP BY incident.team_id, incident.incident_id
		)
		UPDATE embedding_failure_incidents AS incident
		SET status = CASE WHEN remaining.affected_job_count = 0 THEN 'resolved' ELSE incident.status END,
		    resolved_at = CASE WHEN remaining.affected_job_count = 0 THEN now() ELSE NULL END,
		    affected_job_count = remaining.affected_job_count,
		    updated_at = now()
		FROM remaining
		WHERE incident.team_id = remaining.team_id
		  AND incident.incident_id = remaining.incident_id
	`, teamID, contractID, dimensions, sourceKind, failureClass, failureCode).Error
}

func resolveEmbeddingIncidentsWithoutActiveJobs(ctx context.Context, tx *gorm.DB, teamID string) error {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT source_kind, embedding_contract_id::text, embedding_dimensions,
		       failure_class, failure_code
		FROM embedding_failure_incidents
		WHERE team_id = ?::uuid
		  AND status IN ('open', 'recovering')
	`, teamID).Rows()
	if err != nil {
		return err
	}
	identities := make([]embeddingFailureIncidentIdentity, 0)
	seen := make(map[embeddingFailureIncidentIdentity]struct{})
	for rows.Next() {
		var identity embeddingFailureIncidentIdentity
		if err := rows.Scan(&identity.sourceKind, &identity.contractID, &identity.dimensions, &identity.failureClass, &identity.failureCode); err != nil {
			_ = rows.Close()
			return err
		}
		if _, ok := seen[identity]; ok {
			continue
		}
		seen[identity] = struct{}{}
		identities = append(identities, identity)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := lockEmbeddingFailureIncidentIdentities(ctx, tx, teamID, identities); err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		UPDATE embedding_failure_incidents AS incident
		SET status = 'resolved', resolved_at = now(), affected_job_count = 0,
		    updated_at = now()
		WHERE incident.team_id = ?::uuid
		  AND incident.status IN ('open', 'recovering')
		  AND NOT EXISTS (
			SELECT 1 FROM embedding_jobs AS job
			WHERE job.team_id = incident.team_id
			  AND job.embedding_contract_id = incident.embedding_contract_id
			  AND job.embedding_dimensions = incident.embedding_dimensions
			  AND job.source_kind = incident.source_kind
			  AND job.failure_class = incident.failure_class
			  AND job.failure_code = incident.failure_code
			  AND job.first_failed_at IS NOT NULL
			  AND job.status IN ('queued', 'processing', 'failed')
		  )
	`, teamID).Error
}

func failExpiredMaxAttemptEmbeddingJobs(ctx context.Context, tx *gorm.DB, teamID string, limit int) error {
	if err := tx.WithContext(ctx).Exec(`SELECT set_config('app.embedding_job_failure_writer', 'current', true)`).Error; err != nil {
		return err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH document_candidates AS MATERIALIZED (
			SELECT job.team_id, job.embedding_job_id
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
			WHERE job.team_id = ?::uuid
			  AND job.status = 'processing'
			  AND job.lease_until <= clock_timestamp()
			  AND job.attempts >= job.max_attempts
			ORDER BY job.lease_until ASC, job.embedding_job_id ASC
			LIMIT ?
			FOR UPDATE OF document SKIP LOCKED
		), exhausted AS MATERIALIZED (
			SELECT job.team_id, job.embedding_job_id
			FROM document_candidates AS candidate
			JOIN embedding_jobs AS job
			  ON job.team_id = candidate.team_id
			 AND job.embedding_job_id = candidate.embedding_job_id
			WHERE job.status = 'processing'
			  AND job.lease_until <= clock_timestamp()
			  AND job.attempts >= job.max_attempts
			ORDER BY job.lease_until ASC, job.embedding_job_id ASC
			FOR UPDATE OF job SKIP LOCKED
		), failed AS (
			UPDATE embedding_jobs AS job
			SET status = 'failed', error = ?,
			    failure_class = 'transient', failure_code = 'provider_timeout',
			    first_failed_at = COALESCE(first_failed_at, now()),
			    last_failed_at = now(), completed_at = now(),
			    lease_until = NULL, worker_id = '', updated_at = now()
			FROM exhausted
			WHERE job.team_id = exhausted.team_id
			  AND job.embedding_job_id = exhausted.embedding_job_id
			RETURNING job.team_id, job.search_document_id, job.source_version,
			          job.projection_format_version, job.projection_generation_id,
			          job.document_version, job.embedding_contract_id,
			          job.embedding_dimensions, job.source_kind
		), documents AS (
			UPDATE search_documents AS document
			SET search_state = 'failed', embedding_error = ?, updated_at = now()
			FROM failed
			WHERE document.team_id = failed.team_id
			  AND document.search_document_id = failed.search_document_id
			  AND document.source_version = failed.source_version
			  AND document.projection_format_version = failed.projection_format_version
			  AND document.projection_generation_id IS NOT DISTINCT FROM failed.projection_generation_id
			  AND document.document_version = failed.document_version
			  AND document.embedding_contract_id = failed.embedding_contract_id
			  AND document.embedding_dimensions = failed.embedding_dimensions
			RETURNING 1
		)
		SELECT embedding_contract_id::text, embedding_dimensions, source_kind
		FROM failed
	`, teamID, limit, embeddingJobAttemptsExhaustedMessage, embeddingJobAttemptsExhaustedMessage).Rows()
	if err != nil {
		return err
	}
	identities := make([]embeddingFailureIncidentIdentity, 0)
	seen := make(map[embeddingFailureIncidentIdentity]struct{})
	for rows.Next() {
		identity := embeddingFailureIncidentIdentity{
			failureClass: string(domain.EmbeddingFailureTransient),
			failureCode:  string(domain.EmbeddingFailureProviderTimeout),
		}
		if err := rows.Scan(&identity.contractID, &identity.dimensions, &identity.sourceKind); err != nil {
			_ = rows.Close()
			return err
		}
		if _, ok := seen[identity]; ok {
			continue
		}
		seen[identity] = struct{}{}
		identities = append(identities, identity)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := lockEmbeddingFailureIncidentIdentities(ctx, tx, teamID, identities); err != nil {
		return err
	}
	for _, identity := range identities {
		if err := upsertEmbeddingFailureIncident(ctx, tx, teamID,
			identity.failureClass, identity.failureCode,
			identity.contractID, identity.dimensions, identity.sourceKind); err != nil {
			return err
		}
	}
	return nil
}
