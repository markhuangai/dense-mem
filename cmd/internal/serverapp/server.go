package serverapp

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"log"
	"log/slog"
	nethttp "net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/conflictassessment"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/sse"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func RunActiveServer(
	startupCtx context.Context,
	cfg config.Config,
	pgDB *postgres.DB,
	logger observability.LogProvider,
	level slog.Level,
	authority authorityBootstrap,
	options RuntimeOptions,
) {
	if !cfg.IsEmbeddingConfigured() {
		log.Fatal("active authority requires configured embedding provider")
	}
	if !VerifierConfigured(&cfg) {
		log.Fatal("active authority requires configured verifier provider")
	}
	backend, err := buildBackendBundle(startupCtx, cfg)
	if err != nil {
		log.Fatalf("failed to build backend: %v", err)
	}
	defer backend.closeFn()
	if options.RequireRedis && backend.counterStore == nil {
		log.Fatal("runtime requires REDIS_ADDR")
	}
	logInMemoryModeWarning(logger, backend.degraded, backend.reason)

	rlsHelper := postgres.NewRLS()
	teamRepo := repository.NewTeamRepository(pgDB.GetDB(), rlsHelper)
	credentialRepo := repository.NewCredentialRepository(pgDB.GetDB(), rlsHelper)
	accessAuthentication := buildAccessAuthenticationApplication(
		credentialRepo,
		credentialRepo,
		cfg.AuthVerifyMaxConcurrency,
		logger,
	)
	credentialVerifier := accessAuthentication.CredentialVerifier
	activityWriter := accessAuthentication.ActivityWriter
	activityWriter.Start(context.Background())
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = activityWriter.Shutdown(shutdownCtx)
	}()
	ssoRepo := repository.NewSSORepository(pgDB.GetDB(), rlsHelper)
	portalSessionRepo := repository.NewUserPortalSessionRepository(pgDB.GetDB(), rlsHelper)
	directoryIdentityRepo := repository.NewDirectoryIdentityRepository(pgDB.GetDB(), rlsHelper)
	controlIdentityRepo := repository.NewControlIdentityRepository(pgDB.GetDB(), rlsHelper)
	appConfigRepo := repository.NewAppConfigRepository(pgDB.GetDB(), rlsHelper)
	securityRepo := repository.NewSecurityRepository(pgDB.GetDB(), rlsHelper)
	usageMetricsRepo := repository.NewUsageMetricsRepository(pgDB.GetDB(), rlsHelper)
	operationLogRepo := repository.NewOperationLogRepository(pgDB.GetDB(), rlsHelper)
	recallFeedbackEventRepo := repository.NewRecallFeedbackEventRepository(pgDB.GetDB(), rlsHelper)
	privateMemoryRepo := repository.NewPrivateMemoryRepository(pgDB.GetDB(), rlsHelper)
	semanticRepo := repository.NewSemanticRepository(pgDB.GetDB(), rlsHelper)
	ledgerRepo := repository.NewLedgerRepositoryWithRuntimeConfig(
		pgDB.GetDB(),
		rlsHelper,
		repository.ConflictRuntimeConfig{
			ReviewTTLDays: cfg.GetConflictReviewTTLDays(),
			Timezone:      cfg.GetAppTimezone(),
		},
	)
	conflictQueueService := buildConflictQueueApplication(ledgerRepo)
	evidenceConflictService := buildEvidenceConflictApplication(ledgerRepo)
	if err := checkActiveAuthority(authority); err != nil {
		log.Fatalf("active boot blocked: %v", err)
	}
	searchRepo, searchContract, err := buildSearchRepositoryApplication(startupCtx, cfg, pgDB, rlsHelper)
	if err != nil {
		log.Fatalf("active search bootstrap blocked: %v", err)
	}
	logger.Info(
		"postgres authority enabled",
		observability.String("mode", string(authority.Mode)),
		observability.String("readiness", authority.ReadinessMessage),
	)
	logger.Info(
		"search contract ready",
		observability.String("embedding_contract_id", searchContract.Contract.EmbeddingContractID),
		observability.String("search_index_generation_id", searchContract.Contract.SearchIndexGenerationID),
		observability.String("index_strategy", searchContract.Contract.IndexStrategy),
	)
	auditService := buildAuditApplication(pgDB.GetDB())
	appConfigService := buildConfigurationApplication(appConfigRepo, auditService)
	operationLogService := buildOperationLogApplication(operationLogRepo, appConfigService)
	activeLogger := buildActiveApplicationLogger(level, operationLogService)
	logger = activeLogger
	slog.SetDefault(activeLogger.Slog())
	operationLogService.Start(context.Background())
	securityService := buildSecurityApplication(securityRepo, auditService)
	usageMetricsService := buildUsageMetricsApplication(usageMetricsRepo, logger)
	usageMetricsService.Start(context.Background())
	accessApplication := buildAccessApplication(accessApplicationDependencies{
		TeamRepo:              teamRepo,
		CredentialRepo:        credentialRepo,
		ActiveCredentialRepo:  credentialRepo,
		CredentialActivity:    credentialRepo,
		CredentialBatch:       credentialRepo,
		SSORepo:               ssoRepo,
		PortalSessionRepo:     portalSessionRepo,
		DirectoryIdentityRepo: directoryIdentityRepo,
		ControlIdentityRepo:   controlIdentityRepo,
		CredentialVerifier:    credentialVerifier,
		ActivityWriter:        activityWriter,
		Audit:                 auditService,
		RuntimeConfig:         appConfigService,
		Logger:                logger,
		StatePurger:           backend.cleanupRepo,
		SessionInvalidator:    backend.cleanupRepo,
	})
	teamService := accessApplication.TeamService
	credentialService := accessApplication.CredentialService
	ssoService := accessApplication.SSOService
	portalSessionService := accessApplication.PortalSessionService
	directoryIdentityService := accessApplication.DirectoryIdentity
	controlIdentityService := accessApplication.ControlIdentity
	privateMemoryService, err := preparePrivateMemoryService(
		startupCtx, privateMemoryRepo, appConfigService, backend.cleanupRepo, auditService, logger,
	)
	if err != nil {
		log.Fatalf("private-memory erasure boot blocked: %v", err)
	}
	rateLimitService := backend.rateLimitService
	runtimeCtx := RuntimeContext{
		Config:            &cfg,
		TeamService:       teamService,
		CredentialService: credentialService,
		CounterStore:      backend.counterStore,
		PostgresDB:        pgDB.GetDB(),
		RLS:               rlsHelper,
		Logger:            logger,
	}
	telemetry, err := buildTelemetryApplication(startupCtx, cfg, appConfigService, ledgerRepo, ledgerRepo, logger)
	if err != nil {
		log.Fatalf("failed to build telemetry application: %v", err)
	}
	if telemetry.PricingRefreshContext != nil {
		go refreshTelemetryPricingCacheUntilCanceled(telemetry.PricingRefreshContext, appConfigService, logger)
	}
	discoverabilityMetrics := telemetry.Metrics
	telemetryReader := telemetry.Reader
	telemetryPrometheusService := telemetry.Prometheus
	telemetryHTTPMetrics := telemetry.HTTPMetrics
	telemetryScrapeHandler := telemetry.ScrapeHandler
	pricingRefreshCancel := telemetry.PricingRefreshCancel
	searchApplication := buildSearchProviders(cfg, searchRepo, searchContract, discoverabilityMetrics, logger)
	openaiProvider := searchApplication.EmbeddingProvider
	retryEmbedder := searchApplication.RetryEmbedding
	assessmentLimits := assessorprovider.SemanticAssessmentLimitsForConfig(&cfg)
	aiHTTPClient := &nethttp.Client{Timeout: time.Duration(cfg.GetAIVerifierTimeoutSeconds()) * time.Second}
	aiConcurrencyGate := modelprovider.NewConcurrencyGate(config.AIVerifierMaxConcurrency(&cfg))
	verifierProvider := verifier.NewOpenAIVerifierWithAssessmentLimitsAndConcurrencyGate(&cfg, aiHTTPClient, verifier.SemanticAssessmentLimits(assessmentLimits), aiConcurrencyGate)
	verifierProvider.SetMetrics(discoverabilityMetrics)
	assessorProvider := assessorprovider.NewOpenAIAssessorWithAssessmentLimitsAndConcurrencyGate(&cfg, aiHTTPClient, assessmentLimits, aiConcurrencyGate)
	assessorProvider.SetMetrics(discoverabilityMetrics)
	conflictReviewRunner, err := buildConflictReviewApplication(conflictReviewApplicationDependencies{
		Ledger:           ledgerRepo,
		Provider:         verifierProvider,
		Embeddings:       retryEmbedder,
		EmbeddingTimeout: time.Duration(cfg.GetAIEmbeddingTimeoutSeconds()) * time.Second,
		Timezone:         cfg.GetAppTimezone(),
		Limits:           conflictassessment.SemanticAssessmentLimits(assessmentLimits),
		Metrics:          discoverabilityMetrics,
	})
	if err != nil {
		log.Fatalf("failed to build conflict review runner: %v", err)
	}
	applications := buildApplicationBundle(applicationCompositionDependencies{
		Ledger:                 ledgerRepo,
		Semantic:               semanticRepo,
		Search:                 searchRepo,
		RecallFeedbackEvents:   recallFeedbackEventRepo,
		Assessor:               assessorProvider,
		GeneratorTransport:     assessorProvider,
		EmbeddingProvider:      openaiProvider,
		RetryEmbeddingProvider: retryEmbedder,
		AssessmentLimits:       assessmentLimits,
		Metrics:                discoverabilityMetrics,
		Logger:                 logger,
		Audit:                  auditService,
		AppConfig:              appConfigService,
		Teams:                  teamService,
		CommunitySummary:       verifierProvider,
		DreamEvidenceStore:     semanticRepo,
		DreamModel:             cfg.GetAIVerifierModel(),
		ProviderCycleLease:     dreamProviderCycleLease(cfg),
		CorrectionTimeout:      time.Duration(cfg.GetAIEmbeddingTimeoutSeconds()) * time.Second,
		CorrectionExecutor:     buildSemanticWriteCorrectionExecutor(openaiProvider),
		TelemetryPrometheus:    telemetryPrometheusService,
	})
	rememberSvc := applications.Remember
	recallSvc := applications.Recall
	communitySvc := applications.Community
	lifecycleSvc := applications.Lifecycle
	contextSvc := applications.Context
	dreamSvc := applications.Dream
	controlDreamSvc := applications.ControlDream
	graphViewSvc := applications.Graph
	memoryPackSvc := applications.MemoryPack
	recallFeedbackEventService := applications.RecallFeedback
	recallFeedbackEventService.Start(context.Background())

	toolRegistry, err := registry.BuildActive(registry.Dependencies{
		Core: registry.CoreDependencies{
			Metrics:              discoverabilityMetrics,
			RecallFeedbackConfig: appConfigService,
			RecallFeedbackEvents: recallFeedbackEventService,
			EvaluationAudit:      auditService,
		},
		RememberBindings:   registry.RememberBindings{Service: rememberSvc},
		RecallBindings:     registry.RecallBindings{Service: recallSvc, Dreams: dreamSvc},
		LifecycleBindings:  registry.LifecycleBindings{Service: lifecycleSvc},
		TraceBindings:      registry.TraceBindings{Service: contextSvc},
		DreamBindings:      registry.DreamBindings{Service: dreamSvc},
		MemoryPackBindings: registry.MemoryPackBindings{Service: memoryPackSvc},
		EvaluationBindings: registry.EvaluationBindings{
			Repository:  semanticRepo,
			Communities: semanticRepo,
		},
	})
	if err != nil {
		log.Fatalf("failed to build active tool registry: %v", err)
	}
	if options.ConfigureRegistry != nil {
		toolRegistry, err = options.ConfigureRegistry(startupCtx, runtimeCtx, toolRegistry)
		if err != nil {
			log.Fatalf("failed to configure runtime tool registry: %v", err)
		}
	}
	streamLifecycle := sse.NewStreamLifecycleWithConfig(
		backend.concurrencyLimiter,
		sse.NewHeartbeatSenderWithInterval(time.Duration(cfg.GetSSEHeartbeatSeconds())*time.Second),
		time.Duration(cfg.GetSSEMaxDurationSeconds())*time.Second,
		backend.streamCleanupRepo,
	)
	mcpHandler := handler.NewMCPHandlerWithLifecycleAndRuntimeConfig(toolRegistry, logger, streamLifecycle, appConfigService, dreamSvc)

	checks := []http.HealthCheck{
		{Name: "postgres", Check: func(ctx context.Context) error {
			return pgDB.Ping(ctx)
		}},
		{Name: "postgres_topology", Check: func(ctx context.Context) error {
			return postgres.ValidateSinglePrimaryTopology(ctx, pgDB.GetDB())
		}},
		{Name: "pgvector", Check: func(ctx context.Context) error {
			return postgres.CheckPGVectorExtension(ctx, pgDB.GetDB())
		}},
		{Name: "authority", Check: func(ctx context.Context) error {
			return checkActiveAuthority(authority)
		}},
		{Name: "search_readiness", Check: func(ctx context.Context) error {
			return checkSearchReadiness(ctx, searchRepo)
		}},
	}
	if backend.redisPingFn != nil {
		checks = append(checks, http.HealthCheck{Name: "redis", Check: backend.redisPingFn})
	}
	healthConfig := (http.HealthConfig{
		Checks:   checks,
		Degraded: backend.degraded,
		Reason:   backend.reason,
	}).WithSharedDependencyChecks()
	e := http.NewServer(cfg, logger, healthConfig)
	e.Use(middleware.CorrelationIDMiddleware(), middleware.ClientIPMiddleware())
	e.Use(middleware.SecurityBanMiddleware(securityService))
	http.RegisterOAuthProtectedResourceRoutes(e, ssoService)
	if err := http.RegisterDirectorySCIM(e, directoryIdentityService, http.DirectorySCIMConfig{
		RuntimeConfig: appConfigService,
		Security:      securityService,
		RateLimitSvc:  rateLimitService,
		Config:        &cfg,
	}); err != nil {
		log.Fatalf("failed to register directory SCIM routes: %v", err)
	}
	runtimeCtx.Echo = e
	if options.RegisterRoutes != nil {
		if err := options.RegisterRoutes(runtimeCtx); err != nil {
			log.Fatalf("failed to register runtime routes: %v", err)
		}
	}
	protectedDeps := http.ProtectedDeps{
		MCP: http.MCPBindings{
			CredentialRepo:     credentialRepo,
			TeamSvc:            teamService,
			RateLimitService:   rateLimitService,
			UsageMetrics:       usageMetricsService,
			AuditService:       auditService,
			SecurityService:    securityService,
			SSOAuthenticator:   ssoService,
			OAuthAuthenticator: ssoService,
			OAuthMetadata:      ssoService,
			Config:             &cfg,
			Logger:             logger,
			CredentialVerifier: credentialVerifier,
			LastUsedRecorder:   activityWriter,
		},
	}
	protectedDeps.PostAuthMiddleware = append(protectedDeps.PostAuthMiddleware, options.PostAuthMiddleware...)
	if telemetryHTTPMetrics != nil {
		protectedDeps.PostAuthMiddleware = append(protectedDeps.PostAuthMiddleware, middleware.TelemetryHTTPMiddleware(telemetryHTTPMetrics))
	}
	http.RegisterProtectedRoutesWithHandlers(e, protectedDeps, http.ProtectedHandlers{
		MCPPost: mcpHandler.HandlePost,
		MCPGet:  mcpHandler.HandleGet,
	})
	userPortalDeps := http.UserPortalDeps{
		CredentialRepo: credentialRepo,
		TeamSvc:        teamService,
		CredentialSvc:  credentialService,
		RateLimitSvc:   rateLimitService,
		UsageMetrics:   usageMetricsService,
		Telemetry:      telemetryReader,
		Memory: http.MemoryPortalBindings{
			GraphView:     graphViewSvc,
			RecallSvc:     recallSvc,
			DreamSvc:      dreamSvc,
			PrivateMemory: privateMemoryService,
		},
		AuditSvc:           auditService,
		SecuritySvc:        securityService,
		SSOService:         ssoService,
		PortalSession:      portalSessionService,
		AppConfig:          appConfigService,
		Config:             &cfg,
		CredentialVerifier: credentialVerifier,
		LastUsedRecorder:   activityWriter,
	}
	userPortalDeps.ExtraMiddleware = append(userPortalDeps.ExtraMiddleware, options.UserPortalMiddleware...)
	if telemetryHTTPMetrics != nil {
		userPortalDeps.ExtraMiddleware = append(userPortalDeps.ExtraMiddleware, middleware.TelemetryHTTPMiddleware(telemetryHTTPMetrics))
	}
	http.RegisterUserPortal(e, userPortalDeps)
	var controlServer *echo.Echo
	var telemetryServer *echo.Echo
	if !options.DisableControlPortal {
		controlServer, err = http.NewControlPortalServerWithCapabilityBindings(
			&cfg,
			teamService,
			credentialService,
			usageMetricsService,
			http.ControlPortalBindings{Telemetry: http.ControlPortalTelemetry{
				Reader:            telemetryReader,
				HTTPMetrics:       telemetryHTTPMetrics,
				ScrapeHandler:     telemetryScrapeHandler,
				ScrapeToken:       cfg.GetTelemetryScrapeToken(),
				SSO:               ssoService,
				Directory:         directoryIdentityService,
				ControlIdentity:   controlIdentityService,
				Config:            appConfigService,
				Logs:              operationLogService,
				RecallFeedback:    recallFeedbackEventService,
				Dreams:            controlDreamSvc,
				Communities:       communitySvc,
				ConflictQueue:     conflictQueueService,
				EvidenceConflicts: evidenceConflictService,
				Convergence:       searchApplication.Convergence,
				RememberAttempts:  buildRememberAttemptDiagnostics(ledgerRepo),
				PrivateMemory:     privateMemoryService,
			}},
			healthConfig,
			logger,
			securityService,
		)
		if err != nil {
			log.Fatalf("failed to build control portal server: %v", err)
		}
		logger.Info("starting control portal", observability.String("addr", cfg.GetControlHTTPAddr()))
		go func() {
			if err := controlServer.Start(cfg.GetControlHTTPAddr()); err != nil {
				logServerStartError(logger, "control portal server error", err)
			}
		}()
	} else if telemetryScrapeHandler != nil {
		addr := strings.TrimSpace(options.MetricsOnlyAddr)
		if addr == "" {
			addr = ":8091"
		}
		telemetryServer, err = newTelemetryScrapeServer(telemetryScrapeHandler, cfg.GetTelemetryScrapeToken())
		if err != nil {
			log.Fatalf("failed to build telemetry scrape server: %v", err)
		}
		logger.Info("starting telemetry scrape server", observability.String("addr", addr))
		go func() {
			if err := telemetryServer.Start(addr); err != nil {
				logServerStartError(logger, "telemetry scrape server error", err)
			}
		}()
	}
	var runtimeShutdown func(context.Context) error
	if options.StartBackground != nil {
		runtimeShutdown, err = options.StartBackground(context.Background(), runtimeCtx)
		if err != nil {
			log.Fatalf("failed to start runtime background jobs: %v", err)
		}
	}

	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	privateMemoryService.Start(workerCtx)
	searchReconciliationCtx, cancelSearchReconciliation := context.WithCancel(workerCtx)
	go startSearchReconciliation(searchReconciliationCtx, searchApplication.Reconciliation, logger)
	defer cancelSearchReconciliation()
	ledgerRepo.StartRememberAttemptDiagnosticPurger(workerCtx, time.Hour, slog.Default())
	dreamSchedulerCtx, dreamSchedulerCancel := context.WithCancel(context.Background())
	defer dreamSchedulerCancel()
	go dreamservice.NewScheduler(dreamSvc, teamService, slog.Default()).Start(dreamSchedulerCtx)
	communitySchedulerCtx, communitySchedulerCancel := context.WithCancel(context.Background())
	defer communitySchedulerCancel()
	go communityservice.NewScheduler(communitySvc, teamService, appConfigService, slog.Default()).Start(communitySchedulerCtx)
	conflictReviewCtx, conflictReviewCancel := context.WithCancel(context.Background())
	defer conflictReviewCancel()
	startConflictReviewWorkers(conflictReviewCtx, logger, teamService, conflictReviewRunner, &cfg, discoverabilityMetrics)

	httpAddr := os.Getenv("HTTP_ADDR")
	if httpAddr == "" {
		httpAddr = config.DefaultHTTPAddr
	}
	logger.Info("starting server", observability.String("addr", httpAddr))
	go func() {
		if err := e.Start(httpAddr); err != nil {
			logServerStartError(logger, "server error", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down server")
	workerCancel()
	dreamSchedulerCancel()
	communitySchedulerCancel()
	conflictReviewCancel()
	if pricingRefreshCancel != nil {
		pricingRefreshCancel()
	}
	if err := http.ShutdownServer(e, logger); err != nil {
		logger.Error("server shutdown error", err)
	}
	if controlServer != nil {
		if err := http.ShutdownControlPortal(controlServer, logger); err != nil {
			logger.Error("control portal server shutdown error", err)
		}
	}
	if telemetryServer != nil {
		if err := shutdownTelemetryScrapeServer(telemetryServer); err != nil {
			logger.Error("telemetry scrape server shutdown error", err)
		}
	}
	if runtimeShutdown != nil {
		runtimeShutdownCtx, runtimeShutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer runtimeShutdownCancel()
		if err := runtimeShutdown(runtimeShutdownCtx); err != nil {
			logger.Error("runtime background shutdown error", err)
		}
	}
	metricsShutdownCtx, metricsShutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := usageMetricsService.Shutdown(metricsShutdownCtx); err != nil {
		logger.Error("usage metrics shutdown error", err)
	}
	metricsShutdownCancel()
	operationLogShutdownCtx, operationLogShutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer operationLogShutdownCancel()
	if err := operationLogService.Shutdown(operationLogShutdownCtx); err != nil {
		log.Printf("operation log shutdown error: %v", err)
	}
	recallFeedbackShutdownCtx, recallFeedbackShutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer recallFeedbackShutdownCancel()
	if err := recallFeedbackEventService.Shutdown(recallFeedbackShutdownCtx); err != nil {
		log.Printf("recall feedback event shutdown error: %v", err)
	}
}

func startConflictReviewWorkers(
	ctx context.Context,
	logger observability.LogProvider,
	teams service.TeamService,
	ledger conflictReviewLedger,
	cfg *config.Config,
	metrics observability.DiscoverabilityMetrics,
) {
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	hostname, _ := os.Hostname()
	baseWorkerID := fmt.Sprintf("conflict-review-%s-%d", hostname, os.Getpid())
	count := cfg.GetConflictReviewMaxConcurrency()
	if count < 1 {
		count = 1
	}
	if count > 16 {
		count = 16
	}
	for workerIndex := 0; workerIndex < count; workerIndex++ {
		workerID := fmt.Sprintf("%s-%d", baseWorkerID, workerIndex+1)
		go func(workerID string, workerIndex int) {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				processConflictReviewTick(ctx, logger, teams, ledger, cfg, metrics, workerID, workerIndex, count)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}(workerID, workerIndex)
	}
}

type conflictReviewTeamLister interface {
	List(ctx context.Context, limit, offset int) ([]*domain.Team, error)
}

func processConflictReviewTick(
	ctx context.Context,
	logger observability.LogProvider,
	teams conflictReviewTeamLister,
	ledger conflictReviewLedger,
	cfg *config.Config,
	metrics observability.DiscoverabilityMetrics,
	workerID string,
	workerIndex int,
	workerCount int,
) {
	const pageSize = 100
	if workerCount < 1 {
		workerCount = 1
	}
	if workerIndex < 0 || workerIndex >= workerCount {
		workerIndex = 0
	}
	now := time.Now()
	for offset := workerIndex * pageSize; ; offset += pageSize * workerCount {
		page, err := teams.List(ctx, pageSize, offset)
		if err != nil {
			logger.Error("conflict review team list failed", errConflictReviewTeamListFailed)
			return
		}
		if len(page) == 0 {
			return
		}
		for _, team := range page {
			if team == nil || !conflictReviewDueForTeam(now, cfg, team.ID.String()) {
				continue
			}
			if err := processTeamConflictReview(ctx, logger, ledger, cfg, metrics, team.ID.String(), workerID, now); err != nil {
				logger.Error("conflict review run failed", errConflictReviewRunFailed, observability.String("team_id", team.ID.String()))
			}
		}
		if len(page) < pageSize {
			return
		}
	}
}

const conflictReviewCompletionTimeout = 15 * time.Second

func processTeamConflictReview(
	ctx context.Context,
	logger observability.LogProvider,
	ledger conflictReviewLedger,
	cfg *config.Config,
	metrics observability.DiscoverabilityMetrics,
	teamID string,
	workerID string,
	now time.Time,
) error {
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	started := time.Now()
	outcome := "completed"
	defer func() {
		observability.RecordConflictReviewDuration(observability.WithMetricIdentity(ctx, teamID, ""), metrics, time.Since(started).Seconds(), outcome)
	}()
	lease := time.Duration(cfg.GetConflictReviewLeaseSeconds()) * time.Second
	runInput := repository.ConflictReviewRunInput{TeamID: teamID, WorkerID: workerID, LocalRunDate: now, Timezone: cfg.GetAppTimezone(), Lease: lease}
	run, claimed, err := ledger.ReserveRelationshipConflictReviewRun(ctx, runInput)
	if err != nil {
		outcome = "error"
		return err
	}
	if run == nil || !claimed || run.Status == "completed" {
		outcome = "skipped"
		return nil
	}
	counts := repository.ConflictReviewRunCompleteInput{
		TeamID:      teamID,
		ReviewRunID: run.ReviewRunID,
		WorkerID:    workerID,
		Status:      "completed",
	}
	if _, err := ledger.ProcessPendingConflictDerivedEvidence(ctx, repository.ClaimConflictDerivedEvidenceTasksInput{
		TeamID:      teamID,
		ReviewRunID: run.ReviewRunID,
		WorkerID:    workerID,
		Limit:       cfg.GetConflictReviewBatchSize(),
		Lease:       lease,
	}); err != nil {
		counts.Status = "failed"
		counts.LastError = "derived evidence retry failed"
		outcome = "partial_error"
	}
	attempted := map[string]struct{}{}
	for {
		renewed, owned, err := ledger.ReserveRelationshipConflictReviewRun(ctx, runInput)
		if err != nil {
			counts.Status = "failed"
			counts.LastError = safeConflictReviewError(err)
			outcome = "failed"
			break
		}
		if renewed == nil || !owned || renewed.ReviewRunID != run.ReviewRunID || renewed.WorkerID != workerID {
			counts.Status = "failed"
			counts.LastError = "conflict review run lease lost"
			outcome = "failed"
			break
		}
		run = renewed
		excluded := make([]string, 0, len(attempted))
		for id := range attempted {
			excluded = append(excluded, id)
		}
		cases, err := ledger.ClaimRelationshipConflictCases(ctx, repository.ClaimRelationshipConflictCasesInput{
			TeamID:      teamID,
			WorkerID:    workerID,
			ReviewRunID: run.ReviewRunID,
			// Claims are leased per case while synchronous resolution may spend
			// the full embedding timeout before the next case is processed.
			Limit:               1,
			Lease:               lease,
			MaxAttempts:         cfg.GetConflictReviewMaxAttempts(),
			Now:                 time.Now().UTC(),
			ExcludedConflictIDs: excluded,
		})
		if err != nil {
			counts.Status = "failed"
			counts.LastError = safeConflictReviewError(err)
			outcome = "failed"
			break
		}
		if len(cases) == 0 {
			break
		}
		for _, conflictCase := range cases {
			if _, ok := attempted[conflictCase.ConflictID]; ok {
				continue
			}
			attempted[conflictCase.ConflictID] = struct{}{}
			counts.ClaimedCases++
			result, err := ledger.ReviewRelationshipConflictCase(ctx, repository.ReviewRelationshipConflictCaseInput{
				TeamID:      teamID,
				WorkerID:    workerID,
				ReviewRunID: run.ReviewRunID,
				ConflictID:  conflictCase.ConflictID,
				Now:         time.Now().UTC(),
			})
			if err != nil {
				counts.FailedCases++
				logger.Error("conflict review case failed", errConflictReviewCaseFailed, observability.String("team_id", teamID), observability.String("conflict_id", conflictCase.ConflictID))
				continue
			}
			switch result.Outcome {
			case repository.ConflictReviewOutcomeResolve:
				counts.ResolvedCases++
			case repository.ConflictReviewOutcomeOverdue:
				counts.OverdueCases++
			default:
				counts.NoOpCases++
			}
		}
	}
	if counts.FailedCases > 0 && counts.Status == "completed" {
		counts.Status = "failed"
		counts.LastError = "one or more conflict cases failed"
		outcome = "partial_error"
	} else if counts.ClaimedCases == 0 && counts.Status == "completed" {
		outcome = "empty"
	}
	completeCtx, completeCancel := context.WithTimeout(context.WithoutCancel(ctx), conflictReviewCompletionTimeout)
	defer completeCancel()
	if err := ledger.CompleteRelationshipConflictReviewRun(completeCtx, counts); err != nil {
		outcome = "error"
		return err
	}
	return nil
}

var (
	errConflictReviewTeamListFailed = errors.New("conflict review team list failed")
	errConflictReviewRunFailed      = errors.New("conflict review run failed")
	errConflictReviewCaseFailed     = errors.New("conflict review case failed")
)

func safeConflictReviewError(err error) string {
	if err == nil {
		return ""
	}
	return "conflict review repository operation failed"
}

type conflictReviewLedger interface {
	ReserveRelationshipConflictReviewRun(context.Context, repository.ConflictReviewRunInput) (*repository.ConflictReviewRunRecord, bool, error)
	ClaimRelationshipConflictCases(context.Context, repository.ClaimRelationshipConflictCasesInput) ([]repository.RelationshipConflictCaseRecord, error)
	ReviewRelationshipConflictCase(context.Context, repository.ReviewRelationshipConflictCaseInput) (*repository.ReviewRelationshipConflictCaseResult, error)
	ProcessPendingConflictDerivedEvidence(context.Context, repository.ClaimConflictDerivedEvidenceTasksInput) (int, error)
	CompleteRelationshipConflictReviewRun(context.Context, repository.ConflictReviewRunCompleteInput) error
}

func conflictReviewDueForTeam(now time.Time, cfg *config.Config, teamID string) bool {
	location := conflictReviewLocation(cfg.GetAppTimezone())
	localNow := now.In(location)
	start, err := time.Parse("15:04", cfg.GetConflictReviewStartTimeLocal())
	if err != nil {
		return false
	}
	year, month, day := localNow.Date()
	scheduled := time.Date(year, month, day, start.Hour(), start.Minute(), 0, 0, location)
	jitterSeconds := cfg.GetConflictReviewJitterSeconds()
	if jitterSeconds > 3600 {
		jitterSeconds = 3600
	}
	if jitterSeconds > 0 {
		delay := int(crc32.ChecksumIEEE([]byte(teamID))) % (jitterSeconds + 1)
		scheduled = scheduled.Add(time.Duration(delay) * time.Second)
	}
	return !localNow.Before(scheduled)
}

var conflictReviewLocationCache sync.Map

func conflictReviewLocation(name string) *time.Location {
	name = strings.TrimSpace(name)
	if name == "" {
		return time.Local
	}
	if cached, ok := conflictReviewLocationCache.Load(name); ok {
		return cached.(*time.Location)
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.Local
	}
	actual, _ := conflictReviewLocationCache.LoadOrStore(name, location)
	return actual.(*time.Location)
}

func checkActiveAuthority(authority authorityBootstrap) error {
	if authority.Mode != authorityActive ||
		authority.Marker == nil ||
		authority.Marker.Status != domain.MigrationMarkerCompatible {
		return fmt.Errorf("%w: compatible authority marker is required", errAuthorityBlocked)
	}
	return nil
}

func logServerStartError(logger observability.LogProvider, message string, err error) {
	if errors.Is(err, nethttp.ErrServerClosed) {
		return
	}
	logger.Error(message, err)
}
