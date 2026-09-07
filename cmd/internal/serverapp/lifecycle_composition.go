package serverapp

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

type lifecycleApplicationDependencies struct {
	Semantic                   memoryservice.LifecycleSemanticRepository
	Evidence                   memoryservice.LifecycleEvidenceRepository
	CorrectionExecutor         memoryservice.LifecycleCorrectionExecutor
	CorrectionEmbeddingTimeout time.Duration
}

func buildLifecycleApplication(deps lifecycleApplicationDependencies) memoryservice.LifecycleService {
	return memoryservice.NewLifecycleService(memoryservice.LifecycleDependencies{
		Semantic:                   deps.Semantic,
		Evidence:                   deps.Evidence,
		CorrectionExecutor:         deps.CorrectionExecutor,
		CorrectionEmbeddingTimeout: deps.CorrectionEmbeddingTimeout,
	})
}
