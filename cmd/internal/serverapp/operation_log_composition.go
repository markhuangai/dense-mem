package serverapp

import (
	"log/slog"

	"github.com/markhuangai/dense-mem/internal/observability"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
)

func buildOperationLogApplication(repo operationscontract.OperationLogRepository, retention operations.OperationLogRetentionProvider) *operations.OperationLogServiceImpl {
	return operations.NewOperationLogService(repo, retention)
}

func buildActiveApplicationLogger(level slog.Level, sink *operations.OperationLogServiceImpl) *observability.Logger {
	return observability.NewWithSinks(level, sink)
}
