package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/markhuangai/dense-mem/internal/domain"
	"gorm.io/gorm"
)

func loadActiveSearchContractInTx(ctx context.Context, tx *gorm.DB) (*ActiveSearchContract, error) {
	var contract ActiveSearchContract
	err := tx.WithContext(ctx).Raw(`
		SELECT
		    contract.embedding_contract_id::text,
		    contract.dimensions,
		    contract.provider,
		    contract.model,
		    contract.distance_metric,
		    contract.vector_normalization,
		    contract.document_format_version,
		    contract.query_format_version,
		    generation.search_index_generation_id::text,
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
		&contract.EmbeddingDimensions,
		&contract.EmbeddingProvider,
		&contract.EmbeddingModel,
		&contract.DistanceMetric,
		&contract.VectorNormalization,
		&contract.DocumentFormatVersion,
		&contract.QueryFormatVersion,
		&contract.SearchIndexGenerationID,
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
		return nil, err
	}
	if contract.EmbeddingContractID == "" || contract.SearchIndexGenerationID == "" {
		return nil, fmt.Errorf("%w: active search contract not found", ErrSearchContractMismatch)
	}
	return &contract, nil
}

func upsertSearchDocumentInTx(
	ctx context.Context,
	tx *gorm.DB,
	input UpsertSearchDocumentInput,
	contract *ActiveSearchContract,
) (*SearchDocumentResult, error) {
	metadata, err := marshalSearchJSON(input.Metadata)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	if !rows.Next() {
		err := rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrSearchStaleVersion
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
		return nil, err
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// A semantic writer must carry provider results when the rendered text
	// changed or no current vector exists. A same-hash current document already
	// has the required vector, so retaining it does not recreate queue work.
	if inlineEmbeddingResults(ctx) == nil && loaded.SearchState != string(domain.SearchProjectionCurrent) &&
		loaded.SearchState != string(domain.SearchProjectionNotRequired) {
		return nil, ErrSearchEmbeddingRequired
	}
	return &loaded, nil
}

func upsertSemanticEvidenceSearchDocumentWithContract(
	ctx context.Context,
	tx *gorm.DB,
	commit RememberCommitScope,
	fragmentID string,
	metadata map[string]any,
	contract *ActiveSearchContract,
) (*SearchDocumentResult, error) {
	if contract == nil {
		return nil, errors.New("active search contract is required")
	}
	var content string
	var spaceID string
	var spaceGeneration int64
	if err := tx.WithContext(ctx).Raw(`
		SELECT content, space_id::text, COALESCE(space_generation, 0)
		FROM evidence_fragments
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND fragment_id = ?::uuid
		LIMIT 1
	`, commit.TeamID, commit.OwnerProfileID, fragmentID).Row().Scan(&content, &spaceID, &spaceGeneration); err != nil {
		return nil, err
	}
	input := normalizeUpsertSearchDocumentInput(UpsertSearchDocumentInput{
		TeamID:          commit.TeamID,
		OwnerProfileID:  commit.OwnerProfileID,
		SourceKind:      "evidence",
		SourceID:        fragmentID,
		SourceVersion:   1,
		DocumentText:    content,
		Metadata:        metadata,
		SpaceID:         spaceID,
		SpaceGeneration: spaceGeneration,
	})
	if err := validateUpsertSearchDocumentInput(input); err != nil {
		return nil, err
	}
	return upsertSearchDocumentInTx(ctx, tx, input, contract)
}

func appendSemanticSearchDocument(result *submissionSemanticCommitState, document *SearchDocumentResult) {
	if result == nil || document == nil || document.SearchDocumentID == "" {
		return
	}
	for _, existing := range result.SearchDocuments {
		if existing.SearchDocumentID == document.SearchDocumentID {
			return
		}
	}
	result.SearchDocuments = append(result.SearchDocuments, *document)
}
