package config

import (
	"os"
	"strings"
	"testing"
)

// clearEnv clears all config-related environment variables
func clearEnv() {
	envVars := []string{
		"POSTGRES_DSN",
		"POSTGRES_HOST",
		"POSTGRES_PORT",
		"POSTGRES_USER",
		"POSTGRES_PASSWORD",
		"POSTGRES_DB",
		"POSTGRES_SSLMODE",
		"POSTGRES_READ_DSN",
		"POSTGRES_MAX_OPEN_CONNS",
		"POSTGRES_MAX_IDLE_CONNS",
		"POSTGRES_CONN_MAX_LIFETIME_SECONDS",
		"POSTGRES_MIGRATION_TIMEOUT_SECONDS",
		"REDIS_ADDR",
		"REDIS_PASSWORD",
		"REDIS_DB",
		"REDIS_TLS_ENABLED",
		"DISTRIBUTED_COORDINATION_REQUIRED",
		"HTTP_MAX_BODY_BYTES",
		"AUTH_VERIFY_MAX_CONCURRENCY",
		"RATE_LIMIT_PER_MINUTE",
		"SSE_HEARTBEAT_SECONDS",
		"SSE_MAX_DURATION_SECONDS",
		"SSE_MAX_CONCURRENT_STREAMS",
		"AI_API_URL",
		"AI_API_KEY",
		"AI_API_EMBEDDING_MODEL",
		"AI_API_EMBEDDING_DIMENSIONS",
		"AI_API_EMBEDDING_TIMEOUT_SECONDS",
		"AI_API_EMBEDDING_MAX_CONCURRENCY",
		// Knowledge-pipeline knobs
		"AI_VERIFIER_API_URL",
		"AI_VERIFIER_API_KEY",
		"AI_VERIFIER_MODEL",
		"AI_VERIFIER_DISABLE_TEMPERATURE",
		"AI_VERIFIER_TIMEOUT_SECONDS",
		"AI_VERIFIER_MAX_CONCURRENCY",
		"AI_VERIFIER_MAX_INPUT_TOKENS",
		"AI_VERIFIER_MAX_OUTPUT_TOKENS",
		"AI_VERIFIER_MAX_CANDIDATE_CONTEXT_TOKENS",
		"AI_VERIFIER_TOKENIZER",
		"MEMORY_AUTO_WRITE_CONFIDENCE_THRESHOLD",
		"AI_VERIFIER_MAX_INPUT_BYTES",
		"AI_VERIFIER_MAX_OUTPUT_BYTES",
		"AI_VERIFIER_MAX_CANDIDATE_CONTEXT_BYTES",
		"AI_ASSESSOR_MODEL",
		"AI_ASSESSOR_MAX_INPUT_TOKENS",
		"CONTROL_HTTP_ADDR",
		"CONTROL_PORTAL_TOKEN",
		"MCP_TRANSPORT",
		"TELEMETRY_ENABLED",
		"TELEMETRY_PROMETHEUS_URL",
		"TELEMETRY_PROMETHEUS_JOB",
		"TELEMETRY_QUERY_TIMEOUT_SECONDS",
		"TELEMETRY_SCRAPE_TOKEN",
		"APP_TIMEZONE",
		"CONFLICT_REVIEW_TTL_DAYS",
		"CONFLICT_REVIEW_START_TIME_LOCAL",
		"CONFLICT_REVIEW_MAX_CONCURRENCY",
		"CONFLICT_REVIEW_BATCH_SIZE",
		"CONFLICT_REVIEW_LEASE_SECONDS",
		"CONFLICT_REVIEW_MAX_ATTEMPTS",
		"CONFLICT_REVIEW_JITTER_SECONDS",
		"SSO_PUBLIC_BASE_URL",
		"SSO_ENTITLEMENT_CACHE_TTL_SECONDS",
		"SSO_SESSION_TTL_SECONDS",
		"SSO_STATE_TTL_SECONDS",
		"SSO_HTTP_TIMEOUT_SECONDS",
		"SSO_COOKIE_SECURE",
	}
	for _, v := range envVars {
		os.Unsetenv(v)
	}
}

// setRequiredEnv sets the minimum required environment variables for a valid config
func setRequiredEnv() {
	os.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost/db?sslmode=disable")
	os.Setenv("CONTROL_PORTAL_TOKEN", "control-secret")
}

