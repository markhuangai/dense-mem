//go:build uat

package uat

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/neo4j"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	rediscontainer "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/config"
	httpserver "github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/openapi"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/storage/inmem"
	neo4jstorage "github.com/markhuangai/dense-mem/internal/storage/neo4j"
	pgclient "github.com/markhuangai/dense-mem/internal/storage/postgres"
	redisclient "github.com/markhuangai/dense-mem/internal/storage/redis"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// scopedReaderAdapter bridges neo4jstorage.ScopedReader to the fragment
// services' local ScopedReader interface (which returns `any` to avoid
// an import cycle).
type scopedReaderAdapter struct {
	inner neo4jstorage.ScopedReader
}

func (a *scopedReaderAdapter) ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (any, []map[string]any, error) {
	summary, rows, err := a.inner.ScopedRead(ctx, profileID, query, params)
	return summary, rows, err
}

// fragmentAuditAdapter bridges fragmentservice.AuditLogEntry to the canonical
// service.AuditLogEntry. The fragmentservice version is a structural duplicate
// restated to avoid an import cycle; this adapter copies fields across.
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

// TestEnvOptions configures how the test environment is set up.
type TestEnvOptions struct {
	NoRedisMode bool
	// RateLimitPerMinute overrides the default tier-based rate limit (100/min) when > 0.
	// Set to a small value (e.g. 2) to exercise rate limiting without sending hundreds of requests.
	RateLimitPerMinute int
}

// TestEnvProvider is the companion interface for TestEnv to enable mockability
type TestEnvProvider interface {
	Setup(ctx context.Context) error
	Teardown(ctx context.Context) error
	GetPostgresDSN() string
	GetNeo4jURI() string
	GetNeo4jAuth() (username, password string)
	GetRedisAddr() string
	GetServerURL() string
	GetDB() *gorm.DB
	GetAPIKeyRepo() repository.APIKeyRepository
	GetProfileRepo() repository.ProfileRepository
	GetProfileService() service.ProfileService
	GetAPIKeyService() service.APIKeyService
	GetAuditService() service.AuditService
	GetRedisClient() redisclient.RedisClientInterface
	GetNeo4jClient() *neo4jstorage.Neo4jClient
	GetHTTPClient() *httptest.Server
	IsNoRedisMode() bool
}

// TestEnv is a shared integration fixture that manages test containers
// and in-process server lifecycle for UAT tests
type TestEnv struct {
	// Postgres container
	postgresContainer testcontainers.Container
	postgresDSN       string
	db                *gorm.DB

	// Neo4j container
	neo4jContainer testcontainers.Container
	neo4jURI       string
	neo4jUser      string
	neo4jPassword  string
	neo4jClient    *neo4jstorage.Neo4jClient

	// Redis container
	redisContainer      testcontainers.Container
	redisAddr           string
	redisClient         redisclient.RedisClientInterface
	redisConcreteClient *redisclient.RedisClient

	// Server
	server     *echo.Echo
	httpServer *httptest.Server
	serverURL  string

	// Services
	apiKeyRepo   repository.APIKeyRepository
	profileRepo  repository.ProfileRepository
	auditService service.AuditService
	profileSvc   service.ProfileService
	apiKeySvc    service.APIKeyService
	rateLimitSvc *service.RateLimitService

	// Options
	noRedisMode        bool
	rateLimitPerMinute int

	t *testing.T
}

// Ensure TestEnv implements TestEnvProvider
var _ TestEnvProvider = (*TestEnv)(nil)

