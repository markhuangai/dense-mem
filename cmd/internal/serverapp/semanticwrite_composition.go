package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func buildSemanticWriteCorrectionExecutor(provider embedding.EmbeddingProviderInterface) memoryservice.LifecycleCorrectionExecutor {
	return newSemanticwriteEmbeddingExecutor(provider)
}
