package config

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// clearEnv clears all config-related environment variables
func clearEnv() {
	envVars := []string{
		"POSTGRES_DSN",
		"POSTGRES_READ_DSN",
		"POSTGRES_MAX_OPEN_CONNS",
		"POSTGRES_MAX_IDLE_CONNS",
		"POSTGRES_CONN_MAX_LIFETIME_SECONDS",
		"POSTGRES_VECTOR_MAX_CONCURRENCY",
		"PGVECTOR_EXTENSION_REQUIRED",
		"POSTGRES_STATEMENT_TIMEOUT_SECONDS",
		"POSTGRES_LOCK_TIMEOUT_SECONDS",
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
		"SEARCH_DOCUMENT_FORMAT_VERSION",
		"EMBEDDING_NORMALIZATION_VERSION",
		"PGVECTOR_DISTANCE",
		"PGVECTOR_ANN_STRATEGY",
		"PGVECTOR_HNSW_M",
		"PGVECTOR_HNSW_EF_CONSTRUCTION",
		"PGVECTOR_INDEX_BUILD_MAX_CONCURRENCY",
		// Knowledge-pipeline knobs
		"AI_VERIFIER_API_URL",
		"AI_VERIFIER_API_KEY",
		"AI_REVIEWER_MODEL",
		"AI_VERIFIER_MODEL",
		"AI_VERIFIER_DISABLE_TEMPERATURE",
		"AI_VERIFIER_TIMEOUT_SECONDS",
		"AI_VERIFIER_MAX_CONCURRENCY",
		"AI_VERIFIER_COOLDOWN_POLL_SECONDS",
		"AI_VERIFIER_MAX_ENTITY_RESULTS",
		"AI_VERIFIER_MAX_RELATIONSHIP_RESULTS",
		"AI_VERIFIER_MAX_INPUT_BYTES",
		"AI_VERIFIER_MAX_OUTPUT_BYTES",
		"AI_VERIFIER_MAX_RESPONSE_REGENERATIONS",
		"RELATIONSHIP_MATCH_MAX_CANDIDATES",
		"MEMORY_PLACEMENT_WORKER_COUNT",
		"MEMORY_PLACEMENT_LEASE_SECONDS",
		"MEMORY_PLACEMENT_HEARTBEAT_SECONDS",
		"MEMORY_PLACEMENT_POLL_SECONDS",
		"MEMORY_PLACEMENT_MAX_ATTEMPTS",
		"EMBEDDING_WORKER_COUNT",
		"EMBEDDING_BATCH_SIZE",
		"EMBEDDING_JOB_LEASE_SECONDS",
		"EMBEDDING_JOB_POLL_SECONDS",
		"EMBEDDING_JOB_MAX_ATTEMPTS",
		"EMBEDDING_JOB_RETRY_MAX_SECONDS",
		"EMBEDDING_PENDING_STALE_SECONDS",
		"RECALL_RRF_ENABLED",
		"RECALL_RRF_K",
		"RECALL_RRF_BRANCH_WEIGHTS",
		"RECALL_BRANCH_PRIORITY",
		"RECALL_BRANCH_LIMIT_MULTIPLIER",
		"RECALL_BRANCH_LIMIT_FLOOR",
		"RECALL_BRANCH_LIMIT_MAX",
		"RECALL_DETERMINISTIC_RERANK_ENABLED",
		"RECALL_MAX_ENTITY_SEEDS",
		"RECALL_DISCOVERY_RELATIONSHIPS_PER_EVIDENCE",
		"RECALL_MAX_GRAPH_DEPTH",
		"RECALL_MAX_EDGES",
		"RECALL_GRAPH_TIMEOUT_MILLISECONDS",
		"RECALL_REQUIRED_BRANCH_PROFILE",
		"PGVECTOR_EXACT_FILTERED_MAX_ROWS",
		"PGVECTOR_HNSW_EF_SEARCH",
		"PGVECTOR_HNSW_ITERATIVE_SCAN",
		"PGVECTOR_HNSW_MAX_SCAN_TUPLES",
		"PGVECTOR_HNSW_SCAN_MEM_MULTIPLIER",
		"PREDICATE_REGISTRY_VERSION",
		"PREDICATE_REGISTRY_CACHE_TTL_SECONDS",
		"PREDICATE_UNKNOWN_ACTION",
		"REDIS_TLS_ENABLED",
		"DISTRIBUTED_COORDINATION_REQUIRED",
		"REMEMBER_RATE_LIMIT_PER_MINUTE",
		"RECALL_RATE_LIMIT_PER_MINUTE",
		"TRACE_RATE_LIMIT_PER_MINUTE",
		"CLAIM_WRITE_RATE_LIMIT",
		"CLAIM_READ_RATE_LIMIT",
		"RECALL_VALIDATED_CLAIM_WEIGHT",
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
		"V2_BOOT_MODE",
	}
	for _, v := range envVars {
		os.Unsetenv(v)
	}
}

