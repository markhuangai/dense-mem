package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/observability"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
)

func buildUsageMetricsApplication(repo operationscontract.UsageMetricsRepository, logger observability.LogProvider) *operations.UsageMetricsServiceImpl {
	return operations.NewUsageMetricsService(repo, logger)
}
