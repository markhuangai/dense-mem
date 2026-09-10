package serverapp

import (
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/markhuangai/dense-mem/internal/lifecycle"
)

func buildSemanticWriteCorrectionExecutor(provider embeddingcontract.EmbeddingProviderInterface) lifecycle.LifecycleCorrectionExecutor {
	return newSemanticwriteEmbeddingExecutor(provider)
}
