package serverapp

import (
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/lifecycle"
)

type lifecycleApplicationDependencies struct {
	Port knowledgecontract.LifecyclePort
}

func buildLifecycleApplication(deps lifecycleApplicationDependencies) lifecycle.LifecycleService {
	return lifecycle.NewLifecycleService(lifecycle.LifecycleDependencies{Port: deps.Port})
}
