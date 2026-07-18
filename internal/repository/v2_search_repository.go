package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const defaultV2SearchProfileKey = "default"

var (
	ErrV2SearchStaleVersion    = errors.New("v2 search stale source or document version")
	ErrV2SearchProfileMismatch = errors.New("v2 search profile mismatch")
)

type V2SearchRepositoryImpl struct {
	db  *gorm.DB
	rls v2RLSHelper
}

var _ V2SearchRepository = (*V2SearchRepositoryImpl)(nil)

func NewV2SearchRepository(db *gorm.DB, rls *postgres.RLS) *V2SearchRepositoryImpl {
	return &V2SearchRepositoryImpl{db: db, rls: rls}
}

func (r *V2SearchRepositoryImpl) GetActiveSearchProfile(ctx context.Context, profileKey string) (*V2SearchProfile, error) {
	profileKey = normalizeV2SearchProfileKey(profileKey)
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	var profile V2SearchProfile
	err = db.WithContext(ctx).Raw(`
		SELECT
		    search.profile_key,
		    contract.embedding_contract_id::text,
		    search.search_index_profile_id::text,
		    ranking.ranking_profile_id::text,
		    contract.dimensions,
		    contract.provider,
		    contract.model,
		    contract.distance_metric,
		    contract.vector_normalization,
		    contract.document_format_version,
		    contract.query_format_version,
		    search.ann_strategy,
		    search.operator_class,
		    search.indexed_expression,
		    search.physical_index_name,
		    search.exact_max_rows,
		    search.candidate_limit,
		    search.allow_exact_fallback
		FROM search_index_profiles AS search
		JOIN embedding_contracts AS contract
		  ON contract.embedding_contract_id = search.embedding_contract_id
		 AND contract.dimensions = search.embedding_dimensions
		JOIN ranking_profiles AS ranking
		  ON ranking.profile_key = search.profile_key
		 AND ranking.activation_state = 'active'
		WHERE search.profile_key = ?
		  AND search.activation_state = 'active'
		  AND contract.lifecycle_state = 'active'
		ORDER BY search.version DESC, ranking.version DESC
		LIMIT 1
	`, profileKey).Row().Scan(
		&profile.ProfileKey,
		&profile.EmbeddingContractID,
		&profile.SearchIndexProfileID,
		&profile.RankingProfileID,
		&profile.EmbeddingDimensions,
		&profile.EmbeddingProvider,
		&profile.EmbeddingModel,
		&profile.DistanceMetric,
		&profile.VectorNormalization,
		&profile.DocumentFormatVersion,
		&profile.QueryFormatVersion,
		&profile.IndexStrategy,
		&profile.OperatorClass,
		&profile.IndexedExpression,
		&profile.PhysicalIndexName,
		&profile.ExactMaxRows,
		&profile.CandidateLimit,
		&profile.AllowExactFallback,
	)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w: active search profile %q not found", ErrV2SearchProfileMismatch, profileKey)
	}
	if err != nil {
		return nil, fmt.Errorf("v2 search: load active profile: %w", err)
	}
	if profile.ProfileKey == "" {
		return nil, fmt.Errorf("%w: active search profile %q not found", ErrV2SearchProfileMismatch, profileKey)
	}
	return &profile, nil
}

func (r *V2SearchRepositoryImpl) CheckSearchReadiness(ctx context.Context, profileKey string) (*V2SearchReadiness, error) {
	profile, err := r.GetActiveSearchProfile(ctx, profileKey)
	if err != nil {
		return nil, err
	}
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	readiness := &V2SearchReadiness{ProfileKey: profile.ProfileKey, Ready: true, Profile: profile}
	var vectorPresent bool
	if err := db.WithContext(ctx).Raw(`
		SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')
	`).Scan(&vectorPresent).Error; err != nil {
		return nil, fmt.Errorf("v2 search: readiness extension check: %w", err)
	}
	if !vectorPresent {
		readiness.Ready = false
		readiness.Reasons = append(readiness.Reasons, V2SearchReadinessReason{
			Code:    "missing_pgvector_extension",
			Message: "pgvector extension is not installed",
		})
	}
	if profile.IndexStrategy != string(domain.V2VectorIndexExact) {
		var indexDefinition string
		if err := db.WithContext(ctx).Raw(`
			SELECT COALESCE((
			    SELECT indexdef
			    FROM pg_indexes
			    WHERE schemaname = current_schema()
			      AND indexname = ?
			), '')
		`, profile.PhysicalIndexName).Scan(&indexDefinition).Error; err != nil {
			return nil, fmt.Errorf("v2 search: readiness index check: %w", err)
		}
		if indexDefinition == "" {
			readiness.Ready = false
			readiness.Reasons = append(readiness.Reasons, V2SearchReadinessReason{
				Code:    "missing_physical_index",
				Message: fmt.Sprintf("physical index %q is missing", profile.PhysicalIndexName),
			})
		} else if missing := v2SearchMissingIndexCompatibility(profile, indexDefinition); len(missing) > 0 {
			readiness.Ready = false
			readiness.Reasons = append(readiness.Reasons, V2SearchReadinessReason{
				Code: "incompatible_physical_index",
				Message: fmt.Sprintf(
					"physical index %q is incompatible with profile %q: missing %s",
					profile.PhysicalIndexName,
					profile.ProfileKey,
					strings.Join(missing, ", "),
				),
			})
		}
	}
	return readiness, nil
}

