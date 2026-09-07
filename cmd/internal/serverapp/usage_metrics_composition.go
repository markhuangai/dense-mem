package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

func buildUsageMetricsApplication(repo repository.UsageMetricsRepository, logger observability.LogProvider) *service.UsageMetricsServiceImpl {
	return service.NewUsageMetricsService(repo, logger)
}
