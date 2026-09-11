package repository

import (
	"github.com/markhuangai/dense-mem/internal/conflict/postgres"
)

// ConflictStore constructs the native Conflict adapter over the canonical
// Knowledge-backed persistence port. Legacy repository methods remain only as
// forwarding facades for callers that have not migrated yet.
func (r *LedgerRepositoryImpl) ConflictStore() *postgres.Store {
	if r == nil {
		return nil
	}
	return postgres.NewStore(r.db, r.rls, r)
}
