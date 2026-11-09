package serverapp

import (
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/markhuangai/dense-mem/internal/lifecycle"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

func buildSemanticWriteCorrectionExecutor(provider embeddingcontract.EmbeddingProviderInterface) lifecycle.LifecycleCorrectionExecutor {
	return semanticwrite.NewProviderExecutor(provider)
}