func TestConfigJSONExcludesAPICredentials(t *testing.T) {
	configType := reflect.TypeOf(Config{})
	for _, fieldName := range []string{
		"AIAPIKey",
		"AIVerifierAPIKey",
		"ControlPortalToken",
		"TelemetryScrapeToken",
	} {
		field, ok := configType.FieldByName(fieldName)
		if !ok {
			t.Fatalf("Config missing credential field %q", fieldName)
		}
		if got := field.Tag.Get("json"); got != "-" {
			t.Fatalf("Config.%s json tag = %q, want %q", fieldName, got, "-")
		}
	}

	cfg := Config{
		AIAPIKey:             "ai-api-secret",
		AIVerifierAPIKey:     "verifier-secret",
		ControlPortalToken:   "control-secret",
		TelemetryScrapeToken: "telemetry-secret",
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal(Config): %v", err)
	}
	encoded := string(data)
	for _, secret := range []string{
		"ai-api-secret",
		"verifier-secret",
		"control-secret",
		"telemetry-secret",
	} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("serialized Config contains credential %q: %s", secret, encoded)
		}
	}
	for _, field := range []string{
		"AIAPIKey",
		"AIVerifierAPIKey",
		"ControlPortalToken",
		"TelemetryScrapeToken",
	} {
		if strings.Contains(encoded, field) {
			t.Fatalf("serialized Config contains credential field %q: %s", field, encoded)
		}
	}
}

// setRequiredEnv sets the minimum required environment variables for a valid config
func setRequiredEnv() {
	os.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost/db?sslmode=disable")
	os.Setenv("NEO4J_URI", "bolt://localhost:7687")
	os.Setenv("NEO4J_USER", "neo4j")
	os.Setenv("NEO4J_PASSWORD", "password")
	os.Setenv("CONTROL_PORTAL_TOKEN", "control-secret")
}

