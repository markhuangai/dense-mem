package serverapp

import (
	"context"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// CounterStore is the atomic counter surface used by runtimes that need
// Redis-backed limits outside the normal rate limiter.
type CounterStore interface {
	IncrWithExpire(ctx context.Context, key string, expireSeconds int64) (int64, error)
	AddWithExpire(ctx context.Context, key string, delta int64, expireSeconds int64) (int64, error)
}

type RuntimeContext struct {
	Echo              *echo.Echo
	Config            *config.Config
	TeamService       accessservice.TeamService
	CredentialService accessservice.CredentialService
	CounterStore      CounterStore
	PostgresDB        *gorm.DB
	RLS               postgres.RLSHelper
	Logger            observability.LogProvider
}

// WriteRuntime is the single composition seam for test-only writer
// substitutions. Production callers leave it untouched, so the release
// registry continues to use the legacy intake service until an adoption
// ticket wires a terminal processor.
type WriteRuntime struct {
	Remember         rememberapp.Service
	RegistryOverride func(context.Context, RuntimeContext, registry.Registry) (registry.Registry, error)
	Slice            string
}

type RuntimeOptions struct {
	DisableControlPortal bool
	RequireRedis         bool
	MetricsOnlyAddr      string
	ConfigureRegistry    func(context.Context, RuntimeContext, registry.Registry) (registry.Registry, error)
	RegisterRoutes       func(RuntimeContext) error
	StartBackground      func(context.Context, RuntimeContext) (func(context.Context) error, error)
	WriteRuntimeOverride func(context.Context, RuntimeContext, *WriteRuntime) error
	PostAuthMiddleware   []echo.MiddlewareFunc
	UserPortalMiddleware []echo.MiddlewareFunc
}
