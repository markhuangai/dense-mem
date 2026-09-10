package postgres

import (
	"context"
	"errors"

	"gorm.io/gorm"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
)

func loadRelationshipRecord(ctx context.Context, tx *gorm.DB, teamID, relationshipID string) (*knowledgecontract.RelationshipRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, relationship_id::text, owner_profile_id::text,
		       space_id::text,
		       space_generation,
		       semantic_group_key, subject_entity_id::text, predicate_key,
		       predicate_version, COALESCE(object_entity_id::text, ''),
		       COALESCE(object_value_id::text, ''), relationship_kind,
		       current_cardinality, status, polarity, COALESCE(scope_key, ''),
		       valid_from, valid_to,
		       COALESCE(identity_alias_of_relationship_id::text, ''),
		       support_count, source_group_count, version
		FROM relationship_records
		WHERE team_id = ?::uuid
		  AND relationship_id = ?::uuid
	`, teamID, relationshipID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, gorm.ErrRecordNotFound
	}
	var record knowledgecontract.RelationshipRecord
	if err := rows.Scan(&record.TeamID, &record.RelationshipID, &record.OwnerProfileID, &record.SpaceID, &record.SpaceGeneration,
		&record.SemanticGroupKey, &record.SubjectEntityID, &record.PredicateKey,
		&record.PredicateVersion, &record.ObjectEntityID, &record.ObjectValueID,
		&record.RelationshipKind, &record.CurrentCardinality, &record.Status,
		&record.Polarity, &record.ScopeKey, &record.ValidFrom,
		&record.ValidTo, &record.IdentityAliasOfID, &record.SupportCount,
		&record.SourceGroupCount, &record.Version); err != nil {
		return nil, err
	}
	return &record, rows.Err()
}

func searchDocumentResultFromKnowledge(value *knowledgecontract.SearchDocumentResult) *searchmaintenance.SearchDocumentResult {
	if value == nil {
		return nil
	}
	return &searchmaintenance.SearchDocumentResult{
		TeamID: value.TeamID, SearchDocumentID: value.SearchDocumentID, OwnerProfileID: value.OwnerProfileID,
		SourceKind: value.SourceKind, SourceID: value.SourceID, SourceVersion: value.SourceVersion,
		ProjectionFormat: value.ProjectionFormat, ProjectionGenerationID: value.ProjectionGenerationID,
		DocumentVersion: value.DocumentVersion, EmbeddingContractID: value.EmbeddingContractID,
		EmbeddingDimensions: value.EmbeddingDimensions, SearchState: value.SearchState,
		SpaceID: value.SpaceID, SpaceGeneration: value.SpaceGeneration,
	}
}

func upsertSearchDocumentInTx(ctx context.Context, tx *gorm.DB, input searchmaintenance.UpsertSearchDocumentInput, contract *searchcontract.ActiveSearchContract) (*searchmaintenance.SearchDocumentResult, error) {
	result, err := knowledgepostgres.UpsertSearchDocumentInTx(
		knowledgepostgres.WithInlineEmbeddingResults(ctx, []knowledgepostgres.InlineEmbeddingResult{}),
		knowledgepostgres.LegacyTransaction(tx),
		knowledgepostgres.UpsertSearchDocumentInput(input),
		(*knowledgecontract.ActiveSearchContract)(contract),
	)
	if errors.Is(err, knowledgepostgres.ErrSearchStaleVersion) {
		return nil, nil
	}
	return searchDocumentResultFromKnowledge(result), err
}
