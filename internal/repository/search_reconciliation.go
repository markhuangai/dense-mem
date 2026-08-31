package repository

import (
	"context"
)

// CheckSearchConvergence performs an explicit full canonical check for
// maintenance and operator callers; public health checks use search readiness.
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
