package serverapp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	nethttp "net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/openapi"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/service/embeddingservice"
	"github.com/markhuangai/dense-mem/internal/service/graphview"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
	"github.com/markhuangai/dense-mem/internal/sse"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

// CounterStore is the atomic counter surface used by runtimes that need
// Redis-backed limits outside the normal rate limiter.
type CounterStore interface {
	IncrWithExpire(ctx context.Context, key string, expireSeconds int64) (int64, error)
	AddWithExpire(ctx context.Context, key string, delta int64, expireSeconds int64) (int64, error)
}

type RuntimeContext struct {
	Echo           *echo.Echo
	Config         *config.Config
	ProfileService service.ProfileService
	APIKeyService  service.APIKeyService
	CounterStore   CounterStore
	PostgresDB     *gorm.DB
	RLS            postgres.RLSHelper
	Logger         observability.LogProvider
}

type RuntimeOptions struct {
	DisableControlPortal bool
	RequireRedis         bool
	MetricsOnlyAddr      string
	ConfigureRegistry    func(context.Context, RuntimeContext, registry.Registry) (registry.Registry, error)
	RegisterRoutes       func(RuntimeContext) error
	StartBackground      func(context.Context, RuntimeContext) (func(context.Context) error, error)
	PostAuthMiddleware   []echo.MiddlewareFunc
	UserPortalMiddleware []echo.MiddlewareFunc
}

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
	profileRepo := repository.NewProfileRepository(pgDB.GetDB(), rlsHelper)
	apiKeyRepo := repository.NewAPIKeyRepository(pgDB.GetDB(), rlsHelper)
	ssoRepo := repository.NewSSORepository(pgDB.GetDB(), rlsHelper)
	appConfigRepo := repository.NewAppConfigRepository(pgDB.GetDB(), rlsHelper)
	securityRepo := repository.NewSecurityRepository(pgDB.GetDB(), rlsHelper)
	usageMetricsRepo := repository.NewUsageMetricsRepository(pgDB.GetDB(), rlsHelper)
	operationLogRepo := repository.NewOperationLogRepository(pgDB.GetDB(), rlsHelper)
	recallFeedbackEventRepo := repository.NewRecallFeedbackEventRepository(pgDB.GetDB(), rlsHelper)
	skillPackImportRepo := repository.NewSkillPackImportRepository(pgDB.GetDB(), rlsHelper)
	semanticRepo := repository.NewV2SemanticRepository(pgDB.GetDB(), rlsHelper)
	ledgerRepo := repository.NewV2LedgerRepository(pgDB.GetDB(), rlsHelper)
	searchRepo := repository.NewV2SearchRepository(pgDB.GetDB(), rlsHelper)

	if err := checkActiveAuthority(authority); err != nil {
		log.Fatalf("active boot blocked: %v", err)
	}
	searchContract, err := searchRepo.EnsureActiveSearchContract(startupCtx, repository.V2EnsureActiveSearchContractInput{
		Provider:   "openai",
		Model:      cfg.GetAIEmbeddingModel(),
		Dimensions: cfg.GetAIEmbeddingDimensions(),
	})
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

	auditService := service.NewAuditService(pgDB.GetDB())
	appConfigService := service.NewAppConfigService(appConfigRepo, auditService)
	operationLogService := service.NewOperationLogService(operationLogRepo, appConfigService)
	activeLogger := observability.NewWithSinks(level, operationLogService)
	logger = activeLogger
	slog.SetDefault(activeLogger.Slog())
	operationLogService.Start(context.Background())
	securityService := service.NewSecurityService(securityRepo, auditService)
	usageMetricsService := service.NewUsageMetricsService(usageMetricsRepo, logger)
	usageMetricsService.Start(context.Background())
	profileService := service.NewProfileService(profileRepo, auditService, backend.cleanupRepo)
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, profileService, auditService, backend.cleanupRepo, backend.cleanupRepo)
	ssoService := service.NewSSOService(ssoRepo, service.SSOConfig{
		RuntimeConfig: appConfigService,
		Logger:        logger,
	})
	rateLimitService := backend.rateLimitService
	runtimeCtx := RuntimeContext{
		Config:         &cfg,
		ProfileService: profileService,
		APIKeyService:  apiKeyService,
		CounterStore:   backend.counterStore,
		PostgresDB:     pgDB.GetDB(),
		RLS:            rlsHelper,
		Logger:         logger,
	}

	discoverabilityMetrics := observability.NoopDiscoverabilityMetrics()
	var (
		telemetryReader        service.TelemetryReader
		telemetryHTTPMetrics   observability.HTTPMetrics
		telemetryScrapeHandler nethttp.Handler
	)
	if cfg.GetTelemetryEnabled() {
		prometheusMetrics := observability.NewPrometheusMetrics()
		discoverabilityMetrics = prometheusMetrics
		telemetryHTTPMetrics = prometheusMetrics
		telemetryScrapeHandler = prometheusMetrics.Handler()
		telemetryReader = service.NewPrometheusTelemetryServiceWithJobAndLogger(
			cfg.GetTelemetryPrometheusURL(),
			time.Duration(cfg.GetTelemetryQueryTimeoutSeconds())*time.Second,
			cfg.GetTelemetryPrometheusJob(),
			logger,
		)
	}

	openaiProvider := embedding.NewOpenAIEmbeddingProvider(&cfg, nil)
	openaiProvider.SetMetrics(discoverabilityMetrics)
	retryEmbedder := embedding.NewRetryEmbeddingProviderWithKey(openaiProvider, logger, cfg.GetAIAPIKey())
	retryEmbedder.SetMetrics(discoverabilityMetrics)
	verifierProvider := verifier.NewOpenAIVerifier(&cfg, nil)
	verifierProvider.SetMetrics(discoverabilityMetrics)

	rememberSvc := memoryservice.NewRememberService(memoryservice.RememberDependencies{Ledger: ledgerRepo})
	recallSvc := memoryservice.NewRecallService(memoryservice.RecallDependencies{
		Search:          searchRepo,
		Provider:        retryEmbedder,
		Hypotheses:      semanticRepo,
		Communities:     semanticRepo,
		CommunityConfig: appConfigService,
	})
	lifecycleSvc := memoryservice.NewLifecycleService(memoryservice.LifecycleDependencies{
		Semantic:  semanticRepo,
		Placement: ledgerRepo,
	})
	contextSvc := contextservice.NewSemantic(semanticRepo)
	dreamSvc := dreamservice.New(dreamservice.Dependencies{
		Remember:  rememberSvc,
		Store:     semanticRepo,
		AppConfig: appConfigService,
		Profiles:  profileService,
		Locker:    dreamservice.NewPostgresCycleLocker(),
		Postgres:  pgDB.GetDB(),
		Generator: dreamservice.NewHeuristicGenerator(""),
		Metrics:   discoverabilityMetrics,
	})
	graphViewSvc := graphview.NewSemantic(semanticRepo)
	memoryPackSvc := skillpackservice.NewMemoryPackService(skillpackservice.MemoryPackDependencies{
		Semantic:    semanticRepo,
		Remember:    rememberSvc,
		Ledger:      skillPackImportRepo,
		HistoryDays: cfg.GetSkillPackImportHistoryDays(),
	})
	recallFeedbackEventService := service.NewRecallFeedbackEventService(recallFeedbackEventRepo, appConfigService, nil)
	recallFeedbackEventService.Start(context.Background())

	toolRegistry, err := registry.BuildActive(registry.Dependencies{
		Metrics:              discoverabilityMetrics,
		RecallFeedbackConfig: appConfigService,
		RecallFeedbackEvents: recallFeedbackEventService,
		EvaluationConfig:     appConfigService,
		EvaluationAudit:      auditService,
		Context:              contextSvc,
		Remember:             rememberSvc,
		Recall:               recallSvc,
		Lifecycle:            lifecycleSvc,
		Evaluation:           semanticRepo,
		EvaluationEnabled:    true,
		Communities:          semanticRepo,
		MemoryPack:           memoryPackSvc,
		Dreams:               dreamSvc,
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
	httpRegistry, err := registry.HTTPRegistryView(toolRegistry)
	if err != nil {
		log.Fatalf("failed to build active HTTP registry view: %v", err)
	}

	openAPIGen := openapi.New(httpRegistry, openapi.DefaultRoutes())
	streamLifecycle := sse.NewStreamLifecycleWithConfig(
		backend.concurrencyLimiter,
		sse.NewHeartbeatSenderWithInterval(time.Duration(cfg.GetSSEHeartbeatSeconds())*time.Second),
		time.Duration(cfg.GetSSEMaxDurationSeconds())*time.Second,
		backend.streamCleanupRepo,
	)
	toolCatalogHandler := handler.NewToolCatalogHandlerWithRuntimeConfig(httpRegistry, appConfigService)
	toolReadHandler := handler.NewToolReadHandlerWithRuntimeConfig(httpRegistry, appConfigService)
	toolExecuteHandler := handler.NewToolExecuteHandlerWithRuntimeConfig(httpRegistry, appConfigService)
	mcpHandler := handler.NewMCPHandlerWithLifecycleAndRuntimeConfig(toolRegistry, logger, streamLifecycle, appConfigService)
	openAPIAISafeHandler := handler.NewOpenAPIHandler(openAPIGen, openapi.SpecVariantAISafe)
	openAPIFullHandler := handler.NewOpenAPIHandler(openAPIGen, openapi.SpecVariantFull)
	recallHandler := handler.NewRecallHandler(recallSvc)
	dreamHandler := handler.NewDreamHandler(dreamSvc)

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
	healthConfig := http.HealthConfig{
		Checks:   checks,
		Degraded: backend.degraded,
		Reason:   backend.reason,
	}

	e := http.NewServer(cfg, logger, healthConfig)
	e.Use(middleware.CorrelationIDMiddleware())
	e.Use(middleware.ClientIPMiddleware())
	e.Use(middleware.SecurityBanMiddleware(securityService))
	runtimeCtx.Echo = e
	if options.RegisterRoutes != nil {
		if err := options.RegisterRoutes(runtimeCtx); err != nil {
			log.Fatalf("failed to register runtime routes: %v", err)
		}
	}
	protectedDeps := http.ProtectedDeps{
		APIKeyRepo:       apiKeyRepo,
		ProfileService:   profileService,
		ProfileSvc:       profileService,
		RateLimitService: rateLimitService,
		UsageMetrics:     usageMetricsService,
		AuditService:     auditService,
		SecurityService:  securityService,
		SSOAuthenticator: ssoService,
		Config:           &cfg,
		Logger:           logger,
	}
	protectedDeps.PostAuthMiddleware = append(protectedDeps.PostAuthMiddleware, options.PostAuthMiddleware...)
	if telemetryHTTPMetrics != nil {
		protectedDeps.PostAuthMiddleware = append(protectedDeps.PostAuthMiddleware, middleware.TelemetryHTTPMiddleware(telemetryHTTPMetrics))
	}
	http.RegisterProtectedRoutesWithHandlers(e, protectedDeps, http.ProtectedHandlers{
		APIKeySvc:      apiKeyService,
		ToolCatalog:    toolCatalogHandler.Handle,
		GetTool:        toolReadHandler.Handle,
		ExecuteTool:    toolExecuteHandler.Handle,
		MCPPost:        mcpHandler.HandlePost,
		MCPGet:         mcpHandler.HandleGet,
		OpenAPIAISafe:  openAPIAISafeHandler.Handle,
		OpenAPIFull:    openAPIFullHandler.Handle,
		Recall:         recallHandler.Handle,
		DreamingStatus: dreamHandler.Status,
		DreamingRuns:   dreamHandler.Runs,
		DreamList:      dreamHandler.List,
		DreamGet:       dreamHandler.Get,
	})
	userPortalDeps := http.UserPortalDeps{
		APIKeyRepo:   apiKeyRepo,
		ProfileSvc:   profileService,
		APIKeySvc:    apiKeyService,
		RateLimitSvc: rateLimitService,
		UsageMetrics: usageMetricsService,
		Telemetry:    telemetryReader,
		GraphView:    graphViewSvc,
		AuditSvc:     auditService,
		SecuritySvc:  securityService,
		SSOService:   ssoService,
		AppConfig:    appConfigService,
		Config:       &cfg,
	}
	userPortalDeps.ExtraMiddleware = append(userPortalDeps.ExtraMiddleware, options.UserPortalMiddleware...)
	if telemetryHTTPMetrics != nil {
		userPortalDeps.ExtraMiddleware = append(userPortalDeps.ExtraMiddleware, middleware.TelemetryHTTPMiddleware(telemetryHTTPMetrics))
	}
	http.RegisterUserPortal(e, userPortalDeps)

	var controlServer *echo.Echo
	var telemetryServer *echo.Echo
	if !options.DisableControlPortal {
		controlServer, err = http.NewControlPortalServerWithMetricsAndTelemetry(
			&cfg,
			profileService,
			apiKeyService,
			usageMetricsService,
			http.ControlPortalTelemetry{
				Reader:         telemetryReader,
				HTTPMetrics:    telemetryHTTPMetrics,
				ScrapeHandler:  telemetryScrapeHandler,
				ScrapeToken:    cfg.GetTelemetryScrapeToken(),
				SSO:            ssoService,
				Config:         appConfigService,
				Logs:           operationLogService,
				RecallFeedback: recallFeedbackEventService,
				Dreams:         dreamSvc,
			},
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
	startActiveWorkers(
		workerCtx,
		logger,
		profileService,
		ledgerRepo,
		searchRepo,
		semanticRepo,
		retryEmbedder,
		verifierProvider,
		discoverabilityMetrics,
		activePlacementLease(cfg.GetAIVerifierTimeoutSeconds(), cfg.GetPromoteTxTimeoutSeconds()),
		activeWorkerCount(cfg.GetAIVerifierMaxConcurrency()),
	)
	dreamSchedulerCtx, dreamSchedulerCancel := context.WithCancel(context.Background())
	defer dreamSchedulerCancel()
	go dreamservice.NewScheduler(dreamSvc, profileService, slog.Default()).Start(dreamSchedulerCtx)

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

func checkActiveAuthority(authority authorityBootstrap) error {
	if authority.Mode != authorityActive ||
		authority.Marker == nil ||
		authority.Marker.Status != domain.V2MigrationMarkerCompatible {
		return fmt.Errorf("%w: compatible V2 authority marker is required", errAuthorityBlocked)
	}
	return nil
}

func checkSearchReadiness(ctx context.Context, search interface {
	CheckSearchReadiness(context.Context) (*repository.V2SearchReadiness, error)
}) error {
	if search == nil {
		return fmt.Errorf("%w: search repository is required", repository.ErrV2SearchContractMismatch)
	}
	readiness, err := search.CheckSearchReadiness(ctx)
	if err != nil {
		return err
	}
	if readiness == nil || readiness.Ready {
		return nil
	}
	reasons := make([]string, 0, len(readiness.Reasons))
	for _, reason := range readiness.Reasons {
		message := strings.TrimSpace(reason.Message)
		if message == "" {
			message = strings.TrimSpace(reason.Code)
		}
		if message != "" {
			reasons = append(reasons, message)
		}
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "search readiness check failed")
	}
	return fmt.Errorf("%w: %s", repository.ErrV2SearchContractMismatch, strings.Join(reasons, "; "))
}

func startActiveWorkers(
	ctx context.Context,
	logger observability.LogProvider,
	profiles service.ProfileService,
	ledger *repository.V2LedgerRepositoryImpl,
	search repository.V2SearchRepository,
	semantic *repository.V2SemanticRepositoryImpl,
	embedder *embedding.RetryEmbeddingProvider,
	reviewer *verifier.OpenAIVerifier,
	metrics observability.DiscoverabilityMetrics,
	placementLease time.Duration,
	workerCount int,
) {
	hostname, _ := os.Hostname()
	baseWorkerID := fmt.Sprintf("active-%s-%d", hostname, os.Getpid())
	reviewSvc := memoryservice.NewSemanticReviewService(memoryservice.SemanticReviewDependencies{
		Provider: reviewer,
		Ledger:   ledger,
	})
	commitSvc := memoryservice.NewSemanticCommitService(memoryservice.SemanticCommitDependencies{
		PlacementCommit: ledger,
	})
	reviewSource := memoryservice.NewSemanticPlacementReviewSource(memoryservice.SemanticPlacementReviewSourceDependencies{
		Ledger:           ledger,
		Catalog:          semantic,
		ProposalProvider: reviewer,
	})

	for workerIndex := 0; workerIndex < activeWorkerCount(workerCount); workerIndex++ {
		workerID := fmt.Sprintf("%s-%d", baseWorkerID, workerIndex+1)
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				processActiveWorkerTick(ctx, logger, profiles, ledger, search, reviewSvc, commitSvc, reviewSource, embedder, metrics, workerID, placementLease)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
}

func activePlacementLease(verifierTimeoutSeconds int, commitTimeoutSeconds int) time.Duration {
	if verifierTimeoutSeconds <= 0 {
		verifierTimeoutSeconds = 60
	}
	if commitTimeoutSeconds <= 0 {
		commitTimeoutSeconds = 10
	}
	lease := time.Duration((verifierTimeoutSeconds*memoryservice.SemanticPlacementDefaultVerifierCallBudget)+commitTimeoutSeconds+30) * time.Second
	if lease < 5*time.Minute {
		return 5 * time.Minute
	}
	return lease
}

func logServerStartError(logger observability.LogProvider, message string, err error) {
	if errors.Is(err, nethttp.ErrServerClosed) {
		return
	}
	logger.Error(message, err)
}

func activeWorkerCount(verifierMaxConcurrency int) int {
	if verifierMaxConcurrency <= 0 {
		return 1
	}
	if verifierMaxConcurrency > 16 {
		return 16
	}
	return verifierMaxConcurrency
}

func processActiveWorkerTick(
	ctx context.Context,
	logger observability.LogProvider,
	profiles service.ProfileService,
	ledger *repository.V2LedgerRepositoryImpl,
	search repository.V2SearchRepository,
	reviewSvc memoryservice.SemanticReviewService,
	commitSvc memoryservice.SemanticCommitService,
	reviewSource memoryservice.SemanticPlacementReviewSource,
	embedder *embedding.RetryEmbeddingProvider,
	metrics observability.DiscoverabilityMetrics,
	workerID string,
	placementLease time.Duration,
) {
	const (
		pageSize                    = 100
		maxPlacementsPerTeamPerTick = 25
	)
	for offset := 0; ; offset += pageSize {
		teams, err := profiles.List(ctx, pageSize, offset)
		if err != nil {
			logger.Error("placement worker profile list failed", err)
			return
		}
		if len(teams) == 0 {
			return
		}
		for _, team := range teams {
			if team == nil {
				continue
			}
			teamID := team.ID.String()
			placementWorker := memoryservice.NewSemanticPlacementWorkerService(memoryservice.SemanticPlacementWorkerDependencies{
				Ledger:       ledger,
				Review:       reviewSvc,
				Commit:       commitSvc,
				ReviewSource: reviewSource,
				TeamID:       teamID,
				WorkerID:     workerID,
				Lease:        placementLease,
			})
			for i := 0; i < maxPlacementsPerTeamPerTick; i++ {
				processed, err := placementWorker.ProcessNextSemanticPlacement(ctx)
				if err != nil {
					logger.Error("semantic placement worker failed", err, observability.String("team_id", teamID))
					break
				}
				if !processed {
					break
				}
			}
			embeddingWorker := embeddingservice.NewEmbeddingWorkerService(embeddingservice.EmbeddingWorkerDependencies{
				Search:   search,
				Provider: embedder,
				Metrics:  metrics,
				TeamID:   teamID,
				WorkerID: workerID,
			})
			if _, err := embeddingWorker.ProcessNextBatch(ctx); err != nil {
				logger.Error("embedding worker failed", err, observability.String("team_id", teamID))
			}
		}
		if len(teams) < pageSize {
			return
		}
	}
}
