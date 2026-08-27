package repository

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// CheckSearchConvergence is the startup health probe for the active search
// contract. It checks canonical documents directly; no queue or worker state
// participates in readiness.
func (r *SearchRepositoryImpl) CheckSearchConvergence(ctx context.Context) error {
	contract, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return err
	}
	var attentionRequired bool
	err = r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw(`
			SELECT EXISTS (
			    SELECT 1
			    FROM search_documents AS document
			    JOIN teams AS team
			      ON team.id = document.team_id
			     AND team.status = 'active'
			     AND team.deleted_at IS NULL
			    WHERE document.embedding_contract_id = ?::uuid
			      AND document.embedding_dimensions = ?
			      AND document.search_state <> 'not_required'
			      AND (
			        document.search_state <> 'current'
			        OR document.embedding IS NULL
			        OR vector_dims(document.embedding) <> document.embedding_dimensions
			      )
			    LIMIT 1
			)
		`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Scan(&attentionRequired).Error
	})
	if err != nil {
		return fmt.Errorf("search: convergence health: %w", err)
	}
	if attentionRequired {
		return ErrSearchConvergenceAttentionRequired
	}
	return nil
}
