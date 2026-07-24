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

const (
	v2EmbeddingJobAttemptsExhaustedMessage = "embedding attempts exhausted after lease expiration"
	defaultV2EmbeddingJobMaxAttempts       = 20
)

var (
	ErrV2SearchStaleVersion     = errors.New("v2 search stale source or document version")
	ErrV2SearchContractMismatch = errors.New("v2 search contract mismatch")
	ErrV2EmbeddingLeaseLost     = errors.New("v2 embedding lease lost")
)

type V2SearchRepositoryImpl struct {
	db                      *gorm.DB
	rls                     v2RLSHelper
	embeddingJobMaxAttempts int
}

var _ V2SearchRepository = (*V2SearchRepositoryImpl)(nil)

func NewV2SearchRepository(db *gorm.DB, rls *postgres.RLS) *V2SearchRepositoryImpl {
	return NewV2SearchRepositoryWithEmbeddingJobMaxAttempts(db, rls, defaultV2EmbeddingJobMaxAttempts)
}

func NewV2SearchRepositoryWithEmbeddingJobMaxAttempts(
	db *gorm.DB,
	rls *postgres.RLS,
	maxAttempts int,
) *V2SearchRepositoryImpl {
	return &V2SearchRepositoryImpl{
		db:                      db,
		rls:                     rls,
		embeddingJobMaxAttempts: normalizeV2EmbeddingJobMaxAttempts(maxAttempts),
	}
}

func (r *V2SearchRepositoryImpl) GetActiveSearchContract(ctx context.Context) (*V2ActiveSearchContract, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	var contract V2ActiveSearchContract
	err = db.WithContext(ctx).Raw(`
		SELECT
		    contract.embedding_contract_id::text,
		    generation.search_index_generation_id::text,
		    contract.dimensions,
		    contract.provider,
		    contract.model,
		    contract.distance_metric,
		    contract.vector_normalization,
		    contract.document_format_version,
		    contract.query_format_version,
		    generation.generation,
		    generation.ann_strategy,
		    generation.operator_class,
		    generation.indexed_expression,
		    generation.physical_index_name,
		    generation.exact_max_rows,
		    generation.candidate_limit,
		    generation.allow_exact_fallback
		FROM search_index_generations AS generation
		JOIN embedding_contracts AS contract
		  ON contract.embedding_contract_id = generation.embedding_contract_id
		 AND contract.dimensions = generation.embedding_dimensions
		WHERE generation.activation_state = 'active'
		  AND contract.lifecycle_state = 'active'
		  AND contract.distance_metric = ?
		ORDER BY contract.version DESC, generation.generation DESC, generation.created_at DESC
		LIMIT 1
	`, string(domain.V2VectorDistanceCosine)).Row().Scan(
		&contract.EmbeddingContractID,
		&contract.SearchIndexGenerationID,
		&contract.EmbeddingDimensions,
		&contract.EmbeddingProvider,
		&contract.EmbeddingModel,
		&contract.DistanceMetric,
		&contract.VectorNormalization,
		&contract.DocumentFormatVersion,
		&contract.QueryFormatVersion,
		&contract.IndexGeneration,
		&contract.IndexStrategy,
		&contract.OperatorClass,
		&contract.IndexedExpression,
		&contract.PhysicalIndexName,
		&contract.ExactMaxRows,
		&contract.CandidateLimit,
		&contract.AllowExactFallback,
	)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w: active search contract not found", ErrV2SearchContractMismatch)
	}
	if err != nil {
		return nil, fmt.Errorf("v2 search: load active contract: %w", err)
	}
	if contract.EmbeddingContractID == "" || contract.SearchIndexGenerationID == "" {
		return nil, fmt.Errorf("%w: active search contract not found", ErrV2SearchContractMismatch)
	}
	return &contract, nil
}

