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

type inlineEmbeddingResultsContextKey struct{}

// WithInlineEmbeddingResults carries provider vectors into a fenced semantic
// transaction. The provider must have completed before this context is used.
func WithInlineEmbeddingResults(ctx context.Context, results []InlineEmbeddingResult) context.Context {
	copyResults := make([]InlineEmbeddingResult, len(results))
	for index, result := range results {
		copyResults[index] = InlineEmbeddingResult{
			DocumentHash:            result.DocumentHash,
			Embedding:               append([]float32(nil), result.Embedding...),
			EmbeddingContractID:     result.EmbeddingContractID,
			EmbeddingDimensions:     result.EmbeddingDimensions,
			EmbeddingModel:          result.EmbeddingModel,
			SearchIndexGenerationID: result.SearchIndexGenerationID,
			IndexGeneration:         result.IndexGeneration,
		}
	}
	return context.WithValue(ctx, inlineEmbeddingResultsContextKey{}, copyResults)
}

func inlineEmbeddingResults(ctx context.Context) []InlineEmbeddingResult {
	value, _ := ctx.Value(inlineEmbeddingResultsContextKey{}).([]InlineEmbeddingResult)
	return value
}

var (
	ErrSearchStaleVersion                 = errors.New("search stale source or document version")
	ErrSearchContractMismatch             = errors.New("search contract mismatch")
	ErrSearchEmbeddingRequired            = errors.New("synchronous semantic write requires inline embeddings")
	ErrSearchConvergenceAttentionRequired = errors.New("search convergence is attention_required")
	ErrInlineEmbeddingPlanMismatch        = errors.New("inline embedding plan does not match rendered search documents")
	ErrInlineEmbeddingPlanTooLarge        = errors.New("inline embedding plan exceeds the document bound")
)

type SearchRepositoryImpl struct {
	db  *gorm.DB
	rls rLSHelper
}

var _ SearchRepository = (*SearchRepositoryImpl)(nil)

type searchPhysicalIndexState struct {
	Exists     bool
	Valid      bool
	Definition string
}

func loadSearchPhysicalIndexState(
	ctx context.Context,
	db *gorm.DB,
	indexName string,
) (searchPhysicalIndexState, error) {
	var state searchPhysicalIndexState
	err := db.WithContext(ctx).Raw(`
		SELECT index_meta.indisvalid,
		       pg_get_indexdef(index_meta.indexrelid)
		FROM pg_class AS index_class
		JOIN pg_namespace AS index_schema
		  ON index_schema.oid = index_class.relnamespace
		JOIN pg_index AS index_meta
		  ON index_meta.indexrelid = index_class.oid
		WHERE index_schema.nspname = current_schema()
		  AND index_class.relname = ?
	`, indexName).Row().Scan(&state.Valid, &state.Definition)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return state, nil
	}
	if err != nil {
		return searchPhysicalIndexState{}, err
	}
	state.Exists = true
	return state, nil
}

func NewSearchRepository(db *gorm.DB, rls *postgres.RLS) *SearchRepositoryImpl {
	return &SearchRepositoryImpl{
		db:  db,
		rls: rls,
	}
}

