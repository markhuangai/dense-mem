package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const conflictResolutionDeletionOnlySourceSummary = "overdue conflict deletion-only derivation"

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
			    projection_format_version, projection_generation_id,
			    document_version, embedding_contract_id, embedding_dimensions,
			    search_state, document_text, document_hash, metadata
			) VALUES (
			    ?::uuid, ?::uuid, ?, ?::uuid, ?, ?, NULLIF(?, '')::uuid, 1, ?::uuid, ?,
			    'pending', ?, ?, ?::jsonb
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
			        THEN search_documents.document_version
			        ELSE search_documents.document_version + 1
			    END,
			    search_state = CASE
			        WHEN search_documents.document_hash = EXCLUDED.document_hash
			         AND search_documents.projection_format_version = EXCLUDED.projection_format_version
			         AND search_documents.search_state = 'current'
			        THEN 'current'
			        ELSE 'pending'
			    END,
			    document_text = EXCLUDED.document_text,
			    document_hash = EXCLUDED.document_hash,
			    embedding = CASE
			        WHEN search_documents.document_hash = EXCLUDED.document_hash
			         AND search_documents.projection_format_version = EXCLUDED.projection_format_version
			        THEN search_documents.embedding
			        ELSE NULL
			    END,
			    embedding_updated_at = CASE
			        WHEN search_documents.document_hash = EXCLUDED.document_hash
			         AND search_documents.projection_format_version = EXCLUDED.projection_format_version
			        THEN search_documents.embedding_updated_at
			        ELSE NULL
			    END,
			    embedding_error = CASE
			        WHEN search_documents.document_hash = EXCLUDED.document_hash
			         AND search_documents.projection_format_version = EXCLUDED.projection_format_version
			        THEN search_documents.embedding_error
			        ELSE ''
			    END,
			    metadata = EXCLUDED.metadata,
			    updated_at = now()
			RETURNING team_id::text, search_document_id::text, owner_profile_id::text,
			          source_kind, source_id::text, source_version,
			          projection_format_version, COALESCE(projection_generation_id::text, ''),
			          document_version,
			          embedding_contract_id::text, embedding_dimensions, search_state
		)
		SELECT * FROM upserted
	`, input.TeamID, input.OwnerProfileID, input.SourceKind, input.SourceID, input.SourceVersion,
		input.ProjectionFormat, input.ProjectionGenerationID,
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
	jobID, err := enqueueEmbeddingJob(ctx, tx, loaded, embeddingJobMaxAttempts)
	if err != nil {
		return nil, err
	}
	loaded.QueuedJobID = jobID
	return &loaded, nil
}

func placementRelationshipSearchText(ctx context.Context, tx *gorm.DB, relationship *RelationshipRecord) (string, error) {
	if relationship == nil {
		return "", errors.New("relationship is required")
	}
	var names relationshipProjectionNames
	err := tx.WithContext(ctx).Raw(`
		SELECT
		    COALESCE(NULLIF(subject_name.display_name, ''), relationship.subject_entity_id::text) AS subject_name,
			    COALESCE(
			        NULLIF(object_name.display_name, ''),
			        NULLIF(value_record.display, ''),
			        NULLIF(value_record.canonical_value, ''),
			        relationship.object_entity_id::text,
			        relationship.object_value_id::text,
			        ''
			    ) AS object_name,
		    COALESCE(value_record.value_type, '') AS object_value_type,
		    COALESCE(value_record.canonical_value, '') AS object_value,
		    COALESCE(value_record.unit, '') AS object_unit
		FROM relationship_records AS relationship
		LEFT JOIN entity_names AS subject_name
		  ON subject_name.team_id = relationship.team_id
		 AND subject_name.entity_id = relationship.subject_entity_id
		 AND subject_name.name_kind = 'canonical'
		 AND subject_name.valid_to IS NULL
		LEFT JOIN entity_names AS object_name
		  ON object_name.team_id = relationship.team_id
		 AND object_name.entity_id = relationship.object_entity_id
		 AND object_name.name_kind = 'canonical'
		 AND object_name.valid_to IS NULL
		LEFT JOIN value_records AS value_record
		  ON value_record.team_id = relationship.team_id
		 AND value_record.value_id = relationship.object_value_id
		WHERE relationship.team_id = ?::uuid
		  AND relationship.relationship_id = ?::uuid
		LIMIT 1
	`, relationship.TeamID, relationship.RelationshipID).Row().Scan(
		&names.SubjectName,
		&names.ObjectName,
		&names.ObjectValueType,
		&names.ObjectValue,
		&names.ObjectUnit,
	)
	if err != nil {
		return "", fmt.Errorf("render relationship projection names: %w", err)
	}
	return relationshipProjectionText(relationship, names), nil
}

type relationshipProjectionNames struct {
	SubjectName     string
	ObjectName      string
	ObjectValueType string
	ObjectValue     string
	ObjectUnit      string
}

func relationshipProjectionText(relationship *RelationshipRecord, names relationshipProjectionNames) string {
	lines := []string{
		"relationship",
		"subject: " + firstNonEmpty(names.SubjectName, relationship.SubjectEntityID),
		"predicate: " + strings.ReplaceAll(relationship.PredicateKey, "_", " "),
		"object: " + firstNonEmpty(names.ObjectName, names.ObjectValue, relationship.ObjectEntityID, relationship.ObjectValueID),
		"polarity: " + relationshipProjectionPolarity(relationship.Polarity),
	}
	if scope := strings.TrimSpace(relationship.ScopeKey); scope != "" {
		lines = append(lines, "scope: "+scope)
	}
	if relationship.ValidFrom != nil {
		lines = append(lines, "valid_from: "+relationship.ValidFrom.UTC().Format(time.RFC3339Nano))
	}
	if relationship.ValidTo != nil {
		lines = append(lines, "valid_to: "+relationship.ValidTo.UTC().Format(time.RFC3339Nano))
	}
	return strings.Join(lines, "\n")
}

func relationshipProjectionPolarity(value string) string {
	if strings.TrimSpace(value) == "-" {
		return "negative"
	}
	return "positive"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func relationshipSearchEligible(relationship *RelationshipRecord) bool {
	return relationship != nil &&
		relationship.IdentityAliasOfID == "" &&
		relationship.Status == "active" &&
		relationship.SupportCount > 0
}

func relationshipSearchDocumentProjectionGenerationID(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipID string,
	embeddingContractID string,
) (string, error) {
	var projectionGenerationID sql.NullString
	err := tx.WithContext(ctx).Raw(`
		SELECT projection_generation_id::text
		FROM search_documents
		WHERE team_id = ?::uuid
		  AND source_kind = 'relationship'
		  AND source_id = ?::uuid
		  AND embedding_contract_id = ?::uuid
		LIMIT 1
	`, teamID, relationshipID, embeddingContractID).Row().Scan(&projectionGenerationID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return projectionGenerationID.String, nil
}

func relationshipForegroundRecallGenerationID(ctx context.Context, tx *gorm.DB, teamID string) (string, error) {
	var projectionGenerationID sql.NullString
	err := tx.WithContext(ctx).Raw(`
		WITH activated AS (
		    SELECT projection_generation_id, generation, created_at
		    FROM search_projection_generations
		    WHERE team_id = ?::uuid
		      AND source_kind = 'relationship'
		      AND projection_format_version = 2
		      AND state = 'current'
		      AND activated_at IS NOT NULL
		    ORDER BY generation DESC, created_at DESC
		    LIMIT 1
		),
		selected AS (
		    SELECT projection_generation_id, 0 AS priority, generation, created_at
		    FROM activated
		    UNION ALL
		    SELECT projection_generation_id, 1 AS priority, generation, created_at
		    FROM search_projection_generations
		    WHERE team_id = ?::uuid
		      AND source_kind = 'relationship'
		      AND projection_format_version = 2
		      AND NOT EXISTS (SELECT 1 FROM activated)
		)
		SELECT projection_generation_id::text
		FROM selected
		ORDER BY priority ASC, generation DESC, created_at DESC
		LIMIT 1
	`, teamID, teamID).Row().Scan(&projectionGenerationID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return projectionGenerationID.String, nil
}

func relationshipForegroundSearchMetadata(ctx context.Context, tx *gorm.DB, teamID string) (map[string]any, error) {
	projectionGenerationID, err := relationshipForegroundRecallGenerationID(ctx, tx, teamID)
	if err != nil || projectionGenerationID == "" {
		return nil, err
	}
	return map[string]any{
		relationshipForegroundRecallGenerationMetadataKey: projectionGenerationID,
	}, nil
}

func refreshPreviousRelationshipProjectionGeneration(ctx context.Context, tx *gorm.DB, teamID string, projectionGenerationID string) error {
	if strings.TrimSpace(projectionGenerationID) == "" {
		return nil
	}
	return refreshRelationshipProjectionGeneration(ctx, tx, teamID, projectionGenerationID)
}

func markRelationshipSearchDocumentNotRequired(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	relationship *RelationshipRecord,
) (*SearchDocumentResult, error) {
	if relationship == nil || relationship.RelationshipID == "" {
		return nil, nil
	}
	contract, err := loadActiveSearchContractInTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	previousGenerationID, err := relationshipSearchDocumentProjectionGenerationID(
		ctx,
		tx,
		commit.TeamID,
		relationship.RelationshipID,
		contract.EmbeddingContractID,
	)
	if err != nil {
		return nil, err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH updated AS (
			UPDATE search_documents
			SET source_version = GREATEST(source_version, ?),
			    projection_format_version = 2,
			    projection_generation_id = NULL,
			    search_state = 'not_required',
			    embedding = NULL,
			    embedding_updated_at = NULL,
			    embedding_error = '',
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND source_kind = 'relationship'
			  AND source_id = ?::uuid
			  AND embedding_contract_id = ?::uuid
			RETURNING team_id::text, search_document_id::text, owner_profile_id::text,
			          source_kind, source_id::text, source_version,
			          projection_format_version, COALESCE(projection_generation_id::text, ''),
			          document_version, embedding_contract_id::text, embedding_dimensions,
			          search_state
		)
		SELECT *
		FROM updated
		ORDER BY search_document_id
		LIMIT 1
	`, int64(relationship.Version), commit.TeamID, relationship.RelationshipID, contract.EmbeddingContractID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
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
		&loaded.ProjectionFormat,
		&loaded.ProjectionGenerationID,
		&loaded.DocumentVersion,
		&loaded.EmbeddingContractID,
		&loaded.EmbeddingDimensions,
		&loaded.SearchState,
	); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := refreshPreviousRelationshipProjectionGeneration(ctx, tx, commit.TeamID, previousGenerationID); err != nil {
		return nil, err
	}
	return &loaded, nil
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

func isConflictResolutionDeletionOnlyFragment(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	fragmentID string,
) (bool, error) {
	var deletionOnly bool
	err := tx.WithContext(ctx).Raw(`
		SELECT COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') = 'true'
		   AND COALESCE(ingest.metadata->>'conflict_resolution_deletion_only', '') = 'true'
		   AND ingest.source_summary = ?
		   AND profile.auth_source = 'system'
		   AND profile.is_system
		FROM evidence_fragments AS fragment
		JOIN knowledge_ingests AS ingest
		  ON ingest.team_id = fragment.team_id
		 AND ingest.ingest_id = fragment.ingest_id
		JOIN team_profiles AS profile
		  ON profile.team_id = fragment.team_id
		 AND profile.id = fragment.owner_profile_id
		WHERE fragment.team_id = ?::uuid
		  AND fragment.fragment_id = ?::uuid
	`, conflictResolutionDeletionOnlySourceSummary, teamID, fragmentID).Row().Scan(&deletionOnly)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return deletionOnly, nil
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
