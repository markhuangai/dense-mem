package main

import (
	"context"
	"log"
	"log/slog"
	nethttp "net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/openapi"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/claimdedupe"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentdedupe"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
	"github.com/markhuangai/dense-mem/internal/sse"
	"github.com/markhuangai/dense-mem/internal/storage/neo4j"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/graphquery"
	"github.com/markhuangai/dense-mem/internal/tools/keywordsearch"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

// scopedReaderAdapter bridges neo4j.ScopedReader (which returns
// neo4j.ResultSummary) to the fragment services' local ScopedReader
// interface (which returns `any` to avoid an import cycle).
type scopedReaderAdapter struct {
	inner neo4j.ScopedReader
}

func (a *scopedReaderAdapter) ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (any, []map[string]any, error) {
	summary, rows, err := a.inner.ScopedRead(ctx, profileID, query, params)
	return summary, rows, err
}

// fragmentAuditAdapter bridges the fragmentservice.AuditLogEntry to the
// canonical service.AuditLogEntry consumed by the audit repository. The
// fragmentservice version is a structural duplicate restated to avoid an
// import cycle; this adapter copies the fields across.
type fragmentAuditAdapter struct {
	inner service.AuditService
}

func (a *fragmentAuditAdapter) Append(ctx context.Context, entry fragmentservice.AuditLogEntry) error {
	return a.inner.Append(ctx, service.AuditLogEntry{
		ID:            entry.ID,
		ProfileID:     entry.ProfileID,
		Timestamp:     entry.Timestamp,
		Operation:     entry.Operation,
		EntityType:    entry.EntityType,
		EntityID:      entry.EntityID,
		AfterPayload:  entry.AfterPayload,
		ActorKeyID:    entry.ActorKeyID,
		ActorRole:     entry.ActorRole,
		ClientIP:      entry.ClientIP,
		CorrelationID: entry.CorrelationID,
		Metadata:      entry.Metadata,
	})
}

func main() {
	// Load configuration from environment variables
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	runtimeMode := newServerRuntimeMode()
	validateStartup := cfg.ValidateServerStartup
	if runtimeMode.ValidateConfig != nil {
		validateStartup = func() error { return runtimeMode.ValidateConfig(&cfg) }
	}
	if err := validateStartup(); err != nil {
		log.Fatalf("invalid startup config: %v", err)
	}

	// Create logger
	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	logger := observability.New(level)

	// Wire embedding dimension into request validator so the embedding_dim tag
	// on dto.SemanticSearchRequest enforces the configured length at bind time.
	validation.SetEmbeddingDimensions(cfg.GetEmbeddingDimensions())
	middleware.SetAuthVerificationConcurrency(cfg.AuthVerifyMaxConcurrency)

	// Create root context with timeout for startup. A cold database may need
	// time to apply migrations before schema-dependent checks can run.
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer startupCancel()

	// Initialize Postgres connection (REQUIRED for production)
	pgDB, err := postgres.OpenWithClient(startupCtx, &cfg)
	if err != nil {
		log.Fatalf("failed to connect to postgres: %v", err)
	}
	defer pgDB.Close()

	logger.Info("running postgres migrations")
	if err := postgres.RunUp(startupCtx, pgDB.GetDB()); err != nil {
		log.Fatalf("failed to run postgres migrations: %v", err)
	}
	logger.Info("postgres migrations completed")

	// ========================================
	// Embedding consistency check
	// ========================================
	// Ensure configured embedding model matches what's stored in the database.
	// This prevents accidentally switching models and creating dimension mismatches.
	embeddingConfigRepo := postgres.NewEmbeddingConfigRepository(pgDB.GetDB())
	embeddingConsistencySvc := service.NewEmbeddingConsistencyService(embeddingConfigRepo, &cfg)
	if err := embeddingConsistencySvc.CheckAtStartup(startupCtx); err != nil {
		log.Fatalf("embedding consistency check failed: %v", err)
	}

	// Initialize Neo4j client with 5-second timeout
	neo4jClient, err := neo4j.NewClient(startupCtx, &cfg)
	if err != nil {
		log.Fatalf("failed to connect to neo4j: %v", err)
	}
	defer neo4jClient.Close(context.Background())

	// ========================================
	// Neo4j schema bootstrap
	// ========================================
	// Creates uniqueness constraints, team_id indexes, full-text indexes,
	// vector index with configured dimensions, and composite fragment dedupe
	// indexes. Idempotent; legacy index names are dropped and recreated with
	// canonical names. Config loading makes EmbeddingDimensions match the
	// configured AI embedding dimensions.
	schemaBootstrapper := neo4j.NewSchemaBootstrapper(neo4jClient, cfg.GetEmbeddingDimensions(), logger)
	if err := schemaBootstrapper.EnsureSchema(startupCtx); err != nil {
		log.Fatalf("failed to bootstrap neo4j schema: %v", err)
	}

	// ========================================
	// Build backend bundle (Redis or in-memory)
	// ========================================
	backend, err := buildBackendBundle(startupCtx, cfg)
	if err != nil {
		log.Fatalf("failed to build backend: %v", err)
	}
	defer backend.closeFn()
	if runtimeMode.RequireRedis && backend.counterStore == nil {
		log.Fatalf("demo runtime requires REDIS_ADDR for quota counters and provisioning limits")
	}

	// Emit warning if running in degraded (in-memory) mode
	logInMemoryModeWarning(logger, backend.degraded, backend.reason)

	// ========================================
	// Repository layer
	// ========================================
	// RLSHelper is shared across repos so every query runs with Postgres
	// FORCE RLS session variables (app.current_team_id / app.tx_mode) set.
	rlsHelper := postgres.NewRLS()
	profileRepo := repository.NewProfileRepository(pgDB.GetDB(), rlsHelper)
	apiKeyRepo := repository.NewAPIKeyRepository(pgDB.GetDB(), rlsHelper)
	ssoRepo := repository.NewSSORepository(pgDB.GetDB(), rlsHelper)
	appConfigRepo := repository.NewAppConfigRepository(pgDB.GetDB(), rlsHelper)
	securityRepo := repository.NewSecurityRepository(pgDB.GetDB(), rlsHelper)
	usageMetricsRepo := repository.NewUsageMetricsRepository(pgDB.GetDB(), rlsHelper)
	skillPackImportRepo := repository.NewSkillPackImportRepository(pgDB.GetDB(), rlsHelper)

	// ========================================
	// Neo4j profile scope enforcer and graph writer
	// ========================================
	profileScopeEnforcer := neo4j.NewProfileScopeEnforcer(neo4jClient)
	profileDataPurger := service.NewNeo4jProfileDataPurger(profileScopeEnforcer)

	// ========================================
	// Service layer
	// ========================================
	auditService := service.NewAuditService(pgDB.GetDB())
	appConfigService := service.NewAppConfigService(appConfigRepo, auditService)
	securityService := service.NewSecurityService(securityRepo, auditService)
	usageMetricsService := service.NewUsageMetricsService(usageMetricsRepo, logger)
	usageMetricsService.Start(context.Background())

	profileService := service.NewProfileServiceWithDataPurger(profileRepo, auditService, backend.cleanupRepo, profileDataPurger)
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, profileService, auditService, backend.cleanupRepo, backend.cleanupRepo)
	ssoService := service.NewSSOService(ssoRepo, service.SSOConfig{
		RuntimeConfig: appConfigService,
		Logger:        logger,
	})
	rateLimitService := backend.rateLimitService
	runtimeCtx := serverRuntimeContext{
		Config:         &cfg,
		ProfileService: profileService,
		APIKeyService:  apiKeyService,
		DataPurger:     profileDataPurger,
		CounterStore:   backend.counterStore,
		PostgresDB:     pgDB.GetDB(),
		RLS:            rlsHelper,
		Logger:         logger,
	}

	// ========================================
	// Tool services
	// ========================================
	// Graph query service
	cypherValidator := graphquery.NewCypherValidator()
	graphQueryService := graphquery.NewGraphQueryService(profileScopeEnforcer, cypherValidator)
	graphQueryService = graphquery.NewTimeoutService(graphQueryService, time.Duration(cfg.GraphQueryDefaultTimeoutSeconds)*time.Second)

	// Keyword search services (fragment and fact searchers)
	// These use the profileScopeEnforcer as their searcher interface
	fragmentSearcher := keywordsearch.NewFragmentSearcher(profileScopeEnforcer)
	factSearcher := keywordsearch.NewFactSearcher(profileScopeEnforcer)
	keywordSearchService := keywordsearch.NewKeywordSearchService(fragmentSearcher, factSearcher)

	// Semantic search service
	embeddingSearcher := semanticsearch.NewEmbeddingSearcher(profileScopeEnforcer)
	semanticSearchService := semanticsearch.NewSemanticSearchService(embeddingSearcher, cfg.GetEmbeddingDimensions())

	// ========================================
	// Discoverability: embedding, fragments, recall, registry, openapi
	// ========================================
	// Production startup uses the no-op metrics backend unless telemetry is
	// explicitly enabled. Tests can still inject the in-memory recorder directly
	// where assertions need captured observations.
	discoverabilityMetrics := observability.NoopDiscoverabilityMetrics()
	var (
		telemetryReader        service.TelemetryReader
		telemetryHTTPMetrics   observability.HTTPMetrics
		telemetryScrapeHandler nethttp.Handler
	)
	if cfg.GetTelemetryEnabled() {
		prometheusMetrics := observability.NewPrometheusMetrics()
		prometheusMetrics.RegisterKnowledgeBacklogCollector(neo4jClient, logger)
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
	// Adapters translate between neo4j's ScopedReader and the fragment services'
	// local ScopedReader, and between fragmentservice's AuditLogEntry and the
	// canonical service.AuditLogEntry.
	readerAdapter := &scopedReaderAdapter{inner: profileScopeEnforcer}
	fragmentAuditor := &fragmentAuditAdapter{inner: auditService}
	claimAuditor := &claimAuditAdapter{inner: auditService}
	factAuditor := &factAuditAdapter{inner: auditService}
	dedupeLookup := fragmentdedupe.NewNeo4jDedupeLookup(readerAdapter)
	claimDedupeLookup := claimdedupe.NewNeo4jDedupeLookup(readerAdapter)

	// Embedding provider — startup enforces AI_* config before reaching this
	// point. The unavailable stub is kept as a defensive fallback for this
	// wiring layer.
	var (
		retryEmbedder             *embedding.RetryEmbeddingProvider
		fragmentCreateRegistrySvc fragmentservice.CreateFragmentService = unavailableFragmentCreateService{}
		fragmentCreateHTTPSvc     fragmentservice.CreateFragmentService = unavailableFragmentCreateService{}
	)
	if cfg.IsEmbeddingConfigured() {
		openaiProvider := embedding.NewOpenAIEmbeddingProvider(&cfg, nil)
		openaiProvider.SetMetrics(discoverabilityMetrics)
		retryEmbedder = embedding.NewRetryEmbeddingProviderWithKey(openaiProvider, logger, cfg.GetAIAPIKey())
		retryEmbedder.SetMetrics(discoverabilityMetrics)

		fragmentCreateRegistrySvc = fragmentservice.NewCreateFragmentService(
			retryEmbedder,
			profileScopeEnforcer,
			dedupeLookup,
			fragmentAuditor,
			embeddingConsistencySvc,
			slog.Default(),
			discoverabilityMetrics,
		)
		fragmentCreateHTTPSvc = fragmentCreateRegistrySvc
	}

	// Read/list/delete work without embedding.
	fragmentGetSvc := fragmentservice.NewGetFragmentService(readerAdapter)
	fragmentListSvc := fragmentservice.NewListFragmentsService(readerAdapter)
	fragmentDeleteSvc := fragmentservice.NewDeleteFragmentService(profileScopeEnforcer, readerAdapter, fragmentAuditor, slog.Default())
	fragmentRetractSvc := fragmentservice.NewRetractFragmentService(profileScopeEnforcer, fragmentAuditor, slog.Default(), discoverabilityMetrics)

	claimCreateSvc := claimservice.NewCreateClaimService(
		claimDedupeLookup,
		profileScopeEnforcer,
		profileScopeEnforcer,
		claimAuditor,
		slog.Default(),
		discoverabilityMetrics,
	)
	claimGetSvc := claimservice.NewGetClaimService(profileScopeEnforcer, slog.Default())
	claimListSvc := claimservice.NewListClaimsService(profileScopeEnforcer)
	claimListFilteredSvc := claimservice.NewListClaimsFilteredService(profileScopeEnforcer)
	claimDeleteSvc := claimservice.NewDeleteClaimService(profileScopeEnforcer, claimAuditor, slog.Default())

	claimLock := postgres.NewClaimLock(discoverabilityMetrics)
	factPromoteSvc := factservice.NewPromoteClaimService(
		profileScopeEnforcer,
		claimLock,
		pgDB.GetDB(),
		factAuditor,
		slog.Default(),
		discoverabilityMetrics,
		time.Duration(cfg.GetPromoteTxTimeoutSeconds())*time.Second,
	)
	var factConfirmSvc factservice.ConfirmMemoryService = unavailableConfirmMemoryService{}
	if confirmSvc, ok := factPromoteSvc.(factservice.ConfirmMemoryService); ok {
		factConfirmSvc = confirmSvc
	}
	factGetSvc := factservice.NewGetFactService(profileScopeEnforcer)
	factListSvc := factservice.NewListFactsService(profileScopeEnforcer)
	factRetractSvc := factservice.NewRetractFactService(profileScopeEnforcer, factAuditor, slog.Default())
	communityGetSvc := communityservice.NewGetCommunitySummaryService(neo4jClient)
	communityListSvc := communityservice.NewListCommunitiesService(neo4jClient)
	recallFactSearcher := recallservice.NewFactSearcher(profileScopeEnforcer)
	recallClaimSearcher := recallservice.NewClaimSearcher(profileScopeEnforcer)

	var (
		claimVerifyRegistrySvc   claimservice.VerifyClaimService = unavailableVerifyClaimService{}
		claimVerifyHTTPSvc       claimservice.VerifyClaimService = unavailableVerifyClaimService{}
		skillPackConflictDecider skillpackservice.ConflictDecider
	)
	if verifierConfigured(&cfg) {
		baseVerifier := verifier.NewOpenAIVerifier(&cfg, nil)
		baseVerifier.SetMetrics(discoverabilityMetrics)
		retryVerifier := verifier.NewRetryVerifier(baseVerifier, &cfg, logger)
		skillPackConflictDecider = skillpackservice.NewOpenAIConflictDecider(&cfg, nil)

		claimVerifyRegistrySvc = claimservice.NewVerifyClaimService(
			profileScopeEnforcer,
			profileScopeEnforcer,
			profileScopeEnforcer,
			retryVerifier,
			cfg.GetAIVerifierModel(),
			claimAuditor,
			slog.Default(),
			discoverabilityMetrics,
		)
		claimVerifyHTTPSvc = claimVerifyRegistrySvc
	}

	// Recall requires embedding (query vectors).
	var (
		recallRegistrySvc recallservice.RecallService = unavailableRecallService{}
		recallHTTPSvc     recallservice.RecallService = unavailableRecallService{}
	)
	if cfg.IsEmbeddingConfigured() {
		tieredRecallSvc := recallservice.NewRecallServiceWithTiers(
			retryEmbedder,
			embeddingSearcher,
			fragmentSearcher,
			fragmentGetSvc,
			recallFactSearcher,
			factGetSvc,
			recallClaimSearcher,
			claimGetSvc,
			cfg.GetRecallValidatedClaimWeight(),
			logger,
			discoverabilityMetrics,
		)
		recallRegistrySvc = tieredRecallSvc
		recallHTTPSvc = tieredRecallSvc
	}

	var (
		communityDetectRegistrySvc communityservice.DetectCommunityService = unavailableCommunityDetectService{}
	)
	communityAvailabilitySvc := communityservice.NewAvailabilityService(neo4jClient, slog.Default())
	communityProbeCtx, communityProbeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	communityAvailable := communityAvailabilitySvc.ProbeGDS(communityProbeCtx)
	communityProbeCancel()
	if communityAvailable {
		communityDetectRegistrySvc = communityservice.NewLeidenService(pgDB.GetDB(), neo4jClient, &cfg, slog.Default())
	}

	runtimeServices := serverRuntimeServices{
		FragmentCreateRegistrySvc:  fragmentCreateRegistrySvc,
		FragmentCreateHTTPSvc:      fragmentCreateHTTPSvc,
		ClaimCreateSvc:             claimCreateSvc,
		ClaimVerifyRegistrySvc:     claimVerifyRegistrySvc,
		ClaimVerifyHTTPSvc:         claimVerifyHTTPSvc,
		FactPromoteSvc:             factPromoteSvc,
		FactConfirmSvc:             factConfirmSvc,
		RecallRegistrySvc:          recallRegistrySvc,
		RecallHTTPSvc:              recallHTTPSvc,
		CommunityDetectRegistrySvc: communityDetectRegistrySvc,
	}
	if runtimeMode.ConfigureServices != nil {
		if err := runtimeMode.ConfigureServices(startupCtx, runtimeCtx, &runtimeServices); err != nil {
			log.Fatalf("failed to configure runtime services: %v", err)
		}
	}
	if telemetryHTTPMetrics != nil {
		runtimeServices.PostAuthMiddleware = append(runtimeServices.PostAuthMiddleware, middleware.TelemetryHTTPMiddleware(telemetryHTTPMetrics))
		runtimeServices.UserPortalMiddleware = append(runtimeServices.UserPortalMiddleware, middleware.TelemetryHTTPMiddleware(telemetryHTTPMetrics))
	}
	fragmentCreateRegistrySvc = runtimeServices.FragmentCreateRegistrySvc
	fragmentCreateHTTPSvc = runtimeServices.FragmentCreateHTTPSvc
	claimCreateSvc = runtimeServices.ClaimCreateSvc
	claimVerifyRegistrySvc = runtimeServices.ClaimVerifyRegistrySvc
	claimVerifyHTTPSvc = runtimeServices.ClaimVerifyHTTPSvc
	factPromoteSvc = runtimeServices.FactPromoteSvc
	factConfirmSvc = runtimeServices.FactConfirmSvc
	recallRegistrySvc = runtimeServices.RecallRegistrySvc
	recallHTTPSvc = runtimeServices.RecallHTTPSvc
	communityDetectRegistrySvc = runtimeServices.CommunityDetectRegistrySvc

	memorySvc := memoryservice.New(memoryservice.Dependencies{
		FragmentCreate: fragmentCreateRegistrySvc,
		ClaimCreate:    claimCreateSvc,
		ClaimVerify:    claimVerifyRegistrySvc,
		ClaimGet:       claimGetSvc,
		ClaimList:      claimListSvc,
		FactPromote:    factPromoteSvc,
		FactConfirm:    factConfirmSvc,
		FactList:       factListSvc,
	})
	contextSvc := contextservice.New(contextservice.Dependencies{
		Reader:      profileScopeEnforcer,
		FactGet:     factGetSvc,
		ClaimGet:    claimGetSvc,
		FragmentGet: fragmentGetSvc,
		Recall:      recallRegistrySvc,
		Memory:      memorySvc,
	})
	skillPackSvc := skillpackservice.New(skillpackservice.Dependencies{
		FragmentCreate:  fragmentCreateRegistrySvc,
		ClaimCreate:     claimCreateSvc,
		ClaimGet:        claimGetSvc,
		ClaimList:       claimListSvc,
		FactPromote:     factPromoteSvc,
		FactGet:         factGetSvc,
		FactList:        factListSvc,
		ConflictDecider: skillPackConflictDecider,
		Graph:           profileScopeEnforcer,
		Ledger:          skillPackImportRepo,
		HistoryDays:     cfg.GetSkillPackImportHistoryDays(),
	})

	// Tool registry is the single source of truth for MCP / HTTP catalog / OpenAPI.
	toolRegistry, err := registry.BuildDefault(registry.Dependencies{
		FragmentCreate:              fragmentCreateRegistrySvc,
		FragmentGet:                 fragmentGetSvc,
		FragmentList:                fragmentListSvc,
		Recall:                      recallRegistrySvc,
		KeywordSearch:               keywordSearchService,
		SemanticSearch:              semanticSearchService,
		GraphQuery:                  graphQueryService,
		GraphQueryMaxTimeoutSeconds: cfg.GraphQueryMaxTimeoutSeconds,
		ClaimCreate:                 claimCreateSvc,
		ClaimGet:                    claimGetSvc,
		ClaimList:                   claimListSvc,
		ClaimVerify:                 claimVerifyRegistrySvc,
		FactPromote:                 factPromoteSvc,
		FactGet:                     factGetSvc,
		FactList:                    factListSvc,
		FactRetract:                 factRetractSvc,
		FragmentRetract:             fragmentRetractSvc,
		CommunityDetect:             communityDetectRegistrySvc,
		CommunityGet:                communityGetSvc,
		CommunityList:               communityListSvc,
		Context:                     contextSvc,
		Memory:                      memorySvc,
		SkillPack:                   skillPackSvc,
	})
	if err != nil {
		log.Fatalf("failed to build tool registry: %v", err)
	}

	openAPIGen := openapi.New(toolRegistry, openapi.DefaultRoutes())

	// ========================================
	// SSE lifecycle
	// ========================================
	streamLifecycle := sse.NewStreamLifecycleWithConfig(
		backend.concurrencyLimiter,
		sse.NewHeartbeatSenderWithInterval(time.Duration(cfg.GetSSEHeartbeatSeconds())*time.Second),
		time.Duration(cfg.GetSSEMaxDurationSeconds())*time.Second,
		backend.streamCleanupRepo,
	)

	// ========================================
	// Handlers
	// ========================================
	graphQueryHandler := handler.NewGraphQueryHandlerWithTimeouts(graphQueryService, time.Duration(cfg.GraphQueryMaxTimeoutSeconds)*time.Second)
	keywordSearchHandler := handler.NewKeywordSearchHandler(keywordSearchService)
	semanticSearchHandler := handler.NewSemanticSearchHandler(semanticSearchService)

	// Query stream orchestrator and handler
	queryStreamOrchestrator := handler.NewQueryStreamOrchestrator(graphQueryService, keywordSearchService, semanticSearchService)
	queryStreamHandler := handler.NewQueryStreamHandler(queryStreamOrchestrator, streamLifecycle)

	// Fragment + catalog + openapi handlers
	fragmentCreateHandler := handler.NewFragmentCreateHandler(fragmentCreateHTTPSvc)
	fragmentReadHandler := handler.NewFragmentReadHandler(fragmentGetSvc)
	fragmentListHandler := handler.NewFragmentListHandler(fragmentListSvc)
	fragmentDeleteHandler := handler.NewFragmentDeleteHandler(fragmentDeleteSvc)
	fragmentRetractHandler := handler.NewFragmentRetractHandler(fragmentRetractSvc)
	claimCreateHandler := handler.NewClaimCreateHandler(claimCreateSvc)
	claimReadHandler := handler.NewClaimReadHandler(claimGetSvc)
	claimListHandler := handler.NewClaimListHandlerWithFiltered(claimListSvc, claimListFilteredSvc)
	claimDeleteHandler := handler.NewClaimDeleteHandler(claimDeleteSvc)
	claimVerifyHandler := handler.NewClaimVerifyHandler(claimVerifyHTTPSvc)
	claimPromoteHandler := handler.NewClaimPromoteHandler(factPromoteSvc)
	factReadHandler := handler.NewFactReadHandler(factGetSvc)
	factListHandler := handler.NewFactListHandler(factListSvc)
	factRetractHandler := handler.NewFactRetractHandler(factRetractSvc)
	communityReadHandler := handler.NewCommunityReadHandler(communityGetSvc)
	communityListHandler := handler.NewCommunityListHandler(communityListSvc)
	toolCatalogHandler := handler.NewToolCatalogHandler(toolRegistry)
	toolReadHandler := handler.NewToolReadHandler(toolRegistry)
	toolExecuteHandler := handler.NewToolExecuteHandler(toolRegistry)
	mcpHandler := handler.NewMCPHandlerWithLifecycle(toolRegistry, logger, streamLifecycle)
	openAPIAISafeHandler := handler.NewOpenAPIHandler(openAPIGen, openapi.SpecVariantAISafe)
	openAPIFullHandler := handler.NewOpenAPIHandler(openAPIGen, openapi.SpecVariantFull)

	recallHandler := handler.NewRecallHandler(recallHTTPSvc)

	// ========================================
	// Health checks
	// ========================================
	checks := []http.HealthCheck{
		{Name: "postgres", Check: func(ctx context.Context) error {
			return pgDB.Ping(ctx)
		}},
		{Name: "neo4j", Check: func(ctx context.Context) error {
			return neo4jClient.Verify(ctx)
		}},
	}

	if backend.redisPingFn != nil {
		checks = append(checks, http.HealthCheck{
			Name:  "redis",
			Check: backend.redisPingFn,
		})
	}

	// ========================================
	// Create Echo server
	// ========================================
	healthConfig := http.HealthConfig{
		Checks:   checks,
		Degraded: backend.degraded,
		Reason:   backend.reason,
	}
	e := http.NewServer(cfg, logger, healthConfig)

	// Register request context middleware globally.
	e.Use(middleware.CorrelationIDMiddleware())
	e.Use(middleware.ClientIPMiddleware())
	e.Use(middleware.SecurityBanMiddleware(securityService))
	runtimeCtx.Echo = e
	if runtimeMode.RegisterRoutes != nil {
		if err := runtimeMode.RegisterRoutes(runtimeCtx); err != nil {
			log.Fatalf("failed to register runtime routes: %v", err)
		}
	}

	// ========================================
	// Register protected routes with all handlers
	// ========================================
	protectedDeps := http.ProtectedDeps{
		APIKeyRepo:         apiKeyRepo,
		ProfileService:     profileService,
		ProfileSvc:         profileService,
		RateLimitService:   rateLimitService,
		UsageMetrics:       usageMetricsService,
		AuditService:       auditService,
		SecurityService:    securityService,
		SSOAuthenticator:   ssoService,
		Config:             &cfg,
		Logger:             logger,
		PostAuthMiddleware: runtimeServices.PostAuthMiddleware,
	}

	protectedHandlers := http.ProtectedHandlers{
		APIKeySvc:       apiKeyService,
		GraphQuery:      graphQueryHandler.Handle,
		KeywordSearch:   keywordSearchHandler.Handle,
		SemanticSearch:  semanticSearchHandler.Handle,
		QueryStream:     queryStreamHandler.Handle,
		FragmentRead:    fragmentReadHandler.Handle,
		FragmentList:    fragmentListHandler.Handle,
		FragmentDelete:  fragmentDeleteHandler.Handle,
		FragmentRetract: fragmentRetractHandler.Handle,
		ClaimCreate:     claimCreateHandler.Handle,
		ClaimRead:       claimReadHandler.Handle,
		ClaimList:       claimListHandler.Handle,
		ClaimDelete:     claimDeleteHandler.Handle,
		ClaimVerify:     claimVerifyHandler.Handle,
		ClaimPromote:    claimPromoteHandler.Handle,
		FactGet:         factReadHandler.Handle,
		FactList:        factListHandler.Handle,
		FactRetract:     factRetractHandler.Handle,
		CommunityRead:   communityReadHandler.Handle,
		CommunityList:   communityListHandler.Handle,
		ToolCatalog:     toolCatalogHandler.Handle,
		GetTool:         toolReadHandler.Handle,
		ExecuteTool:     toolExecuteHandler.Handle,
		MCPPost:         mcpHandler.HandlePost,
		MCPGet:          mcpHandler.HandleGet,
		OpenAPIAISafe:   openAPIAISafeHandler.Handle,
		OpenAPIFull:     openAPIFullHandler.Handle,
		Recall:          recallHandler.Handle,
	}
	protectedHandlers.FragmentCreate = fragmentCreateHandler.Handle

	http.RegisterProtectedRoutesWithHandlers(e, protectedDeps, protectedHandlers)
	http.RegisterUserPortal(e, http.UserPortalDeps{
		APIKeyRepo:      apiKeyRepo,
		ProfileSvc:      profileService,
		APIKeySvc:       apiKeyService,
		RateLimitSvc:    rateLimitService,
		UsageMetrics:    usageMetricsService,
		Telemetry:       telemetryReader,
		AuditSvc:        auditService,
		SecuritySvc:     securityService,
		SSOService:      ssoService,
		Config:          &cfg,
		ExtraMiddleware: runtimeServices.UserPortalMiddleware,
	})

	var runtimeShutdown func(context.Context) error
	if runtimeMode.StartBackground != nil {
		runtimeShutdown, err = runtimeMode.StartBackground(context.Background(), runtimeCtx)
		if err != nil {
			log.Fatalf("failed to start runtime background jobs: %v", err)
		}
	}

	var controlServer *echo.Echo
	var telemetryServer *echo.Echo
	if !runtimeMode.DisableControlPortal {
		controlServer, err = http.NewControlPortalServerWithMetricsAndTelemetry(
			&cfg,
			profileService,
			apiKeyService,
			usageMetricsService,
			http.ControlPortalTelemetry{
				Reader:        telemetryReader,
				ScrapeHandler: telemetryScrapeHandler,
				ScrapeToken:   cfg.GetTelemetryScrapeToken(),
				SSO:           ssoService,
				Config:        appConfigService,
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
				logger.Error("control portal server error", err)
			}
		}()
	} else if telemetryScrapeHandler != nil {
		telemetryServer, err = newDemoTelemetryServer(telemetryScrapeHandler, cfg.GetTelemetryScrapeToken())
		if err != nil {
			log.Fatalf("failed to build telemetry scrape server: %v", err)
		}
		logger.Info("starting telemetry scrape server", observability.String("addr", demoTelemetryHTTPAddr))
		go func() {
			if err := telemetryServer.Start(demoTelemetryHTTPAddr); err != nil {
				logger.Error("telemetry scrape server error", err)
			}
		}()
	}

	logger.Info("starting server", observability.String("addr", config.DefaultHTTPAddr))

	// Start server in a goroutine
	go func() {
		if err := e.Start(config.DefaultHTTPAddr); err != nil {
			logger.Error("server error", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down server")

	// Graceful shutdown with 10-second timeout
	if err := http.ShutdownServer(e, logger); err != nil {
		logger.Error("server shutdown error", err)
	}
	if controlServer != nil {
		if err := http.ShutdownControlPortal(controlServer, logger); err != nil {
			logger.Error("control portal shutdown error", err)
		}
	}
	if telemetryServer != nil {
		if err := shutdownDemoTelemetryServer(telemetryServer); err != nil {
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
	defer metricsShutdownCancel()
	if err := usageMetricsService.Shutdown(metricsShutdownCtx); err != nil {
		logger.Error("usage metrics shutdown error", err)
	}
}
