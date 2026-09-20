package serverapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	nethttp "net/http"
	"os"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	communitypostgres "github.com/markhuangai/dense-mem/internal/community/postgres"
	communityapp "github.com/markhuangai/dense-mem/internal/community/service"
	"github.com/markhuangai/dense-mem/internal/config"
	conflictassessment "github.com/markhuangai/dense-mem/internal/conflict/assessment"
	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	conflictreview "github.com/markhuangai/dense-mem/internal/conflict/review"
	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/dream"
	dreampostgres "github.com/markhuangai/dense-mem/internal/dream/postgres"
	graphpostgres "github.com/markhuangai/dense-mem/internal/graph/postgres"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	operationspostgres "github.com/markhuangai/dense-mem/internal/operations/postgres"
	privacy "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	recallpostgres "github.com/markhuangai/dense-mem/internal/recall/postgres"
	settingspostgres "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

// runtimeWorkerFailure keeps the underlying cause for control flow while exposing a bounded message to logs.
type runtimeWorkerFailure struct {
	cause error
}

func (e runtimeWorkerFailure) Error() string { return "runtime worker failed" }

func (e runtimeWorkerFailure) Unwrap() error { return e.cause }

func RunActiveServer(
	processCtx context.Context,
	startupCtx context.Context,
	cfg config.Config,
	pgDB *postgres.DB,
	logger observability.LogProvider,
	level slog.Level,
	authority authorityBootstrap,
	options RuntimeOptions,
) error {
	if processCtx == nil {
		processCtx = context.Background()
	}
	if startupCtx == nil {
		startupCtx = processCtx
	}
	if pgDB == nil || pgDB.GetDB() == nil {
		return errors.New("active server requires postgres database")
	}
	if !cfg.IsEmbeddingConfigured() {
		return errors.New("active authority requires configured embedding provider")
	}
	if !VerifierConfigured(&cfg) {
		return errors.New("active authority requires configured verifier provider")
	}
	backend, err := buildBackendBundle(startupCtx, cfg)
	if err != nil {
		return fmt.Errorf("failed to build backend: %w", err)
	}
	closeBackend := true
	defer func() {
		if closeBackend && backend.closeFn != nil {
			_ = backend.closeFn()
		}
	}()
	if options.RequireRedis && backend.counterStore == nil {
		return errors.New("runtime requires REDIS_ADDR")
	}
	logInMemoryModeWarning(logger, backend.degraded, backend.reason)

	rlsHelper := postgres.NewRLS()
	teamRepo := accesspostgres.NewTeamRepository(pgDB.GetDB(), rlsHelper)
	credentialDeletionRepo := privacy.NewCredentialDeletionRepository(pgDB.GetDB(), rlsHelper)
	credentialRepo := accesspostgres.NewCredentialRepository(pgDB.GetDB(), rlsHelper, credentialDeletionRepo)
	accessAuthentication := buildAccessAuthenticationApplication(
		credentialRepo,
		credentialRepo,
		cfg.AuthVerifyMaxConcurrency,
		logger,
	)
	credentialVerifier := accessAuthentication.CredentialVerifier
	activityWriter := accessAuthentication.ActivityWriter
	ssoRepo := accesspostgres.NewSSORepository(pgDB.GetDB(), rlsHelper)
	portalSessionRepo := accesspostgres.NewUserPortalSessionRepository(pgDB.GetDB(), rlsHelper)
	directoryIdentityRepo := accesspostgres.NewDirectoryIdentityRepository(pgDB.GetDB(), rlsHelper)
	controlIdentityRepo := accesspostgres.NewControlIdentityRepository(pgDB.GetDB(), rlsHelper)
	appConfigRepo := settingspostgres.NewAppConfigRepository(pgDB.GetDB(), rlsHelper)
	securityRepo := settingspostgres.NewSecurityRepository(pgDB.GetDB(), rlsHelper)
	usageMetricsRepo := operationspostgres.NewUsageMetricsRepository(pgDB.GetDB(), rlsHelper)
	telemetryLifecycleRepo := operationspostgres.NewTelemetryLifecycleRepository(pgDB.GetDB(), rlsHelper)
	recallFeedbackEventRepo := recallpostgres.NewFeedbackStore(pgDB.GetDB(), rlsHelper)
	privateMemoryRepo := privacy.NewStore(pgDB.GetDB(), rlsHelper)
	knowledgeStore := knowledgepostgres.NewStore(pgDB.GetDB(), rlsHelper, knowledgecontract.ConflictRuntimeConfig{
		ReviewTTLDays: cfg.GetConflictReviewTTLDays(),
		Timezone:      cfg.GetAppTimezone(),
	})
	dreamStore := dreampostgres.NewStoreWithLogger(pgDB.GetDB(), rlsHelper, logger)
	conflictStore := conflictpostgres.NewStore(pgDB.GetDB(), rlsHelper, knowledgeStore)
	graphStore := graphpostgres.NewStore(pgDB.GetDB(), rlsHelper)
	traceStore := buildTraceStore(pgDB.GetDB(), rlsHelper)
	conflictQueueService := buildConflictQueueApplication(conflictStore)
	evidenceConflictService := buildEvidenceConflictApplication(conflictStore)
	if err := checkActiveAuthority(authority); err != nil {
		return fmt.Errorf("active boot blocked: %w", err)
	}
	searchRepo, searchContract, err := buildSearchRepositoryApplication(startupCtx, cfg, pgDB, rlsHelper, knowledgeStore)
	if err != nil {
		return fmt.Errorf("active search bootstrap blocked: %w", err)
	}
	recallStore := recallpostgres.NewStore(
		pgDB.GetDB(), rlsHelper, searchRepo,
		searchRecallConflictReader(), recallpostgres.LoadRecallEvidenceConflictRecords,
	)
	operationLogDB, err := postgres.OpenOperationLogClient(startupCtx, &cfg, logger)
	if err != nil {
		return fmt.Errorf("open operation log sink pool: %w", err)
	}
	closeOperationLogDB := true
	defer func() {
		if closeOperationLogDB {
			_ = operationLogDB.Close()
		}
	}()
	operationLogRepo := operationspostgres.NewOperationLogRepository(operationLogDB.GetDB(), rlsHelper)
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
	operationLogService.SetMinimumLevel(level)
	if root, ok := logger.(observability.SinkAttacher); ok {
		if err := root.AttachSink(operationLogService); err != nil {
			return fmt.Errorf("attach operation log sink: %w", err)
		}
	}
	if root, ok := logger.(*observability.Logger); ok {
		slog.SetDefault(root.Slog())
	}
	// Keep the sink worker alive until lifecycle.shutdown reaches its first
	// registered worker, after listeners and producers have drained.
	operationLogService.Start(context.Background())
	// Every bootstrap return path after Start must stop the worker before the
	// dedicated pool's close defer runs.
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), writerShutdownTimeout)
		_ = operationLogService.Shutdown(shutdownCtx)
		cancel()
	}()
	if err := operationLogService.CheckReadiness(startupCtx); err != nil {
		return fmt.Errorf("operation log sink readiness failed: %w", err)
	}
	securityService := buildSecurityApplication(securityRepo, auditService)
	usageMetricsService := buildUsageMetricsApplication(usageMetricsRepo, logger)
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
		return fmt.Errorf("private-memory erasure boot blocked: %w", err)
	}
	runtimeCtx := RuntimeContext{
		Config:            &cfg,
		TeamService:       teamService,
		CredentialService: credentialService,
		CounterStore:      backend.counterStore,
		PostgresDB:        pgDB.GetDB(),
		RLS:               rlsHelper,
		Logger:            logger,
	}
	telemetry, err := buildTelemetryApplication(startupCtx, cfg, appConfigService, conflictStore, telemetryLifecycleRepo, logger)
	if err != nil {
		return fmt.Errorf("failed to build telemetry application: %w", err)
	}
	discoverabilityMetrics := telemetry.Metrics
	telemetryPrometheusService := telemetry.Prometheus
	searchApplication := buildSearchProviders(cfg, searchRepo, searchContract, knowledgeStore, discoverabilityMetrics, logger)
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
		Store:            conflictStore,
		Provider:         verifierProvider,
		Embeddings:       retryEmbedder,
		EmbeddingTimeout: time.Duration(cfg.GetAIEmbeddingTimeoutSeconds()) * time.Second,
		Timezone:         cfg.GetAppTimezone(),
		Limits:           conflictassessment.SemanticAssessmentLimits(assessmentLimits),
		Metrics:          discoverabilityMetrics,
	})
	if err != nil {
		return fmt.Errorf("failed to build conflict review runner: %w", err)
	}
	diagnosticProtector, err := newRootProtector(cfg)
	if err != nil {
		return fmt.Errorf("configure diagnostic protection: %w", err)
	}
	applications := buildApplicationBundle(applicationCompositionDependencies{
		Knowledge:              knowledgeStore,
		Dream:                  dreamStore,
		RememberPersistence:    knowledgeStore,
		GraphStore:             graphStore,
		TraceStore:             traceStore,
		CommunityStore:         communitypostgres.NewStore(pgDB.GetDB(), rlsHelper),
		RememberCatalog:        knowledgeStore,
		Search:                 searchRepo,
		RecallSearch:           recallStore,
		RecallFeedbackEvents:   recallFeedbackEventRepo,
		Assessor:               assessorProvider,
		GeneratorTransport:     assessorProvider,
		EmbeddingProvider:      openaiProvider,
		RetryEmbeddingProvider: retryEmbedder,
		AssessmentLimits:       assessmentLimits,
		Metrics:                discoverabilityMetrics,
		Logger:                 logger,
		DiagnosticProtector:    diagnosticProtector,
		Audit:                  auditService,
		AppConfig:              appConfigService,
		Teams:                  teamService,
		CommunitySummary: communitySummaryProvider{
			model:    cfg.GetAIVerifierModel(),
			complete: verifierProvider.StructuredChatJSON,
		},
		DreamEvidenceStore:  dreamStore,
		DreamModel:          cfg.GetAIVerifierModel(),
		ProviderCycleLease:  dreamProviderCycleLease(cfg),
		CorrectionTimeout:   time.Duration(cfg.GetAIEmbeddingTimeoutSeconds()) * time.Second,
		CorrectionExecutor:  buildSemanticWriteCorrectionExecutor(openaiProvider),
		TelemetryPrometheus: telemetryPrometheusService,
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

	evaluationBindings, err := buildEvaluationRegistryBindings(pgDB.GetDB(), rlsHelper, communitypostgres.NewStore(pgDB.GetDB(), rlsHelper), auditService)
	if err != nil {
		return fmt.Errorf("failed to build evaluation registry bindings: %w", err)
	}
	toolRegistry, err := registry.BuildActive(registry.Dependencies{
		Core: registry.CoreDependencies{
			Metrics:              discoverabilityMetrics,
			RecallFeedbackConfig: appConfigService,
			RecallFeedbackEvents: recallFeedbackEventService,
		},
		RememberBindings:   registry.RememberBindings{Service: rememberSvc},
		RecallBindings:     registry.RecallBindings{Service: recallSvc, Dreams: dreamSvc},
		LifecycleBindings:  registry.LifecycleBindings{Service: lifecycleSvc},
		TraceBindings:      registry.TraceBindings{Service: contextSvc},
		DreamBindings:      registry.DreamBindings{Service: dreamSvc},
		MemoryPackBindings: registry.MemoryPackBindings{Service: memoryPackSvc},
		EvaluationBindings: evaluationBindings,
	})
	if err != nil {
		return fmt.Errorf("failed to build active tool registry: %w", err)
	}
	if options.ConfigureRegistry != nil {
		toolRegistry, err = options.ConfigureRegistry(startupCtx, runtimeCtx, toolRegistry)
		if err != nil {
			return fmt.Errorf("failed to configure runtime tool registry: %w", err)
		}
	}
	transport, err := buildTransportComposition(transportCompositionInputs{
		startupCtx:               startupCtx,
		cfg:                      cfg,
		pgDB:                     pgDB,
		authority:                authority,
		backend:                  backend,
		rls:                      rlsHelper,
		options:                  options,
		logger:                   logger,
		credentialLookupPrefixes: crypto.GetLookupPrefixes,
		searchRepo:               searchRepo,
		telemetry:                telemetry,
		toolRegistry:             toolRegistry,
		convergence:              searchApplication.Convergence,
		rememberAttempts:         buildRememberAttemptDiagnostics(knowledgeStore),
		credentialRepo:           credentialRepo,
		credentialVerifier:       credentialVerifier,
		activityWriter:           activityWriter,
		teamService:              teamService,
		credentialService:        credentialService,
		ssoService:               ssoService,
		portalSession:            portalSessionService,
		directoryIdentity:        directoryIdentityService,
		controlIdentity:          controlIdentityService,
		privateMemory:            privateMemoryService,
		auditService:             auditService,
		securityService:          securityService,
		appConfig:                appConfigService,
		operationLogs:            operationLogService,
		operationLogHealth:       operationLogService.CheckReadiness,
		usageMetrics:             usageMetricsService,
		conflictQueue:            conflictQueueService,
		evidenceConflicts:        evidenceConflictService,
		recallFeedback:           recallFeedbackEventService,
		community:                communitySvc,
		controlDream:             controlDreamSvc,
		graph:                    graphViewSvc,
		recall:                   recallSvc,
		dream:                    dreamSvc,
	})
	if err != nil {
		return fmt.Errorf("failed to build transport composition: %w", err)
	}
	e := transport.e
	runtimeCtx.Echo = e
	controlServer := transport.controlServer
	telemetryServer := transport.telemetryServer
	if err := processCtx.Err(); err != nil {
		return err
	}

	// Reserve every listener before starting application work. A bind failure
	// leaves no worker running and closes any listener already reserved.
	httpAddr := strings.TrimSpace(os.Getenv("HTTP_ADDR"))
	if httpAddr == "" {
		httpAddr = config.DefaultHTTPAddr
	}
	if err := processCtx.Err(); err != nil {
		return err
	}
	publicListener, err := bindEchoServer(e, httpAddr)
	if err != nil {
		return fmt.Errorf("bind server listener: %w", err)
	}
	var controlListener, telemetryListener net.Listener
	closeBoundListeners := func() {
		_ = publicListener.Close()
		_ = controlListenerClose(controlListener)
		_ = telemetryListenerClose(telemetryListener)
	}
	if controlServer != nil {
		controlListener, err = bindEchoServer(controlServer, cfg.GetControlHTTPAddr())
		if err != nil {
			closeBoundListeners()
			return fmt.Errorf("bind control portal listener: %w", err)
		}
	} else if telemetryServer != nil {
		addr := strings.TrimSpace(transport.telemetryServerAddr)
		if addr == "" {
			addr = ":8091"
		}
		telemetryListener, err = bindEchoServer(telemetryServer, addr)
		if err != nil {
			closeBoundListeners()
			return fmt.Errorf("bind telemetry listener: %w", err)
		}
	}
	if err := processCtx.Err(); err != nil {
		closeBoundListeners()
		return err
	}

	var runtimeWorker RuntimeWorker
	runtimeFailures := make(chan error, 3)
	lifecycle := newRuntimeLifecycle(context.Background())
	stopStartupCancel := context.AfterFunc(processCtx, lifecycle.cancel)
	startupBridgeArmed := true
	defer func() {
		if startupBridgeArmed {
			stopStartupCancel()
		}
	}()
	startupCheck := func() error {
		if err := processCtx.Err(); err != nil {
			return err
		}
		return lifecycle.Context().Err()
	}
	// The operation sink is added first so reverse-order shutdown flushes it
	// after producers have stopped emitting records.
	lifecycle.add(managedRuntimeWorker{name: "operation log", shutdown: operationLogService.Shutdown, shutdownTimeout: writerShutdownTimeout})
	abortStartup := func(startupErr error) error {
		closeBoundListeners()
		workerCtx, workerCancel := context.WithTimeout(context.Background(), workerJoinTimeout)
		shutdownErr := lifecycle.shutdown(workerCtx)
		workerCancel()
		if shutdownErr != nil {
			startupErr = errors.Join(startupErr, shutdownErr)
			if errors.Is(shutdownErr, ErrRuntimeShutdownTimeout) {
				closeBackend = false
				closeOperationLogDB = false
			}
		}
		return startupErr
	}
	if options.BuildWorker != nil {
		runtimeWorker, err = options.BuildWorker(lifecycle.Context(), runtimeCtx)
		if err != nil {
			return abortStartup(fmt.Errorf("failed to start runtime background jobs: %w", err))
		}
	}
	if err := startupCheck(); err != nil {
		return abortStartup(err)
	}

	// Start all configured workers and listeners exactly once after binding.
	if err := startupCheck(); err != nil {
		return abortStartup(err)
	}
	activityWriter.Start(lifecycle.Context())
	lifecycle.add(managedRuntimeWorker{name: "credential activity", shutdown: activityWriter.Shutdown, shutdownTimeout: writerShutdownTimeout})
	if err := startupCheck(); err != nil {
		return abortStartup(err)
	}
	if err := startupCheck(); err != nil {
		return abortStartup(err)
	}
	usageMetricsService.Start(lifecycle.Context())
	lifecycle.add(managedRuntimeWorker{name: "usage metrics", shutdown: usageMetricsService.Shutdown, shutdownTimeout: writerShutdownTimeout})
	if err := startupCheck(); err != nil {
		return abortStartup(err)
	}
	recallFeedbackEventService.Start(lifecycle.Context())
	lifecycle.add(managedRuntimeWorker{name: "recall feedback", shutdown: recallFeedbackEventService.Shutdown, shutdownTimeout: writerShutdownTimeout})
	if err := startupCheck(); err != nil {
		return abortStartup(err)
	}
	if privateMemoryDone := privateMemoryService.Start(lifecycle.Context()); privateMemoryDone != nil {
		lifecycle.add(managedRuntimeWorker{name: "private memory", done: privateMemoryDone, shutdown: privateMemoryService.Shutdown})
	}
	if err := startupCheck(); err != nil {
		return abortStartup(err)
	}
	if telemetry.PricingRefreshEnabled {
		lifecycle.start("telemetry pricing refresh", func(ctx context.Context) {
			operations.RefreshTelemetryPricingCacheUntilCanceled(ctx, appConfigService, logger)
		})
	}
	if err := startupCheck(); err != nil {
		return abortStartup(err)
	}
	lifecycle.start("search reconciliation", func(ctx context.Context) {
		startSearchReconciliation(ctx, searchApplication.Reconciliation, logger)
	})
	if err := startupCheck(); err != nil {
		return abortStartup(err)
	}
	diagnosticDone := knowledgeStore.StartRememberAttemptDiagnosticPurger(lifecycle.Context(), time.Hour, rootSlogLogger(logger))
	lifecycle.add(managedRuntimeWorker{name: "remember diagnostics", done: diagnosticDone, shutdown: knowledgeStore.ShutdownRememberAttemptDiagnosticPurger})
	if err := startupCheck(); err != nil {
		return abortStartup(err)
	}
	lifecycle.start("dream scheduler", func(ctx context.Context) {
		dream.NewScheduler(dreamSvc, teamService, rootSlogLogger(logger)).Start(ctx)
	})
	if err := startupCheck(); err != nil {
		return abortStartup(err)
	}
	lifecycle.start("community scheduler", func(ctx context.Context) {
		communityapp.NewScheduler(communitySvc, teamService, appConfigService, rootSlogLogger(logger)).Start(ctx)
	})
	if err := startupCheck(); err != nil {
		return abortStartup(err)
	}
	conflictReviewScheduler := conflictreview.NewReviewService(teamService, conflictReviewRunner, &cfg, logger, discoverabilityMetrics)
	lifecycle.start("conflict review", conflictReviewScheduler.Run)
	if err := startupCheck(); err != nil {
		return abortStartup(err)
	}
	if runtimeWorker != nil {
		workerName := strings.TrimSpace(runtimeWorker.Name())
		if workerName == "" {
			workerName = "runtime worker"
		}
		lifecycle.start(workerName, func(ctx context.Context) {
			if err := runtimeWorker.Run(ctx); err != nil && ctx.Err() == nil {
				runtimeFailures <- runtimeWorkerFailure{cause: err}
			}
		})
	}
	if err := startupCheck(); err != nil {
		return abortStartup(err)
	}
	if !stopStartupCancel() {
		if err := processCtx.Err(); err != nil {
			return abortStartup(err)
		}
		return abortStartup(context.Canceled)
	}
	startupBridgeArmed = false
	if err := processCtx.Err(); err != nil {
		return abortStartup(err)
	}

	listenerFailures := make(chan error, 3)
	startListener := func(name string, server *echo.Echo, listener net.Listener, errorsCh <-chan error) {
		if server == nil || listener == nil {
			return
		}
		logger.Info("starting "+name, observability.String("addr", listener.Addr().String()))
		lifecycle.start(name, func(ctx context.Context) {
			select {
			case err := <-errorsCh:
				if !isExpectedServerClose(err) {
					listenerFailures <- fmt.Errorf("%s: %w", name, err)
				}
			case <-ctx.Done():
			}
		})
	}
	publicErrors := serveEchoServer(e, httpAddr)
	startListener("server", e, publicListener, publicErrors)
	if controlServer != nil {
		startListener("control portal", controlServer, controlListener, serveEchoServer(controlServer, cfg.GetControlHTTPAddr()))
	} else if telemetryServer != nil {
		addr := strings.TrimSpace(transport.telemetryServerAddr)
		if addr == "" {
			addr = ":8091"
		}
		startListener("telemetry scrape server", telemetryServer, telemetryListener, serveEchoServer(telemetryServer, addr))
	}

	var runErr error
	select {
	case <-processCtx.Done():
		logger.Info("shutting down server")
	case err := <-listenerFailures:
		runErr = err
		logger.Error("server listener stopped unexpectedly", err)
	case err := <-runtimeFailures:
		runErr = err
		logger.Error("runtime worker stopped unexpectedly", err, observability.String("error_code", "runtime_worker_failed"))
	}
	listenerCtx, listenerCancel := context.WithTimeout(context.Background(), listenerShutdownTimeout)
	listenerShutdowns := []listenerShutdown{{
		name: "server",
		shutdown: func(ctx context.Context) error {
			return shutdownEchoServer(ctx, e)
		},
	}}
	if controlServer != nil {
		listenerShutdowns = append(listenerShutdowns, listenerShutdown{
			name: "control portal",
			shutdown: func(ctx context.Context) error {
				return shutdownEchoServer(ctx, controlServer)
			},
		})
	}
	if telemetryServer != nil {
		listenerShutdowns = append(listenerShutdowns, listenerShutdown{
			name: "telemetry",
			shutdown: func(ctx context.Context) error {
				return shutdownEchoServer(ctx, telemetryServer)
			},
		})
	}
	if err := shutdownListeners(listenerCtx, listenerShutdowns...); err != nil {
		runErr = errors.Join(runErr, err)
	}
	listenerCancel()
	workerCtx, workerCancel := context.WithTimeout(context.Background(), workerJoinTimeout)
	if err := lifecycle.shutdown(workerCtx); err != nil {
		runErr = errors.Join(runErr, err)
		if errors.Is(err, ErrRuntimeShutdownTimeout) {
			runErr = errors.Join(runErr, ErrRuntimeShutdownTimeout)
			closeBackend = false
			closeOperationLogDB = false
		}
	}
	workerCancel()
	return runErr
}

func controlListenerClose(listener net.Listener) error {
	if listener == nil {
		return nil
	}
	return listener.Close()
}

func telemetryListenerClose(listener net.Listener) error {
	if listener == nil {
		return nil
	}
	return listener.Close()
}
