package postgres

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// UpsertSearchDocument is the owner for standalone search projection writes.
// It deliberately marks the call as an explicit non-provider path: callers
// may leave a document pending for the configured embedding coordinator, while
// semantic commits pass their actual inline vectors through the same helper.
func (r *Store) UpsertSearchDocument(ctx context.Context, input UpsertSearchDocumentInput) (*SearchDocumentResult, error) {
	input = normalizeUpsertSearchDocumentInput(input)
	if err := validateUpsertSearchDocumentInput(input); err != nil {
		return nil, err
	}
	ctx = WithInlineEmbeddingResults(ctx, []InlineEmbeddingResult{})
	var result *SearchDocumentResult
	err := r.withActiveTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
		contract, err := loadActiveSearchContractInTx(ctx, tx)
		if err != nil {
			return err
		}
		if input.EmbeddingContractID != "" && input.EmbeddingContractID != contract.EmbeddingContractID {
			return fmt.Errorf("%w: requested contract %s is not the active contract %s", ErrSearchContractMismatch, input.EmbeddingContractID, contract.EmbeddingContractID)
		}
		result, err = upsertSearchDocumentInTx(ctx, tx, input, contract)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("search: upsert document: %w", err)
	}
	return result, nil
}
