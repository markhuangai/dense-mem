package repository

import (
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

// LifecycleStore is the native construction seam for lifecycle mutations.
// Lifecycle application policy consumes this port directly; the historical
// repository methods remain compatibility-only forwarders.
func (r *SemanticRepositoryImpl) LifecycleStore() knowledgecontract.LifecyclePort {
	if r == nil || r.db == nil || r.rls == nil {
		return nil
	}
	return knowledgepostgres.NewStore(r.db, r.rls, knowledgecontract.ConflictRuntimeConfig{})
}
