package config

import (
	"bytes"
	"os"
	"testing"
)

// clearEnv clears all config-related environment variables
func clearEnv() {
	envVars := []string{
		"POSTGRES_DSN",
		"POSTGRES_TOPOLOGY",
		"POSTGRES_READ_DSN",
		"NEO4J_URI",
		"NEO4J_USER",
		"NEO4J_PASSWORD",
		"NEO4J_DATABASE",
		"REDIS_ADDR",
		"REDIS_PASSWORD",
		"REDIS_DB",
		"HTTP_MAX_BODY_BYTES",
		"AUTH_VERIFY_MAX_CONCURRENCY",
		"GRAPH_QUERY_DEFAULT_TIMEOUT_SECONDS",
		"GRAPH_QUERY_MAX_TIMEOUT_SECONDS",
		"RATE_LIMIT_PER_MINUTE",
		"FRAGMENT_CREATE_RATE_LIMIT",
		"FRAGMENT_READ_RATE_LIMIT",
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
		"AI_REVIEWER_MODEL",
		"AI_VERIFIER_MODEL",
		"AI_VERIFIER_DISABLE_TEMPERATURE",
		"AI_VERIFIER_TIMEOUT_SECONDS",
		"AI_VERIFIER_MAX_CONCURRENCY",
		"MEMORY_PLACEMENT_WORKER_COUNT",
		"MEMORY_PLACEMENT_MAX_ATTEMPTS",
		"MEMORY_PLACEMENT_POLL_SECONDS",
		"EMBEDDING_WORKER_COUNT",
		"EMBEDDING_BATCH_SIZE",
		"CLAIM_WRITE_RATE_LIMIT",
		"CLAIM_READ_RATE_LIMIT",
		"RECALL_VALIDATED_CLAIM_WEIGHT",
		"RECALL_RRF_ENABLED",
		"RECALL_RRF_K",
		"RECALL_RRF_BRANCH_WEIGHTS",
		"RECALL_BRANCH_PRIORITY",
		"RECALL_BRANCH_LIMIT_MULTIPLIER",
		"RECALL_BRANCH_LIMIT_FLOOR",
		"RECALL_BRANCH_LIMIT_MAX",
		"PROMOTE_TX_TIMEOUT_SECONDS",
		"MEMORY_PACK_IMPORT_HISTORY_DAYS",
		"SKILL_PACK_IMPORT_HISTORY_DAYS",
		"AI_COMMUNITY_MAX_NODES",
		"CONTROL_HTTP_ADDR",
		"CONTROL_PORTAL_TOKEN",
		"TELEMETRY_ENABLED",
		"TELEMETRY_PROMETHEUS_URL",
		"TELEMETRY_PROMETHEUS_JOB",
		"TELEMETRY_QUERY_TIMEOUT_SECONDS",
		"TELEMETRY_SCRAPE_TOKEN",
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
	os.Setenv("AI_API_EMBEDDING_MODEL", "text-embedding-3-large")
	os.Setenv("AI_API_EMBEDDING_DIMENSIONS", "3072")
}

func setRequiredAIModelEnv() {
	os.Setenv("AI_REVIEWER_MODEL", "reviewer-model")
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
	if cfg.GraphQueryDefaultTimeoutSeconds != 10 {
		t.Errorf("GraphQueryDefaultTimeoutSeconds default = %d, want %d", cfg.GraphQueryDefaultTimeoutSeconds, 10)
	}
	if cfg.GraphQueryMaxTimeoutSeconds != 30 {
		t.Errorf("GraphQueryMaxTimeoutSeconds default = %d, want %d", cfg.GraphQueryMaxTimeoutSeconds, 30)
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
	if cfg.EmbeddingDimensions != 0 {
		t.Errorf("EmbeddingDimensions default = %d, want %d", cfg.EmbeddingDimensions, 0)
	}
	if cfg.GetPostgresReadDSN() != "" {
		t.Errorf("PostgresReadDSN default = %q, want empty", cfg.GetPostgresReadDSN())
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
	if cfg.GetAIEmbeddingMaxConcurrency() != 8 {
		t.Errorf("AIEmbeddingMaxConcurrency default = %d, want %d", cfg.GetAIEmbeddingMaxConcurrency(), 8)
	}
	if cfg.GetEmbeddingWorkerCount() != 2 {
		t.Errorf("EmbeddingWorkerCount default = %d, want %d", cfg.GetEmbeddingWorkerCount(), 2)
	}
	if cfg.GetEmbeddingBatchSize() != 64 {
		t.Errorf("EmbeddingBatchSize default = %d, want %d", cfg.GetEmbeddingBatchSize(), 64)
	}
	if cfg.GetRecallRRFEnabled() {
		t.Errorf("RecallRRFEnabled default = true, want false")
	}
	if cfg.GetRecallRRFK() != 60 {
		t.Errorf("RecallRRFK default = %d, want %d", cfg.GetRecallRRFK(), 60)
	}
	if cfg.GetRecallRRFBranchWeights() != "exact=2,evidence_text=1,evidence_vector=1" {
		t.Errorf("RecallRRFBranchWeights default = %q", cfg.GetRecallRRFBranchWeights())
	}
	if cfg.GetRecallBranchPriority() != "exact,evidence_vector,evidence_text" {
		t.Errorf("RecallBranchPriority default = %q", cfg.GetRecallBranchPriority())
	}
	if cfg.GetRecallBranchLimitMultiplier() != 6 {
		t.Errorf("RecallBranchLimitMultiplier default = %d, want 6", cfg.GetRecallBranchLimitMultiplier())
	}
	if cfg.GetRecallBranchLimitFloor() != 60 {
		t.Errorf("RecallBranchLimitFloor default = %d, want 60", cfg.GetRecallBranchLimitFloor())
	}
	if cfg.GetRecallBranchLimitMax() != 200 {
		t.Errorf("RecallBranchLimitMax default = %d, want 200", cfg.GetRecallBranchLimitMax())
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

func TestLoadValidation_Neo4jConfigNotRequired(t *testing.T) {
	clearEnv()
	setRequiredEnv()

	if _, err := Load(); err != nil {
		t.Fatalf("Load() returned unexpected error without Neo4j config: %v", err)
	}
}

func TestLoadIgnoresPostgresTopologyHintBecauseBootDetectsServerState(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("POSTGRES_TOPOLOGY", "primary_with_standbys")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v; topology must be detected from Postgres at boot", err)
	}
}

func TestLoadValidation_RejectsPostgresReadDSN(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("POSTGRES_READ_DSN", "postgres://reader:pass@localhost/db?sslmode=disable")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for POSTGRES_READ_DSN, got nil")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "POSTGRES_READ_DSN" {
		t.Errorf("ValidationError.Field = %q, want POSTGRES_READ_DSN", validationErr.Field)
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

func TestLoadValidation_GraphQueryDefaultTimeoutExceedsMax(t *testing.T) {
	clearEnv()
	setRequiredEnv()
	os.Setenv("GRAPH_QUERY_DEFAULT_TIMEOUT_SECONDS", "60")
	os.Setenv("GRAPH_QUERY_MAX_TIMEOUT_SECONDS", "30")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for graph_query timeout mismatch, got nil")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "GRAPH_QUERY_DEFAULT_TIMEOUT_SECONDS" {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, "GRAPH_QUERY_DEFAULT_TIMEOUT_SECONDS")
	}
}

func TestLoadOverrides(t *testing.T) {
	clearEnv()
	setRequiredEnv()

	// Override all values
	os.Setenv("NEO4J_DATABASE", "testdb")
	os.Setenv("REDIS_PASSWORD", "redispass")
	os.Setenv("REDIS_DB", "5")
	os.Setenv("HTTP_MAX_BODY_BYTES", "2097152")
	os.Setenv("AUTH_VERIFY_MAX_CONCURRENCY", "12")
	os.Setenv("GRAPH_QUERY_DEFAULT_TIMEOUT_SECONDS", "20")
	os.Setenv("GRAPH_QUERY_MAX_TIMEOUT_SECONDS", "60")
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

	// String overrides
	if cfg.Neo4jDatabase != "testdb" {
		t.Errorf("Neo4jDatabase = %q, want %q", cfg.Neo4jDatabase, "testdb")
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
	if cfg.GraphQueryDefaultTimeoutSeconds != 20 {
		t.Errorf("GraphQueryDefaultTimeoutSeconds = %d, want %d", cfg.GraphQueryDefaultTimeoutSeconds, 20)
	}
	if cfg.GraphQueryMaxTimeoutSeconds != 60 {
		t.Errorf("GraphQueryMaxTimeoutSeconds = %d, want %d", cfg.GraphQueryMaxTimeoutSeconds, 60)
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
	_ = provider.GetPostgresReadDSN()
	_ = provider.GetNeo4jURI()
	_ = provider.GetNeo4jUser()
	_ = provider.GetNeo4jPassword()
	_ = provider.GetNeo4jDatabase()
	_ = provider.GetRedisAddr()
	_ = provider.GetRedisPassword()
	_ = provider.GetRedisDB()
	_ = provider.GetHTTPMaxBodyBytes()
	_ = provider.GetRateLimitPerMinute()
	_ = provider.GetFragmentCreateRateLimit()
	_ = provider.GetFragmentReadRateLimit()
	_ = provider.GetSSEHeartbeatSeconds()
	_ = provider.GetSSEMaxDurationSeconds()
	_ = provider.GetSSEMaxConcurrentStreams()
	_ = provider.GetEmbeddingDimensions()
	_ = provider.GetAIAPIURL()
	_ = provider.GetAIAPIKey()
	_ = provider.GetAIEmbeddingModel()
	_ = provider.GetAIEmbeddingDimensions()
	_ = provider.GetAIEmbeddingTimeoutSeconds()
	_ = provider.IsEmbeddingConfigured()
	_ = provider.GetAIVerifierAPIURL()
	_ = provider.GetAIVerifierAPIKey()
	_ = provider.GetAIReviewerModel()
	_ = provider.GetAIVerifierModel()
	_ = provider.GetAIVerifierTimeoutSeconds()
	_ = provider.GetAIVerifierMaxConcurrency()
	_ = provider.GetClaimWriteRateLimit()
	_ = provider.GetClaimReadRateLimit()
	_ = provider.GetRecallValidatedClaimWeight()
	_ = provider.GetPromoteTxTimeoutSeconds()
	_ = provider.GetSkillPackImportHistoryDays()
	_ = provider.GetAICommunityMaxNodes()
	_ = provider.GetControlHTTPAddr()
	_ = provider.GetControlPortalToken()
}

func TestConfigGetterFallbacksAndParsers(t *testing.T) {
	cfg := &Config{
		HTTPMaxBodyBytes:         2048,
		FragmentCreateRateLimit:  11,
		FragmentReadRateLimit:    22,
		AIAPIURL:                 "https://shared.example/v1",
		AIAPIKey:                 "shared-key",
		AIReviewerModel:          "reviewer-model",
		AIVerifierModel:          "verifier-model",
		AIVerifierTimeoutSeconds: 0,
	}

	if got := cfg.GetHTTPMaxBodyBytes(); got != 2048 {
		t.Fatalf("GetHTTPMaxBodyBytes() = %d, want 2048", got)
	}
	if got := cfg.GetFragmentCreateRateLimit(); got != 11 {
		t.Fatalf("GetFragmentCreateRateLimit() = %d, want 11", got)
	}
	if got := cfg.GetFragmentReadRateLimit(); got != 22 {
		t.Fatalf("GetFragmentReadRateLimit() = %d, want 22", got)
	}
	if got := cfg.GetAIVerifierTimeoutSeconds(); got != 60 {
		t.Fatalf("GetAIVerifierTimeoutSeconds() fallback = %d, want 60", got)
	}
	if got := cfg.GetAIReviewerModel(); got != "reviewer-model" {
		t.Fatalf("GetAIReviewerModel() = %q, want reviewer-model", got)
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
	if cfg.GetAIEmbeddingDimensions() != 3072 {
		t.Errorf("GetAIEmbeddingDimensions() = %d, want %d", cfg.GetAIEmbeddingDimensions(), 3072)
	}
	if cfg.GetEmbeddingDimensions() != 3072 {
		t.Errorf("GetEmbeddingDimensions() = %d, want %d", cfg.GetEmbeddingDimensions(), 3072)
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
	os.Setenv("AI_REVIEWER_MODEL", "local-reviewer")
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
	if got := cfg.GetAIReviewerModel(); got != "local-reviewer" {
		t.Errorf("GetAIReviewerModel() = %q, want %q", got, "local-reviewer")
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
	setRequiredAIModelEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if err := cfg.ValidateServerStartup(); err != nil {
		t.Fatalf("ValidateServerStartup() returned unexpected error: %v", err)
	}
}

func TestEnvExampleDocumentsRequiredAIStartupConfig(t *testing.T) {
	contents, err := os.ReadFile("../../examples/.env.example")
	if err != nil {
		t.Fatalf("ReadFile examples/.env.example: %v", err)
	}

	required := []string{
		"AI_API_URL=",
		"AI_API_KEY=",
		"AI_API_EMBEDDING_MODEL=",
		"AI_API_EMBEDDING_DIMENSIONS=",
		"AI_REVIEWER_MODEL=",
		"AI_VERIFIER_MODEL=",
	}
	for _, key := range required {
		if !bytes.Contains(contents, []byte(key)) {
			t.Fatalf("examples/.env.example missing %s", key)
		}
	}
}

// TestLoadKnowledgeConfigDefaults verifies that all knowledge-pipeline knobs
// have their expected default values when no environment variables are set (AC-X3).