func setRequiredEmbeddingEnv() {
	os.Setenv("AI_API_URL", "https://example.com/v1")
	os.Setenv("AI_API_KEY", "sk-test")
	os.Setenv("AI_API_EMBEDDING_MODEL", "text-embedding-3-small")
	os.Setenv("AI_API_EMBEDDING_DIMENSIONS", "1536")
}

func setRequiredModelEnv() {
	os.Setenv("AI_VERIFIER_MODEL", "verifier-model")
}

func TestLoadDefaults(t *testing.T) {
	clearEnv()
	setRequiredEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	// Test listener defaults
	if DefaultHTTPPort != "8080" {
		t.Errorf("DefaultHTTPPort = %q, want %q", DefaultHTTPPort, "8080")
	}
	if DefaultHTTPAddr != ":8080" {
		t.Errorf("DefaultHTTPAddr = %q, want %q", DefaultHTTPAddr, ":8080")
	}

	// Test integer defaults
	if cfg.RateLimitPerMinute != 100 {
		t.Errorf("RateLimitPerMinute default = %d, want %d", cfg.RateLimitPerMinute, 100)
	}
	if cfg.HTTPMaxBodyBytes != 1048576 {
		t.Errorf("HTTPMaxBodyBytes default = %d, want %d", cfg.HTTPMaxBodyBytes, 1048576)
	}
	if cfg.AuthVerifyMaxConcurrency != 8 {
		t.Errorf("AuthVerifyMaxConcurrency default = %d, want %d", cfg.AuthVerifyMaxConcurrency, 8)
	}
	if cfg.SSEHeartbeatSeconds != 30 {
		t.Errorf("SSEHeartbeatSeconds default = %d, want %d", cfg.SSEHeartbeatSeconds, 30)
	}
	if cfg.SSEMaxDurationSeconds != 300 {
		t.Errorf("SSEMaxDurationSeconds default = %d, want %d", cfg.SSEMaxDurationSeconds, 300)
	}
	if cfg.SSEMaxConcurrentStreams != 10 {
		t.Errorf("SSEMaxConcurrentStreams default = %d, want %d", cfg.SSEMaxConcurrentStreams, 10)
	}
	if cfg.EmbeddingDimensions != 3072 {
		t.Errorf("EmbeddingDimensions default = %d, want %d", cfg.EmbeddingDimensions, 3072)
	}
	if cfg.GetAIEmbeddingMaxConcurrency() != DefaultAIEmbeddingMaxConcurrency {
		t.Errorf("AIEmbeddingMaxConcurrency default = %d, want %d", cfg.GetAIEmbeddingMaxConcurrency(), DefaultAIEmbeddingMaxConcurrency)
	}
	budget := AIVerifierAssessmentBudgetFor(&cfg)
	if budget.MaxInputTokens != DefaultAIVerifierMaxInputTokens ||
		budget.MaxOutputTokens != DefaultAIVerifierMaxOutputTokens ||
		budget.MaxCandidateContextTokens != DefaultAIVerifierMaxCandidateContextTokens ||
		budget.MaxPredicateOptions != DefaultAIVerifierMaxPredicateOptions ||
		budget.Tokenizer != DefaultAIVerifierTokenizer {
		t.Fatalf("default assessor budget = %#v", budget)
	}
	if cfg.GetAppTimezone() != "Local" {
		t.Errorf("AppTimezone default = %q, want Local", cfg.GetAppTimezone())
	}
	if cfg.GetConflictReviewTTLDays() != DefaultConflictReviewTTLDays {
		t.Errorf("ConflictReviewTTLDays default = %d, want %d", cfg.GetConflictReviewTTLDays(), DefaultConflictReviewTTLDays)
	}
	if cfg.GetConflictReviewStartTimeLocal() != DefaultConflictReviewStartTime {
		t.Errorf("ConflictReviewStartTimeLocal default = %q, want %q", cfg.GetConflictReviewStartTimeLocal(), DefaultConflictReviewStartTime)
	}
	if cfg.GetConflictReviewMaxConcurrency() != 1 {
		t.Errorf("ConflictReviewMaxConcurrency default = %d, want 1", cfg.GetConflictReviewMaxConcurrency())
	}
	if cfg.GetConflictReviewBatchSize() != 100 {
		t.Errorf("ConflictReviewBatchSize default = %d, want 100", cfg.GetConflictReviewBatchSize())
	}
	if cfg.GetConflictReviewLeaseSeconds() != 300 {
		t.Errorf("ConflictReviewLeaseSeconds default = %d, want 300", cfg.GetConflictReviewLeaseSeconds())
	}
	if cfg.GetConflictReviewMaxAttempts() != 5 {
		t.Errorf("ConflictReviewMaxAttempts default = %d, want 5", cfg.GetConflictReviewMaxAttempts())
	}
	if cfg.GetConflictReviewJitterSeconds() != 600 {
		t.Errorf("ConflictReviewJitterSeconds default = %d, want 600", cfg.GetConflictReviewJitterSeconds())
	}

	// Test other defaults
	if cfg.RedisDB != 0 {
		t.Errorf("RedisDB default = %d, want %d", cfg.RedisDB, 0)
	}
	if cfg.ControlHTTPAddr != ":8090" {
		t.Errorf("ControlHTTPAddr default = %q, want %q", cfg.ControlHTTPAddr, ":8090")
	}
	if cfg.TelemetryPrometheusJob != "" {
		t.Errorf("TelemetryPrometheusJob default = %q, want empty", cfg.TelemetryPrometheusJob)
	}
}

