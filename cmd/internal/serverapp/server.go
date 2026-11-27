package serverapp

import (
	"context"
	"errors"
	"fmt"
	sessionpostgres "github.com/markhuangai/dense-mem/internal/session/postgres"
	"log/slog"
	"net"
	nethttp "net/http"
	"os"
	"strings"
	"time"

	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/config"
	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	"github.com/markhuangai/dense-mem/internal/crypto"
	densehttp "github.com/markhuangai/dense-mem/internal/http"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	operationspostgres "github.com/markhuangai/dense-mem/internal/operations/postgres"
	privacy "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	decisionprovider "github.com/markhuangai/dense-mem/internal/provider/decision"
	"github.com/markhuangai/dense-mem/internal/provider/structured"
	recallpostgres "github.com/markhuangai/dense-mem/internal/recall/postgres"
	settingspostgres "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres/lockadmission"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	topics "github.com/markhuangai/dense-mem/internal/topics"
	topicspostgres "github.com/markhuangai/dense-mem/internal/topics/postgres"
)

// runtimeWorkerFailure keeps the underlying cause for control flow while exposing a bounded message to logs.
type runtimeWorkerFailure struct {
	cause error
}

func (e runtimeWorkerFailure) Error() string { return "runtime worker failed" }

func (e runtimeWorkerFailure) Unwrap() error { return e.cause }

type aiSessionModels struct {
	remember string
}