func (r *V2SearchRepositoryImpl) UpsertSearchDocument(
	ctx context.Context,
	input V2UpsertSearchDocumentInput,
) (*V2SearchDocumentResult, error) {
	input = normalizeV2UpsertSearchDocumentInput(input)
	if err := validateV2UpsertSearchDocumentInput(input); err != nil {
		return nil, err
	}
	profile, err := r.profileForDocument(ctx, input)
	if err != nil {
		return nil, err
	}
	var result *V2SearchDocumentResult
	err = r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureV2SemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		metadata, err := marshalV2SearchJSON(input.Metadata)
		if err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			WITH upserted AS (
				INSERT INTO search_documents (
				    team_id, owner_profile_id, source_kind, source_id, source_version,
				    document_version, embedding_contract_id, embedding_dimensions,
				    search_state, document_text, document_hash, metadata
				) VALUES (
				    ?::uuid, ?::uuid, ?, ?::uuid, ?, 1, ?::uuid, ?,
				    'pending', ?, ?, ?::jsonb
				)
				ON CONFLICT (team_id, source_kind, source_id, embedding_contract_id)
				DO UPDATE SET
				    owner_profile_id = EXCLUDED.owner_profile_id,
				    source_version = EXCLUDED.source_version,
				    document_version = CASE
				        WHEN search_documents.document_hash = EXCLUDED.document_hash
				        THEN search_documents.document_version
				        ELSE search_documents.document_version + 1
				    END,
				    search_state = CASE
				        WHEN search_documents.document_hash = EXCLUDED.document_hash
				         AND search_documents.search_state = 'current'
				        THEN 'current'
				        ELSE 'pending'
				    END,
				    document_text = EXCLUDED.document_text,
				    document_hash = EXCLUDED.document_hash,
				    embedding_error = CASE
				        WHEN search_documents.document_hash = EXCLUDED.document_hash
				        THEN search_documents.embedding_error
				        ELSE ''
				    END,
				    metadata = EXCLUDED.metadata,
				    updated_at = now()
				RETURNING team_id::text, search_document_id::text, owner_profile_id::text,
				          source_kind, source_id::text, source_version, document_version,
				          embedding_contract_id::text, embedding_dimensions, search_state
			)
			SELECT * FROM upserted
		`, input.TeamID, input.OwnerProfileID, input.SourceKind, input.SourceID, input.SourceVersion,
			profile.EmbeddingContractID, profile.EmbeddingDimensions, input.DocumentText,
			input.DocumentHash, string(metadata)).Rows()
		if err != nil {
			return err
		}
		if !rows.Next() {
			_ = rows.Close()
			return rows.Err()
		}
		loaded := V2SearchDocumentResult{}
		if err := rows.Scan(
			&loaded.TeamID,
			&loaded.SearchDocumentID,
			&loaded.OwnerProfileID,
			&loaded.SourceKind,
			&loaded.SourceID,
			&loaded.SourceVersion,
			&loaded.DocumentVersion,
			&loaded.EmbeddingContractID,
			&loaded.EmbeddingDimensions,
			&loaded.SearchState,
		); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		jobID, err := enqueueV2EmbeddingJob(ctx, tx, loaded)
		if err != nil {
			return err
		}
		loaded.QueuedJobID = jobID
		result = &loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 search: upsert document: %w", err)
	}
	return result, nil
}

func (r *V2SearchRepositoryImpl) ClaimEmbeddingJobs(
	ctx context.Context,
	input V2ClaimEmbeddingJobsInput,
) ([]V2EmbeddingJob, error) {
	input = normalizeV2ClaimEmbeddingJobsInput(input)
	if err := validateV2ClaimEmbeddingJobsInput(input); err != nil {
		return nil, err
	}
	jobs := []V2EmbeddingJob{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH claimed AS (
				SELECT team_id, embedding_job_id
				FROM embedding_jobs
				WHERE team_id = ?::uuid
				  AND attempts < max_attempts
				  AND (
					(status IN ('queued', 'failed') AND available_at <= now())
					OR (status = 'processing' AND lease_until IS NOT NULL AND lease_until < now())
				  )
				ORDER BY
					CASE WHEN status IN ('queued', 'failed') THEN 0 ELSE 1 END,
					available_at ASC,
					created_at ASC,
					embedding_job_id ASC
				LIMIT ?
				FOR UPDATE SKIP LOCKED
			),
			updated AS (
				UPDATE embedding_jobs AS job
				SET status = 'processing',
				    attempts = attempts + 1,
				    worker_id = ?,
				    lease_until = now() + (? * interval '1 second'),
				    updated_at = now(),
				    error = ''
				FROM claimed
				WHERE job.team_id = claimed.team_id
				  AND job.embedding_job_id = claimed.embedding_job_id
				RETURNING job.team_id::text, job.embedding_job_id::text,
				          job.search_document_id::text, job.owner_profile_id::text,
				          job.source_kind, job.source_id::text, job.source_version,
				          job.document_version, job.embedding_contract_id::text,
				          job.embedding_dimensions, job.status, job.attempts,
				          job.lease_until
			)
			SELECT updated.*, document.document_text
			FROM updated
			JOIN search_documents AS document
			  ON document.team_id = updated.team_id::uuid
			 AND document.search_document_id = updated.search_document_id::uuid
			ORDER BY updated.lease_until ASC, updated.embedding_job_id ASC
		`, input.TeamID, input.Limit, input.WorkerID, int(input.Lease.Seconds())).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var job V2EmbeddingJob
			if err := rows.Scan(
				&job.TeamID,
				&job.EmbeddingJobID,
				&job.SearchDocumentID,
				&job.OwnerProfileID,
				&job.SourceKind,
				&job.SourceID,
				&job.SourceVersion,
				&job.DocumentVersion,
				&job.EmbeddingContractID,
				&job.EmbeddingDimensions,
				&job.Status,
				&job.Attempts,
				&job.LeaseUntil,
				&job.DocumentText,
			); err != nil {
				return err
			}
			jobs = append(jobs, job)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 search: claim embedding jobs: %w", err)
	}
	return jobs, nil
}

