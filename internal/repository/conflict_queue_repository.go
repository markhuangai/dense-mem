package repository

import (
	"context"

	conflictcontract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	"github.com/markhuangai/dense-mem/internal/domain"
)

// ConflictQueueRepository is retained for callers that have not moved to the
// Conflict capability. The live implementation is conflict/postgres.Store.
type ConflictQueueRepository = conflictcontract.ConflictQueueRepository

func (r *LedgerRepositoryImpl) ListConflictQueue(ctx context.Context, query domain.ConflictQueueQuery) (*domain.ConflictQueuePage, error) {
	return r.ConflictStore().ListConflictQueue(ctx, query)
}

func (r *LedgerRepositoryImpl) CollectConflictQueueMetrics(ctx context.Context) (domain.ConflictQueueMetricsSnapshot, error) {
	return r.ConflictStore().CollectConflictQueueMetrics(ctx)
}