func (r *V2SearchRepositoryImpl) CheckSearchReadiness(ctx context.Context) (*V2SearchReadiness, error) {
	contract, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	readiness := &V2SearchReadiness{Ready: true, Contract: contract}
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
	if contract.IndexStrategy != string(domain.V2VectorIndexExact) {
		var indexDefinition string
		if err := db.WithContext(ctx).Raw(`
			SELECT COALESCE((
			    SELECT indexdef
			    FROM pg_indexes
			    WHERE schemaname = current_schema()
			      AND indexname = ?
			), '')
			`, contract.PhysicalIndexName).Scan(&indexDefinition).Error; err != nil {
			return nil, fmt.Errorf("v2 search: readiness index check: %w", err)
		}
		if indexDefinition == "" {
			readiness.Ready = false
			readiness.Reasons = append(readiness.Reasons, V2SearchReadinessReason{
				Code:    "missing_physical_index",
				Message: fmt.Sprintf("physical index %q is missing", contract.PhysicalIndexName),
			})
		} else if missing := v2SearchMissingIndexCompatibility(contract, indexDefinition); len(missing) > 0 {
			readiness.Ready = false
			readiness.Reasons = append(readiness.Reasons, V2SearchReadinessReason{
				Code: "incompatible_physical_index",
				Message: fmt.Sprintf(
					"physical index %q is incompatible with active search contract: missing %s",
					contract.PhysicalIndexName,
					strings.Join(missing, ", "),
				),
			})
		}
	}
	stats, err := r.GetEmbeddingQueueStats(ctx, V2EmbeddingQueueStatsInput{
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions,
	})
	if err != nil {
		return nil, err
	}
	if stats.CutoverBlocking {
		readiness.Ready = false
		if stats.Queued+stats.Processing > 0 {
			readiness.Reasons = append(readiness.Reasons, V2SearchReadinessReason{
				Code:    "embedding_backlog_pending",
				Message: fmt.Sprintf("%d embedding jobs are queued or processing for active search contract", stats.Queued+stats.Processing),
			})
		}
		if stats.ExpiredLeases > 0 {
			readiness.Reasons = append(readiness.Reasons, V2SearchReadinessReason{
				Code:    "expired_embedding_leases",
				Message: fmt.Sprintf("%d embedding jobs have expired leases for active search contract", stats.ExpiredLeases),
			})
		}
		if stats.TerminalFailures > 0 {
			readiness.Reasons = append(readiness.Reasons, V2SearchReadinessReason{
				Code:    "terminal_embedding_failures",
				Message: fmt.Sprintf("%d terminal embedding jobs exist for active search contract", stats.TerminalFailures),
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
	contract, err := r.contractForDocument(ctx, input)
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
				WHERE EXCLUDED.source_version >= search_documents.source_version
				RETURNING team_id::text, search_document_id::text, owner_profile_id::text,
				          source_kind, source_id::text, source_version, document_version,
				          embedding_contract_id::text, embedding_dimensions, search_state
			)
			SELECT * FROM upserted
		`, input.TeamID, input.OwnerProfileID, input.SourceKind, input.SourceID, input.SourceVersion,
			contract.EmbeddingContractID, contract.EmbeddingDimensions, input.DocumentText,
			input.DocumentHash, string(metadata)).Rows()
		if err != nil {
			return err
		}
		if !rows.Next() {
			err := rows.Err()
			_ = rows.Close()
			if err != nil {
				return err
			}
			return ErrV2SearchStaleVersion
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
		jobID, err := enqueueV2EmbeddingJob(ctx, tx, loaded, r.embeddingJobMaxAttempts)
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
			       search_state,
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
	contract, err := r.contractForVectorSearch(ctx, input)
	if err != nil {
		return nil, err
	}
	if len(input.QueryEmbedding) != contract.EmbeddingDimensions {
		return nil, fmt.Errorf("%w: contract dimensions %d, query dimensions %d", ErrV2SearchContractMismatch, contract.EmbeddingDimensions, len(input.QueryEmbedding))
	}
	if contract.IndexStrategy != string(domain.V2VectorIndexExact) && !contract.AllowExactFallback {
		return nil, fmt.Errorf("%w: active search contract does not allow exact vector search", ErrV2SearchContractMismatch)
	}
	if contract.DistanceMetric != string(domain.V2VectorDistanceCosine) {
		return nil, fmt.Errorf("%w: exact vector search supports %s distance only", ErrV2SearchContractMismatch, domain.V2VectorDistanceCosine)
	}
	vectorLiteral, err := v2VectorLiteral(input.QueryEmbedding)
	if err != nil {
		return nil, err
	}
	hits := []V2SearchHit{}
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		sourceFilter := ""
		countArgs := []any{input.TeamID, contract.EmbeddingContractID, contract.EmbeddingDimensions}
		args := []any{vectorLiteral, input.TeamID, contract.EmbeddingContractID, contract.EmbeddingDimensions}
		if input.SourceKind != "" {
			sourceFilter = "AND source_kind = ?"
			countArgs = append(countArgs, input.SourceKind)
			args = append(args, input.SourceKind)
		}
		countArgs = append(countArgs, contract.ExactMaxRows+1)
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
		if candidateCount > int64(contract.ExactMaxRows) {
			return fmt.Errorf("%w: exact vector candidates %d exceed contract max %d", ErrV2SearchContractMismatch, candidateCount, contract.ExactMaxRows)
		}
		args = append(args, vectorLiteral, input.Limit)
		rows, err := tx.WithContext(ctx).Raw(`
				SELECT team_id::text, search_document_id::text, source_kind, source_id::text,
				       source_version, document_version, embedding_contract_id::text,
				       search_state,
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

func (r *V2SearchRepositoryImpl) contractForDocument(ctx context.Context, input V2UpsertSearchDocumentInput) (*V2ActiveSearchContract, error) {
	if input.EmbeddingContractID == "" {
		return r.GetActiveSearchContract(ctx)
	}
	contract, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	if contract.EmbeddingContractID != input.EmbeddingContractID {
		return nil, fmt.Errorf("%w: requested contract %s is not the active contract %s", ErrV2SearchContractMismatch, input.EmbeddingContractID, contract.EmbeddingContractID)
	}
	return contract, nil
}

func (r *V2SearchRepositoryImpl) contractForVectorSearch(ctx context.Context, input V2ExactVectorSearchInput) (*V2ActiveSearchContract, error) {
	contract, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	if input.EmbeddingContractID != "" && input.EmbeddingContractID != contract.EmbeddingContractID {
		return nil, fmt.Errorf("%w: requested contract %s is not the active contract %s", ErrV2SearchContractMismatch, input.EmbeddingContractID, contract.EmbeddingContractID)
	}
	if contract.DistanceMetric != string(domain.V2VectorDistanceCosine) {
		return nil, fmt.Errorf("%w: active search contract distance %q is not supported", ErrV2SearchContractMismatch, contract.DistanceMetric)
	}
	return contract, nil
}

func enqueueV2EmbeddingJob(
	ctx context.Context,
	tx *gorm.DB,
	document V2SearchDocumentResult,
	maxAttempts int,
) (string, error) {
	if document.SearchState != string(domain.V2SearchProjectionPending) {
		return "", nil
	}
	maxAttempts = normalizeV2EmbeddingJobMaxAttempts(maxAttempts)
	var jobID string
	err := tx.WithContext(ctx).Raw(`
		INSERT INTO embedding_jobs (
		    team_id, search_document_id, owner_profile_id, source_kind, source_id,
		    source_version, document_version, embedding_contract_id, embedding_dimensions,
		    max_attempts
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?, ?::uuid,
		    ?, ?, ?::uuid, ?, ?
		)
		ON CONFLICT (
		    team_id, source_kind, source_id, source_version,
		    document_version, embedding_contract_id
		) DO NOTHING
		RETURNING embedding_job_id::text
		`, document.TeamID, document.SearchDocumentID, document.OwnerProfileID,
		document.SourceKind, document.SourceID, document.SourceVersion,
		document.DocumentVersion, document.EmbeddingContractID,
		document.EmbeddingDimensions, maxAttempts).Scan(&jobID).Error
	if err != nil {
		return "", err
	}
	return jobID, nil
}

func normalizeV2EmbeddingJobMaxAttempts(maxAttempts int) int {
	if maxAttempts <= 0 {
		return defaultV2EmbeddingJobMaxAttempts
	}
	return maxAttempts
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
		&hit.SearchState,
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

func (r *V2SearchRepositoryImpl) withSystemTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if _, err := r.database(); err != nil {
		return err
	}
	if r.rls == nil {
		return errors.New("v2 search: rls helper is required")
	}
	return r.rls.WithSystemTx(ctx, r.db, fn)
}

func (r *V2SearchRepositoryImpl) database() (*gorm.DB, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("v2 search: database is required")
	}
	return r.db, nil
}
