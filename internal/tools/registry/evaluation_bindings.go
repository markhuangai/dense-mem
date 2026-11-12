package registry

import (
	"context"

	evaluationcontract "github.com/markhuangai/dense-mem/internal/evaluation/contract"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

type EvaluationRepository = evaluationcontract.EvaluationRepository

type EvaluationAuditAppender interface {
	Append(ctx context.Context, entry accessservice.AuditLogEntry) error
}

// EvaluationApplication is the registry's consumer-owned view of the
// evaluation application. Keeping this interface here avoids importing the
// evaluation implementation into the production registry build.
type EvaluationApplication interface {
	ListKnowledgeRefs(context.Context, string, string, int, string, string, bool) (map[string]any, error)
	RunRecallCase(context.Context, string, recallcontract.Request, string, string, bool) (map[string]any, error)
}

type EvaluationBindings struct {
	Application EvaluationApplication
	Repository  EvaluationRepository
	Audit       EvaluationAuditAppender
}