// Setup initializes all test containers and starts the in-process server
func (te *TestEnv) Setup(ctx context.Context) error {
	// Start Postgres container
	pgContainer, err := postgrescontainer.Run(ctx,
		"postgres:16-alpine",
		postgrescontainer.WithDatabase("uatdb"),
		postgrescontainer.WithUsername("uatuser"),
		postgrescontainer.WithPassword("uatpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		return fmt.Errorf("failed to start postgres container: %w", err)
	}
	te.postgresContainer = pgContainer

	dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		return fmt.Errorf("failed to get postgres connection string: %w", err)
	}
	te.postgresDSN = dsn

	// Open database connection
	te.db, err = pgclient.Open(ctx, &pgTestConfig{dsn: dsn})
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		return fmt.Errorf("failed to open postgres: %w", err)
	}

	// Run migrations
	if err := pgclient.RunUp(ctx, te.db); err != nil {
		_ = pgContainer.Terminate(ctx)
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	// Start Neo4j container
	neo4jCont, err := neo4j.Run(ctx,
		"neo4j:5-community",
		neo4j.WithAdminPassword("uatneo4jpass"),
		neo4j.WithLabsPlugin(neo4j.Apoc),
		testcontainers.WithWaitStrategy(
			wait.ForLog("Started").
				WithOccurrence(1).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return fmt.Errorf("failed to start neo4j container: %w", err)
	}
	te.neo4jContainer = neo4jCont

	neo4jURI, err := neo4jCont.BoltUrl(ctx)
	if err != nil {
		_ = neo4jCont.Terminate(ctx)
		return fmt.Errorf("failed to get neo4j URI: %w", err)
	}
	te.neo4jURI = neo4jURI
	te.neo4jUser = "neo4j"
	te.neo4jPassword = "uatneo4jpass"

	// Create Neo4j client
	neo4jCfg := &neo4jTestConfig{
		uri:      te.neo4jURI,
		user:     te.neo4jUser,
		password: te.neo4jPassword,
		database: "neo4j",
	}
	te.neo4jClient, err = neo4jstorage.NewClient(ctx, neo4jCfg)
	if err != nil {
		return fmt.Errorf("failed to create neo4j client: %w", err)
	}

	// Bootstrap Neo4j schema (constraints, indexes, vector index).
	// UAT uses the legacy EmbeddingDimensions (1536) since AI provider is unconfigured.
	bootstrapLogger := observability.New(slog.LevelInfo)
	schemaBootstrapper := neo4jstorage.NewSchemaBootstrapper(te.neo4jClient, 1536, bootstrapLogger)
	if err := schemaBootstrapper.EnsureSchema(ctx); err != nil {
		return fmt.Errorf("failed to bootstrap neo4j schema: %w", err)
	}

	if !te.noRedisMode {
		// Start Redis container
		redisCont, err := rediscontainer.Run(ctx, "redis:7-alpine")
		if err != nil {
			return fmt.Errorf("failed to start redis container: %w", err)
		}
		te.redisContainer = redisCont

		redisHost, err := redisCont.Host(ctx)
		if err != nil {
			_ = redisCont.Terminate(ctx)
			return fmt.Errorf("failed to get redis host: %w", err)
		}
		redisPort, err := redisCont.MappedPort(ctx, "6379")
		if err != nil {
			_ = redisCont.Terminate(ctx)
			return fmt.Errorf("failed to get redis port: %w", err)
		}
		te.redisAddr = fmt.Sprintf("%s:%s", redisHost, redisPort.Port())

		// Create Redis client
		redisCfg := &redisTestConfig{addr: te.redisAddr}
		concreteClient, err := redisclient.NewClient(ctx, redisCfg)
		if err != nil {
			return fmt.Errorf("failed to create redis client: %w", err)
		}
		te.redisConcreteClient = concreteClient
		te.redisClient = concreteClient
	}

	// Initialize repositories
	rlsHelper := pgclient.NewRLS()
	te.apiKeyRepo = repository.NewAPIKeyRepository(te.db, rlsHelper)
	te.profileRepo = repository.NewProfileRepository(te.db, rlsHelper)

	// Initialize services
	te.auditService = service.NewAuditService(te.db)

	// Wire cleanup repos — must never be nil (AC-E2).
	// No-Redis mode: noop in-memory implementations.
	// Redis mode: Redis-backed cleanup repository.
	var statePurger service.ProfileStatePurger
	var sessionInvalidator service.KeySessionInvalidator
	if te.noRedisMode {
		noopCleanup := inmem.NewNoopCleanupRepository()
		statePurger = noopCleanup
		sessionInvalidator = noopCleanup
	} else {
		redisCleanup := redisclient.NewCleanupRepository(te.redisConcreteClient.GetClient())
		statePurger = redisCleanup
		sessionInvalidator = redisCleanup
	}

	te.profileSvc = service.NewProfileService(te.profileRepo, te.auditService, statePurger)
	te.apiKeySvc = service.NewAPIKeyService(te.apiKeyRepo, te.profileSvc, te.auditService, sessionInvalidator, statePurger)
	if te.noRedisMode {
		// Build in-memory rate limiting store
		memStore := inmem.NewInMemoryRateLimitStore()
		te.rateLimitSvc = service.NewRateLimitService(memStore)
	} else {
		te.rateLimitSvc = service.NewRateLimitService(te.redisClient)
	}

	// Create server with health checks
	logger := observability.New(slog.LevelInfo)

	var checks []httpserver.HealthCheck

	if !te.noRedisMode {
		checks = append(checks, httpserver.HealthCheck{
			Name: "redis",
			Check: func(ctx context.Context) error {
				return te.redisClient.Ping(ctx)
			},
		})
	}

	checks = append(checks, httpserver.HealthCheck{
		Name: "neo4j",
		Check: func(ctx context.Context) error {
			return te.neo4jClient.Verify(ctx)
		},
	}, httpserver.HealthCheck{
		Name: "postgres",
		Check: func(ctx context.Context) error {
			sqlDB, _ := te.db.DB()
			if sqlDB == nil {
				return fmt.Errorf("no underlying sql.DB")
			}
			return sqlDB.PingContext(ctx)
		},
	})

	var healthConfig httpserver.HealthConfig
	if te.noRedisMode {
		healthConfig = httpserver.HealthConfig{
			Checks:   checks,
			Degraded: true,
			Reason:   "in-memory backend: no cross-instance rate limiting or session cleanup",
		}
	} else {
		healthConfig = httpserver.HealthConfig{Checks: checks}
	}

	te.server = httpserver.NewServer(te.buildConfigConcrete(), logger, healthConfig)

	// Build config pointer for deps
	cfg := te.buildConfig()

	// Register protected routes with handlers
	deps := httpserver.ProtectedDeps{
		APIKeyRepo:       te.apiKeyRepo,
		ProfileService:   te.profileSvc,
		ProfileSvc:       te.profileSvc,
		RateLimitService: te.rateLimitSvc,
		AuditService:     te.auditService,
		Config:           cfg,
		Logger:           logger,
	}

	// ========================================
	// Discoverability wiring: fragments, registry, OpenAPI, catalog
	// ========================================
	// Embedding is unconfigured in UAT (IsEmbeddingConfigured() == false), so the
	// create and recall services remain unwired in this harness. The registry
	// still exposes the stable catalog and read/list/delete work.
	profileScopeEnforcer := neo4jstorage.NewProfileScopeEnforcer(te.neo4jClient)
	readerAdapter := &scopedReaderAdapter{inner: profileScopeEnforcer}
	fragmentAuditor := &fragmentAuditAdapter{inner: te.auditService}

	fragmentGetSvc := fragmentservice.NewGetFragmentService(readerAdapter)
	fragmentListSvc := fragmentservice.NewListFragmentsService(readerAdapter)
	fragmentDeleteSvc := fragmentservice.NewDeleteFragmentService(profileScopeEnforcer, readerAdapter, fragmentAuditor, slog.Default())

	toolRegistry, err := registry.BuildDefault(registry.Dependencies{
		FragmentGet:  fragmentGetSvc,
		FragmentList: fragmentListSvc,
	})
	if err != nil {
		return fmt.Errorf("failed to build tool registry: %w", err)
	}

	openAPIGen := openapi.New(toolRegistry, openapi.DefaultRoutes())

	fragmentReadHandler := handler.NewFragmentReadHandler(fragmentGetSvc)
	fragmentListHandler := handler.NewFragmentListHandler(fragmentListSvc)
	fragmentDeleteHandler := handler.NewFragmentDeleteHandler(fragmentDeleteSvc)
	toolCatalogHandler := handler.NewToolCatalogHandler(toolRegistry)
	openAPIAISafeHandler := handler.NewOpenAPIHandler(openAPIGen, openapi.SpecVariantAISafe)
	openAPIFullHandler := handler.NewOpenAPIHandler(openAPIGen, openapi.SpecVariantFull)

	handlers := httpserver.ProtectedHandlers{
		APIKeySvc:      te.apiKeySvc,
		FragmentRead:   fragmentReadHandler.Handle,
		FragmentList:   fragmentListHandler.Handle,
		FragmentDelete: fragmentDeleteHandler.Handle,
		ToolCatalog:    toolCatalogHandler.Handle,
		OpenAPIAISafe:  openAPIAISafeHandler.Handle,
		OpenAPIFull:    openAPIFullHandler.Handle,
	}

	httpserver.RegisterProtectedRoutesWithHandlers(te.server, deps, handlers)

	// Create httptest server
	te.httpServer = httptest.NewServer(te.server)
	te.serverURL = te.httpServer.URL

	return nil
}

// Teardown stops all containers and cleans up resources
func (te *TestEnv) Teardown(ctx context.Context) error {
	// Close httptest server
	if te.httpServer != nil {
		te.httpServer.Close()
	}

	// Close Neo4j
	if te.neo4jClient != nil {
		_ = te.neo4jClient.Close(ctx)
	}

	// Close Redis (only in Redis mode)
	if te.redisClient != nil && !te.noRedisMode {
		if rc, ok := te.redisClient.(interface{ Close() error }); ok {
			_ = rc.Close()
		}
	}

	// Close Postgres
	if te.db != nil {
		sqlDB, _ := te.db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	}

	// Terminate containers
	if te.neo4jContainer != nil {
		_ = te.neo4jContainer.Terminate(ctx)
	}
	if te.redisContainer != nil {
		_ = te.redisContainer.Terminate(ctx)
	}
	if te.postgresContainer != nil {
		_ = te.postgresContainer.Terminate(ctx)
	}

	return nil
}

// Getters for TestEnvProvider interface
func (te *TestEnv) GetPostgresDSN() string                           { return te.postgresDSN }
func (te *TestEnv) GetNeo4jURI() string                              { return te.neo4jURI }
func (te *TestEnv) GetNeo4jAuth() (string, string)                   { return te.neo4jUser, te.neo4jPassword }
func (te *TestEnv) GetRedisAddr() string                             { return te.redisAddr }
func (te *TestEnv) GetServerURL() string                             { return te.serverURL }
func (te *TestEnv) GetDB() *gorm.DB                                  { return te.db }
func (te *TestEnv) GetAPIKeyRepo() repository.APIKeyRepository       { return te.apiKeyRepo }
func (te *TestEnv) GetProfileRepo() repository.ProfileRepository     { return te.profileRepo }
func (te *TestEnv) GetProfileService() service.ProfileService        { return te.profileSvc }
func (te *TestEnv) GetAPIKeyService() service.APIKeyService          { return te.apiKeySvc }
func (te *TestEnv) GetAuditService() service.AuditService            { return te.auditService }
func (te *TestEnv) GetRedisClient() redisclient.RedisClientInterface { return te.redisClient }
func (te *TestEnv) GetNeo4jClient() *neo4jstorage.Neo4jClient        { return te.neo4jClient }
func (te *TestEnv) GetHTTPClient() *httptest.Server                  { return te.httpServer }
func (te *TestEnv) IsNoRedisMode() bool                              { return te.noRedisMode }

// NewTestEnv creates a new TestEnv instance
func NewTestEnv(t *testing.T, opts ...TestEnvOptions) *TestEnv {
	te := &TestEnv{t: t}
	if len(opts) > 0 {
		te.noRedisMode = opts[0].NoRedisMode
		te.rateLimitPerMinute = opts[0].RateLimitPerMinute
	}
	return te
}

// SetupTestEnv is a helper function that sets up the test environment
// and returns a cleanup function. If the Docker daemon is unreachable
// (e.g. CI runners without Docker-in-Docker or restricted socket perms),
// the test is skipped rather than failed — testcontainer dependencies
// are an environment requirement, not a contract failure.
func SetupTestEnv(t *testing.T, ctx context.Context, opts ...TestEnvOptions) (*TestEnv, func()) {
	t.Helper()
	env := NewTestEnv(t, opts...)
	if err := env.Setup(ctx); err != nil {
		if isDockerUnavailable(err) {
			t.Skipf("Docker unavailable — skipping testcontainer-backed UAT: %v", err)
		}
		t.Fatalf("failed to setup test environment: %v", err)
	}
	cleanup := func() {
		if err := env.Teardown(ctx); err != nil {
			t.Logf("warning: failed to teardown test environment: %v", err)
		}
	}
	return env, cleanup
}

// isDockerUnavailable returns true when the error chain indicates the Docker
// daemon cannot be reached. Used to decide Skip vs Fatal in test setup.
func isDockerUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "docker.sock"),
		strings.Contains(msg, "failed to create Docker provider"),
		strings.Contains(msg, "rootless Docker not found"),
		strings.Contains(msg, "Cannot connect to the Docker daemon"):
		return true
	}
	return false
}

