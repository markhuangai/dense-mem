package repository

import (
	"context"
)

// CheckSearchConvergence is the startup health probe for the active search
// contract. It checks canonical documents directly; no queue or worker state
// participates in readiness.
func (r *SearchRepositoryImpl) CheckSearchConvergence(ctx context.Context) error {
	convergence, err := r.GetSearchConvergence(ctx, SearchConvergenceInput{})
	if err != nil {
		return err
	}
	if convergence != nil && convergence.DriftedDocuments > 0 {
		return ErrSearchConvergenceAttentionRequired
	}
	return nil
}