func aiSessionModelsForConfig(cfg *config.Config) aiSessionModels {
	if cfg == nil {
		return aiSessionModels{}
	}
	return aiSessionModels{
		remember: cfg.GetAIRememberModel(),
	}
}

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
	seedAdmission, err := loadPublicSeedAdmission(cfg.DangerouslySkipAISecurityCheck)
	if err != nil {
		return err
	}
	if cfg.PostgresAdvisoryLockConcurrency != 0 {
		if err := lockadmission.Configure(pgDB.GetDB(), cfg.PostgresAdvisoryLockConcurrency); err != nil {
			return fmt.Errorf("configure PostgreSQL lock admission: %w", err)
		}
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
	knowledgeStore := knowledgepostgres.NewStore(pgDB.GetDB(), rlsHelper)
	conflictStore := conflictpostgres.NewStore(pgDB.GetDB(), rlsHelper)
	traceStore := buildTraceStore(pgDB.GetDB(), rlsHelper)
	evidenceConflictService := buildEvidenceConflictApplication(conflictStore)
	if err := checkActiveAuthority(authority); err != nil {
		return fmt.Errorf("active boot blocked: %w", err)
	}
	searchRepo, searchContract, err := buildSearchRepositoryApplication(startupCtx, cfg, pgDB, rlsHelper, knowledgeStore)
	if err != nil {
		return fmt.Errorf("active search bootstrap blocked: %w", err)
	}
	topicStore := topicspostgres.NewStore(pgDB.GetDB(), rlsHelper)
	recallStore := recallpostgres.NewStore(
		pgDB.GetDB(), rlsHelper, searchRepo,
		recallpostgres.LoadRecallEvidenceConflictRecords,
	).WithOrganizationReader(func() recallpostgres.OrganizationReader { return topicStore.NewRecallReader() }).WithTopics(topicStore.NewTopicReader())
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
	appConfigService := buildDecisionConfigurationApplication(appConfigRepo, auditService, cfg, logger)
	operationLogService := buildOperationLogApplication(operationLogRepo, appConfigService)
	operationLogService.SetMinimumLevel(level)
	if contextual, ok := logger.(interface{ ContextSlog() *slog.Logger }); ok {
		operationLogService.SetPruneLogger(contextual.ContextSlog())
	}
	if root, ok := logger.(observability.SinkAttacher); ok {
		if err := root.AttachSink(operationLogService); err != nil {
			return fmt.Errorf("attach operation log sink: %w", err)
		}
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
	diagnosticProtector, err := newRootProtector(cfg)
	if err != nil {
		return fmt.Errorf("configure diagnostic protection: %w", err)
	}
	securityService := buildSecurityApplication(securityRepo, auditService, diagnosticProtector, logger)
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
	recallStore.WithTopicDiagnostics(logger, diagnosticProtector)
	publicationDiagnostics := topics.PublicationDiagnostics(rootDiagnosticLogger(logger), diagnosticProtector)
	topicStore.WithPublicationDiagnostics(publicationDiagnostics)
	telemetry, err := buildTelemetryApplication(startupCtx, cfg, appConfigService, telemetryLifecycleRepo, logger, diagnosticProtector)
	if err != nil {
		return fmt.Errorf("failed to build telemetry application: %w", err)
	}
	discoverabilityMetrics := telemetry.Metrics
	defer func() {
		if err := telemetry.Exports.Shutdown(context.Background()); err != nil {
			logger.Warn("optional OTLP shutdown incomplete", observability.String("reason", "shutdown_deadline"))
		}
	}()
	telemetryPrometheusService := telemetry.Prometheus
	searchApplication := buildSearchProviders(cfg, searchRepo, searchContract, knowledgeStore, discoverabilityMetrics, logger, diagnosticProtector)
	openaiProvider := searchApplication.EmbeddingProvider
	retryEmbedder := searchApplication.RetryEmbedding
	sessionModels := aiSessionModelsForConfig(&cfg)
	assessmentLimits := config.SemanticAssessmentLimitsForConfig(&cfg)
	assessmentLimits.ProviderModel = sessionModels.remember
	aiHTTPClient := observability.DiagnosticHTTPClient(&nethttp.Client{Timeout: time.Duration(cfg.GetAIVerifierTimeoutSeconds()) * time.Second}, rootDiagnosticLogger(logger), diagnosticProtector)
	aiConcurrencyGate := modelprovider.NewConcurrencyGate(config.AIVerifierMaxConcurrency(&cfg))
	assessorProvider := structured.NewOpenAIWithModel(&cfg, aiHTTPClient, aiConcurrencyGate, sessionModels.remember)
	assessorProvider.SetMetrics(discoverabilityMetrics)
	var decisions modelprovider.DecisionProvider
	if cfg.Decision.Configured() {
		decisions, err = decisionprovider.New(cfg.Decision, cfg.AIVerifierTokenizer, nil, logger, diagnosticProtector, discoverabilityMetrics)
		if err != nil {
			return fmt.Errorf("decision provider configuration: %w", err)
		}
		decisionSnapshot, snapshotErr := appConfigService.DecisionRuntimeConfig(startupCtx)
		if snapshotErr != nil {
			return fmt.Errorf("decision settings initialization: %w", snapshotErr)
		}
		logger.Info("decision.configuration.effective", observability.LogAttr{Key: "event", Value: "decision.configuration.effective"}, observability.LogAttr{Key: "action", Value: "enable_configured_tasks"}, observability.LogAttr{Key: "settings", Value: observability.DecisionLogValue(decisionSnapshot)})
	}
	sessionAssessmentLimits := assessmentLimits
	sessionAssessmentLimits.MaxEvidenceItems = 100
	topicMaintenance := buildTopicMaintenance(topicStore, appConfigService, assessorProvider, assessmentLimits, sessionModels.remember,
		time.Duration(cfg.GetAIVerifierTimeoutSeconds())*time.Second, time.Duration(cfg.Decision.TimeoutSeconds)*time.Second,
		auditService, rootDiagnosticLogger(logger), topics.MaintenanceDiagnostics(operationLogService, diagnosticProtector, rootDiagnosticLogger(logger)))
	rootLogger := rootDiagnosticLogger(logger)
	topicAssignment := topics.NewAssignmentService(topicStore, appConfigService, assessorProvider, decisions, assessmentLimits, sessionModels.remember, rootLogger)
	knowledgeStore.WithTopicAssignments(topicStore)
	topicMaintenance.WithTopicProcessing(topicAssignment, topicStore, topicStore, topics.CommunityAssessor{
		Transport: assessorProvider, Decisions: decisions, Settings: appConfigService, Limits: assessmentLimits, Logger: rootLogger,
	}, appConfigService)
	topicMaintenance.WithTopicEmbeddings(topicStore, retryEmbedder, time.Duration(cfg.GetAIEmbeddingTimeoutSeconds())*time.Second)
	applications := buildApplicationBundle(applicationCompositionDependencies{
		TopicAssignment:      topicAssignment,
		PublicSeedAdmission:  seedAdmission,
		Knowledge:            knowledgeStore,
		SessionRepository:    sessionpostgres.New(pgDB.GetDB(), rlsHelper, knowledgeStore.CommitEvidenceTx, knowledgeStore.WithSubmissionLock),
		RememberPersistence:  knowledgeStore,
		TraceStore:           traceStore,
		MemoryPackReader:     traceStore,
		Search:               searchRepo,
		RecallSearch:         recallStore,
		RecallFeedbackEvents: recallFeedbackEventRepo,
		Assessor:             assessorProvider,
		Decisions:            decisions, DecisionSettings: appConfigService,
		SessionAssessor:             assessorProvider,
		SessionEnabled:              cfg.SessionIngestEnabled && cfg.GetAIVerifierAPIURL() != "" && cfg.GetAIVerifierAPIKey() != "" && cfg.IsEmbeddingConfigured(),
		RememberModel:               sessionModels.remember,
		RecallExpansionEnabled:      cfg.RecallExpansionEnabled,
		RecallDisableQueryEmbedding: cfg.RecallDisableQueryEmbedding,
		GeneratorTransport:          assessorProvider,
		EmbeddingProvider:           openaiProvider,
		RetryEmbeddingProvider:      retryEmbedder,
		AssessmentLimits:            assessmentLimits,
		Metrics:                     discoverabilityMetrics,
		Logger:                      logger,
		DiagnosticProtector:         diagnosticProtector,
		Audit:                       auditService,
		AppConfig:                   appConfigService,
		TelemetryPrometheus:         telemetryPrometheusService,
	})
	rememberSvc := applications.Remember
	recallSvc := applications.Recall
	lifecycleSvc := applications.Lifecycle
	contextSvc := applications.Context
	memoryPackSvc := applications.MemoryPack
	recallFeedbackEventService := applications.RecallFeedback

	toolRegistry, err := registry.BuildActive(registry.Dependencies{
		Core: registry.CoreDependencies{
			Metrics:              discoverabilityMetrics,
			RecallFeedbackConfig: appConfigService,
			RecallFeedbackEvents: recallFeedbackEventService,
		},
		RememberBindings:   registry.RememberBindings{Service: rememberSvc},
		SessionBindings:    registry.SessionBindings{Service: applications.Session},
		RecallBindings:     registry.RecallBindings{Service: recallSvc},
		LifecycleBindings:  registry.LifecycleBindings{Service: lifecycleSvc},
		TraceBindings:      registry.TraceBindings{Service: contextSvc},
		MemoryPackBindings: registry.MemoryPackBindings{Service: memoryPackSvc},
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
		rememberInvocations:      buildRememberInvocationDiagnostics(knowledgeStore),
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
		decisionConfig:           appConfigService,
		operationLogs:            operationLogService,
		operationLogHealth:       operationLogService.CheckReadiness,
		usageMetrics:             usageMetricsService,
		evidenceConflicts:        evidenceConflictService,
		recallFeedback:           recallFeedbackEventService,
		topicMaintenance:         topicMaintenance,
		topicManagement:          topics.NewManagementService(topicStore, auditService).WithPublicationDiagnostics(publicationDiagnostics),
		recall:                   recallSvc,
	})
	if err != nil {
		return fmt.Errorf("failed to build transport composition: %w", err)
	}
	e := transport.e
	runtimeCtx.Echo = e
	publicServer := densehttp.NewHTTPServer(e, logger)
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
	publicListener, err := bindHTTPServer(publicServer, httpAddr)
	if err != nil {
		return fmt.Errorf("bind server listener: %w", err)
	}
	runtimeCtx.HTTPAddress = publicServer.Addr
	var controlListener, telemetryListener net.Listener
	closeBoundListeners := func() {
		_ = publicListener.Close()
		_ = closeOptionalListener(controlListener)
		_ = closeOptionalListener(telemetryListener)
	}
	if controlServer != nil {
		controlListener, err = bindHTTPServer(controlServer, cfg.GetControlHTTPAddr())
		if err != nil {
			closeBoundListeners()
			return fmt.Errorf("bind control portal listener: %w", err)
		}
	} else if telemetryServer != nil {
		addr := strings.TrimSpace(transport.telemetryServerAddr)
		if addr == "" {
			addr = ":8091"
		}
		telemetryListener, err = bindHTTPServer(telemetryServer, addr)
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
	// Start all configured workers and listeners exactly once after binding.
	if err := startupCheck(); err != nil {
		return abortStartup(err)
	}
	activityWriter.Start(lifecycle.Context())
	lifecycle.add(managedRuntimeWorker{name: "credential activity", shutdown: activityWriter.Shutdown, shutdownTimeout: writerShutdownTimeout})
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
	lifecycle.startDiagnosticRetention(knowledgeStore.PurgeExpiredDiagnostics, time.Hour, rootSlogLogger(logger))
	if err := startupCheck(); err != nil {
		return abortStartup(err)
	}
	lifecycle.start("topic maintenance", func(ctx context.Context) {
		topics.NewMaintenanceScheduler(topicMaintenance, rootSlogLogger(logger)).Start(ctx)
	})
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
	startListener := func(name string, server *nethttp.Server, listener net.Listener) {
		if server == nil || listener == nil {
			return
		}
		errorsCh := serveHTTPServer(server, listener)
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
	startListener("server", publicServer, publicListener)
	startListener("control portal", controlServer, controlListener)
	startListener("telemetry scrape server", telemetryServer, telemetryListener)

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
			return shutdownHTTPServer(ctx, publicServer)
		},
	}}
	if controlServer != nil {
		listenerShutdowns = append(listenerShutdowns, listenerShutdown{
			name: "control portal",
			shutdown: func(ctx context.Context) error {
				return shutdownHTTPServer(ctx, controlServer)
			},
		})
	}
	if telemetryServer != nil {
		listenerShutdowns = append(listenerShutdowns, listenerShutdown{
			name: "telemetry",
			shutdown: func(ctx context.Context) error {
				return shutdownHTTPServer(ctx, telemetryServer)
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
			closeBackend = false
			closeOperationLogDB = false
		}
	}
	workerCancel()
	return runErr
}

func closeOptionalListener(listener net.Listener) error {
	if listener == nil {
		return nil
	}
	return listener.Close()
}
