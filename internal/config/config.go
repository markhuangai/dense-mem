package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	DefaultHTTPPort = "8080"
	DefaultHTTPAddr = ":" + DefaultHTTPPort
)

// ConfigProvider is the companion interface for Config.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type ConfigProvider interface {
	GetPostgresDSN() string
	GetNeo4jURI() string
	GetNeo4jUser() string
	GetNeo4jPassword() string
	GetNeo4jDatabase() string
	GetRedisAddr() string
	GetRedisPassword() string
	GetRedisDB() int
	GetHTTPMaxBodyBytes() int
	GetRateLimitPerMinute() int
	GetFragmentCreateRateLimit() int
	GetFragmentReadRateLimit() int
	GetSSEHeartbeatSeconds() int
	GetSSEMaxDurationSeconds() int
	GetSSEMaxConcurrentStreams() int
	GetEmbeddingDimensions() int
	GetAIAPIURL() string
	GetAIAPIKey() string
	GetAIEmbeddingModel() string
	GetAIEmbeddingDimensions() int
	GetAIEmbeddingTimeoutSeconds() int
	IsEmbeddingConfigured() bool
	// Knowledge-pipeline knobs (AC-X3)
	GetAIVerifierAPIURL() string
	GetAIVerifierAPIKey() string
	GetAIVerifierModel() string
	GetAIVerifierTimeoutSeconds() int
	GetAIVerifierMaxConcurrency() int
	GetClaimWriteRateLimit() int
	GetClaimReadRateLimit() int
	GetRecallValidatedClaimWeight() float64
	GetPromoteTxTimeoutSeconds() int
	GetAICommunityMaxNodes() int
	GetControlHTTPAddr() string
	GetControlPortalToken() string
}

// Config holds all configuration for the application.
// All fields are populated from environment variables with sensible defaults.
type Config struct {
	PostgresDSN                     string
	Neo4jURI                        string
	Neo4jUser                       string
	Neo4jPassword                   string
	Neo4jDatabase                   string
	RedisAddr                       string
	RedisPassword                   string
	RedisDB                         int
	HTTPMaxBodyBytes                int
	AuthVerifyMaxConcurrency        int
	GraphQueryDefaultTimeoutSeconds int
	GraphQueryMaxTimeoutSeconds     int
	RateLimitPerMinute              int
	FragmentCreateRateLimit         int
	FragmentReadRateLimit           int
	SSEHeartbeatSeconds             int
	SSEMaxDurationSeconds           int
	SSEMaxConcurrentStreams         int
	EmbeddingDimensions             int
	AIAPIURL                        string
	AIAPIKey                        string `json:"-"`
	AIEmbeddingModel                string
	AIEmbeddingDimensions           int
	AIEmbeddingTimeoutSeconds       int
	// Knowledge-pipeline knobs (AC-X3)
	AIVerifierAPIURL           string
	AIVerifierAPIKey           string `json:"-"`
	AIVerifierModel            string
	AIVerifierTimeoutSeconds   int
	AIVerifierMaxConcurrency   int
	ClaimWriteRateLimit        int
	ClaimReadRateLimit         int
	RecallValidatedClaimWeight float64
	PromoteTxTimeoutSeconds    int
	AICommunityMaxNodes        int
	ControlHTTPAddr            string
	ControlPortalToken         string `json:"-"`
}

// Ensure Config implements ConfigProvider
var _ ConfigProvider = (*Config)(nil)

