package repository

import (
	"context"

	"gorm.io/gorm"
)

func searchRecallRelationshipFullText(
	ctx context.Context,
	tx *gorm.DB,
	input RecallRelationshipsInput,
	contract *ActiveSearchContract,
	limit int,
) ([]SearchHit, error) {
	eventAt := recallEventAt(input.ValidAt, input.KnownAt)
	spaceClause := recallSpacePredicate("document.space_id", input.TeamID, input.SpaceID, input.SpaceKind)
	rows, err := tx.WithContext(ctx).Raw(`
		WITH `+recallRelationshipGenerationScopeSQL+`
		SELECT document.team_id::text, document.search_document_id::text, document.source_kind,
		       document.source_id::text, document.source_version, document.document_version,
		       document.embedding_contract_id::text, document.search_state,
		       0::double precision AS distance,
		       ts_rank_cd(document.search_tsv, plainto_tsquery('simple', ?))::double precision AS text_rank
		FROM recall_relationship_generation AS generation
		JOIN search_documents AS document
		  ON document.team_id = ?::uuid
		 AND document.source_kind = 'relationship'
		 AND document.embedding_contract_id = ?::uuid
		 AND document.projection_format_version = 2
		 AND `+recallRelationshipGenerationDocumentSQL+`
		 AND (document.search_state IN ('pending', 'current', 'failed') OR (?::timestamptz IS NOT NULL AND document.search_state = 'not_required'))
		WHERE document.search_tsv @@ plainto_tsquery('simple', ?)
		  `+spaceClause+`
		ORDER BY text_rank DESC, document.updated_at DESC, document.search_document_id ASC
		LIMIT ?
	`, input.TeamID, input.Query, input.TeamID, contract.EmbeddingContractID, eventAt, input.Query, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSearchHits(rows)
}
