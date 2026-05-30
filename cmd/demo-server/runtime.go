package main

import (
	"context"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type runtimeCounterStore interface {
	IncrWithExpire(ctx context.Context, key string, expireSeconds int64) (int64, error)
	AddWithExpire(ctx context.Context, key string, delta int64, expireSeconds int64) (int64, error)
}

type serverRuntimeMode struct {
	DisableControlPortal bool
	RequireRedis         bool
	ValidateConfig       func(*config.Config) error
	ConfigureServices    func(context.Context, serverRuntimeContext, *serverRuntimeServices) error
	RegisterRoutes       func(serverRuntimeContext) error
	StartBackground      func(context.Context, serverRuntimeContext) (func(context.Context) error, error)
}

type serverRuntimeContext struct {
	Echo           *echo.Echo
	Config         *config.Config
	ProfileService service.ProfileService
	APIKeyService  service.APIKeyService
	DataPurger     service.ProfileDataPurger
	CounterStore   runtimeCounterStore
	PostgresDB     *gorm.DB
	RLS            postgres.RLSHelper
	Logger         observability.LogProvider
}

type serverRuntimeServices struct {
	FragmentCreateRegistrySvc  fragmentservice.CreateFragmentService
	FragmentCreateHTTPSvc      fragmentservice.CreateFragmentService
	ClaimCreateSvc             claimservice.CreateClaimService
	ClaimVerifyRegistrySvc     claimservice.VerifyClaimService
	ClaimVerifyHTTPSvc         claimservice.VerifyClaimService
	FactPromoteSvc             factservice.PromoteClaimService
	FactConfirmSvc             factservice.ConfirmMemoryService
	RecallRegistrySvc          recallservice.RecallService
	RecallHTTPSvc              recallservice.RecallService
	CommunityDetectRegistrySvc communityservice.DetectCommunityService
	PostAuthMiddleware         []echo.MiddlewareFunc
	UserPortalMiddleware       []echo.MiddlewareFunc
}

var configureServerRuntime = func(*serverRuntimeMode) {}

func newServerRuntimeMode() serverRuntimeMode {
	mode := serverRuntimeMode{}
	configureServerRuntime(&mode)
	return mode
}
