package registry

import (
	"context"

	communitycontract "github.com/markhuangai/dense-mem/internal/community/contract"
	"github.com/markhuangai/dense-mem/internal/dream"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

type EvaluationRepository = dreamcontract.EvaluationRepository
type CommunityRepository = communitycontract.CommunityRepository

type EvaluationAuditAppender interface {
	Append(ctx context.Context, entry accessservice.AuditLogEntry) error
}

// EvaluationApplication is the registry's consumer-owned view of the
// evaluation application. Keeping this interface here avoids importing the
// evaluation implementation into the production registry build.
type EvaluationApplication interface {
	ListKnowledgeRefs(context.Context, string, string, int, string, string, bool) (map[string]any, error)
	RunRecallCase(context.Context, string, recallcontract.Request, string, string, bool, bool) (map[string]any, error)
	RunDreamCycle(context.Context, string, dream.RunCycleRequest) (map[string]any, error)
}

type EvaluationBindings struct {
	Application EvaluationApplication
	Repository  EvaluationRepository
	Communities CommunityRepository
	Audit       EvaluationAuditAppender
}

func (d Dependencies) withEvaluationBindings() Dependencies {
	if d.Evaluation == nil {
		d.Evaluation = d.EvaluationBindings.Repository
	}
	if d.Communities == nil {
		d.Communities = d.EvaluationBindings.Communities
	}
	if d.EvaluationAudit == nil {
		d.EvaluationAudit = d.EvaluationBindings.Audit
	}
	return d
}