func (r *V2SearchRepositoryImpl) CompleteEmbeddingJob(ctx context.Context, input V2CompleteEmbeddingJobInput) error {
	input = normalizeV2CompleteEmbeddingJobInput(input)
	if err := validateV2CompleteEmbeddingJobInput(input); err != nil {
		return err
	}
	vectorLiteral, err := v2VectorLiteral(input.Embedding)
	if err != nil {
		return err
	}
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var dims int
		if err := tx.WithContext(ctx).Raw(`
			SELECT embedding_dimensions
			FROM embedding_jobs
			WHERE team_id = ?::uuid
			  AND embedding_job_id = ?::uuid
			  AND worker_id = ?
			  AND status = 'processing'
		`, input.TeamID, input.EmbeddingJobID, input.WorkerID).Scan(&dims).Error; err != nil {
			return err
		}
		if dims == 0 {
			return fmt.Errorf("%w: processing job not found", ErrV2SearchStaleVersion)
		}
		if dims != len(input.Embedding) {
			return fmt.Errorf("%w: job dimensions %d, vector dimensions %d", ErrV2SearchProfileMismatch, dims, len(input.Embedding))
		}
		result := tx.WithContext(ctx).Exec(`
			UPDATE search_documents AS document
			SET embedding = ?::vector,
			    search_state = 'current',
			    embedding_updated_at = now(),
			    embedding_error = '',
			    updated_at = now()
			FROM embedding_jobs AS job
			WHERE job.team_id = ?::uuid
			  AND job.embedding_job_id = ?::uuid
			  AND job.worker_id = ?
			  AND job.status = 'processing'
			  AND document.team_id = job.team_id
			  AND document.search_document_id = job.search_document_id
			  AND document.source_version = job.source_version
			  AND document.document_version = job.document_version
			  AND document.embedding_contract_id = job.embedding_contract_id
			  AND document.embedding_dimensions = job.embedding_dimensions
		`, vectorLiteral, input.TeamID, input.EmbeddingJobID, input.WorkerID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			if err := markV2EmbeddingJobTerminal(ctx, tx, input, string(domain.V2EmbeddingJobStale), "source or document version changed before embedding completion"); err != nil {
				return err
			}
			return ErrV2SearchStaleVersion
		}
		if err := markV2EmbeddingJobTerminal(ctx, tx, input, string(domain.V2EmbeddingJobCompleted), ""); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("v2 search: complete embedding job: %w", err)
	}
	return nil
}

