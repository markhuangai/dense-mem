package http

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"
	echomw "github.com/labstack/echo/v5/middleware"

	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	"github.com/markhuangai/dense-mem/internal/tools"
)

// NewEchoLogger uses the protected application root for framework messages.
func NewEchoLogger(logger httpcontract.LogProvider) *slog.Logger {
	if provider, ok := logger.(interface{ Slog() *slog.Logger }); ok {
		if root := provider.Slog(); root != nil {
			return root.With("component", "echo")
		}
	}
	return nil
}

func rootRecover(logger httpcontract.LogProvider) echo.MiddlewareFunc {
	config := echomw.DefaultRecoverConfig
	config.DisableStackAll = true
	recoverRequest := echomw.RecoverWithConfig(config)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		handle := recoverRequest(next)
		return func(c *echo.Context) error {
			err := handle(c)
			panicErr, ok := err.(*echomw.PanicStackError)
			if !ok {
				return err
			}
			httpcontract.LogErrorContext(TransportLogContext(c), logger, "http_panic_recovered",
				errors.New(tools.SanitizeError(panicErr.Err)),
				httpcontract.String("route", c.Path()), httpcontract.Int("stack_bytes", len(panicErr.Stack)))
			return panicErr.Err
		}
	}
}

// RootRecover exposes the transport recovery adapter to the private metrics
// listener without exporting its implementation details.
func RootRecover(logger httpcontract.LogProvider) echo.MiddlewareFunc {
	return rootRecover(logger)
}

// EchoServerErrorLogger routes net/http listener failures through the application root.
func EchoServerErrorLogger(logger httpcontract.LogProvider) *log.Logger {
	if logger == nil {
		return nil
	}
	return log.New(rootLogWriter{logger: logger}, "", 0)
}

type rootLogWriter struct{ logger httpcontract.LogProvider }

func (w rootLogWriter) Write(value []byte) (int, error) {
	if w.logger != nil {
		httpcontract.LogErrorContext(context.Background(), w.logger, "http_server_error", errors.New(tools.SanitizeError(errors.New(string(value)))))
	}
	return len(value), nil
}

func transportStatus(status int) string {
	if status >= http.StatusInternalServerError {
		return "error"
	}
	if status >= http.StatusBadRequest {
		return "client_error"
	}
	return "success"
}

// TransportStatus classifies an HTTP completion for transport observations
// emitted by first-party handlers outside Echo middleware.
func TransportStatus(status int) string {
	return transportStatus(status)
}
