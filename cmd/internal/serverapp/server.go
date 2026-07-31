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
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/observability"
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
	if strings.TrimSpace(cfg.GetAIReviewerModel()) != "" {
		logger.Warn("AI_REVIEWER_MODEL is ignored by the V2.4 integrated assessor")
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
	semanticRepo := repository.NewSemanticRepository(pgDB.GetDB(), rlsHelper)
	ledgerRepo := repository.NewLedgerRepositoryWithRuntimeConfig(
		pgDB.GetDB(),
		rlsHelper,
		cfg.GetEmbeddingJobMaxAttempts(),
		repository.ConflictRuntimeConfig{
			ReviewTTLDays: cfg.GetConflictReviewTTLDays(),
			Timezone:      cfg.GetAppTimezone(),
		},
	)
	searchRepo := repository.NewSearchRepositoryWithEmbeddingJobMaxAttempts(
		pgDB.GetDB(),
		rlsHelper,
		cfg.GetEmbeddingJobMaxAttempts(),
	)

	if err := checkActiveAuthority(authority); err != nil {
		log.Fatalf("active boot blocked: %v", err)
	}
	searchContract, err := searchRepo.EnsureActiveSearchContract(startupCtx, repository.EnsureActiveSearchContractInput{
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
	assessmentLimits := verifier.SemanticAssessmentLimitsForConfig(&cfg)
	verifierProvider := verifier.NewOpenAIVerifierWithAssessmentLimits(&cfg, nil, assessmentLimits)
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
		Evidence:  ledgerRepo,
	})
	contextSvc := contextservice.NewSemantic(semanticRepo)
	dreamSvc := dreamservice.New(dreamservice.Dependencies{
		Remember:       rememberSvc,
		Store:          semanticRepo,
		ScheduledStore: semanticRepo,
		AppConfig:      appConfigService,
		Profiles:       profileService,
		Locker:         dreamservice.NewPostgresCycleLocker(),
		Postgres:       pgDB.GetDB(),
		Generator:      dreamservice.NewHeuristicGenerator(""),
		Metrics:        discoverabilityMetrics,
	})
	controlDreamSvc := dreamservice.NewControl(dreamservice.ControlDependencies{
		Store:     semanticRepo,
		AppConfig: appConfigService,
		Teams:     profileService,
	})
	graphViewSvc := graphview.NewSemantic(semanticRepo)
	memoryPackSvc := skillpackservice.NewMemoryPackService(skillpackservice.MemoryPackDependencies{
		Semantic:    semanticRepo,
		Remember:    rememberSvc,
		Ledger:      skillPackImportRepo,
		HistoryDays: cfg.GetMemoryPackImportHistoryDays(),
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
	streamLifecycle := sse.NewStreamLifecycleWithConfig(
		backend.concurrencyLimiter,
		sse.NewHeartbeatSenderWithInterval(time.Duration(cfg.GetSSEHeartbeatSeconds())*time.Second),
		time.Duration(cfg.GetSSEMaxDurationSeconds())*time.Second,
		backend.streamCleanupRepo,
	)
	mcpHandler := handler.NewMCPHandlerWithLifecycleAndRuntimeConfig(toolRegistry, logger, streamLifecycle, appConfigService)

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
		MCPPost: mcpHandler.HandlePost,
		MCPGet:  mcpHandler.HandleGet,
	})
	userPortalDeps := http.UserPortalDeps{
		APIKeyRepo:   apiKeyRepo,
		ProfileSvc:   profileService,
		APIKeySvc:    apiKeyService,
		RateLimitSvc: rateLimitService,
		UsageMetrics: usageMetricsService,
		Telemetry:    telemetryReader,
		GraphView:    graphViewSvc,
		RecallSvc:    recallSvc,
		DreamSvc:     dreamSvc,
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
				Dreams:         controlDreamSvc,
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
		assessmentLimits,
		config.MemoryAutoWriteConfidenceThresholdFor(&cfg),
		activePlacementLease(cfg.GetAIVerifierTimeoutSeconds(), cfg.GetPromoteTxTimeoutSeconds()),
		cfg.GetMemoryPlacementWorkerCount(),
		time.Duration(cfg.GetMemoryPlacementPollSeconds())*time.Second,
		cfg.GetEmbeddingWorkerCount(),
		cfg.GetEmbeddingBatchSize(),
		time.Duration(cfg.GetEmbeddingJobPollSeconds())*time.Second,
	)
	dreamSchedulerCtx, dreamSchedulerCancel := context.WithCancel(context.Background())
	defer dreamSchedulerCancel()
	go dreamservice.NewScheduler(dreamSvc, profileService, slog.Default()).Start(dreamSchedulerCtx)
	conflictReviewCtx, conflictReviewCancel := context.WithCancel(context.Background())
	defer conflictReviewCancel()
	startConflictReviewWorkers(conflictReviewCtx, logger, profileService, ledgerRepo, &cfg, discoverabilityMetrics)

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
	conflictReviewCancel()
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
	profiles service.ProfileService,
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
				processConflictReviewTick(ctx, logger, profiles, ledger, cfg, metrics, workerID, workerIndex, count)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}(workerID, workerIndex)
	}
}

type conflictReviewProfileLister interface {
	List(ctx context.Context, limit, offset int) ([]*domain.Profile, error)
}

