package repository

import searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"

// SearchMaintenanceStore exposes the native search adapter to capability
// composition while retaining the legacy repository facade for later callers.
func (r *SearchRepositoryImpl) SearchMaintenanceStore() *searchpostgres.Store {
	if r == nil {
		return nil
	}
	if r.searchOwner == nil && r.db != nil {
		r.searchOwner = searchpostgres.NewStore(r.db, r.rls)
	}
	return r.searchOwner
}
