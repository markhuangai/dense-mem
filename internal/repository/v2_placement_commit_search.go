package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

func loadV2ActiveSearchProfileInTx(ctx context.Context, tx *gorm.DB, profileKey string) (*V2SearchProfile, error) {
	profileKey = normalizeV2SearchProfileKey(profileKey)
	var profile V2SearchProfile
	err := tx.WithContext(ctx).Raw(`
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
		return nil, err
	}
	return &profile, nil
}

func upsertV2SearchDocumentInTx(
	ctx context.Context,
	tx *gorm.DB,
	input V2UpsertSearchDocumentInput,
	profile *V2SearchProfile,
) (*V2SearchDocumentResult, error) {
	metadata, err := marshalV2SearchJSON(input.Metadata)
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
		profile.EmbeddingContractID, profile.EmbeddingDimensions, input.DocumentText,
		input.DocumentHash, string(metadata)).Rows()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		_ = rows.Close()
		return nil, rows.Err()
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
		return nil, err
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	jobID, err := enqueueV2EmbeddingJob(ctx, tx, loaded)
	if err != nil {
		return nil, err
	}
	loaded.QueuedJobID = jobID
	return &loaded, nil
}

func v2PlacementRelationshipSearchText(relationship *V2RelationshipRecord) string {
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

func upsertV2PlacementEvidenceSearchDocument(
	ctx context.Context,
	tx *gorm.DB,
	commit V2CommitPlacementSemanticInput,
	fragmentID string,
	relationship *V2RelationshipRecord,
	supportID string,
) (*V2SearchDocumentResult, error) {
	profile, err := loadV2ActiveSearchProfileInTx(ctx, tx, commit.SearchProfileKey)
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
	input := normalizeV2UpsertSearchDocumentInput(V2UpsertSearchDocumentInput{
		TeamID:         commit.TeamID,
		OwnerProfileID: commit.OwnerProfileID,
		ProfileKey:     commit.SearchProfileKey,
		SourceKind:     "evidence",
		SourceID:       fragmentID,
		SourceVersion:  1,
		DocumentText:   content,
		Metadata: map[string]any{
			"relationship_id": relationship.RelationshipID,
			"support_id":      supportID,
		},
	})
	if err := validateV2UpsertSearchDocumentInput(input); err != nil {
		return nil, err
	}
	return upsertV2SearchDocumentInTx(ctx, tx, input, profile)
}
