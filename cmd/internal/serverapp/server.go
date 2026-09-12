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

	"github.com/markhuangai/dense-mem/internal/config"
	conflictreview "github.com/markhuangai/dense-mem/internal/conflict/review"
	"github.com/markhuangai/dense-mem/internal/conflictassessment"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

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
		return fmt.Errorf("active boot blocked: %w", err)
	}
	searchRepo, searchContract, err := buildSearchRepositoryApplication(startupCtx, cfg, pgDB, rlsHelper)
	if err != nil {
		return fmt.Errorf("active search bootstrap blocked: %w", err)
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
	telemetry, err := buildTelemetryApplication(startupCtx, cfg, appConfigService, ledgerRepo, ledgerRepo, logger)
	if err != nil {
		return fmt.Errorf("failed to build telemetry application: %w", err)
	}
	discoverabilityMetrics := telemetry.Metrics
	telemetryPrometheusService := telemetry.Prometheus
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
		return fmt.Errorf("failed to build conflict review runner: %w", err)
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

	evaluationBindings, err := buildEvaluationRegistryBindings(semanticRepo, auditService)
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
		startupCtx:         startupCtx,
		cfg:                cfg,
		pgDB:               pgDB,
		authority:          authority,
		backend:            backend,
		rls:                rlsHelper,
		options:            options,
		logger:             logger,
		searchRepo:         searchRepo,
		telemetry:          telemetry,
		toolRegistry:       toolRegistry,
		convergence:        searchApplication.Convergence,
		rememberAttempts:   buildRememberAttemptDiagnostics(ledgerRepo),
		credentialRepo:     credentialRepo,
		credentialVerifier: credentialVerifier,
		activityWriter:     activityWriter,
		teamService:        teamService,
		credentialService:  credentialService,
		ssoService:         ssoService,
		portalSession:      portalSessionService,
		directoryIdentity:  directoryIdentityService,
		controlIdentity:    controlIdentityService,
		privateMemory:      privateMemoryService,
		auditService:       auditService,
		securityService:    securityService,
		appConfig:          appConfigService,
		operationLogs:      operationLogService,
		usageMetrics:       usageMetricsService,
		conflictQueue:      conflictQueueService,
		evidenceConflicts:  evidenceConflictService,
		recallFeedback:     recallFeedbackEventService,
		community:          communitySvc,
		controlDream:       controlDreamSvc,
		graph:              graphViewSvc,
		recall:             recallSvc,
		dream:              dreamSvc,
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
	lifecycle := newRuntimeLifecycle(processCtx)
	if options.BuildWorker != nil {
		runtimeWorker, err = options.BuildWorker(lifecycle.Context(), runtimeCtx)
		if err != nil {
			closeBoundListeners()
			return fmt.Errorf("failed to start runtime background jobs: %w", err)
		}
	}
	if err := processCtx.Err(); err != nil {
		closeBoundListeners()
		return err
	}

	// Start all configured workers and listeners exactly once after binding.
	activityWriter.Start(lifecycle.Context())
	lifecycle.add(managedRuntimeWorker{name: "credential activity", shutdown: activityWriter.Shutdown, shutdownTimeout: writerShutdownTimeout})
	operationLogService.Start(lifecycle.Context())
	lifecycle.add(managedRuntimeWorker{name: "operation log", shutdown: operationLogService.Shutdown, shutdownTimeout: writerShutdownTimeout})
	usageMetricsService.Start(lifecycle.Context())
	lifecycle.add(managedRuntimeWorker{name: "usage metrics", shutdown: usageMetricsService.Shutdown, shutdownTimeout: writerShutdownTimeout})
	recallFeedbackEventService.Start(lifecycle.Context())
	lifecycle.add(managedRuntimeWorker{name: "recall feedback", shutdown: recallFeedbackEventService.Shutdown, shutdownTimeout: writerShutdownTimeout})
	if privateMemoryDone := privateMemoryService.Start(lifecycle.Context()); privateMemoryDone != nil {
		lifecycle.add(managedRuntimeWorker{name: "private memory", done: privateMemoryDone, shutdown: privateMemoryService.Shutdown})
	}
	if telemetry.PricingRefreshEnabled {
		lifecycle.start("telemetry pricing refresh", func(ctx context.Context) {
			refreshTelemetryPricingCacheUntilCanceled(ctx, appConfigService, logger)
		})
	}
	lifecycle.start("search reconciliation", func(ctx context.Context) {
		startSearchReconciliation(ctx, searchApplication.Reconciliation, logger)
	})
	diagnosticDone := ledgerRepo.StartRememberAttemptDiagnosticPurger(lifecycle.Context(), time.Hour, slog.Default())
	lifecycle.add(managedRuntimeWorker{name: "remember diagnostics", done: diagnosticDone, shutdown: ledgerRepo.ShutdownRememberAttemptDiagnosticPurger})
	lifecycle.start("dream scheduler", func(ctx context.Context) {
		dreamservice.NewScheduler(dreamSvc, teamService, slog.Default()).Start(ctx)
	})
	lifecycle.start("community scheduler", func(ctx context.Context) {
		communityservice.NewScheduler(communitySvc, teamService, appConfigService, slog.Default()).Start(ctx)
	})
	conflictReviewScheduler := conflictreview.NewReviewService(teamService, conflictReviewRunner, &cfg, logger, discoverabilityMetrics)
	lifecycle.start("conflict review", conflictReviewScheduler.Run)
	if runtimeWorker != nil {
		workerName := strings.TrimSpace(runtimeWorker.Name())
		if workerName == "" {
			workerName = "runtime worker"
		}
		lifecycle.start(workerName, func(ctx context.Context) {
			if err := runtimeWorker.Run(ctx); err != nil && ctx.Err() == nil {
				runtimeFailures <- fmt.Errorf("%s: %w", workerName, err)
			}
		})
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
		logger.Error("runtime worker stopped unexpectedly", err)
	}
	listenerCtx, listenerCancel := context.WithTimeout(context.Background(), listenerShutdownTimeout)
	if err := shutdownEchoServer(listenerCtx, e); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("server shutdown: %w", err))
	}
	if controlServer != nil {
		if err := shutdownEchoServer(listenerCtx, controlServer); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("control portal shutdown: %w", err))
		}
	}
	if telemetryServer != nil {
		if err := shutdownEchoServer(listenerCtx, telemetryServer); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("telemetry shutdown: %w", err))
		}
	}
	listenerCancel()
	workerCtx, workerCancel := context.WithTimeout(context.Background(), workerJoinTimeout)
	if err := lifecycle.shutdown(workerCtx); err != nil {
		runErr = errors.Join(runErr, err)
		if errors.Is(err, ErrRuntimeShutdownTimeout) {
			runErr = errors.Join(runErr, ErrRuntimeShutdownTimeout)
			closeBackend = false
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