func (r *SearchRepositoryImpl) GetActiveSearchContract(ctx context.Context) (*ActiveSearchContract, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	var contract ActiveSearchContract
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
		    generation.query_ef_search,
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
	`, string(domain.VectorDistanceCosine)).Row().Scan(
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
		&contract.QueryEFSearch,
		&contract.ExactMaxRows,
		&contract.CandidateLimit,
		&contract.AllowExactFallback,
	)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w: active search contract not found", ErrSearchContractMismatch)
	}
	if err != nil {
		return nil, fmt.Errorf("search: load active contract: %w", err)
	}
	if contract.EmbeddingContractID == "" || contract.SearchIndexGenerationID == "" {
		return nil, fmt.Errorf("%w: active search contract not found", ErrSearchContractMismatch)
	}
	return &contract, nil
}

func (r *SearchRepositoryImpl) CheckSearchReadiness(ctx context.Context) (*SearchReadiness, error) {
	contract, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	readiness := &SearchReadiness{Ready: true, Contract: contract}
	var vectorPresent bool
	if err := db.WithContext(ctx).Raw(`
		SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')
	`).Scan(&vectorPresent).Error; err != nil {
		return nil, fmt.Errorf("search: readiness extension check: %w", err)
	}
	if !vectorPresent {
		readiness.Ready = false
		readiness.Reasons = append(readiness.Reasons, SearchReadinessReason{
			Code:    "missing_pgvector_extension",
			Message: "pgvector extension is not installed",
		})
	}
	if contract.IndexStrategy != string(domain.VectorIndexExact) {
		indexState, err := loadSearchPhysicalIndexState(ctx, db, contract.PhysicalIndexName)
		if err != nil {
			return nil, fmt.Errorf("search: readiness index check: %w", err)
		}
		if !indexState.Exists {
			readiness.Ready = false
			readiness.Reasons = append(readiness.Reasons, SearchReadinessReason{
				Code:    "missing_physical_index",
				Message: fmt.Sprintf("physical index %q is missing", contract.PhysicalIndexName),
			})
		} else if !indexState.Valid {
			readiness.Ready = false
			readiness.Reasons = append(readiness.Reasons, SearchReadinessReason{
				Code:    "invalid_physical_index",
				Message: fmt.Sprintf("physical index %q is invalid", contract.PhysicalIndexName),
			})
		} else if missing := searchMissingIndexCompatibility(contract, indexState.Definition); len(missing) > 0 {
			readiness.Ready = false
			readiness.Reasons = append(readiness.Reasons, SearchReadinessReason{
				Code: "incompatible_physical_index",
				Message: fmt.Sprintf(
					"physical index %q is incompatible with active search contract: missing %s",
					contract.PhysicalIndexName,
					strings.Join(missing, ", "),
				),
			})
		}
	}
	incompleteRelationships, err := r.relationshipProjectionTextIncomplete(ctx, contract)
	if err != nil {
		return nil, err
	}
	if incompleteRelationships {
		readiness.Ready = false
		readiness.Reasons = append(readiness.Reasons, SearchReadinessReason{
			Code:    "relationship_projection_text_incomplete",
			Message: "eligible relationship search documents are missing projection format 2 text",
		})
	}
	return readiness, nil
}

func (r *SearchRepositoryImpl) relationshipProjectionTextIncomplete(ctx context.Context, contract *ActiveSearchContract) (bool, error) {
	var incomplete bool
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw(`
			WITH activated_generation AS (
			    SELECT DISTINCT ON (team_id)
			           team_id, projection_generation_id
			    FROM search_projection_generations
			    WHERE source_kind = 'relationship'
			      AND projection_format_version = 2
			      AND state = 'current'
			      AND activated_at IS NOT NULL
			    ORDER BY team_id, generation DESC, created_at DESC
			),
			latest_generation AS (
			    SELECT DISTINCT ON (team_id)
			           team_id, projection_generation_id
			    FROM search_projection_generations
			    WHERE source_kind = 'relationship'
			      AND projection_format_version = 2
			    ORDER BY team_id, generation DESC, created_at DESC
			),
			selected_generation AS (
			    SELECT COALESCE(activated.team_id, latest.team_id) AS team_id,
			           COALESCE(activated.projection_generation_id, latest.projection_generation_id) AS projection_generation_id
			    FROM activated_generation AS activated
			    FULL JOIN latest_generation AS latest
			      ON latest.team_id = activated.team_id
			)
			SELECT EXISTS (
			    SELECT 1
			    FROM relationship_records AS relationship
			    LEFT JOIN selected_generation AS generation
			      ON generation.team_id = relationship.team_id
			    WHERE relationship.identity_alias_of_relationship_id IS NULL
			      AND relationship.status = 'active'
			      AND relationship.support_count > 0
			      AND NOT EXISTS (
			          SELECT 1
			          FROM search_documents AS document
			          WHERE document.team_id = relationship.team_id
			            AND document.source_kind = 'relationship'
			            AND document.source_id = relationship.relationship_id
			            AND document.embedding_contract_id = ?::uuid
			            AND document.embedding_dimensions = ?
			            AND document.projection_format_version = 2
			            AND document.search_state IN ('pending', 'current', 'failed')
			            AND (
			                document.projection_generation_id = generation.projection_generation_id
			                OR (
			                    document.projection_generation_id IS NULL
			                    AND (
			                        generation.projection_generation_id IS NULL
			                        OR COALESCE(document.metadata->>'`+relationshipForegroundRecallGenerationMetadataKey+`', '') = generation.projection_generation_id::text
			                    )
			                )
			            )
			      )
			    LIMIT 1
			)
			`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Scan(&incomplete).Error
	})
	if err != nil {
		return false, fmt.Errorf("search: relationship projection readiness: %w", err)
	}
	return incomplete, nil
}

func (r *SearchRepositoryImpl) UpsertSearchDocument(
	ctx context.Context,
	input UpsertSearchDocumentInput,
) (*SearchDocumentResult, error) {
	input = normalizeUpsertSearchDocumentInput(input)
	if err := validateUpsertSearchDocumentInput(input); err != nil {
		return nil, err
	}
	contract, err := r.contractForDocument(ctx, input)
	if err != nil {
		return nil, err
	}
	var result *SearchDocumentResult
	err = r.withActiveTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
		metadata, err := marshalSearchJSON(input.Metadata)
		if err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			WITH upserted AS (
				INSERT INTO search_documents (
				    team_id, owner_profile_id, space_id, space_generation, source_kind, source_id, source_version,
				    projection_format_version, projection_generation_id,
				    document_version, embedding_contract_id, embedding_dimensions,
				    search_state, document_text, document_hash, metadata
				) VALUES (
				    ?::uuid, ?::uuid, COALESCE(NULLIF(?, '')::uuid, dense_mem_team_shared_space(?::uuid)), NULLIF(?, 0)::bigint, ?, ?::uuid, ?, ?, NULLIF(?, '')::uuid, 1, ?::uuid, ?,
				    CASE WHEN ? = 'evidence' AND EXISTS (
				        SELECT 1 FROM evidence_exact_aliases AS alias
				        WHERE alias.team_id = ?::uuid AND alias.alias_fragment_id = ?::uuid
				    ) THEN 'not_required' ELSE 'pending' END,
				    ?, ?, ?::jsonb
				)
				ON CONFLICT (team_id, source_kind, source_id, embedding_contract_id)
				DO UPDATE SET
				    owner_profile_id = EXCLUDED.owner_profile_id,
				    source_version = EXCLUDED.source_version,
				    projection_format_version = EXCLUDED.projection_format_version,
				    projection_generation_id = EXCLUDED.projection_generation_id,
				    document_version = CASE
				        WHEN search_documents.document_hash = EXCLUDED.document_hash
				         AND search_documents.projection_format_version = EXCLUDED.projection_format_version
				         AND search_documents.projection_generation_id IS NOT DISTINCT FROM EXCLUDED.projection_generation_id
				        THEN search_documents.document_version
				        ELSE search_documents.document_version + 1
				    END,
				    search_state = CASE
				        WHEN search_documents.source_kind = 'evidence' AND EXISTS (
				            SELECT 1 FROM evidence_exact_aliases AS alias
				            WHERE alias.team_id = search_documents.team_id
				              AND alias.alias_fragment_id = search_documents.source_id
				        ) THEN 'not_required'
				        WHEN search_documents.document_hash = EXCLUDED.document_hash
				         AND search_documents.projection_format_version = EXCLUDED.projection_format_version
				         AND search_documents.projection_generation_id IS NOT DISTINCT FROM EXCLUDED.projection_generation_id
				         AND search_documents.search_state IN ('current', 'failed')
				        THEN search_documents.search_state
				        ELSE 'pending'
				    END,
				    document_text = EXCLUDED.document_text,
				    document_hash = EXCLUDED.document_hash,
				    embedding = CASE
				        WHEN search_documents.source_kind = 'evidence' AND EXISTS (
				            SELECT 1 FROM evidence_exact_aliases AS alias
				            WHERE alias.team_id = search_documents.team_id
				              AND alias.alias_fragment_id = search_documents.source_id
				        ) THEN NULL
				        WHEN search_documents.document_hash = EXCLUDED.document_hash
				         AND search_documents.projection_format_version = EXCLUDED.projection_format_version
				         AND search_documents.projection_generation_id IS NOT DISTINCT FROM EXCLUDED.projection_generation_id
				        THEN search_documents.embedding
				        ELSE NULL
				    END,
				    embedding_updated_at = CASE
				        WHEN search_documents.source_kind = 'evidence' AND EXISTS (
				            SELECT 1 FROM evidence_exact_aliases AS alias
				            WHERE alias.team_id = search_documents.team_id
				              AND alias.alias_fragment_id = search_documents.source_id
				        ) THEN NULL
				        WHEN search_documents.document_hash = EXCLUDED.document_hash
				         AND search_documents.projection_format_version = EXCLUDED.projection_format_version
				         AND search_documents.projection_generation_id IS NOT DISTINCT FROM EXCLUDED.projection_generation_id
				        THEN search_documents.embedding_updated_at
				        ELSE NULL
				    END,
				    embedding_error = CASE
				        WHEN search_documents.source_kind = 'evidence' AND EXISTS (
				            SELECT 1 FROM evidence_exact_aliases AS alias
				            WHERE alias.team_id = search_documents.team_id
				              AND alias.alias_fragment_id = search_documents.source_id
				        ) THEN ''
				        WHEN search_documents.document_hash = EXCLUDED.document_hash
				         AND search_documents.projection_format_version = EXCLUDED.projection_format_version
				         AND search_documents.projection_generation_id IS NOT DISTINCT FROM EXCLUDED.projection_generation_id
				        THEN search_documents.embedding_error
				        ELSE ''
				    END,
				    metadata = EXCLUDED.metadata,
				    updated_at = now()
				WHERE EXCLUDED.source_version >= search_documents.source_version
				  AND search_documents.space_id = EXCLUDED.space_id
				  AND search_documents.space_generation = EXCLUDED.space_generation
				RETURNING team_id::text, search_document_id::text, owner_profile_id::text,
				          space_id::text, space_generation,
				          source_kind, source_id::text, source_version,
				          projection_format_version, COALESCE(projection_generation_id::text, ''),
				          document_version,
				          embedding_contract_id::text, embedding_dimensions, search_state
			)
			SELECT * FROM upserted
			`, input.TeamID, input.OwnerProfileID, input.SpaceID, input.TeamID, input.SpaceGeneration, input.SourceKind, input.SourceID, input.SourceVersion,
			input.ProjectionFormat, input.ProjectionGenerationID,
			contract.EmbeddingContractID, contract.EmbeddingDimensions, input.SourceKind, input.TeamID, input.SourceID,
			input.DocumentText,
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
			return ErrSearchStaleVersion
		}
		loaded := SearchDocumentResult{}
		if err := rows.Scan(
			&loaded.TeamID,
			&loaded.SearchDocumentID,
			&loaded.OwnerProfileID,
			&loaded.SpaceID,
			&loaded.SpaceGeneration,
			&loaded.SourceKind,
			&loaded.SourceID,
			&loaded.SourceVersion,
			&loaded.ProjectionFormat,
			&loaded.ProjectionGenerationID,
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
		result = &loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("search: upsert document: %w", err)
	}
	return result, nil
}

