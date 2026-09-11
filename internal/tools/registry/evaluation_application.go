//go:build evaluation

package registry

import (
	"errors"

	evaluationapp "github.com/markhuangai/dense-mem/internal/evaluation"
)

func bindEvaluationApplication(deps Dependencies) Dependencies {
	if deps.EvaluationBindings.Application == nil {
		deps.EvaluationBindings.Application = evaluationapp.New(evaluationapp.Dependencies{
			Repository: deps.Evaluation,
			Recall:     deps.Recall,
			Dreams:     deps.Dreams,
			Audit:      deps.EvaluationAudit,
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