func TestLoadRejectsRemovedMCPTransportsAndAcceptsSDKNoop(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	t.Setenv("MCP_TRANSPORT", "sdk")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() with sdk MCP compatibility value: %v", err)
	}
	t.Setenv("MCP_TRANSPORT", "legacy")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "only supported MCP transport") {
		t.Fatalf("Load() legacy error = %v, want removed transport guidance", err)
	}
	t.Setenv("MCP_TRANSPORT", "unsupported")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "MCP_TRANSPORT") {
		t.Fatalf("Load() error = %v, want MCP_TRANSPORT validation", err)
	}
}

func TestLoadIgnoresRetiredWorkerEnvironment(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	t.Setenv("EMBEDDING_WORKER_COUNT", "not-a-number")
	t.Setenv("EMBEDDING_BATCH_SIZE", "not-a-number")
	t.Setenv("EMBEDDING_JOB_POLL_SECONDS", "not-a-number")
	t.Setenv("MEMORY_PLACEMENT_WORKER_COUNT", "not-a-number")
	t.Setenv("MEMORY_PLACEMENT_POLL_SECONDS", "not-a-number")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() rejected retired worker settings: %v", err)
	}
}

func TestLoadAllowsPostgresOnlyConfig(t *testing.T) {
	clearEnv()
	os.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost/db?sslmode=disable")
	os.Setenv("CONTROL_PORTAL_TOKEN", "control-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error with Postgres-only config: %v", err)
	}
	if cfg.PostgresDSN == "" {
		t.Fatal("PostgresDSN was not loaded")
	}
}

func TestLoadIgnoresRetiredDatabaseAndWorkerEnvironment(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("NEO4J_URI", "bolt://neo4j:7687")
	os.Setenv("EMBEDDING_JOB_MAX_ATTEMPTS", "not-a-number")
	os.Setenv("PROMOTE_TX_TIMEOUT_SECONDS", "not-a-number")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() rejected retired environment: %v", err)
	}
}

func TestLoadTelemetryConfig(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("TELEMETRY_ENABLED", "true")
	os.Setenv("TELEMETRY_PROMETHEUS_URL", "http://prometheus:9090")
	os.Setenv("TELEMETRY_PROMETHEUS_JOB", " dense-mem-demo ")
	os.Setenv("TELEMETRY_QUERY_TIMEOUT_SECONDS", "12")
	os.Setenv("TELEMETRY_SCRAPE_TOKEN", "scrape-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if !cfg.GetTelemetryEnabled() {
		t.Fatal("GetTelemetryEnabled() = false, want true")
	}
	if got := cfg.GetTelemetryPrometheusURL(); got != "http://prometheus:9090" {
		t.Errorf("GetTelemetryPrometheusURL() = %q", got)
	}
	if got := cfg.GetTelemetryPrometheusJob(); got != "dense-mem-demo" {
		t.Errorf("GetTelemetryPrometheusJob() = %q", got)
	}
	if got := cfg.GetTelemetryQueryTimeoutSeconds(); got != 12 {
		t.Errorf("GetTelemetryQueryTimeoutSeconds() = %d, want 12", got)
	}
	if got := cfg.GetTelemetryScrapeToken(); got != "scrape-secret" {
		t.Errorf("GetTelemetryScrapeToken() = %q", got)
	}

	cfg.TelemetryQueryTimeoutSeconds = 0
	if got := cfg.GetTelemetryQueryTimeoutSeconds(); got != 5 {
		t.Errorf("GetTelemetryQueryTimeoutSeconds() fallback = %d, want 5", got)
	}
}

