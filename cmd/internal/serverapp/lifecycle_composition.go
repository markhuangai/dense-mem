package serverapp

import (
	"time"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/lifecycle"
)

type lifecycleApplicationDependencies struct {
	Port                       knowledgecontract.LifecyclePort
	CorrectionExecutor         lifecycle.LifecycleCorrectionExecutor
	CorrectionEmbeddingTimeout time.Duration
}

func buildLifecycleApplication(deps lifecycleApplicationDependencies) lifecycle.LifecycleService {
	return lifecycle.NewLifecycleService(lifecycle.LifecycleDependencies{
		Port:                       deps.Port,
		CorrectionExecutor:         deps.CorrectionExecutor,
		CorrectionEmbeddingTimeout: deps.CorrectionEmbeddingTimeout,
	})
}