func (r *SearchRepositoryImpl) SearchFullText(ctx context.Context, input FullTextSearchInput) ([]SearchHit, error) {
	input = normalizeFullTextSearchInput(input)
	if err := validateFullTextSearchInput(input); err != nil {
		return nil, err
	}
	hits := []SearchHit{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		sourceFilter := ""
		args := []any{input.TeamID, input.Query, input.TeamID, input.Query}
		if input.SourceKind != "" {
			sourceFilter = "AND document.source_kind = ?"
			args = append(args, input.SourceKind)
		}
		args = append(args, input.Limit)
		rows, err := tx.WithContext(ctx).Raw(`
			WITH `+recallRelationshipGenerationScopeSQL+`
			SELECT document.team_id::text, document.search_document_id::text, document.source_kind, document.source_id::text,
			       document.source_version, document.document_version, document.embedding_contract_id::text,
			       document.search_state,
			       0::double precision AS distance,
			       ts_rank_cd(document.search_tsv, plainto_tsquery('simple', ?))::double precision AS text_rank
			FROM recall_relationship_generation AS generation
			JOIN search_documents AS document
			  ON document.team_id = ?::uuid
			WHERE document.search_state IN ('pending', 'current', 'failed')
			  AND document.search_tsv @@ plainto_tsquery('simple', ?)
			  AND (
			      document.source_kind <> 'evidence'
			      OR NOT EXISTS (
			          SELECT 1
			          FROM evidence_exact_aliases AS alias
			          WHERE alias.team_id = document.team_id
			            AND alias.alias_fragment_id = document.source_id
			      )
			  )
			  AND (
			      document.source_kind <> 'relationship'
			      OR (
			          document.projection_format_version = 2
			          AND `+recallRelationshipGenerationDocumentSQL+`
			      )
			  )
			  `+sourceFilter+`
			ORDER BY text_rank DESC, document.updated_at DESC, document.search_document_id ASC
			LIMIT ?
		`, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			hit, err := scanSearchHit(rows)
			if err != nil {
				return err
			}
			hits = append(hits, hit)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("search: full-text search: %w", err)
	}
	return hits, nil
}

func (r *SearchRepositoryImpl) SearchExactVector(ctx context.Context, input ExactVectorSearchInput) ([]SearchHit, error) {
	input = normalizeExactVectorSearchInput(input)
	if err := validateExactVectorSearchInput(input); err != nil {
		return nil, err
	}
	contract, err := r.contractForVectorSearch(ctx, input)
	if err != nil {
		return nil, err
	}
	if len(input.QueryEmbedding) != contract.EmbeddingDimensions {
		return nil, fmt.Errorf("%w: contract dimensions %d, query dimensions %d", ErrSearchContractMismatch, contract.EmbeddingDimensions, len(input.QueryEmbedding))
	}
	if contract.IndexStrategy != string(domain.VectorIndexExact) && !contract.AllowExactFallback {
		return nil, fmt.Errorf("%w: active search contract does not allow exact vector search", ErrSearchContractMismatch)
	}
	if contract.DistanceMetric != string(domain.VectorDistanceCosine) {
		return nil, fmt.Errorf("%w: exact vector search supports %s distance only", ErrSearchContractMismatch, domain.VectorDistanceCosine)
	}
	vectorLiteral, err := vectorLiteral(input.QueryEmbedding)
	if err != nil {
		return nil, err
	}
	hits := []SearchHit{}
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
				  AND (
				      source_kind <> 'evidence'
				      OR NOT EXISTS (
				          SELECT 1
				          FROM evidence_exact_aliases AS alias
				          WHERE alias.team_id = search_documents.team_id
				            AND alias.alias_fragment_id = search_documents.source_id
				      )
				  )
				  AND (
				      source_kind <> 'relationship'
					      OR (
					          projection_format_version = 2
					          AND (
					              NOT EXISTS (
					                  SELECT 1
					                  FROM search_projection_generations AS generation
					                  WHERE generation.team_id = search_documents.team_id
					                    AND generation.source_kind = 'relationship'
					                    AND generation.projection_format_version = search_documents.projection_format_version
					              )
					              OR EXISTS (
					              SELECT 1
					              FROM search_projection_generations AS generation
					              WHERE generation.team_id = search_documents.team_id
					                AND generation.source_kind = 'relationship'
					                AND generation.projection_format_version = search_documents.projection_format_version
					                AND generation.state = 'current'
				                AND (
				                    generation.projection_generation_id = search_documents.projection_generation_id
				                    OR (
				                        search_documents.projection_generation_id IS NULL
				                        AND COALESCE(search_documents.metadata->>'`+relationshipForegroundRecallGenerationMetadataKey+`', '') = generation.projection_generation_id::text
				                    )
				                )
					              )
					          )
				      )
				  )
				  `+sourceFilter+`
				LIMIT ?
			) AS exact_candidates
		`, countArgs...).Scan(&candidateCount).Error; err != nil {
			return err
		}
		if candidateCount > int64(contract.ExactMaxRows) {
			return fmt.Errorf("%w: exact vector candidates %d exceed contract max %d", ErrSearchContractMismatch, candidateCount, contract.ExactMaxRows)
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
				  AND (
				      source_kind <> 'evidence'
				      OR NOT EXISTS (
				          SELECT 1
				          FROM evidence_exact_aliases AS alias
				          WHERE alias.team_id = search_documents.team_id
				            AND alias.alias_fragment_id = search_documents.source_id
				      )
				  )
				  AND (
					      source_kind <> 'relationship'
						      OR (
						          projection_format_version = 2
						          AND (
						              NOT EXISTS (
						                  SELECT 1
						                  FROM search_projection_generations AS generation
						                  WHERE generation.team_id = search_documents.team_id
						                    AND generation.source_kind = 'relationship'
						                    AND generation.projection_format_version = search_documents.projection_format_version
						              )
						              OR EXISTS (
						              SELECT 1
						              FROM search_projection_generations AS generation
						              WHERE generation.team_id = search_documents.team_id
						                AND generation.source_kind = 'relationship'
						                AND generation.projection_format_version = search_documents.projection_format_version
						                AND generation.state = 'current'
				                AND (
				                    generation.projection_generation_id = search_documents.projection_generation_id
				                    OR (
				                        search_documents.projection_generation_id IS NULL
				                        AND COALESCE(search_documents.metadata->>'`+relationshipForegroundRecallGenerationMetadataKey+`', '') = generation.projection_generation_id::text
				                    )
				                )
						              )
						          )
					      )
					  )
				  `+sourceFilter+`
				ORDER BY embedding <=> ?::vector ASC, search_document_id ASC
				LIMIT ?
			`, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			hit, err := scanSearchHit(rows)
			if err != nil {
				return err
			}
			hits = append(hits, hit)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("search: exact vector search: %w", err)
	}
	return hits, nil
}

