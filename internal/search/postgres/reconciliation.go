package postgres

import (
	"context"

	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
)

// CheckSearchConvergence performs an explicit canonical check for maintenance
// and operator callers; public health checks use search readiness.
func (r *Store) CheckSearchConvergence(ctx context.Context) error {
	convergence, err := r.GetSearchConvergence(ctx, searchmaintenance.SearchConvergenceInput{})
	if err != nil {
		return err
	}
	if convergence != nil && convergence.DriftedDocuments > 0 {
		return ErrSearchConvergenceAttentionRequired
	}
	return nil
}
