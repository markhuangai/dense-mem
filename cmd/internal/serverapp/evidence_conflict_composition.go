package serverapp

import (
	evidenceconflict "github.com/markhuangai/dense-mem/internal/conflict/evidence"
	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
)

func buildEvidenceConflictApplication(store *conflictpostgres.Store) *evidenceconflict.Service {
	if store == nil {
		return evidenceconflict.New(nil)
	}
	return evidenceconflict.New(store)
}
