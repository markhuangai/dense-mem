package serverapp

import (
	"log/slog"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

func buildOperationLogApplication(repo repository.OperationLogRepository, retention service.OperationLogRetentionProvider) *service.OperationLogServiceImpl {
	return service.NewOperationLogService(repo, retention)
}

func buildActiveApplicationLogger(level slog.Level, sink *service.OperationLogServiceImpl) *observability.Logger {
	return observability.NewWithSinks(level, sink)
}
