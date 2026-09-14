//go:build !evaluation

package serverapp

import (
	communitycontract "github.com/markhuangai/dense-mem/internal/community/contract"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"gorm.io/gorm"
)

// buildEvaluationRegistryBindings keeps offline evaluation implementations out
// of the production composition while preserving the shared registry API.
func buildEvaluationRegistryBindings(_ *gorm.DB, _ postgres.RLSHelper, _ communitycontract.CommunityRepository, _ accessservice.AuditService) (registry.EvaluationBindings, error) {
	return registry.EvaluationBindings{}, nil
}
