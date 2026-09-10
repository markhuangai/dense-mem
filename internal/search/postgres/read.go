package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

type searchPhysicalIndexState struct {
	Exists     bool
	Valid      bool
	Definition string
}

func loadSearchPhysicalIndexState(ctx context.Context, db *gorm.DB, indexName string) (searchPhysicalIndexState, error) {
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

func (r *Store) GetActiveSearchContract(ctx context.Context) (*searchcontract.ActiveSearchContract, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	var contract searchcontract.ActiveSearchContract
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

func (r *Store) CheckSearchReadiness(ctx context.Context) (*searchcontract.SearchReadiness, error) {
	contract, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	readiness := &searchcontract.SearchReadiness{Ready: true, Contract: contract}
	var vectorPresent bool
	if err := db.WithContext(ctx).Raw(`
		SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')
	`).Scan(&vectorPresent).Error; err != nil {
		return nil, fmt.Errorf("search: readiness extension check: %w", err)
	}
	if !vectorPresent {
		readiness.Ready = false
		readiness.Reasons = append(readiness.Reasons, searchcontract.SearchReadinessReason{
			Code: "missing_pgvector_extension", Message: "pgvector extension is not installed",
		})
	}
	if contract.IndexStrategy != string(domain.VectorIndexExact) {
		indexState, err := loadSearchPhysicalIndexState(ctx, db, contract.PhysicalIndexName)
		if err != nil {
			return nil, fmt.Errorf("search: readiness index check: %w", err)
		}
		if !indexState.Exists {
			readiness.Ready = false
			readiness.Reasons = append(readiness.Reasons, searchcontract.SearchReadinessReason{
				Code: "missing_physical_index", Message: fmt.Sprintf("physical index %q is missing", contract.PhysicalIndexName),
			})
		} else if !indexState.Valid {
			readiness.Ready = false
			readiness.Reasons = append(readiness.Reasons, searchcontract.SearchReadinessReason{
				Code: "invalid_physical_index", Message: fmt.Sprintf("physical index %q is invalid", contract.PhysicalIndexName),
			})
		} else if missing := searchMissingIndexCompatibility(contract, indexState.Definition); len(missing) > 0 {
			readiness.Ready = false
			readiness.Reasons = append(readiness.Reasons, searchcontract.SearchReadinessReason{
				Code:    "incompatible_physical_index",
				Message: fmt.Sprintf("physical index %q is incompatible with active search contract: missing %s", contract.PhysicalIndexName, strings.Join(missing, ", ")),
			})
		}
	}
	incompleteRelationships, err := r.relationshipProjectionTextIncomplete(ctx, contract)
	if err != nil {
		return nil, err
	}
	if incompleteRelationships {
		readiness.Ready = false
		readiness.Reasons = append(readiness.Reasons, searchcontract.SearchReadinessReason{
			Code: "relationship_projection_text_incomplete", Message: "eligible relationship search documents are missing projection format 2 text",
		})
	}
	return readiness, nil
}

func (r *Store) relationshipProjectionTextIncomplete(ctx context.Context, contract *searchcontract.ActiveSearchContract) (bool, error) {
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

func (r *Store) SearchFullText(ctx context.Context, input searchcontract.FullTextSearchInput) ([]searchcontract.SearchHit, error) {
	input = normalizeFullTextSearchInput(input)
	if err := validateFullTextSearchInput(input); err != nil {
		return nil, err
	}
	hits := []searchcontract.SearchHit{}
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

func (r *Store) SearchExactVector(ctx context.Context, input searchcontract.ExactVectorSearchInput) ([]searchcontract.SearchHit, error) {
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
	hits := []searchcontract.SearchHit{}
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		sourceFilter := ""
		countArgs := []any{input.TeamID, input.TeamID, contract.EmbeddingContractID, contract.EmbeddingDimensions}
		args := []any{input.TeamID, vectorLiteral, input.TeamID, contract.EmbeddingContractID, contract.EmbeddingDimensions}
		if input.SourceKind != "" {
			sourceFilter = "AND document.source_kind = ?"
			countArgs = append(countArgs, input.SourceKind)
			args = append(args, input.SourceKind)
		}
		countArgs = append(countArgs, contract.ExactMaxRows+1)
		var candidateCount int64
		if err := tx.WithContext(ctx).Raw(`
			WITH `+recallRelationshipGenerationScopeSQL+`
			SELECT count(*)
			FROM (
				SELECT document.search_document_id
				FROM recall_relationship_generation AS generation
				JOIN search_documents AS document
				  ON document.team_id = ?::uuid
				WHERE document.embedding_contract_id = ?::uuid
				  AND document.embedding_dimensions = ?
				  AND document.search_state = 'current'
				  AND document.embedding IS NOT NULL
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
				WITH `+recallRelationshipGenerationScopeSQL+`
				SELECT document.team_id::text, document.search_document_id::text, document.source_kind, document.source_id::text,
				       document.source_version, document.document_version, document.embedding_contract_id::text,
				       document.search_state,
				       (document.embedding <=> ?::vector)::double precision AS distance,
				       0::double precision AS text_rank
				FROM recall_relationship_generation AS generation
				JOIN search_documents AS document
				  ON document.team_id = ?::uuid
				WHERE document.embedding_contract_id = ?::uuid
				  AND document.embedding_dimensions = ?
				  AND document.search_state = 'current'
				  AND document.embedding IS NOT NULL
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
				ORDER BY document.embedding <=> ?::vector ASC, document.search_document_id ASC
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

func (r *Store) contractForVectorSearch(ctx context.Context, input searchcontract.ExactVectorSearchInput) (*searchcontract.ActiveSearchContract, error) {
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

func scanSearchHit(scanner searchHitScanner) (searchcontract.SearchHit, error) {
	var hit searchcontract.SearchHit
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

// ScanSearchHit is the compatibility seam used by retained recall query
// helpers while the native adapter owns search-row scanning.
func ScanSearchHit(scanner searchHitScanner) (searchcontract.SearchHit, error) {
	return scanSearchHit(scanner)
}
