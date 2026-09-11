package serverapp

import (
	evidenceconflict "github.com/markhuangai/dense-mem/internal/conflict/evidence"
	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
)

type evidenceConflictStoreSource interface {
	ConflictStore() *conflictpostgres.Store
}

func buildEvidenceConflictApplication(source evidenceConflictStoreSource) *evidenceconflict.Service {
	if source == nil {
		return evidenceconflict.New(nil)
	}
	return evidenceconflict.New(source.ConflictStore())
}