// Getters for ConfigProvider interface
func (c *Config) GetPostgresDSN() string            { return c.PostgresDSN }
func (c *Config) GetNeo4jURI() string               { return c.Neo4jURI }
func (c *Config) GetNeo4jUser() string              { return c.Neo4jUser }
func (c *Config) GetNeo4jPassword() string          { return c.Neo4jPassword }
func (c *Config) GetNeo4jDatabase() string          { return c.Neo4jDatabase }
func (c *Config) GetRedisAddr() string              { return c.RedisAddr }
func (c *Config) GetRedisPassword() string          { return c.RedisPassword }
func (c *Config) GetRedisDB() int                   { return c.RedisDB }
func (c *Config) GetHTTPMaxBodyBytes() int          { return c.HTTPMaxBodyBytes }
func (c *Config) GetRateLimitPerMinute() int        { return c.RateLimitPerMinute }
func (c *Config) GetFragmentCreateRateLimit() int   { return c.FragmentCreateRateLimit }
func (c *Config) GetFragmentReadRateLimit() int     { return c.FragmentReadRateLimit }
func (c *Config) GetSSEHeartbeatSeconds() int       { return c.SSEHeartbeatSeconds }
func (c *Config) GetSSEMaxDurationSeconds() int     { return c.SSEMaxDurationSeconds }
func (c *Config) GetSSEMaxConcurrentStreams() int   { return c.SSEMaxConcurrentStreams }
func (c *Config) GetEmbeddingDimensions() int       { return c.EmbeddingDimensions }
func (c *Config) GetAIAPIURL() string               { return c.AIAPIURL }
func (c *Config) GetAIAPIKey() string               { return c.AIAPIKey }
func (c *Config) GetAIEmbeddingModel() string       { return c.AIEmbeddingModel }
func (c *Config) GetAIEmbeddingDimensions() int     { return c.AIEmbeddingDimensions }
func (c *Config) GetAIEmbeddingTimeoutSeconds() int { return c.AIEmbeddingTimeoutSeconds }
func (c *Config) IsEmbeddingConfigured() bool {
	return c.AIAPIURL != "" && c.AIAPIKey != "" && c.AIEmbeddingModel != "" && c.AIEmbeddingDimensions > 0
}

// Knowledge-pipeline getters (AC-X3)
func (c *Config) GetAIVerifierAPIURL() string {
	if c.AIVerifierAPIURL != "" {
		return c.AIVerifierAPIURL
	}
	return c.AIAPIURL
}
func (c *Config) GetAIVerifierAPIKey() string {
	if c.AIVerifierAPIKey != "" {
		return c.AIVerifierAPIKey
	}
	return c.AIAPIKey
}
func (c *Config) GetAIVerifierModel() string { return c.AIVerifierModel }
func (c *Config) GetAIVerifierTimeoutSeconds() int {
	if c.AIVerifierTimeoutSeconds > 0 {
		return c.AIVerifierTimeoutSeconds
	}
	return 60
}
func (c *Config) GetAIVerifierMaxConcurrency() int       { return c.AIVerifierMaxConcurrency }
func (c *Config) GetClaimWriteRateLimit() int            { return c.ClaimWriteRateLimit }
func (c *Config) GetClaimReadRateLimit() int             { return c.ClaimReadRateLimit }
func (c *Config) GetRecallValidatedClaimWeight() float64 { return c.RecallValidatedClaimWeight }
func (c *Config) GetPromoteTxTimeoutSeconds() int        { return c.PromoteTxTimeoutSeconds }
func (c *Config) GetAICommunityMaxNodes() int            { return c.AICommunityMaxNodes }
func (c *Config) GetControlHTTPAddr() string             { return c.ControlHTTPAddr }
func (c *Config) GetControlPortalToken() string          { return c.ControlPortalToken }

// ValidationError represents a configuration validation failure.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("config validation error for %s: %s", e.Field, e.Message)
}

// ValidateServerStartup checks the config required to boot the main dense-mem
// server process. This is intentionally stricter than Load() so auxiliary
// binaries such as migrations can still reuse the shared loader.
func (c *Config) ValidateServerStartup() error {
	required := []struct {
		field string
		value string
	}{
		{"AI_API_URL", c.AIAPIURL},
		{"AI_API_KEY", c.AIAPIKey},
		{"AI_API_EMBEDDING_MODEL", c.AIEmbeddingModel},
		{"CONTROL_PORTAL_TOKEN", c.ControlPortalToken},
	}
	for _, item := range required {
		if strings.TrimSpace(item.value) == "" {
			return &ValidationError{
				Field:   item.field,
				Message: "required for server startup",
			}
		}
	}
	if c.AIEmbeddingDimensions <= 0 {
		return &ValidationError{
			Field:   "AI_API_EMBEDDING_DIMENSIONS",
			Message: "required for server startup",
		}
	}
	return nil
}

// getEnvOrDefault returns the value of the environment variable or the default.
func getEnvOrDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

