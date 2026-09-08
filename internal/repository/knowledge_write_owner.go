package repository

import (
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

func (r *LedgerRepositoryImpl) knowledgeWriteOwner() *knowledgepostgres.Store {
	if r == nil {
		return nil
	}
	if r.knowledgeOwner == nil {
		r.knowledgeOwner = knowledgepostgres.NewStore(r.db, r.rls, ConflictRuntimeConfig{
			ReviewTTLDays: r.conflictReviewTTLDays,
			Timezone:      r.conflictReviewTimezone,
		})
	}
	return r.knowledgeOwner
}

func (r *SemanticRepositoryImpl) knowledgeWriteOwner() *knowledgepostgres.Store {
	if r == nil {
		return nil
	}
	if r.knowledgeOwner == nil {
		r.knowledgeOwner = knowledgepostgres.NewStore(r.db, r.rls, ConflictRuntimeConfig{})
	}
	return r.knowledgeOwner
}

func (r *SearchRepositoryImpl) knowledgeWriteOwner() *knowledgepostgres.Store {
	if r == nil {
		return nil
	}
	if r.knowledgeOwner == nil {
		r.knowledgeOwner = knowledgepostgres.NewStore(r.db, r.rls, ConflictRuntimeConfig{})
	}
	return r.knowledgeOwner
}
