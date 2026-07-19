package http

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/markhuangai/dense-mem/internal/config"
	httperr "github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/observability"
)

// HealthCheck is a named function interface for dependency health checks.
// Later units can register Postgres/Neo4j/Redis checks without changing the route contract.
type HealthCheck struct {
	Name     string
	Check    func(ctx context.Context) error
	Optional bool
}

// HealthConfig configures the health and ready endpoints.
type HealthConfig struct {
	Checks   []HealthCheck
	Degraded bool
	Reason   string
}

// Server is the Echo server wrapper.
// It holds the Echo instance and configuration.
type Server struct {
	echo *echo.Echo
}

// ServerProvider is the companion interface for Server.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type ServerProvider interface {
	Start(addr string) error
	Shutdown(ctx context.Context) error
	GetEcho() *echo.Echo
}

// Ensure Server implements ServerProvider
var _ ServerProvider = (*Server)(nil)

// GetEcho returns the underlying Echo instance.
func (s *Server) GetEcho() *echo.Echo {
	return s.echo
}

// Start starts the server on the given address.
func (s *Server) Start(addr string) error {
	return s.echo.Start(addr)
}

// Shutdown gracefully shuts down the server with the given context.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.echo.Shutdown(ctx)
}

// NewServer creates a new Echo server with the given configuration and health checks.
// It sets up the correlation ID middleware, error handler, and public routes.
// The health and ready endpoints are not behind auth, profile, or rate-limit middleware.
func NewServer(cfg config.Config, logger observability.LogProvider, health HealthConfig) *echo.Echo {
	e := echo.New()
	applyServerLimits(e)
	applyIPExtractor(e)

	// Set custom error handler
	e.HTTPErrorHandler = httperr.ErrorHandler

	// Global middleware (applies to all routes)
	e.Use(middleware.Recover())
	e.Use(middleware.BodyLimit(fmt.Sprintf("%dB", effectiveMaxBodyBytes(cfg.HTTPMaxBodyBytes))))
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		HandleError:  true,
		LogMethod:    true,
		LogURI:       false,
		LogStatus:    true,
		LogLatency:   true,
		LogRemoteIP:  true,
		LogError:     true,
		LogRoutePath: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			if logger == nil {
				return nil
			}

			attrs := []observability.LogAttr{
				observability.String("method", v.Method),
				observability.String("uri", requestLogURI(c)),
				observability.Int("status", v.Status),
				observability.String("latency", v.Latency.String()),
				observability.String("remote_ip", v.RemoteIP),
			}
			if v.RoutePath != "" {
				attrs = append(attrs, observability.String("route", v.RoutePath))
			}
			if requestID := c.Response().Header().Get(echo.HeaderXRequestID); requestID != "" {
				attrs = append(attrs, observability.String("request_id", requestID))
			}
			if v.Error != nil {
				logger.Error("http_request", v.Error, attrs...)
				return nil
			}
			if v.Status >= http.StatusBadRequest {
				logger.Warn("http_request", attrs...)
				return nil
			}
			logger.Info("http_request", attrs...)
			return nil
		},
	}))

	// Register public routes (no auth/profile/rate-limit middleware)
	registerPublicRoutes(e, health)

	return e
}

func requestLogURI(c echo.Context) string {
	if c == nil || c.Request() == nil || c.Request().URL == nil {
		return ""
	}
	if path := c.Request().URL.EscapedPath(); path != "" {
		return path
	}
	if path := c.Request().URL.Path; path != "" {
		return path
	}
	return "/"
}

func applyServerLimits(e *echo.Echo) {
	e.Server.ReadHeaderTimeout = 5 * time.Second
	e.Server.ReadTimeout = 30 * time.Second
	e.Server.IdleTimeout = 60 * time.Second
}

func applyIPExtractor(e *echo.Echo) {
	e.IPExtractor = echo.ExtractIPDirect()
}

func effectiveMaxBodyBytes(value int) int {
	if value > 0 {
		return value
	}
	return 1048576
}

// NewServerWithGracefulShutdown creates a new server and returns it along with a shutdown function.
// The shutdown function uses a 10-second timeout for graceful shutdown.
func NewServerWithGracefulShutdown(cfg config.Config, logger observability.LogProvider, health HealthConfig) (*echo.Echo, func()) {
	e := NewServer(cfg, logger, health)

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := e.Shutdown(ctx); err != nil {
			logger.Error("server shutdown error", err)
		}
	}

	return e, shutdown
}

// RunServer starts the server and handles graceful shutdown.
// It blocks until the server is shut down.
func RunServer(e *echo.Echo, addr string, logger observability.LogProvider) error {
	// Start server in a goroutine
	go func() {
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			logger.Error("server start error", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)
	<-quit

	logger.Info("shutting down server")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return e.Shutdown(ctx)
}

// ShutdownServer gracefully shuts down the Echo server with a 10-second timeout.
// This function is used by main.go for graceful shutdown.
func ShutdownServer(e *echo.Echo, logger observability.LogProvider) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return e.Shutdown(ctx)
}