func (r *SearchRepositoryImpl) contractForDocument(ctx context.Context, input UpsertSearchDocumentInput) (*ActiveSearchContract, error) {
	if input.EmbeddingContractID == "" {
		return r.GetActiveSearchContract(ctx)
	}
	contract, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	if contract.EmbeddingContractID != input.EmbeddingContractID {
		return nil, fmt.Errorf("%w: requested contract %s is not the active contract %s", ErrSearchContractMismatch, input.EmbeddingContractID, contract.EmbeddingContractID)
	}
	return contract, nil
}

func (r *SearchRepositoryImpl) contractForVectorSearch(ctx context.Context, input ExactVectorSearchInput) (*ActiveSearchContract, error) {
	contract, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	if input.EmbeddingContractID != "" && input.EmbeddingContractID != contract.EmbeddingContractID {
		return nil, fmt.Errorf("%w: requested contract %s is not the active contract %s", ErrSearchContractMismatch, input.EmbeddingContractID, contract.EmbeddingContractID)
	}
	if contract.DistanceMetric != string(domain.VectorDistanceCosine) {
		return nil, fmt.Errorf("%w: active search contract distance %q is not supported", ErrSearchContractMismatch, contract.DistanceMetric)
	}
	return contract, nil
}

type searchHitScanner interface {
	Scan(dest ...any) error
}