func TestLoadTelemetryConfigRequiresScrapeToken(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("TELEMETRY_ENABLED", "true")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for missing telemetry scrape token, got nil")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "TELEMETRY_SCRAPE_TOKEN" {
		t.Errorf("ValidationError.Field = %q, want TELEMETRY_SCRAPE_TOKEN", validationErr.Field)
	}
}

func TestLoadValidation_MissingPostgresDSN(t *testing.T) {
	clearEnv()

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for missing POSTGRES_DSN, got nil")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "POSTGRES_DSN" {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, "POSTGRES_DSN")
	}
}

func TestLoadWithPostgresDSNUsesOperatorResolvedDSN(t *testing.T) {
	clearEnv()
	t.Cleanup(clearEnv)

	const resolvedDSN = "postgres://operator:resolved@localhost:5432/dense_mem?sslmode=disable"
	cfg, err := LoadWithPostgresDSN(resolvedDSN)
	if err != nil {
		t.Fatalf("LoadWithPostgresDSN returned error: %v", err)
	}
	if cfg.PostgresDSN != resolvedDSN {
		t.Fatalf("PostgresDSN = %q, want resolved operator DSN", cfg.PostgresDSN)
	}
}

func TestLoadBuildsEscapedPostgresDSNFromComponents(t *testing.T) {
	clearEnv()
	t.Cleanup(clearEnv)
	os.Setenv("POSTGRES_HOST", "2001:db8::10")
	os.Setenv("POSTGRES_PORT", "5432")
	os.Setenv("POSTGRES_USER", "user:name")
	os.Setenv("POSTGRES_PASSWORD", "p@ss word/?")
	os.Setenv("POSTGRES_DB", "dense mem")
	os.Setenv("POSTGRES_SSLMODE", "disable")
	os.Setenv("CONTROL_PORTAL_TOKEN", "control-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	want := "postgres://user%3Aname:p%40ss%20word%2F%3F@[2001:db8::10]:5432/dense%20mem?sslmode=disable"
	if cfg.PostgresDSN != want {
		t.Fatalf("PostgresDSN = %q, want %q", cfg.PostgresDSN, want)
	}
}

func TestLoadValidation_InvalidInteger(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("RATE_LIMIT_PER_MINUTE", "not-a-number")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for invalid integer, got nil")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "RATE_LIMIT_PER_MINUTE" {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, "RATE_LIMIT_PER_MINUTE")
	}
}

func TestLoadValidation_ZeroOrNegativeInteger(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("RATE_LIMIT_PER_MINUTE", "0")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for zero value, got nil")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "RATE_LIMIT_PER_MINUTE" {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, "RATE_LIMIT_PER_MINUTE")
	}
}

func TestLoadValidation_NegativeInteger(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("RATE_LIMIT_PER_MINUTE", "-5")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for negative value, got nil")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "RATE_LIMIT_PER_MINUTE" {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, "RATE_LIMIT_PER_MINUTE")
	}
}