func setRequiredEmbeddingEnv() {
	os.Setenv("AI_API_URL", "https://example.com/v1")
	os.Setenv("AI_API_KEY", "sk-test")
	os.Setenv("AI_API_EMBEDDING_MODEL", "text-embedding-3-small")
	os.Setenv("AI_API_EMBEDDING_DIMENSIONS", "1536")
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
	if cfg.PostgresMaxOpenConns != 25 {
		t.Errorf("PostgresMaxOpenConns default = %d, want %d", cfg.PostgresMaxOpenConns, 25)
	}
	if cfg.PostgresMaxIdleConns != 10 {
		t.Errorf("PostgresMaxIdleConns default = %d, want %d", cfg.PostgresMaxIdleConns, 10)
	}
	if cfg.PostgresConnMaxLifetimeSeconds != 1800 {
		t.Errorf("PostgresConnMaxLifetimeSeconds default = %d, want %d", cfg.PostgresConnMaxLifetimeSeconds, 1800)
	}
	if cfg.PostgresVectorMaxConcurrency != 4 {
		t.Errorf("PostgresVectorMaxConcurrency default = %d, want %d", cfg.PostgresVectorMaxConcurrency, 4)
	}
	if !cfg.PGVectorExtensionRequired {
		t.Error("PGVectorExtensionRequired default = false, want true")
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
	if cfg.EmbeddingDimensions != 1536 {
		t.Errorf("EmbeddingDimensions default = %d, want %d", cfg.EmbeddingDimensions, 1536)
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
	if cfg.GetV2BootMode() != V2BootModeOff {
		t.Errorf("V2BootMode default = %q, want %q", cfg.GetV2BootMode(), V2BootModeOff)
	}
	if cfg.IsV2BootEnabled() {
		t.Error("IsV2BootEnabled() = true, want false")
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
	os.Setenv("NEO4J_URI", "bolt://localhost:7687")
	os.Setenv("NEO4J_USER", "neo4j")
	os.Setenv("NEO4J_PASSWORD", "password")

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

func TestLoadValidation_MissingNeo4jURI(t *testing.T) {
	clearEnv()
	os.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost/db?sslmode=disable")
	os.Setenv("NEO4J_USER", "neo4j")
	os.Setenv("NEO4J_PASSWORD", "password")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for missing NEO4J_URI, got nil")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "NEO4J_URI" {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, "NEO4J_URI")
	}
}

func TestLoadValidation_MissingNeo4jUser(t *testing.T) {
	clearEnv()
	os.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost/db?sslmode=disable")
	os.Setenv("NEO4J_URI", "bolt://localhost:7687")
	os.Setenv("NEO4J_PASSWORD", "password")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for missing NEO4J_USER, got nil")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "NEO4J_USER" {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, "NEO4J_USER")
	}
}

func TestLoadValidation_MissingNeo4jPassword(t *testing.T) {
	clearEnv()
	os.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost/db?sslmode=disable")
	os.Setenv("NEO4J_URI", "bolt://localhost:7687")
	os.Setenv("NEO4J_USER", "neo4j")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for missing NEO4J_PASSWORD, got nil")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if validationErr.Field != "NEO4J_PASSWORD" {
		t.Errorf("ValidationError.Field = %q, want %q", validationErr.Field, "NEO4J_PASSWORD")
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

func TestConfigGetterFallbacksAndParsers(t *testing.T) {
	cfg := &Config{
		HTTPMaxBodyBytes:         2048,
		FragmentCreateRateLimit:  11,
		FragmentReadRateLimit:    22,
		AIAPIURL:                 "https://shared.example/v1",
		AIAPIKey:                 "shared-key",
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

func TestLoadValidation_RemainingInvalidEnvironmentBranches(t *testing.T) {
	cases := []struct {
		name  string
		set   func()
		field string
	}{
		{"invalid redis db", func() { os.Setenv("REDIS_DB", "bad") }, "REDIS_DB"},
		{"invalid http max body bytes", func() { os.Setenv("HTTP_MAX_BODY_BYTES", "bad") }, "HTTP_MAX_BODY_BYTES"},
		{"invalid auth verify concurrency", func() { os.Setenv("AUTH_VERIFY_MAX_CONCURRENCY", "bad") }, "AUTH_VERIFY_MAX_CONCURRENCY"},
		{"invalid graph default timeout", func() { os.Setenv("GRAPH_QUERY_DEFAULT_TIMEOUT_SECONDS", "bad") }, "GRAPH_QUERY_DEFAULT_TIMEOUT_SECONDS"},
		{"invalid graph max timeout", func() { os.Setenv("GRAPH_QUERY_MAX_TIMEOUT_SECONDS", "bad") }, "GRAPH_QUERY_MAX_TIMEOUT_SECONDS"},
		{"invalid fragment create rate", func() { os.Setenv("FRAGMENT_CREATE_RATE_LIMIT", "bad") }, "FRAGMENT_CREATE_RATE_LIMIT"},
		{"invalid fragment read rate", func() { os.Setenv("FRAGMENT_READ_RATE_LIMIT", "bad") }, "FRAGMENT_READ_RATE_LIMIT"},
		{"invalid sse heartbeat", func() { os.Setenv("SSE_HEARTBEAT_SECONDS", "bad") }, "SSE_HEARTBEAT_SECONDS"},
		{"invalid sse max duration", func() { os.Setenv("SSE_MAX_DURATION_SECONDS", "bad") }, "SSE_MAX_DURATION_SECONDS"},
		{"invalid sse streams", func() { os.Setenv("SSE_MAX_CONCURRENT_STREAMS", "bad") }, "SSE_MAX_CONCURRENT_STREAMS"},
		{"invalid embedding dimensions", func() { os.Setenv("AI_API_EMBEDDING_DIMENSIONS", "bad") }, "AI_API_EMBEDDING_DIMENSIONS"},
		{"invalid embedding timeout", func() { os.Setenv("AI_API_EMBEDDING_TIMEOUT_SECONDS", "bad") }, "AI_API_EMBEDDING_TIMEOUT_SECONDS"},
		{"invalid verifier disable temperature", func() { os.Setenv("AI_VERIFIER_DISABLE_TEMPERATURE", "bad") }, "AI_VERIFIER_DISABLE_TEMPERATURE"},
		{"invalid verifier timeout", func() { os.Setenv("AI_VERIFIER_TIMEOUT_SECONDS", "bad") }, "AI_VERIFIER_TIMEOUT_SECONDS"},
		{"invalid verifier concurrency", func() { os.Setenv("AI_VERIFIER_MAX_CONCURRENCY", "bad") }, "AI_VERIFIER_MAX_CONCURRENCY"},
		{"invalid claim write rate", func() { os.Setenv("CLAIM_WRITE_RATE_LIMIT", "bad") }, "CLAIM_WRITE_RATE_LIMIT"},
		{"invalid claim read rate", func() { os.Setenv("CLAIM_READ_RATE_LIMIT", "bad") }, "CLAIM_READ_RATE_LIMIT"},
		{"invalid recall weight", func() { os.Setenv("RECALL_VALIDATED_CLAIM_WEIGHT", "bad") }, "RECALL_VALIDATED_CLAIM_WEIGHT"},
		{"invalid promote timeout", func() { os.Setenv("PROMOTE_TX_TIMEOUT_SECONDS", "bad") }, "PROMOTE_TX_TIMEOUT_SECONDS"},
		{"invalid community max nodes", func() { os.Setenv("AI_COMMUNITY_MAX_NODES", "bad") }, "AI_COMMUNITY_MAX_NODES"},
		{"zero http max body bytes", func() { os.Setenv("HTTP_MAX_BODY_BYTES", "0") }, "HTTP_MAX_BODY_BYTES"},
		{"recall weight below range", func() { os.Setenv("RECALL_VALIDATED_CLAIM_WEIGHT", "-0.1") }, "RECALL_VALIDATED_CLAIM_WEIGHT"},
		{"recall weight above range", func() { os.Setenv("RECALL_VALIDATED_CLAIM_WEIGHT", "1.1") }, "RECALL_VALIDATED_CLAIM_WEIGHT"},
		{"verifier key without url or shared api url", func() { os.Setenv("AI_VERIFIER_API_KEY", "verifier-key") }, "AI_VERIFIER_API_URL"},
		{"embedding missing url", func() {
			os.Setenv("AI_API_KEY", "sk-test")
			os.Setenv("AI_API_EMBEDDING_MODEL", "text-embedding-3-small")
			os.Setenv("AI_API_EMBEDDING_DIMENSIONS", "1536")
		}, "AI_API_URL"},
		{"embedding missing model", func() {
			os.Setenv("AI_API_URL", "https://example.com/v1")
			os.Setenv("AI_API_KEY", "sk-test")
			os.Setenv("AI_API_EMBEDDING_DIMENSIONS", "1536")
		}, "AI_API_EMBEDDING_MODEL"},
		{"embedding missing dimensions", func() {
			os.Setenv("AI_API_URL", "https://example.com/v1")
			os.Setenv("AI_API_KEY", "sk-test")
			os.Setenv("AI_API_EMBEDDING_MODEL", "text-embedding-3-small")
		}, "AI_API_EMBEDDING_DIMENSIONS"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv()
			setRequiredEnv()
			tc.set()

			_, err := Load()

			if err == nil {
				t.Fatalf("Load() expected error for %s, got nil", tc.field)
			}
			validationErr, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("expected *ValidationError, got %T", err)
			}
			if validationErr.Field != tc.field {
				t.Fatalf("ValidationError.Field = %q, want %q; err=%v", validationErr.Field, tc.field, err)
			}
		})
	}
}

func TestValidateServerStartupRemainingRequiredFields(t *testing.T) {
	cfg := Config{
		AIAPIURL:              "https://example.com/v1",
		AIAPIKey:              "sk-test",
		AIEmbeddingModel:      "text-embedding-3-small",
		AIEmbeddingDimensions: 1536,
		ControlPortalToken:    "control-secret",
	}
	cases := []struct {
		name  string
		edit  func(*Config)
		field string
	}{
		{"missing api key", func(c *Config) { c.AIAPIKey = "" }, "AI_API_KEY"},
		{"missing embedding model", func(c *Config) { c.AIEmbeddingModel = "" }, "AI_API_EMBEDDING_MODEL"},
		{"missing embedding dimensions", func(c *Config) { c.AIEmbeddingDimensions = 0 }, "AI_API_EMBEDDING_DIMENSIONS"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testCfg := cfg
			tc.edit(&testCfg)

			err := testCfg.ValidateServerStartup()

			if err == nil {
				t.Fatal("ValidateServerStartup() expected error, got nil")
			}
			validationErr, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("expected *ValidationError, got %T", err)
			}
			if validationErr.Field != tc.field {
				t.Fatalf("field = %q, want %q", validationErr.Field, tc.field)
			}
		})
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
	os.Setenv("NEO4J_URI", "bolt://localhost:7687")
	os.Setenv("NEO4J_USER", "neo4j")
	os.Setenv("NEO4J_PASSWORD", "password")
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
	os.Setenv("NEO4J_URI", "bolt://localhost:7687")
	os.Setenv("NEO4J_USER", "neo4j")
	os.Setenv("NEO4J_PASSWORD", "password")
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
	os.Setenv("NEO4J_URI", "bolt://localhost:7687")
	os.Setenv("NEO4J_USER", "neo4j")
	os.Setenv("NEO4J_PASSWORD", "password")
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

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if err := cfg.ValidateServerStartup(); err != nil {
		t.Fatalf("ValidateServerStartup() returned unexpected error: %v", err)
	}
}
