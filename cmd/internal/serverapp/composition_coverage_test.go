package serverapp

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/DATA-DOG/go-sqlmock"
	postgresdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/observability"
	searchapp "github.com/markhuangai/dense-mem/internal/search"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type telemetryPricingStub struct {
	err error
}

type authorityCoverageStub struct {
	marker *domain.CompatibilityMarker
}

func (s authorityCoverageStub) GetLatestMarker(context.Context) (*domain.CompatibilityMarker, error) {
	return s.marker, nil
}

func (s telemetryPricingStub) TelemetryPricingRuntimeConfig(context.Context) (domain.TelemetryPricingRuntimeConfig, error) {
	return domain.TelemetryPricingRuntimeConfig{}, s.err
}

func (telemetryPricingStub) CachedTelemetryPricingRuntimeConfig() (domain.TelemetryPricingRuntimeConfig, bool) {
	return domain.TelemetryPricingRuntimeConfig{}, true
}

type searchReconciliationCoverageStub struct {
	result searchapp.SearchReconciliationResult
	err    error
	called chan struct{}
}

func (s *searchReconciliationCoverageStub) Run(context.Context) (searchapp.SearchReconciliationResult, error) {
	if s.called != nil {
		select {
		case s.called <- struct{}{}:
		default:
		}
	}
	return s.result, s.err
}

func TestBootstrapWrappersAndEarlyServerGuards(t *testing.T) {
	bootstrap, err := ClassifyAuthority(context.Background(), authorityCoverageStub{marker: &domain.CompatibilityMarker{
		MarkerKind: domain.MigrationMarkerKindCutover,
		Version:    cutoverMarkerVersion,
		Status:     domain.MigrationMarkerCompatible,
	}})
	if err != nil || bootstrap.Mode != authorityActive {
		t.Fatalf("compatible authority = %+v, %v", bootstrap, err)
	}
	if _, err := ClassifyAuthority(context.Background(), nil); err == nil {
		t.Fatal("nil authority store was accepted")
	}
	if err := checkActiveAuthority(authorityBootstrap{}); err == nil {
		t.Fatal("inactive authority was accepted")
	}
	if err := RunActiveServer(context.Background(), context.Background(), config.Config{}, nil, nil, 0, authorityBootstrap{}, RuntimeOptions{}); err == nil {
		t.Fatal("nil postgres database was accepted")
	}
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost/db?sslmode=disable")
	t.Setenv("CONTROL_PORTAL_TOKEN", "control-secret")
	if err := RunFromEnvironment(context.Background(), RuntimeOptions{ValidateStartup: func(*config.Config) error { return errors.New("startup validation") }}); err == nil || !strings.Contains(err.Error(), "startup validation") {
		t.Fatalf("RunFromEnvironment validation error = %v", err)
	}
}

func TestCompositionHelpersCoverTelemetryAndMaintenanceBranches(t *testing.T) {
	logger := observability.New(0)
	disabled, err := buildTelemetryApplication(context.Background(), config.Config{}, telemetryPricingStub{}, nil, nil, logger)
	if err != nil || disabled.PricingRefreshEnabled || disabled.Reader != nil || disabled.Metrics == nil {
		t.Fatalf("disabled telemetry composition = %+v, %v", disabled, err)
	}
	enabled, err := buildTelemetryApplication(context.Background(), config.Config{TelemetryEnabled: true}, telemetryPricingStub{err: errors.New("pricing unavailable")}, nil, nil, logger)
	if err != nil || !enabled.PricingRefreshEnabled || enabled.Reader == nil || enabled.ScrapeHandler == nil || enabled.Prometheus == nil {
		t.Fatalf("enabled telemetry composition = %+v, %v", enabled, err)
	}
	if buildTraceStore(nil, nil) == nil || buildContextApplication(nil) == nil || buildRememberAttemptDiagnostics(nil) == nil {
		t.Fatal("maintenance composition returned nil service")
	}
	if _, _, err := buildSearchRepositoryApplication(context.Background(), config.Config{}, &postgres.DB{}, nil, nil); err == nil {
		t.Fatal("missing knowledge owner was accepted")
	}
	knowledge := knowledgepostgres.NewStore(nil, nil, knowledgecontract.ConflictRuntimeConfig{})
	searchStore, _, err := buildSearchRepositoryApplication(context.Background(), config.Config{AIEmbeddingModel: "model", AIEmbeddingDimensions: 2}, &postgres.DB{}, nil, knowledge)
	if err == nil || searchStore != nil || !strings.Contains(err.Error(), "database") {
		t.Fatalf("invalid search database result = %v, store=%v", err, searchStore)
	}
	if buildSemanticWriteCorrectionExecutor(nil) == nil {
		t.Fatal("semantic write executor was nil")
	}
	if got := controlListenerClose(nil); got != nil {
		t.Fatalf("nil control listener close = %v", got)
	}
	if got := telemetryListenerClose(nil); got != nil {
		t.Fatalf("nil telemetry listener close = %v", got)
	}
	if !isExpectedServerClose(nil) || !isExpectedServerClose(http.ErrServerClosed) || isExpectedServerClose(errors.New("unexpected")) {
		t.Fatal("server close classification was incorrect")
	}
}

