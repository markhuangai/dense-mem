package repository

import (
	"context"

	"gorm.io/gorm"

	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
)

// canonicalSearchDocument preserves the retained repository fixture seam while
// forwarding canonical projection policy to the native search adapter.
func canonicalSearchDocument(ctx context.Context, tx *gorm.DB, document SearchDocumentForEmbedding) (*SearchDocumentForEmbedding, bool, error) {
	expected, known, err := searchpostgres.CanonicalSearchDocument(ctx, tx, searchmaintenance.SearchDocumentForEmbedding{
		SearchDocumentResult: searchmaintenance.SearchDocumentResult{
			TeamID:                 document.TeamID,
			SearchDocumentID:       document.SearchDocumentID,
			OwnerProfileID:         document.OwnerProfileID,
			SourceKind:             document.SourceKind,
			SourceID:               document.SourceID,
			SourceVersion:          document.SourceVersion,
			ProjectionFormat:       document.ProjectionFormat,
			ProjectionGenerationID: document.ProjectionGenerationID,
			DocumentVersion:        document.DocumentVersion,
			EmbeddingContractID:    document.EmbeddingContractID,
			EmbeddingDimensions:    document.EmbeddingDimensions,
			SearchState:            document.SearchState,
			SpaceID:                document.SpaceID,
			SpaceGeneration:        document.SpaceGeneration,
		},
		DocumentText:       document.DocumentText,
		DocumentHash:       document.DocumentHash,
		StoredDocumentHash: document.StoredDocumentHash,
		Retired:            document.Retired,
	})
	if err != nil || expected == nil {
		return nil, known, err
	}
	converted := searchDocumentFromNative(*expected)
	return &converted, known, nil
}
