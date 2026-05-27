package config

import (
	"os"
	"testing"
)

// clearEnv clears all config-related environment variables
func clearEnv() {
	envVars := []string{
		"HTTP_ADDR",
		"POSTGRES_DSN",
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
		"SSE_HEARTBEAT_SECONDS",
		"SSE_MAX_DURATION_SECONDS",
		"SSE_MAX_CONCURRENT_STREAMS",
		"AI_API_URL",
		"AI_API_KEY",
		"AI_API_EMBEDDING_MODEL",
		"AI_API_EMBEDDING_DIMENSIONS",
		"AI_API_EMBEDDING_TIMEOUT_SECONDS",
		// Knowledge-pipeline knobs
		"AI_VERIFIER_API_URL",
		"AI_VERIFIER_API_KEY",
		"AI_VERIFIER_MODEL",
		"AI_VERIFIER_TIMEOUT_SECONDS",
		"AI_VERIFIER_MAX_CONCURRENCY",
		"CLAIM_WRITE_RATE_LIMIT",
		"CLAIM_READ_RATE_LIMIT",
		"RECALL_VALIDATED_CLAIM_WEIGHT",
		"PROMOTE_TX_TIMEOUT_SECONDS",
		"AI_COMMUNITY_MAX_NODES",
		"CONTROL_HTTP_ADDR",
		"CONTROL_PORTAL_TOKEN",
	}
	for _, v := range envVars {
		os.Unsetenv(v)
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

	// Test string defaults
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr default = %q, want %q", cfg.HTTPAddr, ":8080")
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
		t.Fatal("Load() expected error for graph-query timeout mismatch, got nil")
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
	os.Setenv("HTTP_ADDR", ":9090")
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
	if cfg.HTTPAddr != ":9090" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, ":9090")
	}
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
	_ = provider.GetHTTPAddr()
	_ = provider.GetPostgresDSN()
	_ = provider.GetNeo4jURI()
	_ = provider.GetNeo4jUser()
	_ = provider.GetNeo4jPassword()
	_ = provider.GetNeo4jDatabase()
	_ = provider.GetRedisAddr()
	_ = provider.GetRedisPassword()
	_ = provider.GetRedisDB()
	_ = provider.GetRateLimitPerMinute()
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
	_ = provider.GetAIVerifierModel()
	_ = provider.GetAIVerifierTimeoutSeconds()
	_ = provider.GetAIVerifierMaxConcurrency()
	_ = provider.GetControlHTTPAddr()
	_ = provider.GetControlPortalToken()
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
	if got := cfg.GetAIVerifierTimeoutSeconds(); got != 45 {
		t.Errorf("GetAIVerifierTimeoutSeconds() = %d, want %d", got, 45)
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

// TestLoadKnowledgeConfigDefaults verifies that all knowledge-pipeline knobs
// have their expected default values when no environment variables are set (AC-X3).
func TestLoadKnowledgeConfigDefaults(t *testing.T) {
	clearEnv()
	setRequiredEnv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if got := cfg.GetAIVerifierModel(); got != "gpt-4o-mini" {
		t.Errorf("GetAIVerifierModel() = %q, want %q", got, "gpt-4o-mini")
	}
	if got := cfg.GetAIVerifierMaxConcurrency(); got != 5 {
		t.Errorf("GetAIVerifierMaxConcurrency() = %d, want %d", got, 5)
	}
	if got := cfg.GetClaimWriteRateLimit(); got != 60 {
		t.Errorf("GetClaimWriteRateLimit() = %d, want %d", got, 60)
	}
	if got := cfg.GetClaimReadRateLimit(); got != 300 {
		t.Errorf("GetClaimReadRateLimit() = %d, want %d", got, 300)
	}
	if got := cfg.GetRecallValidatedClaimWeight(); got != 0.5 {
		t.Errorf("GetRecallValidatedClaimWeight() = %f, want %f", got, 0.5)
	}
	if got := cfg.GetPromoteTxTimeoutSeconds(); got != 10 {
		t.Errorf("GetPromoteTxTimeoutSeconds() = %d, want %d", got, 10)
	}
	if got := cfg.GetAICommunityMaxNodes(); got != 500000 {
		t.Errorf("GetAICommunityMaxNodes() = %d, want %d", got, 500000)
	}
}

func TestLoadControlPortalValidation(t *testing.T) {
	t.Run("server startup requires token", func(t *testing.T) {
		clearEnv()
		setRequiredEnv()
		setRequiredEmbeddingEnv()
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
