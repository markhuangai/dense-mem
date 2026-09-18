package serverapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	communityapp "github.com/markhuangai/dense-mem/internal/community/service"
	"github.com/markhuangai/dense-mem/internal/config"
	conflictevidence "github.com/markhuangai/dense-mem/internal/conflict/evidence"
	conflictqueue "github.com/markhuangai/dense-mem/internal/conflict/queue"
	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/dream"
	"github.com/markhuangai/dense-mem/internal/graph"
	densehttp "github.com/markhuangai/dense-mem/internal/http"
	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/observability"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	"github.com/markhuangai/dense-mem/internal/recall"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	searchapp "github.com/markhuangai/dense-mem/internal/search"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	settings "github.com/markhuangai/dense-mem/internal/settings"
	"github.com/markhuangai/dense-mem/internal/sse"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// transportCompositionInputs contains the already-built application and
// infrastructure ports needed to bind supported HTTP, portal, MCP, and SSE
// surfaces. It deliberately contains no transport policy beyond these ports.
type transportCompositionInputs struct {
	startupCtx               context.Context
	cfg                      config.Config
	pgDB                     *postgres.DB
	authority                authorityBootstrap
	backend                  *backendBundle
	rls                      postgres.RLSHelper
	options                  RuntimeOptions
	logger                   observability.LogProvider
	credentialLookupPrefixes httpcontract.CredentialLookupPrefixes
	searchRepo               searchcontract.SearchRepository
	telemetry                telemetryComposition
	toolRegistry             registry.Registry
	convergence              searchapp.SearchConvergenceReader
	rememberAttempts         rememberapp.RememberAttemptDiagnosticsReader

	credentialRepo     accessservice.CredentialStore
	credentialVerifier crypto.CredentialVerifier
	activityWriter     *accessservice.CredentialActivityWriter
	teamService        accessservice.TeamService
	credentialService  accessservice.CredentialService
	ssoService         *accessservice.SSOService
	portalSession      accessservice.UserPortalSessionManager
	directoryIdentity  *accessservice.DirectoryIdentityService
	controlIdentity    *accessservice.ControlIdentityService
	privateMemory      densehttp.PrivateMemoryServiceInterface
	auditService       accessservice.AuditService
	securityService    settings.SecurityService
	appConfig          settings.AppConfigService
	operationLogs      operations.OperationLogReader
	operationLogHealth func(context.Context) error
	usageMetrics       operations.UsageMetricsService
	conflictQueue      conflictqueue.Reader
	evidenceConflicts  conflictevidence.Reader
	recallFeedback     recall.RecallFeedbackEventReader
	community          communityapp.Service
	controlDream       dream.ControlService
	graph              graph.Service
	recall             recall.RecallService
	dream              dream.Service
}

type transportComposition struct {
	e                   *echo.Echo
	controlServer       *echo.Echo
	telemetryServer     *echo.Echo
	telemetryServerAddr string
}

// httpLoggerAdapter keeps the transport's logging contract independent from
// the application logger implementation. Redaction and sink behavior remain
// owned by observability.Logger.
type httpLoggerAdapter struct {
	delegate observability.LogProvider
}

func (a httpLoggerAdapter) Info(message string, attrs ...httpcontract.LogAttr) {
	if a.delegate != nil {
		a.delegate.Info(message, observabilityAttrs(attrs)...)
	}
}

func (a httpLoggerAdapter) InfoContext(ctx context.Context, message string, attrs ...httpcontract.LogAttr) {
	if contextual, ok := a.delegate.(interface {
		InfoContext(context.Context, string, ...observability.LogAttr)
	}); ok {
		contextual.InfoContext(ctx, message, observabilityAttrs(attrs)...)
		return
	}
	a.Info(message, attrs...)
}

func (a httpLoggerAdapter) Error(message string, err error, attrs ...httpcontract.LogAttr) {
	if a.delegate != nil {
		a.delegate.Error(message, err, observabilityAttrs(attrs)...)
	}
}

func (a httpLoggerAdapter) ErrorContext(ctx context.Context, message string, err error, attrs ...httpcontract.LogAttr) {
	if contextual, ok := a.delegate.(interface {
		ErrorContext(context.Context, string, error, ...observability.LogAttr)
	}); ok {
		contextual.ErrorContext(ctx, message, err, observabilityAttrs(attrs)...)
		return
	}
	a.Error(message, err, attrs...)
}

func (a httpLoggerAdapter) Warn(message string, attrs ...httpcontract.LogAttr) {
	if a.delegate != nil {
		a.delegate.Warn(message, observabilityAttrs(attrs)...)
	}
}

func (a httpLoggerAdapter) WarnContext(ctx context.Context, message string, attrs ...httpcontract.LogAttr) {
	if contextual, ok := a.delegate.(interface {
		WarnContext(context.Context, string, ...observability.LogAttr)
	}); ok {
		contextual.WarnContext(ctx, message, observabilityAttrs(attrs)...)
		return
	}
	a.Warn(message, attrs...)
}