// parseIntOrDefault parses an environment variable as int or returns the default.
func parseIntOrDefault(key string, defaultValue int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, &ValidationError{
			Field:   key,
			Message: fmt.Sprintf("invalid integer value: %s", value),
		}
	}
	return parsed, nil
}

// parseFloatOrDefault parses an environment variable as float64 or returns the default.
func parseFloatOrDefault(key string, defaultValue float64) (float64, error) {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, &ValidationError{
			Field:   key,
			Message: fmt.Sprintf("invalid float value: %s", value),
		}
	}
	return parsed, nil
}

func parseBoolOrDefault(key string, defaultValue bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, &ValidationError{
			Field:   key,
			Message: fmt.Sprintf("invalid boolean value: %s", value),
		}
	}
	return parsed, nil
}

// Load reads configuration from environment variables and returns a Config.
// Returns a typed ValidationError for any validation failures.
func Load() (Config, error) {
	cfg := Config{}
	var err error

	// String fields with defaults
	cfg.PostgresDSN = os.Getenv("POSTGRES_DSN")
	cfg.Neo4jURI = os.Getenv("NEO4J_URI")
	cfg.Neo4jUser = os.Getenv("NEO4J_USER")
	cfg.Neo4jPassword = os.Getenv("NEO4J_PASSWORD")
	cfg.Neo4jDatabase = os.Getenv("NEO4J_DATABASE")
	cfg.RedisAddr = os.Getenv("REDIS_ADDR")
	cfg.RedisPassword = os.Getenv("REDIS_PASSWORD")

	// Integer fields with defaults
	cfg.RedisDB, err = parseIntOrDefault("REDIS_DB", 0)
	if err != nil {
		return cfg, err
	}

	cfg.HTTPMaxBodyBytes, err = parseIntOrDefault("HTTP_MAX_BODY_BYTES", 1048576)
	if err != nil {
		return cfg, err
	}

	cfg.AuthVerifyMaxConcurrency, err = parseIntOrDefault("AUTH_VERIFY_MAX_CONCURRENCY", 8)
	if err != nil {
		return cfg, err
	}

	cfg.GraphQueryDefaultTimeoutSeconds, err = parseIntOrDefault("GRAPH_QUERY_DEFAULT_TIMEOUT_SECONDS", 10)
	if err != nil {
		return cfg, err
	}

	cfg.GraphQueryMaxTimeoutSeconds, err = parseIntOrDefault("GRAPH_QUERY_MAX_TIMEOUT_SECONDS", 30)
	if err != nil {
		return cfg, err
	}

	cfg.RateLimitPerMinute, err = parseIntOrDefault("RATE_LIMIT_PER_MINUTE", 100)
	if err != nil {
		return cfg, err
	}

	// Fragment rate-limit tiers (AC-54): writes are stricter than reads because
	// a fragment create triggers an embedding call (external network + cost)
	// plus a graph write, whereas a read is a single indexed lookup.
	cfg.FragmentCreateRateLimit, err = parseIntOrDefault("FRAGMENT_CREATE_RATE_LIMIT", 60)
	if err != nil {
		return cfg, err
	}

	cfg.FragmentReadRateLimit, err = parseIntOrDefault("FRAGMENT_READ_RATE_LIMIT", 300)
	if err != nil {
		return cfg, err
	}

	cfg.SSEHeartbeatSeconds, err = parseIntOrDefault("SSE_HEARTBEAT_SECONDS", 30)
	if err != nil {
		return cfg, err
	}

	cfg.SSEMaxDurationSeconds, err = parseIntOrDefault("SSE_MAX_DURATION_SECONDS", 300)
	if err != nil {
		return cfg, err
	}

	cfg.SSEMaxConcurrentStreams, err = parseIntOrDefault("SSE_MAX_CONCURRENT_STREAMS", 10)
	if err != nil {
		return cfg, err
	}

	// AI embedding configuration
	cfg.AIAPIURL = os.Getenv("AI_API_URL")
	cfg.AIAPIKey = os.Getenv("AI_API_KEY")
	cfg.AIEmbeddingModel = os.Getenv("AI_API_EMBEDDING_MODEL")

	cfg.AIEmbeddingDimensions, err = parseIntOrDefault("AI_API_EMBEDDING_DIMENSIONS", 0)
	if err != nil {
		return cfg, err
	}

	cfg.AIEmbeddingTimeoutSeconds, err = parseIntOrDefault("AI_API_EMBEDDING_TIMEOUT_SECONDS", 30)
	if err != nil {
		return cfg, err
	}
	if cfg.AIEmbeddingDimensions > 0 {
		cfg.EmbeddingDimensions = cfg.AIEmbeddingDimensions
	} else {
		cfg.EmbeddingDimensions = 1536
	}

	// Knowledge-pipeline knobs (AC-X3)
	verifierAPIURLSet := strings.TrimSpace(os.Getenv("AI_VERIFIER_API_URL")) != ""
	verifierAPIKeySet := strings.TrimSpace(os.Getenv("AI_VERIFIER_API_KEY")) != ""
	cfg.AIVerifierAPIURL = os.Getenv("AI_VERIFIER_API_URL")
	if cfg.AIVerifierAPIURL == "" {
		cfg.AIVerifierAPIURL = cfg.AIAPIURL
	}
	cfg.AIVerifierAPIKey = os.Getenv("AI_VERIFIER_API_KEY")
	if cfg.AIVerifierAPIKey == "" && !verifierAPIURLSet {
		cfg.AIVerifierAPIKey = cfg.AIAPIKey
	}
	cfg.AIVerifierModel = getEnvOrDefault("AI_VERIFIER_MODEL", "gpt-4o-mini")

	cfg.AIVerifierTimeoutSeconds, err = parseIntOrDefault("AI_VERIFIER_TIMEOUT_SECONDS", 60)
	if err != nil {
		return cfg, err
	}

	cfg.AIVerifierMaxConcurrency, err = parseIntOrDefault("AI_VERIFIER_MAX_CONCURRENCY", 5)
	if err != nil {
		return cfg, err
	}

	cfg.ClaimWriteRateLimit, err = parseIntOrDefault("CLAIM_WRITE_RATE_LIMIT", 60)
	if err != nil {
		return cfg, err
	}

	cfg.ClaimReadRateLimit, err = parseIntOrDefault("CLAIM_READ_RATE_LIMIT", 300)
	if err != nil {
		return cfg, err
	}

	cfg.RecallValidatedClaimWeight, err = parseFloatOrDefault("RECALL_VALIDATED_CLAIM_WEIGHT", 0.5)
	if err != nil {
		return cfg, err
	}

	cfg.PromoteTxTimeoutSeconds, err = parseIntOrDefault("PROMOTE_TX_TIMEOUT_SECONDS", 10)
	if err != nil {
		return cfg, err
	}

	cfg.AICommunityMaxNodes, err = parseIntOrDefault("AI_COMMUNITY_MAX_NODES", 500000)
	if err != nil {
		return cfg, err
	}

	cfg.ControlHTTPAddr = getEnvOrDefault("CONTROL_HTTP_ADDR", ":8090")
	cfg.ControlPortalToken = os.Getenv("CONTROL_PORTAL_TOKEN")

	// Validation
	if cfg.PostgresDSN == "" {
		return cfg, &ValidationError{
			Field:   "POSTGRES_DSN",
			Message: "required field is empty",
		}
	}

	if cfg.Neo4jURI == "" {
		return cfg, &ValidationError{
			Field:   "NEO4J_URI",
			Message: "required field is empty",
		}
	}

	if cfg.Neo4jUser == "" {
		return cfg, &ValidationError{
			Field:   "NEO4J_USER",
			Message: "required field is empty",
		}
	}

	if cfg.Neo4jPassword == "" {
		return cfg, &ValidationError{
			Field:   "NEO4J_PASSWORD",
			Message: "required field is empty",
		}
	}

	// Validate numeric limits > 0
	numericFields := []struct {
		name  string
		value int
	}{
		{"HTTP_MAX_BODY_BYTES", cfg.HTTPMaxBodyBytes},
		{"AUTH_VERIFY_MAX_CONCURRENCY", cfg.AuthVerifyMaxConcurrency},
		{"GRAPH_QUERY_DEFAULT_TIMEOUT_SECONDS", cfg.GraphQueryDefaultTimeoutSeconds},
		{"GRAPH_QUERY_MAX_TIMEOUT_SECONDS", cfg.GraphQueryMaxTimeoutSeconds},
		{"RATE_LIMIT_PER_MINUTE", cfg.RateLimitPerMinute},
		{"SSE_HEARTBEAT_SECONDS", cfg.SSEHeartbeatSeconds},
		{"SSE_MAX_DURATION_SECONDS", cfg.SSEMaxDurationSeconds},
		{"SSE_MAX_CONCURRENT_STREAMS", cfg.SSEMaxConcurrentStreams},
		{"AI_VERIFIER_TIMEOUT_SECONDS", cfg.AIVerifierTimeoutSeconds},
		{"AI_VERIFIER_MAX_CONCURRENCY", cfg.AIVerifierMaxConcurrency},
		{"CLAIM_WRITE_RATE_LIMIT", cfg.ClaimWriteRateLimit},
		{"CLAIM_READ_RATE_LIMIT", cfg.ClaimReadRateLimit},
		{"PROMOTE_TX_TIMEOUT_SECONDS", cfg.PromoteTxTimeoutSeconds},
		{"AI_COMMUNITY_MAX_NODES", cfg.AICommunityMaxNodes},
	}

	for _, field := range numericFields {
		if field.value <= 0 {
			return cfg, &ValidationError{
				Field:   field.name,
				Message: fmt.Sprintf("must be greater than 0, got %d", field.value),
			}
		}
	}

	if cfg.GraphQueryDefaultTimeoutSeconds > cfg.GraphQueryMaxTimeoutSeconds {
		return cfg, &ValidationError{
			Field:   "GRAPH_QUERY_DEFAULT_TIMEOUT_SECONDS",
			Message: fmt.Sprintf("must be less than or equal to GRAPH_QUERY_MAX_TIMEOUT_SECONDS, got %d > %d", cfg.GraphQueryDefaultTimeoutSeconds, cfg.GraphQueryMaxTimeoutSeconds),
		}
	}

	// RecallValidatedClaimWeight must be in [0, 1]
	if cfg.RecallValidatedClaimWeight < 0 || cfg.RecallValidatedClaimWeight > 1 {
		return cfg, &ValidationError{
			Field:   "RECALL_VALIDATED_CLAIM_WEIGHT",
			Message: fmt.Sprintf("must be between 0 and 1, got %f", cfg.RecallValidatedClaimWeight),
		}
	}

	if verifierAPIURLSet && !verifierAPIKeySet {
		return cfg, &ValidationError{
			Field:   "AI_VERIFIER_API_KEY",
			Message: "required when AI_VERIFIER_API_URL is set",
		}
	}
	if verifierAPIKeySet && strings.TrimSpace(cfg.AIVerifierAPIURL) == "" {
		return cfg, &ValidationError{
			Field:   "AI_VERIFIER_API_URL",
			Message: "required when AI_VERIFIER_API_KEY is set and AI_API_URL is empty",
		}
	}

	// AI embedding configuration validation: all-or-nothing
	// If any of URL, Key, Model, Dimensions is set, all must be set
	hasAIAPIURL := cfg.AIAPIURL != ""
	hasAIAPIKey := cfg.AIAPIKey != ""
	hasAIEmbeddingModel := cfg.AIEmbeddingModel != ""
	hasAIEmbeddingDimensions := cfg.AIEmbeddingDimensions > 0

	if hasAIAPIURL || hasAIAPIKey || hasAIEmbeddingModel || hasAIEmbeddingDimensions {
		if !hasAIAPIURL {
			return cfg, &ValidationError{
				Field:   "AI_API_URL",
				Message: "required for embedding configuration (all-or-nothing)",
			}
		}
		if !hasAIAPIKey {
			return cfg, &ValidationError{
				Field:   "AI_API_KEY",
				Message: "required for embedding configuration (all-or-nothing)",
			}
		}
		if !hasAIEmbeddingModel {
			return cfg, &ValidationError{
				Field:   "AI_API_EMBEDDING_MODEL",
				Message: "required for embedding configuration (all-or-nothing)",
			}
		}
		if !hasAIEmbeddingDimensions {
			return cfg, &ValidationError{
				Field:   "AI_API_EMBEDDING_DIMENSIONS",
				Message: "required for embedding configuration (all-or-nothing)",
			}
		}
	}

	return cfg, nil
}
