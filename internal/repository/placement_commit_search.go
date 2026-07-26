package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
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
	embeddingJobMaxAttempts int,
) (*SearchDocumentResult, error) {
	metadata, err := marshalSearchJSON(input.Metadata)
	if err != nil {
		return nil, err
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
		contract.EmbeddingContractID, contract.EmbeddingDimensions, input.DocumentText,
		input.DocumentHash, string(metadata)).Rows()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		_ = rows.Close()
		return nil, rows.Err()
	}
	loaded := SearchDocumentResult{}
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
		return nil, err
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	jobID, err := enqueueEmbeddingJob(ctx, tx, loaded, embeddingJobMaxAttempts)
	if err != nil {
		return nil, err
	}
	loaded.QueuedJobID = jobID
	return &loaded, nil
}

func placementRelationshipSearchText(relationship *RelationshipRecord) string {
	parts := []string{
		"relationship",
		relationship.PredicateKey,
		relationship.SubjectEntityID,
		relationship.ObjectEntityID,
		relationship.ObjectValueID,
		relationship.SemanticGroupKey,
	}
	return strings.Join(parts, " ")
}

func upsertPlacementEvidenceSearchDocument(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	fragmentID string,
	metadata map[string]any,
	embeddingJobMaxAttempts int,
) (*SearchDocumentResult, error) {
	contract, err := loadActiveSearchContractInTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	var content string
	if err := tx.WithContext(ctx).Raw(`
		SELECT content
		FROM evidence_fragments
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND fragment_id = ?::uuid
		LIMIT 1
	`, commit.TeamID, commit.OwnerProfileID, fragmentID).Row().Scan(&content); err != nil {
		return nil, err
	}
	input := normalizeUpsertSearchDocumentInput(UpsertSearchDocumentInput{
		TeamID:         commit.TeamID,
		OwnerProfileID: commit.OwnerProfileID,
		SourceKind:     "evidence",
		SourceID:       fragmentID,
		SourceVersion:  1,
		DocumentText:   content,
		Metadata:       metadata,
	})
	if err := validateUpsertSearchDocumentInput(input); err != nil {
		return nil, err
	}
	return upsertSearchDocumentInTx(ctx, tx, input, contract, embeddingJobMaxAttempts)
}

func loadPlacementItemFragmentID(ctx context.Context, tx *gorm.DB, commit CommitPlacementSemanticInput) (string, error) {
	var fragmentID string
	if err := tx.WithContext(ctx).Raw(`
		SELECT fragment_id::text
		FROM placement_items
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND placement_item_id = ?::uuid
		LIMIT 1
	`, commit.TeamID, commit.OwnerProfileID, commit.PlacementItemID).Row().Scan(&fragmentID); err != nil {
		return "", err
	}
	return fragmentID, nil
}

func upsertPlacementItemEvidenceSearchDocument(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	fragmentID string,
	embeddingJobMaxAttempts int,
) (*SearchDocumentResult, error) {
	return upsertPlacementEvidenceSearchDocument(
		ctx,
		tx,
		commit,
		fragmentID,
		map[string]any{
			"placement_item_id": commit.PlacementItemID,
		},
		embeddingJobMaxAttempts,
	)
}
