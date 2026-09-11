package registry

import (
	"context"

	communitycontract "github.com/markhuangai/dense-mem/internal/community/contract"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	"github.com/markhuangai/dense-mem/internal/service"
)

type EvaluationRepository = dreamcontract.EvaluationRepository
type CommunityRepository = communitycontract.CommunityRepository

type EvaluationAuditAppender interface {
	Append(ctx context.Context, entry service.AuditLogEntry) error
}

type EvaluationBindings struct {
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