// Helper config implementations
type pgTestConfig struct {
	dsn string
}

func (c *pgTestConfig) GetPostgresDSN() string { return c.dsn }

type neo4jTestConfig struct {
	uri      string
	user     string
	password string
	database string
}

func (c *neo4jTestConfig) GetNeo4jURI() string      { return c.uri }
func (c *neo4jTestConfig) GetNeo4jUser() string     { return c.user }
func (c *neo4jTestConfig) GetNeo4jPassword() string { return c.password }
func (c *neo4jTestConfig) GetNeo4jDatabase() string { return c.database }

type redisTestConfig struct {
	addr string
}

func (c *redisTestConfig) GetRedisAddr() string     { return c.addr }
func (c *redisTestConfig) GetRedisPassword() string { return "" }
func (c *redisTestConfig) GetRedisDB() int          { return 0 }

// buildConfig creates a full config provider for the test environment
func (te *TestEnv) buildConfig() *testConfig {
	rateLimit := 100
	if te.rateLimitPerMinute > 0 {
		rateLimit = te.rateLimitPerMinute
	}
	return &testConfig{
		postgresDSN:             te.postgresDSN,
		neo4jURI:                te.neo4jURI,
		neo4jUser:               te.neo4jUser,
		neo4jPassword:           te.neo4jPassword,
		neo4jDatabase:           "neo4j",
		redisAddr:               te.redisAddr,
		redisPassword:           "",
		redisDB:                 0,
		rateLimitPerMinute:      rateLimit,
		fragmentCreateRateLimit: 60,
		fragmentReadRateLimit:   300,
		sseHeartbeatSeconds:     30,
		sseMaxDurationSeconds:   300,
		sseMaxConcurrentStreams: 10,
		embeddingDimensions:     1536,
		aiEmbeddingTimeoutSecs:  30,
	}
}

