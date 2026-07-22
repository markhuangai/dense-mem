package main

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
	"github.com/markhuangai/dense-mem/internal/service/migrationcontrol"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
	"github.com/markhuangai/dense-mem/internal/sse"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func runActiveV2Server(
	startupCtx context.Context,
	cfg config.Config,
	pgDB *postgres.DB,
	logger observability.LogProvider,
	level slog.Level,
	authority v2AuthorityBootstrap,
) {
	if !cfg.IsEmbeddingConfigured() {
		log.Fatal("v2 active authority requires configured embedding provider")
	}
	if !verifierConfigured(&cfg) {
		log.Fatal("v2 active authority requires configured verifier provider")
	}

	backend, err := buildBackendBundle(startupCtx, cfg)
	if err != nil {
		log.Fatalf("failed to build backend: %v", err)
	}
	defer backend.closeFn()
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
	v2SemanticRepo := repository.NewV2SemanticRepository(pgDB.GetDB(), rlsHelper)
	v2LedgerRepo := repository.NewV2LedgerRepository(pgDB.GetDB(), rlsHelper)
	v2SearchRepo := repository.NewV2SearchRepository(pgDB.GetDB(), rlsHelper)
	v2MigrationControlRepo := repository.NewV2MigrationControlRepository(pgDB.GetDB(), rlsHelper)
	v2MigrationControlSvc := migrationcontrol.New(v2MigrationControlRepo, migrationcontrol.Config{Required: false})

	if err := checkV2ActiveAuthority(startupCtx, v2MigrationControlSvc); err != nil {
		log.Fatalf("v2 active boot blocked: %v", err)
	}
	searchContract, err := v2SearchRepo.EnsureActiveSearchContract(startupCtx, repository.V2EnsureActiveSearchContractInput{
		Provider:   "openai",
		Model:      cfg.GetAIEmbeddingModel(),
		Dimensions: cfg.GetAIEmbeddingDimensions(),
	})
	if err != nil {
		log.Fatalf("v2 active search bootstrap blocked: %v", err)
	}
	logger.Info(
		"v2 active authority enabled",
		observability.String("mode", string(authority.Mode)),
		observability.String("readiness", authority.ReadinessMessage),
	)
	logger.Info(
		"v2 active search contract ready",
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
	v2Verifier := verifier.NewOpenAIVerifier(&cfg, nil)
	v2Verifier.SetMetrics(discoverabilityMetrics)

	v2RememberSvc := memoryservice.NewV2RememberService(memoryservice.V2RememberDependencies{Ledger: v2LedgerRepo})
	v2RecallSvc := memoryservice.NewV2RecallService(memoryservice.V2RecallDependencies{
		Search:          v2SearchRepo,
		Provider:        retryEmbedder,
		Hypotheses:      v2SemanticRepo,
		Communities:     v2SemanticRepo,
		CommunityConfig: appConfigService,
	})
	v2LifecycleSvc := memoryservice.NewV2LifecycleService(memoryservice.V2LifecycleDependencies{
		Semantic:  v2SemanticRepo,
		Placement: v2LedgerRepo,
	})
	contextSvc := contextservice.NewV2Semantic(v2SemanticRepo)
	dreamSvc := dreamservice.New(dreamservice.Dependencies{
		V2Remember: v2RememberSvc,
		V2Dreams:   v2SemanticRepo,
		AppConfig:  appConfigService,
		Profiles:   profileService,
		Locker:     dreamservice.NewPostgresCycleLocker(),
		Postgres:   pgDB.GetDB(),
		Generator:  dreamservice.NewHeuristicGenerator(cfg.GetAIVerifierModel()),
		Metrics:    discoverabilityMetrics,
	})
	graphViewSvc := graphview.NewV2Semantic(v2SemanticRepo)
	v2SkillPackSvc := skillpackservice.NewV2(skillpackservice.V2Dependencies{
		Semantic:    v2SemanticRepo,
		Remember:    v2RememberSvc,
		Ledger:      skillPackImportRepo,
		HistoryDays: cfg.GetSkillPackImportHistoryDays(),
	})
	recallFeedbackEventService := service.NewRecallFeedbackEventService(recallFeedbackEventRepo, appConfigService, nil)
	recallFeedbackEventService.Start(context.Background())

	toolRegistry, err := registry.BuildV2Active(registry.Dependencies{
		Metrics:              discoverabilityMetrics,
		RecallFeedbackConfig: appConfigService,
		RecallFeedbackEvents: recallFeedbackEventService,
		EvaluationConfig:     appConfigService,
		EvaluationAudit:      auditService,
		Context:              contextSvc,
		V2Remember:           v2RememberSvc,
		V2Recall:             v2RecallSvc,
		V2Lifecycle:          v2LifecycleSvc,
		V2Evaluation:         v2SemanticRepo,
		V2EvaluationEnabled:  true,
		V2Communities:        v2SemanticRepo,
		V2SkillPack:          v2SkillPackSvc,
		Dreams:               dreamSvc,
	})
	if err != nil {
		log.Fatalf("failed to build v2 active tool registry: %v", err)
	}
	httpRegistry, err := registry.HTTPRegistryView(toolRegistry)
	if err != nil {
		log.Fatalf("failed to build v2 active HTTP registry view: %v", err)
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
	recallHandler := handler.NewV2RecallHandler(v2RecallSvc)
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
		{Name: "v2_authority", Check: func(ctx context.Context) error {
			return checkV2ActiveAuthority(ctx, v2MigrationControlSvc)
		}},
		{Name: "v2_search_readiness", Check: func(ctx context.Context) error {
			return checkV2SearchReadiness(ctx, v2SearchRepo)
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
		MigrationStatus:  v2MigrationControlSvc,
	}
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
		APIKeyRepo:      apiKeyRepo,
		ProfileSvc:      profileService,
		APIKeySvc:       apiKeyService,
		RateLimitSvc:    rateLimitService,
		UsageMetrics:    usageMetricsService,
		Telemetry:       telemetryReader,
		GraphView:       graphViewSvc,
		AuditSvc:        auditService,
		SecuritySvc:     securityService,
		SSOService:      ssoService,
		AppConfig:       appConfigService,
		Config:          &cfg,
		MigrationStatus: v2MigrationControlSvc,
	}
	if telemetryHTTPMetrics != nil {
		userPortalDeps.ExtraMiddleware = append(userPortalDeps.ExtraMiddleware, middleware.TelemetryHTTPMiddleware(telemetryHTTPMetrics))
	}
	http.RegisterUserPortal(e, userPortalDeps)

	controlServer, err := http.NewControlPortalServerWithMetricsAndTelemetry(
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
			Migration:      v2MigrationControlSvc,
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
			logV2ActiveServerStartError(logger, "control portal server error", err)
		}
	}()

	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	startV2ActiveWorkers(
		workerCtx,
		logger,
		profileService,
		v2LedgerRepo,
		v2SearchRepo,
		v2SemanticRepo,
		retryEmbedder,
		v2Verifier,
		discoverabilityMetrics,
		v2ActivePlacementLease(cfg.GetAIVerifierTimeoutSeconds(), cfg.GetPromoteTxTimeoutSeconds()),
		v2ActiveWorkerCount(cfg.GetAIVerifierMaxConcurrency()),
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
			logV2ActiveServerStartError(logger, "server error", err)
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
	if err := http.ShutdownControlPortal(controlServer, logger); err != nil {
		logger.Error("control portal server shutdown error", err)
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

func checkV2ActiveAuthority(ctx context.Context, migration migrationcontrol.Service) error {
	if migration == nil {
		return fmt.Errorf("%w: migration status is required", errV2AuthorityBlocked)
	}
	status, err := migration.Status(ctx)
	if err != nil {
		return err
	}
	if status == nil || status.State != domain.V2MigrationStateCutOver || !status.DataPlaneAllowed {
		return fmt.Errorf("%w: compatible V2 authority marker is required", errV2AuthorityBlocked)
	}
	return nil
}

func checkV2SearchReadiness(ctx context.Context, search interface {
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

func startV2ActiveWorkers(
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
	baseWorkerID := fmt.Sprintf("v2-active-%s-%d", hostname, os.Getpid())
	reviewSvc := memoryservice.NewV2SemanticReviewService(memoryservice.V2SemanticReviewDependencies{
		Provider: reviewer,
		Ledger:   ledger,
	})
	commitSvc := memoryservice.NewV2SemanticCommitService(memoryservice.V2SemanticCommitDependencies{
		PlacementCommit: ledger,
	})
	reviewSource := memoryservice.NewV2SemanticPlacementReviewSource(memoryservice.V2SemanticPlacementReviewSourceDependencies{
		Ledger:           ledger,
		Catalog:          semantic,
		ProposalProvider: reviewer,
	})

	for workerIndex := 0; workerIndex < v2ActiveWorkerCount(workerCount); workerIndex++ {
		workerID := fmt.Sprintf("%s-%d", baseWorkerID, workerIndex+1)
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				processV2ActiveWorkerTick(ctx, logger, profiles, ledger, search, reviewSvc, commitSvc, reviewSource, embedder, metrics, workerID, placementLease)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
}

func v2ActivePlacementLease(verifierTimeoutSeconds int, commitTimeoutSeconds int) time.Duration {
	if verifierTimeoutSeconds <= 0 {
		verifierTimeoutSeconds = 60
	}
	if commitTimeoutSeconds <= 0 {
		commitTimeoutSeconds = 10
	}
	lease := time.Duration((verifierTimeoutSeconds*memoryservice.V2SemanticPlacementDefaultVerifierCallBudget)+commitTimeoutSeconds+30) * time.Second
	if lease < 5*time.Minute {
		return 5 * time.Minute
	}
	return lease
}

func logV2ActiveServerStartError(logger observability.LogProvider, message string, err error) {
	if errors.Is(err, nethttp.ErrServerClosed) {
		return
	}
	logger.Error(message, err)
}

func v2ActiveWorkerCount(verifierMaxConcurrency int) int {
	if verifierMaxConcurrency <= 0 {
		return 1
	}
	if verifierMaxConcurrency > 16 {
		return 16
	}
	return verifierMaxConcurrency
}

func processV2ActiveWorkerTick(
	ctx context.Context,
	logger observability.LogProvider,
	profiles service.ProfileService,
	ledger *repository.V2LedgerRepositoryImpl,
	search repository.V2SearchRepository,
	reviewSvc memoryservice.V2SemanticReviewService,
	commitSvc memoryservice.V2SemanticCommitService,
	reviewSource memoryservice.V2SemanticPlacementReviewSource,
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
			logger.Error("v2 active worker profile list failed", err)
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
			placementWorker := memoryservice.NewV2SemanticPlacementWorkerService(memoryservice.V2SemanticPlacementWorkerDependencies{
				Ledger:       ledger,
				Review:       reviewSvc,
				Commit:       commitSvc,
				ReviewSource: reviewSource,
				TeamID:       teamID,
				WorkerID:     workerID,
				Lease:        placementLease,
			})
			for i := 0; i < maxPlacementsPerTeamPerTick; i++ {
				processed, err := placementWorker.ProcessNextV2SemanticPlacement(ctx)
				if err != nil {
					logger.Error("v2 semantic placement worker failed", err, observability.String("team_id", teamID))
					break
				}
				if !processed {
					break
				}
			}
			embeddingWorker := embeddingservice.NewV2EmbeddingWorkerService(embeddingservice.V2EmbeddingWorkerDependencies{
				Search:   search,
				Provider: embedder,
				Metrics:  metrics,
				TeamID:   teamID,
				WorkerID: workerID,
			})
			if _, err := embeddingWorker.ProcessNextBatch(ctx); err != nil {
				logger.Error("v2 embedding worker failed", err, observability.String("team_id", teamID))
			}
		}
		if len(teams) < pageSize {
			return
		}
	}
}
