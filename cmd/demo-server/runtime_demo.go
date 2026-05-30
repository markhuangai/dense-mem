package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/demo"
)

func init() {
	configureServerRuntime = func(mode *serverRuntimeMode) {
		mode.DisableControlPortal = true
		mode.RequireRedis = true
		mode.ValidateConfig = validateDemoStartupConfig
		mode.ConfigureServices = configureDemoServices
		mode.RegisterRoutes = registerDemoRoutes
		mode.StartBackground = startDemoBackground
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
	if !verifierConfigured(cfg) {
		return &config.ValidationError{Field: "AI_VERIFIER_MODEL", Message: "verifier configuration is required for demo server startup"}
	}
	return nil
}

func configureDemoServices(_ context.Context, ctx serverRuntimeContext, services *serverRuntimeServices) error {
	if ctx.CounterStore == nil {
		return fmt.Errorf("demo runtime requires redis-backed counters")
	}
	manager := demo.NewQuotaManager(ctx.CounterStore, demo.DefaultQuotas())

	services.FragmentCreateRegistrySvc = demo.WrapFragmentCreate(services.FragmentCreateRegistrySvc, manager)
	services.FragmentCreateHTTPSvc = demo.WrapFragmentCreate(services.FragmentCreateHTTPSvc, manager)
	services.ClaimCreateSvc = demo.WrapClaimCreate(services.ClaimCreateSvc, manager)
	services.ClaimVerifyRegistrySvc = demo.WrapClaimVerify(services.ClaimVerifyRegistrySvc, manager)
	services.ClaimVerifyHTTPSvc = demo.WrapClaimVerify(services.ClaimVerifyHTTPSvc, manager)
	services.FactPromoteSvc = demo.WrapFactPromote(services.FactPromoteSvc, manager)
	services.FactConfirmSvc = demo.WrapFactConfirm(services.FactConfirmSvc, manager)
	services.RecallRegistrySvc = demo.WrapRecall(services.RecallRegistrySvc, manager)
	services.RecallHTTPSvc = demo.WrapRecall(services.RecallHTTPSvc, manager)
	services.CommunityDetectRegistrySvc = demo.DisabledCommunityDetectService{}
	services.PostAuthMiddleware = append(services.PostAuthMiddleware, demo.RequestQuotaMiddleware(manager))
	services.UserPortalMiddleware = append(services.UserPortalMiddleware, demo.RequestQuotaMiddleware(manager))

	return nil
}

func registerDemoRoutes(ctx serverRuntimeContext) error {
	if ctx.Echo == nil {
		return fmt.Errorf("demo runtime missing echo server")
	}
	provisioner := demo.NewProvisioner(ctx.ProfileService, ctx.APIKeyService, ctx.CounterStore, demo.DefaultQuotas())
	demo.RegisterRoutes(ctx.Echo, provisioner, os.Getenv("DEMO_PUBLIC_BASE_URL"))
	return nil
}

func startDemoBackground(ctx context.Context, runtimeCtx serverRuntimeContext) (func(context.Context) error, error) {
	repo := demo.NewRepository(runtimeCtx.PostgresDB, runtimeCtx.RLS)
	cleaner := demo.NewCleanerWithDataPurger(repo, runtimeCtx.ProfileService, runtimeCtx.DataPurger, 10*time.Minute)
	return cleaner.Start(ctx), nil
}
