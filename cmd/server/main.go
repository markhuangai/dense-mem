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
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentdedupe"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/service/graphview"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/migrationcontrol"
	"github.com/markhuangai/dense-mem/internal/service/migrationexecutor"
	"github.com/markhuangai/dense-mem/internal/service/migrationsupervisor"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
	"github.com/markhuangai/dense-mem/internal/sse"
	"github.com/markhuangai/dense-mem/internal/storage/neo4j"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
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

const startupTimeout = 5 * time.Minute

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
	if err := cfg.ValidateServerStartup(); err != nil {
		log.Fatalf("invalid startup config: %v", err)
	}

	// Create logger
	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	logger := observability.New(level)
	slog.SetDefault(logger.Slog())

	// Wire embedding dimension into the shared request validator.
	validation.SetEmbeddingDimensions(cfg.GetEmbeddingDimensions())
	middleware.SetAuthVerificationConcurrency(cfg.AuthVerifyMaxConcurrency)

	// A cold Neo4j instance can need several minutes to create schema indexes.
	startupCtx, startupCancel := context.WithTimeout(context.Background(), startupTimeout)
	defer startupCancel()

	// Initialize Postgres connection (REQUIRED for production)
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

	// RLSHelper is shared across repos so every query runs with Postgres
	// FORCE RLS session variables (app.current_team_id / app.tx_mode) set.
	rlsHelper := postgres.NewRLS()
	v2MigrationControlRepo := repository.NewV2MigrationControlRepository(pgDB.GetDB(), rlsHelper)
	v2Authority, err := classifyV2Authority(startupCtx, cfg, v2MigrationControlRepo)
	if err != nil {
		log.Fatalf("v2 authority bootstrap failed: %v", err)
	}
	v2MigrationControlSvc := migrationcontrol.New(v2MigrationControlRepo, migrationcontrol.Config{
		Required: v2Authority.MigrationRequired,
	})
	if v2Authority.DataPlaneAllowed {
		runActiveV2Server(startupCtx, cfg, pgDB, logger, level, v2Authority)
		return
	}
	migrationDataPlaneStatus := legacyMigrationDataPlaneStatusGate{inner: v2MigrationControlSvc}
	if err := validateLegacyMigrationNeo4jConfig(v2Authority, cfg); err != nil {
		log.Fatal(err)
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

	// Emit warning if running in degraded (in-memory) mode
	logInMemoryModeWarning(logger, backend.degraded, backend.reason)

	// ========================================
	// Repository layer
	// ========================================
	profileRepo := repository.NewProfileRepository(pgDB.GetDB(), rlsHelper)
	apiKeyRepo := repository.NewAPIKeyRepository(pgDB.GetDB(), rlsHelper)
	ssoRepo := repository.NewSSORepository(pgDB.GetDB(), rlsHelper)
	appConfigRepo := repository.NewAppConfigRepository(pgDB.GetDB(), rlsHelper)
	securityRepo := repository.NewSecurityRepository(pgDB.GetDB(), rlsHelper)
	usageMetricsRepo := repository.NewUsageMetricsRepository(pgDB.GetDB(), rlsHelper)
	operationLogRepo := repository.NewOperationLogRepository(pgDB.GetDB(), rlsHelper)
	recallFeedbackEventRepo := repository.NewRecallFeedbackEventRepository(pgDB.GetDB(), rlsHelper)
	memoryPlacementRepo := repository.NewMemoryPlacementRepository(pgDB.GetDB(), rlsHelper)
	skillPackImportRepo := repository.NewSkillPackImportRepository(pgDB.GetDB(), rlsHelper)
	v2LedgerRepo := repository.NewV2LedgerRepositoryWithEmbeddingJobMaxAttempts(
		pgDB.GetDB(),
		rlsHelper,
		cfg.GetEmbeddingJobMaxAttempts(),
	)
	v2SemanticRepo := repository.NewV2SemanticRepository(pgDB.GetDB(), rlsHelper)
	v2SearchRepo := repository.NewV2SearchRepositoryWithEmbeddingJobMaxAttempts(
		pgDB.GetDB(),
		rlsHelper,
		cfg.GetEmbeddingJobMaxAttempts(),
	)
	searchContract, err := v2SearchRepo.EnsureActiveSearchContract(startupCtx, repository.V2EnsureActiveSearchContractInput{
		Provider:   "openai",
		Model:      cfg.GetAIEmbeddingModel(),
		Dimensions: cfg.GetAIEmbeddingDimensions(),
	})
	if err != nil {
		log.Fatalf("v2 migration search bootstrap blocked: %v", err)
	}
	logger.Info(
		"v2 migration search contract ready",
		observability.String("embedding_contract_id", searchContract.Contract.EmbeddingContractID),
		observability.String("search_index_generation_id", searchContract.Contract.SearchIndexGenerationID),
		observability.String("index_strategy", searchContract.Contract.IndexStrategy),
	)

	// ========================================
	// Neo4j profile scope enforcer and graph writer
	// ========================================
	profileScopeEnforcer := neo4j.NewProfileScopeEnforcer(neo4jClient)
	profileDataPurger := service.NewNeo4jProfileDataPurger(profileScopeEnforcer)
	legacyMigrationReader, err := neo4j.NewLegacyCorpusMigrationAdapter(neo4jClient, neo4j.LegacyCorpusAdapterConfig{
		Enabled:     true,
		MaxPageSize: 500,
	})
	if err != nil {
		log.Fatalf("failed to build legacy migration reader: %v", err)
	}
	defer legacyMigrationReader.Close(context.Background())

	// ========================================
	// Service layer
	// ========================================
	auditService := service.NewAuditService(pgDB.GetDB())
	appConfigService := service.NewAppConfigService(appConfigRepo, auditService)
	operationLogService := service.NewOperationLogService(operationLogRepo, appConfigService)
	logger = observability.NewWithSinks(level, operationLogService)
	slog.SetDefault(logger.Slog())
	operationLogService.Start(context.Background())
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
	v2RememberMigrationSvc := memoryservice.NewV2RememberService(memoryservice.V2RememberDependencies{Ledger: v2LedgerRepo})
	v2MigrationExecSvc := migrationexecutor.New(v2MigrationControlRepo, legacyMigrationReader, v2RememberMigrationSvc, migrationexecutor.Config{
		WorkerID: "server-control",
	})
	restartRequests := make(chan string, 1)

	// ========================================
	// Recall searchers
	// ========================================
	fragmentSearcher := keywordsearch.NewFragmentSearcher(profileScopeEnforcer)
	embeddingSearcher := semanticsearch.NewEmbeddingSearcher(profileScopeEnforcer)

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
	recallFeedbackResolver := service.NewRecallFeedbackGraphResolver(readerAdapter)
	recallFeedbackEventService := service.NewRecallFeedbackEventService(recallFeedbackEventRepo, appConfigService, recallFeedbackResolver)
	recallFeedbackEventService.Start(context.Background())

	// Embedding provider — startup enforces AI_* config before reaching this
	// point. The unavailable stub is kept as a defensive fallback for this
	// wiring layer.
	var (
		retryEmbedder             *embedding.RetryEmbeddingProvider
		fragmentCreateRegistrySvc fragmentservice.CreateFragmentService = unavailableFragmentCreateService{}
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
	}

	// Read/list work without embedding.
	fragmentGetSvc := fragmentservice.NewGetFragmentService(readerAdapter)
	fragmentListSvc := fragmentservice.NewListFragmentsService(readerAdapter)

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
	communityGetSvc := communityservice.NewGetCommunitySummaryService(neo4jClient)
	communityListSvc := communityservice.NewListCommunitiesService(neo4jClient)
	recallFactSearcher := recallservice.NewFactSearcher(profileScopeEnforcer)
	recallClaimSearcher := recallservice.NewClaimSearcher(profileScopeEnforcer)
	recallCommunityExpander := recallservice.NewCommunityExpander(profileScopeEnforcer)

	var (
		claimVerifyRegistrySvc   claimservice.VerifyClaimService = unavailableVerifyClaimService{}
		skillPackConflictDecider skillpackservice.ConflictDecider
		v2SemanticReviewer       *verifier.OpenAIVerifier
	)
	if verifierConfigured(&cfg) {
		baseVerifier := verifier.NewOpenAIVerifier(&cfg, nil)
		baseVerifier.SetMetrics(discoverabilityMetrics)
		v2SemanticReviewer = baseVerifier
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
			recallservice.WithCommunityExpander(recallCommunityExpander),
		)
		recallRegistrySvc = tieredRecallSvc
		recallHTTPSvc = tieredRecallSvc
	}

	memorySvc := memoryservice.New(memoryservice.Dependencies{
		FragmentCreate: fragmentCreateRegistrySvc,
		ClaimCreate:    claimCreateSvc,
		ClaimVerify:    claimVerifyRegistrySvc,
		ClaimGet:       claimGetSvc,
		ClaimList:      claimListSvc,
		FactPromote:    factPromoteSvc,
		FactConfirm:    factConfirmSvc,
		FactList:       factListSvc,
		PlacementStore: memoryPlacementRepo,
		Logger:         slog.Default(),
	})
	placementWorkerCtx, placementWorkerCancel := context.WithCancel(context.Background())
	defer placementWorkerCancel()
	memorySvc.StartPlacementWorker(placementWorkerCtx, time.Minute)
	if retryEmbedder == nil {
		log.Fatalf("v2 migration worker requires embedding provider")
	}
	if v2SemanticReviewer == nil {
		log.Fatalf("v2 migration worker requires verifier provider")
	}
	v2MigrationSupervisor := migrationsupervisor.New(migrationsupervisor.Config{
		Control:         v2MigrationControlSvc,
		Executor:        v2MigrationExecSvc,
		Lock:            postgres.NewMigrationLeaderLock(pgDB.GetDB()),
		Restarter:       channelRestarter{ch: restartRequests},
		SearchReadiness: v2SearchRepo,
		ProviderProbe: migrationProviderProbe{
			embedder: retryEmbedder,
			verifier: v2SemanticReviewer,
			timeout:  time.Duration(cfg.GetAIVerifierTimeoutSeconds()) * time.Second,
		},
		Logger: slog.Default(),
	})
	v2WorkerCtx, v2WorkerCancel := context.WithCancel(context.Background())
	defer v2WorkerCancel()
	startV2ActiveWorkers(
		v2WorkerCtx,
		logger,
		profileService,
		v2LedgerRepo,
		v2SearchRepo,
		v2SemanticRepo,
		retryEmbedder,
		v2SemanticReviewer,
		discoverabilityMetrics,
		v2ActivePlacementLease(cfg.GetAIVerifierTimeoutSeconds(), cfg.GetPromoteTxTimeoutSeconds()),
		v2ActiveWorkerCount(cfg.GetAIVerifierMaxConcurrency()),
	)
	dreamSvc := dreamservice.New(dreamservice.Dependencies{
		Graph:     profileScopeEnforcer,
		Memory:    memorySvc,
		AppConfig: appConfigService,
		Profiles:  profileService,
		Locker:    dreamservice.NewPostgresCycleLocker(),
		Postgres:  pgDB.GetDB(),
		Generator: dreamservice.NewHeuristicGenerator(cfg.GetAIVerifierModel()),
		Metrics:   discoverabilityMetrics,
	})
	contextSvc := contextservice.New(contextservice.Dependencies{
		Reader:      profileScopeEnforcer,
		FactGet:     factGetSvc,
		ClaimGet:    claimGetSvc,
		FragmentGet: fragmentGetSvc,
		Recall:      recallRegistrySvc,
		Memory:      memorySvc,
		Dreams:      dreamSvc,
	})
	graphViewSvc := graphview.New(profileScopeEnforcer)
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

	var (
		communityDetectRegistrySvc communityservice.DetectCommunityService = unavailableCommunityDetectService{}
	)
	communityAvailabilitySvc := communityservice.NewAvailabilityService(neo4jClient, slog.Default())
	communityProbeCtx, communityProbeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	communityAvailable := communityAvailabilitySvc.ProbeGDS(communityProbeCtx)
	communityProbeCancel()
	if communityAvailable {
		communityDetectRegistrySvc = communityservice.NewLeidenService(pgDB.GetDB(), neo4jClient, &cfg, slog.Default())
	} else {
		slog.Default().Warn("community scheduler: GDS unavailable, scheduler not started")
	}

	// Tool registry is the single source of truth for MCP / HTTP catalog / OpenAPI.
	toolRegistry, err := registry.BuildDefault(registry.Dependencies{
		FragmentList:         fragmentListSvc,
		FragmentGet:          fragmentGetSvc,
		Recall:               recallRegistrySvc,
		Metrics:              discoverabilityMetrics,
		RecallFeedbackConfig: appConfigService,
		RecallFeedbackEvents: recallFeedbackEventService,
		EvaluationConfig:     appConfigService,
		EvaluationAudit:      auditService,
		ClaimGet:             claimGetSvc,
		ClaimList:            claimListSvc,
		ClaimListFiltered:    claimListFilteredSvc,
		FactGet:              factGetSvc,
		FactList:             factListSvc,
		CommunityGet:         communityGetSvc,
		CommunityList:        communityListSvc,
		Context:              contextSvc,
		Memory:               memorySvc,
		SkillPack:            skillPackSvc,
		Dreams:               dreamSvc,
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
	// Catalog + OpenAPI handlers.
	toolCatalogHandler := handler.NewToolCatalogHandlerWithRuntimeConfig(toolRegistry, appConfigService)
	toolReadHandler := handler.NewToolReadHandlerWithRuntimeConfig(toolRegistry, appConfigService)
	toolExecuteHandler := handler.NewToolExecuteHandlerWithRuntimeConfig(toolRegistry, appConfigService)
	mcpHandler := handler.NewMCPHandlerWithLifecycleAndRuntimeConfig(toolRegistry, logger, streamLifecycle, appConfigService)
	openAPIAISafeHandler := handler.NewOpenAPIHandler(openAPIGen, openapi.SpecVariantAISafe)
	openAPIFullHandler := handler.NewOpenAPIHandler(openAPIGen, openapi.SpecVariantFull)

	recallHandler := handler.NewRecallHandler(recallHTTPSvc)
	dreamHandler := handler.NewDreamHandler(dreamSvc)

	// ========================================
	// Health checks
	// ========================================
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
		{Name: "neo4j", Check: func(ctx context.Context) error {
			return neo4jClient.Verify(ctx)
		}},
		{Name: "v2_migration_authority", Check: func(ctx context.Context) error {
			return checkV2MigrationDataPlaneReadiness(ctx, migrationDataPlaneStatus)
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

	// ========================================
	// Register protected routes with all handlers
	// ========================================
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
		MigrationStatus:  migrationDataPlaneStatus,
	}
	if telemetryHTTPMetrics != nil {
		protectedDeps.PostAuthMiddleware = append(protectedDeps.PostAuthMiddleware, middleware.TelemetryHTTPMiddleware(telemetryHTTPMetrics))
	}

	protectedHandlers := http.ProtectedHandlers{
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
	}

	http.RegisterProtectedRoutesWithHandlers(e, protectedDeps, protectedHandlers)
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
		MigrationStatus: migrationDataPlaneStatus,
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
			Migration:      v2MigrationSupervisor,
			PortalMode:     "migration",
			LegacyConfig:   cfg.HasNeo4jConfig(),
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
	if err := v2MigrationSupervisor.StartBackground(context.Background()); err != nil {
		log.Fatalf("failed to start v2 migration supervisor: %v", err)
	}

	dreamSchedulerCtx, dreamSchedulerCancel := context.WithCancel(context.Background())
	defer dreamSchedulerCancel()
	go dreamservice.NewScheduler(dreamSvc, profileService, slog.Default()).Start(dreamSchedulerCtx)
	communitySchedulerCancel := func() {}
	if communityAvailable {
		communitySchedulerCtx, cancel := context.WithCancel(context.Background())
		communitySchedulerCancel = cancel
		defer communitySchedulerCancel()
		go communityservice.NewScheduler(
			communityDetectRegistrySvc,
			profileService,
			appConfigService,
			discoverabilityMetrics,
			slog.Default(),
			communityservice.WithSchedulerRunStore(communityservice.NewPostgresSchedulerRunStore(pgDB.GetDB())),
		).Start(communitySchedulerCtx)
	}

	httpAddr := os.Getenv("HTTP_ADDR")
	if httpAddr == "" {
		httpAddr = config.DefaultHTTPAddr
	}
	logger.Info("starting server", observability.String("addr", httpAddr))

	// Start server in a goroutine
	go func() {
		if err := e.Start(httpAddr); err != nil {
			logger.Error("server error", err)
		}
	}()

	// Wait for interrupt signal or migration cutover restart request.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	restartReason := ""
	select {
	case <-quit:
	case restartReason = <-restartRequests:
	}

	logger.Info("shutting down server")
	v2MigrationSupervisor.Stop()
	placementWorkerCancel()
	v2WorkerCancel()
	dreamSchedulerCancel()
	communitySchedulerCancel()

	// Graceful shutdown with 10-second timeout
	if err := http.ShutdownServer(e, logger); err != nil {
		logger.Error("server shutdown error", err)
	}
	if err := http.ShutdownControlPortal(controlServer, logger); err != nil {
		logger.Error("control portal shutdown error", err)
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
	if restartReason != "" {
		logger.Info("re-executing server after v2 migration cutover", observability.String("reason", restartReason))
		if err := reexecSelf(); err != nil {
			logger.Error("server re-exec failed", err)
			os.Exit(1)
		}
	}
}