func TestLoadOverrides(t *testing.T) {
	clearEnv()
	setRequiredEnv()

	// Override all values
	os.Setenv("REDIS_PASSWORD", "redispass")
	os.Setenv("REDIS_DB", "5")
	os.Setenv("HTTP_MAX_BODY_BYTES", "2097152")
	os.Setenv("AUTH_VERIFY_MAX_CONCURRENCY", "12")
	os.Setenv("RATE_LIMIT_PER_MINUTE", "200")
	os.Setenv("SSE_HEARTBEAT_SECONDS", "60")
	os.Setenv("SSE_MAX_DURATION_SECONDS", "600")
	os.Setenv("SSE_MAX_CONCURRENT_STREAMS", "20")
	os.Setenv("CONTROL_HTTP_ADDR", "localhost:9091")
	os.Setenv("CONTROL_PORTAL_TOKEN", "control-secret")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if cfg.RedisPassword != "redispass" {
		t.Errorf("RedisPassword = %q, want %q", cfg.RedisPassword, "redispass")
	}

	// Integer overrides
	if cfg.RedisDB != 5 {
		t.Errorf("RedisDB = %d, want %d", cfg.RedisDB, 5)
	}
	if cfg.HTTPMaxBodyBytes != 2097152 {
		t.Errorf("HTTPMaxBodyBytes = %d, want %d", cfg.HTTPMaxBodyBytes, 2097152)
	}
	if cfg.AuthVerifyMaxConcurrency != 12 {
		t.Errorf("AuthVerifyMaxConcurrency = %d, want %d", cfg.AuthVerifyMaxConcurrency, 12)
	}
	if cfg.RateLimitPerMinute != 200 {
		t.Errorf("RateLimitPerMinute = %d, want %d", cfg.RateLimitPerMinute, 200)
	}
	if cfg.SSEHeartbeatSeconds != 60 {
		t.Errorf("SSEHeartbeatSeconds = %d, want %d", cfg.SSEHeartbeatSeconds, 60)
	}
	if cfg.SSEMaxDurationSeconds != 600 {
		t.Errorf("SSEMaxDurationSeconds = %d, want %d", cfg.SSEMaxDurationSeconds, 600)
	}
	if cfg.SSEMaxConcurrentStreams != 20 {
		t.Errorf("SSEMaxConcurrentStreams = %d, want %d", cfg.SSEMaxConcurrentStreams, 20)
	}
	if cfg.ControlHTTPAddr != "localhost:9091" {
		t.Errorf("ControlHTTPAddr = %q, want %q", cfg.ControlHTTPAddr, "localhost:9091")
	}
	if cfg.ControlPortalToken != "control-secret" {
		t.Errorf("ControlPortalToken = %q, want control-secret", cfg.ControlPortalToken)
	}
}

func TestConfigProviderInterface(t *testing.T) {
	clearEnv()
	setRequiredEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	// Verify Config implements ConfigProvider
	var provider ConfigProvider = &cfg

	// Test all getter methods
	_ = provider.GetPostgresDSN()
	_ = provider.GetRedisAddr()
	_ = provider.GetRedisPassword()
	_ = provider.GetRedisDB()
	_ = provider.GetHTTPMaxBodyBytes()
	_ = provider.GetRateLimitPerMinute()
	_ = provider.GetSSEHeartbeatSeconds()
	_ = provider.GetSSEMaxDurationSeconds()
	_ = provider.GetEmbeddingDimensions()
	_ = provider.GetAIAPIURL()
	_ = provider.GetAIAPIKey()
	_ = provider.GetAIEmbeddingModel()
	_ = provider.GetAIEmbeddingDimensions()
	_ = provider.GetAIEmbeddingTimeoutSeconds()
	_ = provider.IsEmbeddingConfigured()
	_ = provider.GetAIVerifierAPIURL()
	_ = provider.GetAIVerifierAPIKey()
	_ = provider.GetAIVerifierModel()
	_ = provider.GetAIVerifierTimeoutSeconds()
	_ = provider.GetAIVerifierMaxConcurrency()
	_ = provider.GetControlHTTPAddr()
	_ = provider.GetControlPortalToken()
}

func TestConfigGetterFallbacksAndParsers(t *testing.T) {
	cfg := &Config{
		HTTPMaxBodyBytes:         2048,
		AIAPIURL:                 "https://shared.example/v1",
		AIAPIKey:                 "shared-key",
		AIVerifierModel:          "verifier-model",
		AIVerifierTimeoutSeconds: 0,
	}

	if got := cfg.GetHTTPMaxBodyBytes(); got != 2048 {
		t.Fatalf("GetHTTPMaxBodyBytes() = %d, want 2048", got)
	}
	if got := cfg.GetAIVerifierTimeoutSeconds(); got != 60 {
		t.Fatalf("GetAIVerifierTimeoutSeconds() fallback = %d, want 60", got)
	}

	clearEnv()
	if got, err := parseBoolOrDefault("FEATURE_FLAG", true); err != nil || !got {
		t.Fatalf("parseBoolOrDefault default = %v, %v; want true, nil", got, err)
	}
	t.Setenv("FEATURE_FLAG", "false")
	if got, err := parseBoolOrDefault("FEATURE_FLAG", true); err != nil || got {
		t.Fatalf("parseBoolOrDefault false = %v, %v; want false, nil", got, err)
	}
	t.Setenv("FEATURE_FLAG", "not-bool")
	if _, err := parseBoolOrDefault("FEATURE_FLAG", true); err == nil {
		t.Fatal("parseBoolOrDefault invalid value: want error")
	}
}

