//go:build !evaluation

package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// buildEvaluationRegistryBindings keeps offline evaluation implementations out
// of the production composition while preserving the shared registry API.
func buildEvaluationRegistryBindings(_ *repository.SemanticRepositoryImpl, _ service.AuditService) (registry.EvaluationBindings, error) {
	return registry.EvaluationBindings{}, nil
}