// buildConfigConcrete creates a concrete config.Config for NewServer
func (te *TestEnv) buildConfigConcrete() config.Config {
	rateLimit := 100
	if te.rateLimitPerMinute > 0 {
		rateLimit = te.rateLimitPerMinute
	}
	return config.Config{
		PostgresDSN:               te.postgresDSN,
		Neo4jURI:                  te.neo4jURI,
		Neo4jUser:                 te.neo4jUser,
		Neo4jPassword:             te.neo4jPassword,
		Neo4jDatabase:             "neo4j",
		RedisAddr:                 te.redisAddr,
		RedisPassword:             "",
		RedisDB:                   0,
		RateLimitPerMinute:        rateLimit,
		FragmentCreateRateLimit:   60,
		FragmentReadRateLimit:     300,
		SSEHeartbeatSeconds:       30,
		SSEMaxDurationSeconds:     300,
		SSEMaxConcurrentStreams:   10,
		EmbeddingDimensions:       1536,
		AIEmbeddingTimeoutSeconds: 30,
	}
}

// testConfig implements config.ConfigProvider
type testConfig struct {
	postgresDSN             string
	neo4jURI                string
	neo4jUser               string
	neo4jPassword           string
	neo4jDatabase           string
	redisAddr               string
	redisPassword           string
	redisDB                 int
	rateLimitPerMinute      int
	fragmentCreateRateLimit int
	fragmentReadRateLimit   int
	sseHeartbeatSeconds     int
	sseMaxDurationSeconds   int
	sseMaxConcurrentStreams int
	embeddingDimensions     int
	aiAPIURL                string
	aiAPIKey                string
	aiEmbeddingModel        string
	aiEmbeddingDimensions   int
	aiEmbeddingTimeoutSecs  int
}

