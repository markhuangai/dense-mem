package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/cmd/internal/demo"
	demopostgres "github.com/markhuangai/dense-mem/cmd/internal/demo/postgres"
	demoservice "github.com/markhuangai/dense-mem/cmd/internal/demo/service"
	"github.com/markhuangai/dense-mem/cmd/internal/serverapp"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func main() {
	processCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serverapp.RunFromEnvironment(processCtx, demoRuntimeOptions()); err != nil {
		log.Fatal("demo server runtime failed")
	}
}

func validateDemoStartupConfig(cfg *config.Config) error {
	required := []struct {
		field string
		value string
	}{
		{"AI_API_URL", cfg.GetAIAPIURL()},
		{"AI_API_KEY", cfg.GetAIAPIKey()},
		{"AI_API_EMBEDDING_MODEL", cfg.GetAIEmbeddingModel()},
		{"AI_VERIFIER_MODEL", cfg.GetAIVerifierModel()},
		{"REDIS_ADDR", cfg.GetRedisAddr()},
	}
	for _, item := range required {
		if strings.TrimSpace(item.value) == "" {
			return &config.ValidationError{Field: item.field, Message: "required for demo server startup"}
		}
	}
	if cfg.GetAIEmbeddingDimensions() <= 0 {
		return &config.ValidationError{Field: "AI_API_EMBEDDING_DIMENSIONS", Message: "required for demo server startup"}
	}
	if !serverapp.VerifierConfigured(cfg) {
		return &config.ValidationError{Field: "AI_VERIFIER_MODEL", Message: "verifier configuration is required for demo server startup"}
	}
	return nil
}

func demoRuntimeOptions() serverapp.RuntimeOptions {
	quotas := demoservice.DefaultQuotas()
	var manager *demoservice.QuotaManager
	return serverapp.RuntimeOptions{
		ValidateStartup:      validateDemoStartupConfig,
		DisableControlPortal: true,
		RequireRedis:         true,
		MetricsOnlyAddr:      ":8091",
		ConfigureRegistry: func(_ context.Context, runtime serverapp.RuntimeContext, reg registry.Registry) (registry.Registry, error) {
			manager = demoservice.NewQuotaManager(runtime.CounterStore, quotas)
			return demo.WrapRegistry(reg, manager)
		},
		RegisterRoutes: func(runtime serverapp.RuntimeContext) error {
			provisioner := demoservice.NewProvisioner(runtime.TeamService, runtime.CredentialService, runtime.CounterStore, quotas)
			demo.RegisterRoutes(runtime.Echo, provisioner, os.Getenv("DEMO_PUBLIC_BASE_URL"))
			return nil
		},
		BuildWorker: func(_ context.Context, runtime serverapp.RuntimeContext) (serverapp.RuntimeWorker, error) {
			repo := demopostgres.NewRepository(runtime.PostgresDB, runtime.RLS)
			cleaner := demoservice.NewCleaner(repo, runtime.TeamService, 10*time.Minute)
			return cleaner, nil
		},
		PostAuthMiddleware:   []echo.MiddlewareFunc{deferredDemoQuotaMiddleware(&manager)},
		UserPortalMiddleware: []echo.MiddlewareFunc{deferredDemoQuotaMiddleware(&manager)},
	}
}

func deferredDemoQuotaMiddleware(manager **demoservice.QuotaManager) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if manager == nil || *manager == nil {
				return httperr.New(httperr.SERVICE_UNAVAILABLE, "demo quota store unavailable")
			}
			return demo.RequestQuotaMiddleware(*manager)(next)(c)
		}
	}
}
