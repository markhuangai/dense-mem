//go:build evaluation

package serverapp

import (
	"errors"
	"gorm.io/gorm"

	communitycontract "github.com/markhuangai/dense-mem/internal/community/contract"

	evaluationpostgres "github.com/markhuangai/dense-mem/internal/evaluation/postgres"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// buildEvaluationRegistryBindings is compiled only into the dedicated
// evaluation image and retains scoped canonical knowledge reads.
func buildEvaluationRegistryBindings(db *gorm.DB, rls postgres.RLSHelper, communities communitycontract.CommunityRepository, audit accessservice.AuditService) (registry.EvaluationBindings, error) {
	if db == nil || rls == nil {
		return registry.EvaluationBindings{}, errors.New("evaluation: database dependencies are required")
	}
	return registry.EvaluationBindings{
		Repository: evaluationpostgres.NewReader(
			db,
			rls,
		),
		Communities: communities,
		Audit:       audit,
	}, nil
}