func TestValidationError_Error(t *testing.T) {
	err := &ValidationError{
		Field:   "TEST_FIELD",
		Message: "test message",
	}

	expected := "config validation error for TEST_FIELD: test message"
	if err.Error() != expected {
		t.Errorf("ValidationError.Error() = %q, want %q", err.Error(), expected)
	}
}

func TestLoad_WithoutRedis_Succeeds(t *testing.T) {
	clearEnv()
	os.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost/db?sslmode=disable")
	os.Setenv("CONTROL_PORTAL_TOKEN", "control-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.RedisAddr != "" {
		t.Errorf("RedisAddr = %q, want %q", cfg.RedisAddr, "")
	}
}

func TestLoad_WithRedis_Succeeds(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("REDIS_ADDR", "localhost:6379")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.RedisAddr != "localhost:6379" {
		t.Errorf("RedisAddr = %q, want %q", cfg.RedisAddr, "localhost:6379")
	}
}

func TestLoad_EmbeddingConfig_AllOrNothing(t *testing.T) {
	clearEnv()
	os.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost/db?sslmode=disable")
	os.Setenv("CONTROL_PORTAL_TOKEN", "control-secret")
	os.Setenv("AI_API_URL", "https://example.com/v1")
	// Missing AI_API_KEY intentionally
	os.Setenv("AI_API_EMBEDDING_MODEL", "text-embedding-3-small")
	os.Setenv("AI_API_EMBEDDING_DIMENSIONS", "1536")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for partial embedding config, got nil")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "AI_API_KEY" {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, "AI_API_KEY")
	}
}

func TestLoad_EmbeddingConfig_Complete(t *testing.T) {
	clearEnv()
	os.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost/db?sslmode=disable")
	os.Setenv("CONTROL_PORTAL_TOKEN", "control-secret")
	setRequiredEmbeddingEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if !cfg.IsEmbeddingConfigured() {
		t.Error("IsEmbeddingConfigured() = false, want true")
	}
	if cfg.GetAIEmbeddingDimensions() != 1536 {
		t.Errorf("GetAIEmbeddingDimensions() = %d, want %d", cfg.GetAIEmbeddingDimensions(), 1536)
	}
	if cfg.GetEmbeddingDimensions() != 1536 {
		t.Errorf("GetEmbeddingDimensions() = %d, want %d", cfg.GetEmbeddingDimensions(), 1536)
	}
	if cfg.GetAIEmbeddingTimeoutSeconds() != 30 {
		t.Errorf("GetAIEmbeddingTimeoutSeconds() = %d, want %d", cfg.GetAIEmbeddingTimeoutSeconds(), 30)
	}
}

func TestLoadEmbeddingDimensions_DefaultsToAIEmbeddingDimensions(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("AI_API_URL", "https://example.com/v1")
	os.Setenv("AI_API_KEY", "sk-test")
	os.Setenv("AI_API_EMBEDDING_MODEL", "text-embedding-nomic-embed-text-v2-moe")
	os.Setenv("AI_API_EMBEDDING_DIMENSIONS", "1024")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if got := cfg.GetEmbeddingDimensions(); got != 1024 {
		t.Errorf("GetEmbeddingDimensions() = %d, want 1024", got)
	}
}

func TestLoadVerifierConfig_DefaultsToSharedAIConfig(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	setRequiredEmbeddingEnv()
	setRequiredModelEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if got := cfg.GetAIVerifierAPIURL(); got != "https://example.com/v1" {
		t.Errorf("GetAIVerifierAPIURL() = %q, want %q", got, "https://example.com/v1")
	}
	if got := cfg.GetAIVerifierAPIKey(); got != "sk-test" {
		t.Errorf("GetAIVerifierAPIKey() = %q, want %q", got, "sk-test")
	}
	if got := cfg.GetAIVerifierTimeoutSeconds(); got != 60 {
		t.Errorf("GetAIVerifierTimeoutSeconds() = %d, want %d", got, 60)
	}
}

