package serverapp

import (
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func buildSemanticWriteCorrectionExecutor(provider embeddingcontract.EmbeddingProviderInterface) memoryservice.LifecycleCorrectionExecutor {
	return newSemanticwriteEmbeddingExecutor(provider)
}
