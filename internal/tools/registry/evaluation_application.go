//go:build evaluation

package registry

import (
	"errors"

	evaluationapp "github.com/markhuangai/dense-mem/internal/evaluation"
)

func bindEvaluationApplication(deps Dependencies) Dependencies {
	if deps.EvaluationBindings.Application == nil {
		deps.EvaluationBindings.Application = evaluationapp.New(evaluationapp.Dependencies{
			Repository: deps.EvaluationBindings.Repository,
			Recall:     deps.RecallBindings.Service,
			Dreams:     deps.DreamBindings.Service,
			Audit:      deps.EvaluationBindings.Audit,
		})
	}
	return deps
}

func translateEvaluationError(err error) error {
	if errors.Is(err, evaluationapp.ErrToolUnavailable) {
		return ErrToolUnavailable
	}
	return err
}