func processConflictReviewTick(
	ctx context.Context,
	logger observability.LogProvider,
	profiles conflictReviewProfileLister,
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
		teams, err := profiles.List(ctx, pageSize, offset)
		if err != nil {
			logger.Error("conflict review profile list failed", errConflictReviewProfileListFailed)
			return
		}
		if len(teams) == 0 {
			return
		}
		for _, team := range teams {
			if team == nil || !conflictReviewDueForTeam(now, cfg, team.ID.String()) {
				continue
			}
			if err := processTeamConflictReview(ctx, logger, ledger, cfg, metrics, team.ID.String(), workerID, now); err != nil {
				logger.Error("conflict review run failed", errConflictReviewRunFailed, observability.String("team_id", team.ID.String()))
			}
		}
		if len(teams) < pageSize {
			return
		}
	}
}

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
		observability.RecordConflictReviewDuration(ctx, metrics, time.Since(started).Seconds(), outcome)
	}()
	lease := time.Duration(cfg.GetConflictReviewLeaseSeconds()) * time.Second
	run, claimed, err := ledger.ReserveRelationshipConflictReviewRun(ctx, repository.ConflictReviewRunInput{
		TeamID:       teamID,
		WorkerID:     workerID,
		LocalRunDate: now,
		Timezone:     cfg.GetAppTimezone(),
		Lease:        lease,
	})
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
	attempted := map[string]struct{}{}
	for {
		excluded := make([]string, 0, len(attempted))
		for id := range attempted {
			excluded = append(excluded, id)
		}
		cases, err := ledger.ClaimRelationshipConflictCases(ctx, repository.ClaimRelationshipConflictCasesInput{
			TeamID:              teamID,
			WorkerID:            workerID,
			ReviewRunID:         run.ReviewRunID,
			Limit:               cfg.GetConflictReviewBatchSize(),
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
	if err := ledger.CompleteRelationshipConflictReviewRun(ctx, counts); err != nil {
		outcome = "error"
		return err
	}
	return nil
}

var (
	errConflictReviewProfileListFailed = errors.New("conflict review profile list failed")
	errConflictReviewRunFailed         = errors.New("conflict review run failed")
	errConflictReviewCaseFailed        = errors.New("conflict review case failed")
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

func checkSearchReadiness(ctx context.Context, search interface {
	CheckSearchReadiness(context.Context) (*repository.SearchReadiness, error)
}) error {
	if search == nil {
		return fmt.Errorf("%w: search repository is required", repository.ErrSearchContractMismatch)
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
	return fmt.Errorf("%w: %s", repository.ErrSearchContractMismatch, strings.Join(reasons, "; "))
}

func startActiveWorkers(
	ctx context.Context,
	logger observability.LogProvider,
	profiles service.ProfileService,
	ledger *repository.LedgerRepositoryImpl,
	search repository.SearchRepository,
	semantic *repository.SemanticRepositoryImpl,
	embedder *embedding.RetryEmbeddingProvider,
	assessor *verifier.OpenAIVerifier,
	metrics observability.DiscoverabilityMetrics,
	assessmentLimits verifier.SemanticAssessmentLimits,
	globalConfidenceThreshold float64,
	placementLease time.Duration,
	placementWorkerCount int,
	placementPollInterval time.Duration,
	embeddingWorkerCount int,
	embeddingBatchSize int,
	embeddingPollInterval time.Duration,
) {
	hostname, _ := os.Hostname()
	baseWorkerID := fmt.Sprintf("active-%s-%d", hostname, os.Getpid())
	reviewExpiry := memoryservice.NewSemanticAssessmentReviewExpiryThrottle(ledger, time.Minute)
	startActiveTeamWorkerPool(ctx, activeTeamWorkerPoolConfig{
		name:         "placement",
		baseWorkerID: baseWorkerID,
		count:        placementWorkerCount,
		pollInterval: placementPollInterval,
		profiles:     profiles,
		logger:       logger,
		workerError:  errSemanticPlacementWorkerFailed,
		work: func(ctx context.Context, teamID string, workerID string) (bool, error) {
			worker := memoryservice.NewSemanticAssessmentPlacementWorkerService(memoryservice.SemanticAssessmentPlacementWorkerDependencies{
				Ledger:                    ledger,
				Assessments:               ledger,
				ReviewExpiry:              reviewExpiry,
				Commit:                    ledger,
				Catalog:                   semantic,
				Provider:                  assessor,
				Limits:                    assessmentLimits,
				GlobalConfidenceThreshold: globalConfidenceThreshold,
				Metrics:                   metrics,
				TeamID:                    teamID,
				WorkerID:                  workerID,
				Lease:                     placementLease,
			})
			return worker.ProcessNextSemanticAssessmentPlacement(ctx)
		},
	})
	startActiveTeamWorkerPool(ctx, activeTeamWorkerPoolConfig{
		name:         "embedding",
		baseWorkerID: baseWorkerID,
		count:        embeddingWorkerCount,
		pollInterval: embeddingPollInterval,
		profiles:     profiles,
		logger:       logger,
		workerError:  errEmbeddingWorkerFailed,
		work: func(ctx context.Context, teamID string, workerID string) (bool, error) {
			worker := embeddingservice.NewEmbeddingWorkerService(embeddingservice.EmbeddingWorkerDependencies{
				Search:    search,
				Provider:  embedder,
				Metrics:   metrics,
				TeamID:    teamID,
				WorkerID:  workerID,
				BatchSize: embeddingBatchSize,
			})
			result, err := worker.ProcessNextBatch(ctx)
			return result.Claimed > 0, err
		},
	})
}

func activePlacementLease(verifierTimeoutSeconds int, commitTimeoutSeconds int) time.Duration {
	if verifierTimeoutSeconds <= 0 {
		verifierTimeoutSeconds = 60
	}
	if commitTimeoutSeconds <= 0 {
		commitTimeoutSeconds = 10
	}
	lease := time.Duration((verifierTimeoutSeconds*memoryservice.SemanticPlacementMaxAssessorTurns)+commitTimeoutSeconds+30) * time.Second
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