func TestSearchReconciliationRunnerHandlesSuccessFailureAndCancellation(t *testing.T) {
	for name, stub := range map[string]*searchReconciliationCoverageStub{
		"success": {result: searchapp.SearchReconciliationResult{Skipped: true}, called: make(chan struct{}, 1)},
		"failure": {result: searchapp.SearchReconciliationResult{ErrorCode: "failed"}, err: errors.New("repair failed"), called: make(chan struct{}, 1)},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go startSearchReconciliation(ctx, stub, observability.New(0))
			select {
			case <-stub.called:
				cancel()
			case <-time.After(time.Second):
				t.Fatal("reconciliation runner was not called")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	startSearchReconciliation(ctx, nil, nil)
}

func TestRedisBackendFailureIsBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := buildRedisBackend(ctx, config.Config{RedisAddr: "127.0.0.1:1"}); err == nil {
		t.Fatal("unreachable Redis backend was accepted")
	}
}

func TestRunFromEnvironmentStopsAtLogLevelAndDatabaseBoundaries(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@127.0.0.1:1/db?sslmode=disable")
	t.Setenv("CONTROL_PORTAL_TOKEN", "control-secret")
	t.Setenv("LOG_LEVEL", "not-a-level")
	if err := RunFromEnvironment(context.Background(), RuntimeOptions{ValidateStartup: func(*config.Config) error { return nil }}); err == nil || !strings.Contains(err.Error(), "parse log level") {
		t.Fatalf("invalid log level error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	t.Setenv("LOG_LEVEL", "info")
	if err := RunFromEnvironment(ctx, RuntimeOptions{ValidateStartup: func(*config.Config) error { return nil }}); err == nil || !strings.Contains(err.Error(), "connect to postgres") {
		t.Fatalf("database connection error = %v", err)
	}
}

func TestRunMigrationControlRetirementStopsAtLogLevelAndDatabaseBoundaries(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@127.0.0.1:1/db?sslmode=disable")
	t.Setenv("LOG_LEVEL", "not-a-level")
	if err := RunMigrationControlRetirement(context.Background()); err == nil || !strings.Contains(err.Error(), "parse log level") {
		t.Fatalf("invalid log level error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	t.Setenv("LOG_LEVEL", "info")
	if err := RunMigrationControlRetirement(ctx); err == nil || !strings.Contains(err.Error(), "connect to postgres") {
		t.Fatalf("database connection error = %v", err)
	}
}

func TestRunActiveServerBootGuardsWithRealGORMWrapper(t *testing.T) {
	pgDB, mock, cleanup := coveragePostgresDB(t)
	defer cleanup()
	cfg := config.Config{
		AIAPIURL:                "http://embedding.example",
		AIAPIKey:                "embedding-key",
		AIEmbeddingModel:        "embedding-model",
		AIEmbeddingDimensions:   2,
		AIVerifierAPIURL:        "http://verifier.example",
		AIVerifierAPIKey:        "verifier-key",
		AIVerifierModel:         "verifier-model",
		ControlPortalToken:      "control-token",
		SSEMaxConcurrentStreams: 1,
	}
	logger := observability.New(0)
	if err := RunActiveServer(context.Background(), context.Background(), cfg, pgDB, logger, 0, authorityBootstrap{}, RuntimeOptions{}); err == nil || !strings.Contains(err.Error(), "active boot blocked") {
		t.Fatalf("inactive authority boot error = %v", err)
	}
	authority := authorityBootstrap{Mode: authorityActive, ReadinessMessage: "ready", Marker: &domain.CompatibilityMarker{MarkerKind: domain.MigrationMarkerKindCutover, Version: cutoverMarkerVersion, Status: domain.MigrationMarkerCompatible}}
	if err := RunActiveServer(context.Background(), context.Background(), cfg, pgDB, logger, 0, authority, RuntimeOptions{ /* RLS is supplied through the composition boundary below. */ }); err == nil || !strings.Contains(err.Error(), "active search bootstrap blocked") {
		t.Fatalf("search bootstrap error = %v", err)
	}
	_ = mock
}

func coveragePostgresDB(t *testing.T) (*postgres.DB, sqlmock.Sqlmock, func()) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectPing().WillReturnError(nil)
	gdb, err := gorm.Open(postgresdriver.New(postgresdriver.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{})
	if err != nil {
		sqlDB.Close()
		t.Fatal(err)
	}
	wrapped := &postgres.DB{}
	setUnexported := func(name string, value any) {
		field := reflect.ValueOf(wrapped).Elem().FieldByName(name)
		reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
	}
	setUnexported("db", gdb)
	setUnexported("sqlDB", sqlDB)
	return wrapped, mock, func() { _ = sqlDB.Close() }
}