func (r *V2SearchRepositoryImpl) SearchFullText(ctx context.Context, input V2FullTextSearchInput) ([]V2SearchHit, error) {
	input = normalizeV2FullTextSearchInput(input)
	if err := validateV2FullTextSearchInput(input); err != nil {
		return nil, err
	}
	hits := []V2SearchHit{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		sourceFilter := ""
		args := []any{input.Query, input.TeamID, input.Query}
		if input.SourceKind != "" {
			sourceFilter = "AND source_kind = ?"
			args = append(args, input.SourceKind)
		}
		args = append(args, input.Limit)
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, search_document_id::text, source_kind, source_id::text,
			       source_version, document_version, embedding_contract_id::text,
			       0::double precision AS distance,
			       ts_rank_cd(search_tsv, plainto_tsquery('simple', ?))::double precision AS text_rank
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND search_state IN ('pending', 'current')
			  AND search_tsv @@ plainto_tsquery('simple', ?)
			  `+sourceFilter+`
			ORDER BY text_rank DESC, updated_at DESC, search_document_id ASC
			LIMIT ?
		`, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			hit, err := scanV2SearchHit(rows)
			if err != nil {
				return err
			}
			hits = append(hits, hit)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 search: full-text search: %w", err)
	}
	return hits, nil
}

func (r *V2SearchRepositoryImpl) SearchExactVector(ctx context.Context, input V2ExactVectorSearchInput) ([]V2SearchHit, error) {
	input = normalizeV2ExactVectorSearchInput(input)
	if err := validateV2ExactVectorSearchInput(input); err != nil {
		return nil, err
	}
	profile, err := r.profileForVectorSearch(ctx, input)
	if err != nil {
		return nil, err
	}
	if len(input.QueryEmbedding) != profile.EmbeddingDimensions {
		return nil, fmt.Errorf("%w: profile dimensions %d, query dimensions %d", ErrV2SearchProfileMismatch, profile.EmbeddingDimensions, len(input.QueryEmbedding))
	}
	if profile.IndexStrategy != string(domain.V2VectorIndexExact) && !profile.AllowExactFallback {
		return nil, fmt.Errorf("%w: profile %q does not allow exact vector search", ErrV2SearchProfileMismatch, profile.ProfileKey)
	}
	vectorLiteral, err := v2VectorLiteral(input.QueryEmbedding)
	if err != nil {
		return nil, err
	}
	hits := []V2SearchHit{}
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		sourceFilter := ""
		countArgs := []any{input.TeamID, profile.EmbeddingContractID, profile.EmbeddingDimensions}
		args := []any{vectorLiteral, input.TeamID, profile.EmbeddingContractID, profile.EmbeddingDimensions}
		if input.SourceKind != "" {
			sourceFilter = "AND source_kind = ?"
			countArgs = append(countArgs, input.SourceKind)
			args = append(args, input.SourceKind)
		}
		countArgs = append(countArgs, profile.ExactMaxRows+1)
		var candidateCount int64
		if err := tx.WithContext(ctx).Raw(`
			SELECT count(*)
			FROM (
				SELECT search_document_id
				FROM search_documents
				WHERE team_id = ?::uuid
				  AND embedding_contract_id = ?::uuid
				  AND embedding_dimensions = ?
				  AND search_state = 'current'
				  AND embedding IS NOT NULL
				  `+sourceFilter+`
				LIMIT ?
			) AS exact_candidates
		`, countArgs...).Scan(&candidateCount).Error; err != nil {
			return err
		}
		if candidateCount > int64(profile.ExactMaxRows) {
			return fmt.Errorf("%w: exact vector candidates %d exceed profile max %d", ErrV2SearchProfileMismatch, candidateCount, profile.ExactMaxRows)
		}
		args = append(args, vectorLiteral, input.Limit)
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, search_document_id::text, source_kind, source_id::text,
			       source_version, document_version, embedding_contract_id::text,
			       (embedding <=> ?::vector)::double precision AS distance,
			       0::double precision AS text_rank
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND embedding_contract_id = ?::uuid
			  AND embedding_dimensions = ?
			  AND search_state = 'current'
			  AND embedding IS NOT NULL
			  `+sourceFilter+`
			ORDER BY embedding <=> ?::vector ASC, search_document_id ASC
			LIMIT ?
		`, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			hit, err := scanV2SearchHit(rows)
			if err != nil {
				return err
			}
			hits = append(hits, hit)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 search: exact vector search: %w", err)
	}
	return hits, nil
}

