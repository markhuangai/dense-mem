package registry

import (
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	"github.com/markhuangai/dense-mem/internal/repository"
)

type EvaluationBindings struct {
	Repository  dreamcontract.EvaluationRepository
	Communities repository.CommunityRepository
}

func (d Dependencies) withEvaluationBindings() Dependencies {
	if d.Evaluation == nil {
		d.Evaluation = d.EvaluationBindings.Repository
	}
	if d.Communities == nil {
		d.Communities = d.EvaluationBindings.Communities
	}
	return d
}
