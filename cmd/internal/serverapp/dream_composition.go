package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/dream"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
)

type controlDreamApplicationDependencies struct {
	Store dreamcontract.DreamControlRepository
}

func buildControlDreamApplication(deps controlDreamApplicationDependencies) dream.ControlService {
	return dream.NewControl(dream.ControlDependencies{
		Store: deps.Store,
	})
}
