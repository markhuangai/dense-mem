package serverapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/config"
	conflictevidence "github.com/markhuangai/dense-mem/internal/conflict/evidence"
	conflictqueue "github.com/markhuangai/dense-mem/internal/conflict/queue"
	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/dream"
	densehttp "github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/recall"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/graphview"
	"github.com/markhuangai/dense-mem/internal/sse"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// transportCompositionInputs contains the already-built application and
// infrastructure ports needed to bind supported HTTP, portal, MCP, and SSE
// surfaces. It deliberately contains no transport policy beyond these ports.
type transportCompositionInputs struct {
	startupCtx       context.Context
	cfg              config.Config
	pgDB             *postgres.DB
	authority        authorityBootstrap
	backend          *backendBundle
	rls              postgres.RLSHelper
	options          RuntimeOptions
	logger           observability.LogProvider
	searchRepo       *repository.SearchRepositoryImpl
	telemetry        telemetryComposition
	toolRegistry     registry.Registry
	convergence      service.SearchConvergenceReader
	rememberAttempts service.RememberAttemptDiagnosticsReader

	credentialRepo     repository.CredentialRepository
	credentialVerifier crypto.CredentialVerifier
	activityWriter     *service.CredentialActivityWriter
	teamService        service.TeamService
	credentialService  service.CredentialService
	ssoService         *service.SSOService
	portalSession      service.UserPortalSessionManager
	directoryIdentity  *service.DirectoryIdentityService
	controlIdentity    *service.ControlIdentityService
	privateMemory      densehttp.PrivateMemoryServiceInterface
	auditService       service.AuditService
	securityService    service.SecurityService
	appConfig          service.AppConfigService
	operationLogs      service.OperationLogReader
	usageMetrics       service.UsageMetricsService
	conflictQueue      conflictqueue.Reader
	evidenceConflicts  conflictevidence.Reader
	recallFeedback     recall.RecallFeedbackEventReader
	community          communityservice.Service
	controlDream       dream.ControlService
	graph              graphview.Service
	recall             recall.RecallService
	dream              dream.Service
}

type transportComposition struct {
	e                   *echo.Echo
	controlServer       *echo.Echo
	telemetryServer     *echo.Echo
	telemetryServerAddr string
}