func (r *V2SearchRepositoryImpl) profileForDocument(ctx context.Context, input V2UpsertSearchDocumentInput) (*V2SearchProfile, error) {
	if input.EmbeddingContractID == "" {
		return r.GetActiveSearchProfile(ctx, input.ProfileKey)
	}
	profile, err := r.GetActiveSearchProfile(ctx, input.ProfileKey)
	if err != nil {
		return nil, err
	}
	if profile.EmbeddingContractID != input.EmbeddingContractID {
		return nil, fmt.Errorf("%w: requested contract %s is not active profile contract %s", ErrV2SearchProfileMismatch, input.EmbeddingContractID, profile.EmbeddingContractID)
	}
	return profile, nil
}

func (r *V2SearchRepositoryImpl) profileForVectorSearch(ctx context.Context, input V2ExactVectorSearchInput) (*V2SearchProfile, error) {
	profile, err := r.GetActiveSearchProfile(ctx, input.ProfileKey)
	if err != nil {
		return nil, err
	}
	if input.EmbeddingContractID != "" && input.EmbeddingContractID != profile.EmbeddingContractID {
		return nil, fmt.Errorf("%w: requested contract %s is not active profile contract %s", ErrV2SearchProfileMismatch, input.EmbeddingContractID, profile.EmbeddingContractID)
	}
	return profile, nil
}

func enqueueV2EmbeddingJob(ctx context.Context, tx *gorm.DB, document V2SearchDocumentResult) (string, error) {
	if document.SearchState != string(domain.V2SearchProjectionPending) {
		return "", nil
	}
	var jobID string
	err := tx.WithContext(ctx).Raw(`
		INSERT INTO embedding_jobs (
		    team_id, search_document_id, owner_profile_id, source_kind, source_id,
		    source_version, document_version, embedding_contract_id, embedding_dimensions
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?, ?::uuid,
		    ?, ?, ?::uuid, ?
		)
		ON CONFLICT (
		    team_id, source_kind, source_id, source_version,
		    document_version, embedding_contract_id
		) DO NOTHING
		RETURNING embedding_job_id::text
	`, document.TeamID, document.SearchDocumentID, document.OwnerProfileID,
		document.SourceKind, document.SourceID, document.SourceVersion,
		document.DocumentVersion, document.EmbeddingContractID,
		document.EmbeddingDimensions).Scan(&jobID).Error
	if err != nil {
		return "", err
	}
	return jobID, nil
}

func markV2EmbeddingJobTerminal(ctx context.Context, tx *gorm.DB, input V2CompleteEmbeddingJobInput, status string, message string) error {
	return tx.WithContext(ctx).Exec(`
		UPDATE embedding_jobs
		SET status = ?,
		    error = ?,
		    completed_at = now(),
		    updated_at = now(),
		    lease_until = NULL
		WHERE team_id = ?::uuid
		  AND embedding_job_id = ?::uuid
		  AND worker_id = ?
		  AND status = 'processing'
	`, status, message, input.TeamID, input.EmbeddingJobID, input.WorkerID).Error
}

type v2SearchHitScanner interface {
	Scan(dest ...any) error
}

func scanV2SearchHit(scanner v2SearchHitScanner) (V2SearchHit, error) {
	var hit V2SearchHit
	err := scanner.Scan(
		&hit.TeamID,
		&hit.SearchDocumentID,
		&hit.SourceKind,
		&hit.SourceID,
		&hit.SourceVersion,
		&hit.DocumentVersion,
		&hit.EmbeddingContractID,
		&hit.Distance,
		&hit.TextRank,
	)
	return hit, err
}

func (r *V2SearchRepositoryImpl) withTeamProfileTx(ctx context.Context, teamID, profileID string, fn func(tx *gorm.DB) error) error {
	if _, err := r.database(); err != nil {
		return err
	}
	if r.rls == nil {
		return errors.New("v2 search: rls helper is required")
	}
	return r.rls.WithTeamProfileTx(ctx, r.db, teamID, profileID, fn)
}

func (r *V2SearchRepositoryImpl) withTeamTx(ctx context.Context, teamID string, fn func(tx *gorm.DB) error) error {
	if _, err := r.database(); err != nil {
		return err
	}
	if r.rls == nil {
		return errors.New("v2 search: rls helper is required")
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
}

func (r *V2SearchRepositoryImpl) database() (*gorm.DB, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("v2 search: database is required")
	}
	return r.db, nil
}
