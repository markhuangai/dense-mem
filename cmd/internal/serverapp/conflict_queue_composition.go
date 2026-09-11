package serverapp

import (
	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	conflictqueue "github.com/markhuangai/dense-mem/internal/conflict/queue"
)

type conflictQueueStoreSource interface {
	ConflictStore() *conflictpostgres.Store
}

func buildConflictQueueApplication(source conflictQueueStoreSource) *conflictqueue.Service {
	if source == nil {
		return conflictqueue.New(nil)
	}
	return conflictqueue.New(source.ConflictStore())
}