func TestLoadVerifierConfig_SeparateEndpoint(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	setRequiredEmbeddingEnv()
	os.Setenv("AI_VERIFIER_API_URL", "https://verifier.example.com/v1")
	os.Setenv("AI_VERIFIER_API_KEY", "verifier-key")
	os.Setenv("AI_VERIFIER_MODEL", "local-verifier")
	os.Setenv("AI_VERIFIER_TIMEOUT_SECONDS", "45")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if got := cfg.GetAIAPIURL(); got != "https://example.com/v1" {
		t.Errorf("GetAIAPIURL() = %q, want %q", got, "https://example.com/v1")
	}
	if got := cfg.GetAIVerifierAPIURL(); got != "https://verifier.example.com/v1" {
		t.Errorf("GetAIVerifierAPIURL() = %q, want %q", got, "https://verifier.example.com/v1")
	}
	if got := cfg.GetAIVerifierAPIKey(); got != "verifier-key" {
		t.Errorf("GetAIVerifierAPIKey() = %q, want %q", got, "verifier-key")
	}
	if got := cfg.GetAIVerifierModel(); got != "local-verifier" {
		t.Errorf("GetAIVerifierModel() = %q, want %q", got, "local-verifier")
	}
	if cfg.GetAIVerifierDisableTemperature() {
		t.Error("GetAIVerifierDisableTemperature() = true, want false")
	}
	if got := cfg.GetAIVerifierTimeoutSeconds(); got != 45 {
		t.Errorf("GetAIVerifierTimeoutSeconds() = %d, want %d", got, 45)
	}
}

func TestLoadVerifierConfig_DisableTemperature(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	setRequiredEmbeddingEnv()
	setRequiredModelEnv()
	os.Setenv("AI_VERIFIER_DISABLE_TEMPERATURE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if !cfg.GetAIVerifierDisableTemperature() {
		t.Fatal("GetAIVerifierDisableTemperature() = false, want true")
	}
	if !AIVerifierTemperatureDisabled(&cfg) {
		t.Fatal("AIVerifierTemperatureDisabled() = false, want true")
	}
}

func TestLoadVerifierConfig_SeparateURLRequiresSeparateKey(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	setRequiredEmbeddingEnv()
	os.Setenv("AI_VERIFIER_API_URL", "https://verifier.example.com/v1")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for verifier URL without verifier key, got nil")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "AI_VERIFIER_API_KEY" {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, "AI_VERIFIER_API_KEY")
	}
}

func TestValidateServerStartup_RequiresEmbeddingConfig(t *testing.T) {
	clearEnv()
	setRequiredEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	err = cfg.ValidateServerStartup()
	if err == nil {
		t.Fatal("ValidateServerStartup() expected error for missing embedding config, got nil")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "AI_API_URL" {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, "AI_API_URL")
	}
}

func TestValidateServerStartup_SucceedsWithEmbeddingConfig(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	setRequiredEmbeddingEnv()
	setRequiredModelEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if err := cfg.ValidateServerStartup(); err != nil {
		t.Fatalf("ValidateServerStartup() returned unexpected error: %v", err)
	}
}

func TestValidateServerStartupRejectsAssessorBudgetWithoutRepairRoom(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	setRequiredEmbeddingEnv()
	setRequiredModelEnv()
	t.Setenv("AI_VERIFIER_MAX_INPUT_TOKENS", "50000")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	err = cfg.ValidateServerStartup()
	validationErr, ok := err.(*ValidationError)
	if !ok || validationErr.Field != "AI_VERIFIER_MAX_INPUT_TOKENS" {
		t.Fatalf("ValidateServerStartup() error = %v, want assessor input budget validation error", err)
	}
}

func TestValidateServerStartup_RequiresVerifierModel(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	setRequiredEmbeddingEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if err := cfg.ValidateServerStartup(); err == nil {
		t.Fatal("ValidateServerStartup() expected missing verifier model error, got nil")
	} else if validationErr, ok := err.(*ValidationError); !ok || validationErr.Field != "AI_VERIFIER_MODEL" {
		t.Fatalf("ValidateServerStartup() error = %v, want AI_VERIFIER_MODEL validation error", err)
	}

	os.Setenv("AI_VERIFIER_MODEL", "verifier-model")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() with verifier model returned unexpected error: %v", err)
	}
	if err := cfg.ValidateServerStartup(); err != nil {
		t.Fatalf("ValidateServerStartup() with verifier model returned unexpected error: %v", err)
	}
}

// TestLoadKnowledgeConfigDefaults verifies that all knowledge-pipeline knobs
// have their expected default values when no environment variables are set (AC-X3).
func TestLoadKnowledgeConfigDefaults(t *testing.T) {
	clearEnv()
	setRequiredEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if got := cfg.GetAIVerifierModel(); got != "" {
		t.Errorf("GetAIVerifierModel() = %q, want empty", got)
	}
	if cfg.GetAIVerifierDisableTemperature() {
		t.Error("GetAIVerifierDisableTemperature() = true, want false")
	}
	if got := cfg.GetAIVerifierMaxConcurrency(); got != 5 {
		t.Errorf("GetAIVerifierMaxConcurrency() = %d, want %d", got, 5)
	}
}