func buildTransportComposition(deps transportCompositionInputs) (*transportComposition, error) {
	if deps.backend == nil {
		return nil, fmt.Errorf("transport: backend is required")
	}

	streamLifecycle := sse.NewStreamLifecycleWithConfig(
		deps.backend.concurrencyLimiter,
		sse.NewHeartbeatSenderWithInterval(time.Duration(deps.cfg.GetSSEHeartbeatSeconds())*time.Second),
		time.Duration(deps.cfg.GetSSEMaxDurationSeconds())*time.Second,
		deps.backend.streamCleanupRepo,
	)
	mcpHandler := handler.NewMCPHandlerWithLifecycleAndRuntimeConfig(
		deps.toolRegistry,
		deps.logger,
		streamLifecycle,
		deps.appConfig,
		deps.dream,
	)

	checks := []densehttp.HealthCheck{
		{Name: "postgres", Check: func(ctx context.Context) error {
			return deps.pgDB.Ping(ctx)
		}},
		{Name: "postgres_topology", Check: func(ctx context.Context) error {
			return postgres.ValidateSinglePrimaryTopology(ctx, deps.pgDB.GetDB())
		}},
		{Name: "pgvector", Check: func(ctx context.Context) error {
			return postgres.CheckPGVectorExtension(ctx, deps.pgDB.GetDB())
		}},
		{Name: "authority", Check: func(context.Context) error {
			return checkActiveAuthority(deps.authority)
		}},
		{Name: "search_readiness", Check: func(ctx context.Context) error {
			return checkSearchReadiness(ctx, deps.searchRepo)
		}},
	}
	if deps.backend.redisPingFn != nil {
		checks = append(checks, densehttp.HealthCheck{Name: "redis", Check: deps.backend.redisPingFn})
	}
	healthConfig := (densehttp.HealthConfig{
		Checks:   checks,
		Degraded: deps.backend.degraded,
		Reason:   deps.backend.reason,
	}).WithSharedDependencyChecks()
	e := densehttp.NewServer(deps.cfg, deps.logger, healthConfig)
	e.Use(middleware.CorrelationIDMiddleware(), middleware.ClientIPMiddleware())
	e.Use(middleware.SecurityBanMiddleware(deps.securityService))
	densehttp.RegisterOAuthProtectedResourceRoutes(e, deps.ssoService)
	if err := densehttp.RegisterDirectorySCIM(e, deps.directoryIdentity, densehttp.DirectorySCIMConfig{
		RuntimeConfig: deps.appConfig,
		Security:      deps.securityService,
		RateLimitSvc:  deps.backend.rateLimitService,
		Config:        &deps.cfg,
	}); err != nil {
		return nil, fmt.Errorf("register directory SCIM routes: %w", err)
	}

	runtimeCtx := RuntimeContext{
		Echo:              e,
		Config:            &deps.cfg,
		TeamService:       deps.teamService,
		CredentialService: deps.credentialService,
		CounterStore:      deps.backend.counterStore,
		PostgresDB:        deps.pgDB.GetDB(),
		RLS:               deps.rls,
		Logger:            deps.logger,
	}
	if deps.options.RegisterRoutes != nil {
		if err := deps.options.RegisterRoutes(runtimeCtx); err != nil {
			return nil, fmt.Errorf("register runtime routes: %w", err)
		}
	}

	protectedDeps := densehttp.ProtectedDeps{
		MCP: densehttp.MCPBindings{
			CredentialRepo:     deps.credentialRepo,
			TeamSvc:            deps.teamService,
			RateLimitService:   deps.backend.rateLimitService,
			UsageMetrics:       deps.usageMetrics,
			AuditService:       deps.auditService,
			SecurityService:    deps.securityService,
			SSOAuthenticator:   deps.ssoService,
			OAuthAuthenticator: deps.ssoService,
			OAuthMetadata:      deps.ssoService,
			Config:             &deps.cfg,
			Logger:             deps.logger,
			CredentialVerifier: deps.credentialVerifier,
			LastUsedRecorder:   deps.activityWriter,
		},
	}
	protectedDeps.PostAuthMiddleware = append(protectedDeps.PostAuthMiddleware, deps.options.PostAuthMiddleware...)
	if deps.telemetry.HTTPMetrics != nil {
		protectedDeps.PostAuthMiddleware = append(protectedDeps.PostAuthMiddleware, middleware.TelemetryHTTPMiddleware(deps.telemetry.HTTPMetrics))
	}
	densehttp.RegisterProtectedRoutesWithHandlers(e, protectedDeps, densehttp.ProtectedHandlers{
		MCPPost: mcpHandler.HandlePost,
		MCPGet:  mcpHandler.HandleGet,
	})

	userPortalDeps := densehttp.UserPortalDeps{
		CredentialRepo:     deps.credentialRepo,
		TeamSvc:            deps.teamService,
		CredentialSvc:      deps.credentialService,
		RateLimitSvc:       deps.backend.rateLimitService,
		UsageMetrics:       deps.usageMetrics,
		Telemetry:          deps.telemetry.Reader,
		Memory:             densehttp.MemoryPortalBindings{GraphView: deps.graph, RecallSvc: deps.recall, DreamSvc: deps.dream, PrivateMemory: deps.privateMemory},
		AuditSvc:           deps.auditService,
		SecuritySvc:        deps.securityService,
		SSOService:         deps.ssoService,
		PortalSession:      deps.portalSession,
		AppConfig:          deps.appConfig,
		Config:             &deps.cfg,
		CredentialVerifier: deps.credentialVerifier,
		LastUsedRecorder:   deps.activityWriter,
	}
	userPortalDeps.ExtraMiddleware = append(userPortalDeps.ExtraMiddleware, deps.options.UserPortalMiddleware...)
	if deps.telemetry.HTTPMetrics != nil {
		userPortalDeps.ExtraMiddleware = append(userPortalDeps.ExtraMiddleware, middleware.TelemetryHTTPMiddleware(deps.telemetry.HTTPMetrics))
	}
	densehttp.RegisterUserPortal(e, userPortalDeps)

	composition := &transportComposition{e: e}
	if !deps.options.DisableControlPortal {
		controlServer, err := densehttp.NewControlPortalServerWithCapabilityBindings(
			&deps.cfg,
			deps.teamService,
			deps.credentialService,
			deps.usageMetrics,
			densehttp.ControlPortalBindings{Telemetry: densehttp.ControlPortalTelemetry{
				Reader:            deps.telemetry.Reader,
				HTTPMetrics:       deps.telemetry.HTTPMetrics,
				ScrapeHandler:     deps.telemetry.ScrapeHandler,
				ScrapeToken:       deps.cfg.GetTelemetryScrapeToken(),
				SSO:               deps.ssoService,
				Directory:         deps.directoryIdentity,
				ControlIdentity:   deps.controlIdentity,
				Config:            deps.appConfig,
				Logs:              deps.operationLogs,
				RecallFeedback:    deps.recallFeedback,
				Dreams:            deps.controlDream,
				Communities:       deps.community,
				ConflictQueue:     deps.conflictQueue,
				EvidenceConflicts: deps.evidenceConflicts,
				Convergence:       deps.convergence,
				RememberAttempts:  deps.rememberAttempts,
				PrivateMemory:     deps.privateMemory,
			}},
			healthConfig,
			deps.logger,
			deps.securityService,
		)
		if err != nil {
			return nil, fmt.Errorf("build control portal server: %w", err)
		}
		composition.controlServer = controlServer
	} else if deps.telemetry.ScrapeHandler != nil {
		addr := strings.TrimSpace(deps.options.MetricsOnlyAddr)
		if addr == "" {
			addr = ":8091"
		}
		telemetryServer, err := newTelemetryScrapeServer(deps.telemetry.ScrapeHandler, deps.cfg.GetTelemetryScrapeToken())
		if err != nil {
			return nil, fmt.Errorf("build telemetry scrape server: %w", err)
		}
		composition.telemetryServer = telemetryServer
		composition.telemetryServerAddr = addr
	}
	return composition, nil
}
