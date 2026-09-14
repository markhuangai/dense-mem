package serverapp

import (
	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	conflictqueue "github.com/markhuangai/dense-mem/internal/conflict/queue"
)

func buildConflictQueueApplication(store *conflictpostgres.Store) *conflictqueue.Service {
	if store == nil {
		return conflictqueue.New(nil)
	}
	return conflictqueue.New(store)
}
