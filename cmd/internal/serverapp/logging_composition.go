package serverapp

import (
	"log/slog"

	"github.com/markhuangai/dense-mem/internal/observability"
)

type rootSlogProvider interface {
	Slog() *slog.Logger
}

func rootSlogLogger(logger observability.LogProvider) *slog.Logger {
	if provider, ok := logger.(rootSlogProvider); ok {
		return provider.Slog()
	}
	return nil
}