func scanSearchHit(scanner searchHitScanner) (SearchHit, error) {
	var hit SearchHit
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

func (r *SearchRepositoryImpl) withActiveTeamProfileTx(ctx context.Context, teamID, profileID string, fn func(tx *gorm.DB) error) error {
	if _, err := r.database(); err != nil {
		return err
	}
	if r.rls == nil {
		return errors.New("search: rls helper is required")
	}
	return r.rls.WithTeamProfileTx(ctx, r.db, teamID, profileID, func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (r *SearchRepositoryImpl) withTeamTx(ctx context.Context, teamID string, fn func(tx *gorm.DB) error) error {
	if _, err := r.database(); err != nil {
		return err
	}
	if r.rls == nil {
		return errors.New("search: rls helper is required")
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
}

func (r *SearchRepositoryImpl) withActiveTeamTx(ctx context.Context, teamID string, fn func(tx *gorm.DB) error) error {
	if _, err := r.database(); err != nil {
		return err
	}
	if r.rls == nil {
		return errors.New("search: rls helper is required")
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID); err != nil {
			return err
		}
		return fn(tx)
	})
}

// withActiveSystemTeamTx runs internal worker work with system visibility while
// retaining an explicit active-team fence and caller-supplied team predicates.
// Background workers have no request actor, so team-mode RLS would hide
// private memory spaces from their generation checks.
func (r *SearchRepositoryImpl) withActiveSystemTeamTx(ctx context.Context, teamID string, fn func(tx *gorm.DB) error) error {
	if _, err := r.database(); err != nil {
		return err
	}
	if r.rls == nil {
		return errors.New("search: rls helper is required")
	}
	return r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('app.current_team_id', ?, true)", teamID).Error; err != nil {
			return fmt.Errorf("failed to set app.current_team_id: %w", err)
		}
		if err := ensureActiveTeamForMutation(ctx, tx, teamID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (r *SearchRepositoryImpl) withSystemTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if _, err := r.database(); err != nil {
		return err
	}
	if r.rls == nil {
		return errors.New("search: rls helper is required")
	}
	return r.rls.WithSystemTx(ctx, r.db, fn)
}

func (r *SearchRepositoryImpl) database() (*gorm.DB, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("search: database is required")
	}
	return r.db, nil
}
