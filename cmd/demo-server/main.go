package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/cmd/internal/demo"
	"github.com/markhuangai/dense-mem/cmd/internal/serverapp"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

const startupTimeout = 5 * time.Minute

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	if err := validateDemoStartupConfig(&cfg); err != nil {
		log.Fatalf("invalid demo startup config: %v", err)
	}

	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	logger := observability.New(level)
	slog.SetDefault(logger.Slog())

	validation.SetEmbeddingDimensions(cfg.GetEmbeddingDimensions())
	middleware.SetAuthVerificationConcurrency(cfg.AuthVerifyMaxConcurrency)

	startupCtx, startupCancel := context.WithTimeout(context.Background(), startupTimeout)
	defer startupCancel()

	pgDB, err := postgres.OpenWithClient(startupCtx, &cfg)
	if err != nil {
		log.Fatalf("failed to connect to postgres: %v", err)
	}
	defer pgDB.Close()
	if err := postgres.ValidateSinglePrimaryTopology(startupCtx, pgDB.GetDB()); err != nil {
		log.Fatalf("unsupported postgres topology: %v", err)
	}

	logger.Info("running postgres migrations")
	if err := postgres.RunUp(startupCtx, pgDB.GetDB()); err != nil {
		log.Fatalf("failed to run postgres migrations: %v", err)
	}
	logger.Info("postgres migrations completed")
	if err := postgres.CheckPGVectorExtension(startupCtx, pgDB.GetDB()); err != nil {
		log.Fatalf("pgvector extension check failed: %v", err)
	}

	embeddingConfigRepo := postgres.NewEmbeddingConfigRepository(pgDB.GetDB())
	embeddingConsistencySvc := service.NewEmbeddingConsistencyService(embeddingConfigRepo, &cfg)
	if err := embeddingConsistencySvc.CheckAtStartup(startupCtx); err != nil {
		log.Fatalf("embedding consistency check failed: %v", err)
	}

	rlsHelper := postgres.NewRLS()
	migrationControlRepo := repository.NewV2MigrationControlRepository(pgDB.GetDB(), rlsHelper)
	authority, err := serverapp.EnsureAuthority(startupCtx, migrationControlRepo)
	if err != nil {
		log.Fatalf("authority bootstrap failed: %v", err)
	}

	serverapp.RunActiveServer(startupCtx, cfg, pgDB, logger, level, authority, demoRuntimeOptions())
}

func validateDemoStartupConfig(cfg *config.Config) error {
	if err := cfg.ValidateServerStartup(); err != nil {
		return err
	}
	required := []struct {
		field string
		value string
	}{
		{"AI_API_URL", cfg.GetAIAPIURL()},
		{"AI_API_KEY", cfg.GetAIAPIKey()},
		{"AI_API_EMBEDDING_MODEL", cfg.GetAIEmbeddingModel()},
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
	quotas := demo.DefaultQuotas()
	var manager *demo.QuotaManager
	return serverapp.RuntimeOptions{
		DisableControlPortal: true,
		RequireRedis:         true,
		MetricsOnlyAddr:      ":8091",
		ConfigureRegistry: func(_ context.Context, runtime serverapp.RuntimeContext, reg registry.Registry) (registry.Registry, error) {
			manager = demo.NewQuotaManager(runtime.CounterStore, quotas)
			return demo.WrapRegistry(reg, manager)
		},
		RegisterRoutes: func(runtime serverapp.RuntimeContext) error {
			provisioner := demo.NewProvisioner(runtime.ProfileService, runtime.APIKeyService, runtime.CounterStore, quotas)
			demo.RegisterRoutes(runtime.Echo, provisioner, os.Getenv("DEMO_PUBLIC_BASE_URL"))
			return nil
		},
		StartBackground: func(ctx context.Context, runtime serverapp.RuntimeContext) (func(context.Context) error, error) {
			repo := demo.NewRepository(runtime.PostgresDB, runtime.RLS)
			cleaner := demo.NewCleaner(repo, runtime.ProfileService, 10*time.Minute)
			return cleaner.Start(ctx), nil
		},
		PostAuthMiddleware:   []echo.MiddlewareFunc{deferredDemoQuotaMiddleware(&manager)},
		UserPortalMiddleware: []echo.MiddlewareFunc{deferredDemoQuotaMiddleware(&manager)},
	}
}

func deferredDemoQuotaMiddleware(manager **demo.QuotaManager) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if manager == nil || *manager == nil {
				return httperr.New(httperr.SERVICE_UNAVAILABLE, "demo quota store unavailable")
			}
			return demo.RequestQuotaMiddleware(*manager)(next)(c)
		}
	}
}
