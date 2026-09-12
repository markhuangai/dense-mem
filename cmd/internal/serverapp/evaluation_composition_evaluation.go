//go:build evaluation

package serverapp

import (
	"errors"

	dreampostgres "github.com/markhuangai/dense-mem/internal/dream/postgres"
	evaluationpostgres "github.com/markhuangai/dense-mem/internal/evaluation/postgres"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// buildEvaluationRegistryBindings is compiled only into the dedicated
// evaluation image. Dream remains the owner of Hypothesis SQL while the
// evaluation reader owns the generic read model and its transaction boundary.
func buildEvaluationRegistryBindings(semantic *repository.SemanticRepositoryImpl, audit service.AuditService) (registry.EvaluationBindings, error) {
	if semantic == nil {
		return registry.EvaluationBindings{}, errors.New("evaluation: semantic repository is required")
	}
	return registry.EvaluationBindings{
		Repository: evaluationpostgres.NewReader(
			semantic.DreamDatabase(),
			semantic.DreamRLS(),
			dreampostgres.HypothesisEvaluationQuery,
		),
		Communities: semantic,
		Audit:       audit,
	}, nil
}