func TestLoadControlPortalValidation(t *testing.T) {
	t.Run("server startup requires token", func(t *testing.T) {
		clearEnv()
		setRequiredEnv()
		setRequiredEmbeddingEnv()
		setRequiredModelEnv()
		os.Unsetenv("CONTROL_PORTAL_TOKEN")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() returned unexpected error: %v", err)
		}

		err = cfg.ValidateServerStartup()
		if err == nil {
			t.Fatal("ValidateServerStartup() expected error for missing control token, got nil")
		}
		validationErr, ok := err.(*ValidationError)
		if !ok {
			t.Fatalf("expected *ValidationError, got %T", err)
		}
		if validationErr.Field != "CONTROL_PORTAL_TOKEN" {
			t.Errorf("ValidationError.Field = %q, want CONTROL_PORTAL_TOKEN", validationErr.Field)
		}
	})

	t.Run("allows explicit network bind", func(t *testing.T) {
		clearEnv()
		setRequiredEnv()
		os.Setenv("CONTROL_HTTP_ADDR", "0.0.0.0:8090")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() returned unexpected error: %v", err)
		}
		if cfg.ControlHTTPAddr != "0.0.0.0:8090" {
			t.Errorf("ControlHTTPAddr = %q, want 0.0.0.0:8090", cfg.ControlHTTPAddr)
		}
	})
}

func TestConflictReviewGettersUseDefaultsAndConfiguredValues(t *testing.T) {
	var defaults Config
	if got := defaults.GetAppTimezone(); got != "Local" {
		t.Fatalf("GetAppTimezone default = %q, want Local", got)
	}
	if got := defaults.GetConflictReviewTTLDays(); got != DefaultConflictReviewTTLDays {
		t.Fatalf("GetConflictReviewTTLDays default = %d, want %d", got, DefaultConflictReviewTTLDays)
	}
	if got := defaults.GetConflictReviewStartTimeLocal(); got != DefaultConflictReviewStartTime {
		t.Fatalf("GetConflictReviewStartTimeLocal default = %q, want %q", got, DefaultConflictReviewStartTime)
	}
	if got := defaults.GetConflictReviewMaxConcurrency(); got != 1 {
		t.Fatalf("GetConflictReviewMaxConcurrency default = %d, want 1", got)
	}
	if got := defaults.GetConflictReviewBatchSize(); got != 100 {
		t.Fatalf("GetConflictReviewBatchSize default = %d, want 100", got)
	}
	if got := defaults.GetConflictReviewLeaseSeconds(); got != 300 {
		t.Fatalf("GetConflictReviewLeaseSeconds default = %d, want 300", got)
	}
	if got := defaults.GetConflictReviewMaxAttempts(); got != 5 {
		t.Fatalf("GetConflictReviewMaxAttempts default = %d, want 5", got)
	}
	negativeJitter := Config{ConflictReviewJitterSeconds: -1}
	if got := negativeJitter.GetConflictReviewJitterSeconds(); got != 0 {
		t.Fatalf("GetConflictReviewJitterSeconds negative = %d, want 0", got)
	}

	configured := Config{
		AppTimezone:                  "UTC",
		ConflictReviewTTLDays:        3,
		ConflictReviewStartTimeLocal: "05:30",
		ConflictReviewMaxConcurrency: 2,
		ConflictReviewBatchSize:      25,
		ConflictReviewLeaseSeconds:   120,
		ConflictReviewMaxAttempts:    4,
		ConflictReviewJitterSeconds:  90,
	}
	if configured.GetAppTimezone() != "UTC" ||
		configured.GetConflictReviewTTLDays() != 3 ||
		configured.GetConflictReviewStartTimeLocal() != "05:30" ||
		configured.GetConflictReviewMaxConcurrency() != 2 ||
		configured.GetConflictReviewBatchSize() != 25 ||
		configured.GetConflictReviewLeaseSeconds() != 120 ||
		configured.GetConflictReviewMaxAttempts() != 4 ||
		configured.GetConflictReviewJitterSeconds() != 90 {
		t.Fatalf("configured conflict review getters returned unexpected values: %#v", configured)
	}
}
