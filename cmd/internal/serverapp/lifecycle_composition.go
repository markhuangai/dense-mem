package serverapp

import (
	"context"
	"time"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/lifecycle"
	"github.com/markhuangai/dense-mem/internal/repository"
)

type lifecycleApplicationDependencies struct {
	Semantic *repository.SemanticRepositoryImpl
	Evidence interface {
		RetractEvidence(context.Context, knowledgecontract.RetractEvidenceInput) (*knowledgecontract.EvidenceLifecycleResult, error)
	}
	CorrectionExecutor         lifecycle.LifecycleCorrectionExecutor
	CorrectionEmbeddingTimeout time.Duration
}

func buildLifecycleApplication(deps lifecycleApplicationDependencies) lifecycle.LifecycleService {
	var semantic lifecycle.LifecycleSemanticRepository
	if deps.Semantic != nil {
		semantic = deps.Semantic.LifecycleStore()
	}
	return lifecycle.NewLifecycleService(lifecycle.LifecycleDependencies{
		Port:                       semantic,
		CorrectionExecutor:         deps.CorrectionExecutor,
		CorrectionEmbeddingTimeout: deps.CorrectionEmbeddingTimeout,
	})
}
