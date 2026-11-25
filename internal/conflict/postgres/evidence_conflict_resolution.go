package postgres

import (
	"context"
	"github.com/markhuangai/dense-mem/internal/conflict/postgres/casewrite"
)

func (r *Store) ResolveEvidenceConflict(ctx context.Context, input EvidenceConflictResolutionInput) (*EvidenceConflictCaseRecord, error) {
	return casewrite.ResolveEvidenceConflict(ctx, input, r.withSystemTx)
}
