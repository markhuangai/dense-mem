package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/conflictqueue"
)

func buildConflictQueueApplication(store repository.ConflictQueueRepository) *conflictqueue.Service {
	return conflictqueue.New(store)
}