func (a httpLoggerAdapter) Debug(message string, attrs ...httpcontract.LogAttr) {
	if a.delegate != nil {
		a.delegate.Debug(message, observabilityAttrs(attrs)...)
	}
}

func (a httpLoggerAdapter) DebugContext(ctx context.Context, message string, attrs ...httpcontract.LogAttr) {
	if contextual, ok := a.delegate.(interface {
		DebugContext(context.Context, string, ...observability.LogAttr)
	}); ok {
		contextual.DebugContext(ctx, message, observabilityAttrs(attrs)...)
		return
	}
	a.Debug(message, attrs...)
}

func (a httpLoggerAdapter) With(attrs ...httpcontract.LogAttr) httpcontract.LogProvider {
	if a.delegate == nil {
		return a
	}
	return httpLoggerAdapter{delegate: a.delegate.With(observabilityAttrs(attrs)...)}
}

func observabilityAttrs(attrs []httpcontract.LogAttr) []observability.LogAttr {
	converted := make([]observability.LogAttr, 0, len(attrs))
	for _, attr := range attrs {
		converted = append(converted, observability.LogAttr{Key: attr.Key, Value: attr.Value})
	}
	return converted
}

func transportLogger(logger observability.LogProvider) httpcontract.LogProvider {
	if logger == nil {
		return nil
	}
	return httpLoggerAdapter{delegate: logger}
}

func buildTransportComposition(deps transportCompositionInputs) (*transportComposition, error) {
	if deps.backend == nil {
		return nil, fmt.Errorf("transport: backend is required")
	}
	if deps.credentialVerifier == nil || deps.credentialLookupPrefixes == nil {
		return nil, fmt.Errorf("transport: credential verifier and prefix lookup are required")
	}

	streamLifecycle := sse.NewStreamLifecycleWithConfig(
		deps.backend.concurrencyLimiter,
		sse.NewHeartbeatSenderWithInterval(time.Duration(deps.cfg.GetSSEHeartbeatSeconds())*time.Second),
		time.Duration(deps.cfg.GetSSEMaxDurationSeconds())*time.Second,
	)
	mcpHandler := handler.NewMCPHandlerWithLifecycleAndRuntimeConfig(
		deps.toolRegistry,
		handler.NewMCPLogger(transportLogger(deps.logger)),
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
			return operations.CheckSearchReadiness(ctx, deps.searchRepo)
		}},
	}
	if deps.backend.redisPingFn != nil {
		checks = append(checks, densehttp.HealthCheck{Name: "redis", Check: deps.backend.redisPingFn})
	}
	if deps.operationLogHealth != nil {
		checks = append(checks, densehttp.HealthCheck{Name: "operation_log_sink", Check: deps.operationLogHealth})
	}
	healthConfig := (densehttp.HealthConfig{
		Checks:   checks,
		Degraded: deps.backend.degraded,
		Reason:   deps.backend.reason,
	}).WithSharedDependencyChecks()
	e := densehttp.NewServer(deps.cfg, transportLogger(deps.logger), healthConfig)
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
		CredentialRepo:           deps.credentialRepo,
		TeamSvc:                  deps.teamService,
		RateLimitService:         deps.backend.rateLimitService,
		UsageMetrics:             deps.usageMetrics,
		AuditService:             deps.auditService,
		SecurityService:          deps.securityService,
		SSOAuthenticator:         deps.ssoService,
		OAuthAuthenticator:       deps.ssoService,
		OAuthMetadata:            deps.ssoService,
		Config:                   &deps.cfg,
		Logger:                   transportLogger(deps.logger),
		CredentialVerifier:       deps.credentialVerifier,
		CredentialLookupPrefixes: deps.credentialLookupPrefixes,
		LastUsedRecorder:         deps.activityWriter,
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
		CredentialRepo:           deps.credentialRepo,
		TeamSvc:                  deps.teamService,
		CredentialSvc:            deps.credentialService,
		RateLimitSvc:             deps.backend.rateLimitService,
		UsageMetrics:             deps.usageMetrics,
		Telemetry:                deps.telemetry.Reader,
		GraphView:                deps.graph,
		RecallSvc:                deps.recall,
		DreamSvc:                 deps.dream,
		PrivateMemory:            deps.privateMemory,
		AuditSvc:                 deps.auditService,
		SecuritySvc:              deps.securityService,
		SSOService:               deps.ssoService,
		PortalSession:            deps.portalSession,
		AppConfig:                deps.appConfig,
		Config:                   &deps.cfg,
		CredentialVerifier:       deps.credentialVerifier,
		CredentialLookupPrefixes: deps.credentialLookupPrefixes,
		LastUsedRecorder:         deps.activityWriter,
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
			transportLogger(deps.logger),
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