func (c *testConfig) GetPostgresDSN() string            { return c.postgresDSN }
func (c *testConfig) GetNeo4jURI() string               { return c.neo4jURI }
func (c *testConfig) GetNeo4jUser() string              { return c.neo4jUser }
func (c *testConfig) GetNeo4jPassword() string          { return c.neo4jPassword }
func (c *testConfig) GetNeo4jDatabase() string          { return c.neo4jDatabase }
func (c *testConfig) GetRedisAddr() string              { return c.redisAddr }
func (c *testConfig) GetRedisPassword() string          { return c.redisPassword }
func (c *testConfig) GetRedisDB() int                   { return c.redisDB }
func (c *testConfig) GetHTTPMaxBodyBytes() int          { return 0 }
func (c *testConfig) GetRateLimitPerMinute() int        { return c.rateLimitPerMinute }
func (c *testConfig) GetFragmentCreateRateLimit() int   { return c.fragmentCreateRateLimit }
func (c *testConfig) GetFragmentReadRateLimit() int     { return c.fragmentReadRateLimit }
func (c *testConfig) GetSSEHeartbeatSeconds() int       { return c.sseHeartbeatSeconds }
func (c *testConfig) GetSSEMaxDurationSeconds() int     { return c.sseMaxDurationSeconds }
func (c *testConfig) GetSSEMaxConcurrentStreams() int   { return c.sseMaxConcurrentStreams }
func (c *testConfig) GetEmbeddingDimensions() int       { return c.embeddingDimensions }
func (c *testConfig) GetAIAPIURL() string               { return c.aiAPIURL }
func (c *testConfig) GetAIAPIKey() string               { return c.aiAPIKey }
func (c *testConfig) GetAIEmbeddingModel() string       { return c.aiEmbeddingModel }
func (c *testConfig) GetAIEmbeddingDimensions() int     { return c.aiEmbeddingDimensions }
func (c *testConfig) GetAIEmbeddingTimeoutSeconds() int { return c.aiEmbeddingTimeoutSecs }
func (c *testConfig) IsEmbeddingConfigured() bool {
	return c.aiAPIURL != "" && c.aiAPIKey != "" && c.aiEmbeddingModel != "" && c.aiEmbeddingDimensions > 0
}
func (c *testConfig) GetAIVerifierAPIURL() string            { return c.aiAPIURL }
func (c *testConfig) GetAIVerifierAPIKey() string            { return c.aiAPIKey }
func (c *testConfig) GetAIVerifierModel() string             { return "gpt-4o-mini" }
func (c *testConfig) GetAIVerifierTimeoutSeconds() int       { return 60 }
func (c *testConfig) GetAIVerifierMaxConcurrency() int       { return 5 }
func (c *testConfig) GetClaimWriteRateLimit() int            { return 60 }
func (c *testConfig) GetClaimReadRateLimit() int             { return 300 }
func (c *testConfig) GetRecallValidatedClaimWeight() float64 { return 0.5 }
func (c *testConfig) GetPromoteTxTimeoutSeconds() int        { return 10 }
func (c *testConfig) GetSkillPackImportHistoryDays() int     { return 30 }
func (c *testConfig) GetAICommunityMaxNodes() int            { return 500000 }
func (c *testConfig) GetControlHTTPAddr() string             { return "127.0.0.1:0" }
func (c *testConfig) GetControlPortalToken() string          { return "" }
