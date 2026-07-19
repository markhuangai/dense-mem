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

	V2BootModeOff     = "off"
	V2BootModeDormant = "dormant"
	V2BootModeUAT     = "uat"
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
	GetSkillPackImportHistoryDays() int
	GetAICommunityMaxNodes() int
	GetControlHTTPAddr() string
	GetControlPortalToken() string
}

type aiVerifierTemperatureConfig interface {
	GetAIVerifierDisableTemperature() bool
}

// AIVerifierTemperatureDisabled returns true when a config provider exposes
// the verifier temperature omission flag.
func AIVerifierTemperatureDisabled(cfg ConfigProvider) bool {
	if cfg == nil {
		return false
	}
	temperatureConfig, ok := cfg.(aiVerifierTemperatureConfig)
	return ok && temperatureConfig.GetAIVerifierDisableTemperature()
}

// Config holds all configuration for the application.
// All fields are populated from environment variables with sensible defaults.
type Config struct {
	PostgresDSN                     string `json:"-"`
	PostgresMaxOpenConns            int
	PostgresMaxIdleConns            int
	PostgresConnMaxLifetimeSeconds  int
	Neo4jURI                        string
	Neo4jUser                       string
	Neo4jPassword                   string `json:"-"`
	Neo4jDatabase                   string
	RedisAddr                       string
	RedisPassword                   string `json:"-"`
	RedisDB                         int
	RedisTLSEnabled                 bool
	DistributedCoordinationRequired bool
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
	AIVerifierAPIURL             string
	AIVerifierAPIKey             string `json:"-"`
	AIVerifierModel              string
	AIVerifierDisableTemperature bool
	AIVerifierTimeoutSeconds     int
	AIVerifierMaxConcurrency     int
	ClaimWriteRateLimit          int
	ClaimReadRateLimit           int
	RecallValidatedClaimWeight   float64
	PromoteTxTimeoutSeconds      int
	SkillPackImportHistoryDays   int
	AICommunityMaxNodes          int
	ControlHTTPAddr              string
	ControlPortalToken           string `json:"-"`
	V2BootMode                   string
	V2LegacyMigrationRequired    bool
	TelemetryEnabled             bool
	TelemetryPrometheusURL       string
	TelemetryPrometheusJob       string
	TelemetryQueryTimeoutSeconds int
	TelemetryScrapeToken         string `json:"-"`
}

// Ensure Config implements ConfigProvider
var _ ConfigProvider = (*Config)(nil)

// Getters for ConfigProvider interface
func (c *Config) GetPostgresDSN() string                 { return c.PostgresDSN }
func (c *Config) GetPostgresMaxOpenConns() int           { return c.PostgresMaxOpenConns }
func (c *Config) GetPostgresMaxIdleConns() int           { return c.PostgresMaxIdleConns }
func (c *Config) GetPostgresConnMaxLifetimeSeconds() int { return c.PostgresConnMaxLifetimeSeconds }
func (c *Config) GetNeo4jURI() string                    { return c.Neo4jURI }
func (c *Config) GetNeo4jUser() string                   { return c.Neo4jUser }
func (c *Config) GetNeo4jPassword() string               { return c.Neo4jPassword }
func (c *Config) GetNeo4jDatabase() string               { return c.Neo4jDatabase }
func (c *Config) GetRedisAddr() string                   { return c.RedisAddr }
func (c *Config) GetRedisPassword() string               { return c.RedisPassword }
func (c *Config) GetRedisDB() int                        { return c.RedisDB }
func (c *Config) GetRedisTLSEnabled() bool               { return c.RedisTLSEnabled }
func (c *Config) GetDistributedCoordinationRequired() bool {
	return c.DistributedCoordinationRequired
}
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
func (c *Config) GetAIVerifierDisableTemperature() bool {
	return c.AIVerifierDisableTemperature
}
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
func (c *Config) GetMemoryPackImportHistoryDays() int    { return c.SkillPackImportHistoryDays }
func (c *Config) GetSkillPackImportHistoryDays() int     { return c.SkillPackImportHistoryDays }
func (c *Config) GetAICommunityMaxNodes() int            { return c.AICommunityMaxNodes }
func (c *Config) GetControlHTTPAddr() string             { return c.ControlHTTPAddr }
func (c *Config) GetControlPortalToken() string          { return c.ControlPortalToken }
func (c *Config) GetV2BootMode() string                  { return c.V2BootMode }
func (c *Config) GetV2LegacyMigrationRequired() bool     { return c.V2LegacyMigrationRequired }
func (c *Config) GetTelemetryEnabled() bool              { return c.TelemetryEnabled }
func (c *Config) GetTelemetryPrometheusURL() string      { return c.TelemetryPrometheusURL }
func (c *Config) GetTelemetryPrometheusJob() string      { return c.TelemetryPrometheusJob }
func (c *Config) GetTelemetryQueryTimeoutSeconds() int {
	if c.TelemetryQueryTimeoutSeconds > 0 {
		return c.TelemetryQueryTimeoutSeconds
	}
	return 5
}
func (c *Config) GetTelemetryScrapeToken() string { return c.TelemetryScrapeToken }

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

func (c *Config) IsV2BootEnabled() bool {
	switch strings.TrimSpace(c.V2BootMode) {
	case V2BootModeDormant, V2BootModeUAT:
		return true
	default:
		return false
	}
}

func (c *Config) validateV2BootMode() error {
	mode := strings.TrimSpace(c.V2BootMode)
	switch mode {
	case "", V2BootModeOff, V2BootModeDormant, V2BootModeUAT:
	default:
		return &ValidationError{
			Field:   "V2_BOOT_MODE",
			Message: fmt.Sprintf("unsupported value %q", c.V2BootMode),
		}
	}
	if c.V2LegacyMigrationRequired && !c.IsV2BootEnabled() {
		return &ValidationError{
			Field:   "V2_LEGACY_MIGRATION_REQUIRED",
			Message: "requires V2_BOOT_MODE=dormant or V2_BOOT_MODE=uat",
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

func parseMemoryPackImportHistoryDays(defaultValue int) (int, error) {
	if strings.TrimSpace(os.Getenv("MEMORY_PACK_IMPORT_HISTORY_DAYS")) != "" {
		return parseIntOrDefault("MEMORY_PACK_IMPORT_HISTORY_DAYS", defaultValue)
	}
	return parseIntOrDefault("SKILL_PACK_IMPORT_HISTORY_DAYS", defaultValue)
}

type intEnvSpec struct {
	key          string
	defaultValue int
	apply        func(*Config, int)
}

func applyIntEnvSpecs(cfg *Config, specs []intEnvSpec) error {
	for _, spec := range specs {
		value, err := parseIntOrDefault(spec.key, spec.defaultValue)
		if err != nil {
			return err
		}
		spec.apply(cfg, value)
	}
	return nil
}

// Load reads configuration from environment variables and returns a Config.
// Returns a typed ValidationError for any validation failures.
func Load() (Config, error) {
	cfg := Config{}
	var err error

	// String fields with defaults
	cfg.PostgresDSN = os.Getenv("POSTGRES_DSN")
	if strings.TrimSpace(os.Getenv("POSTGRES_READ_DSN")) != "" {
		return cfg, &ValidationError{
			Field:   "POSTGRES_READ_DSN",
			Message: "replica reads are not supported in this release",
		}
	}
	cfg.Neo4jURI = os.Getenv("NEO4J_URI")
	cfg.Neo4jUser = os.Getenv("NEO4J_USER")
	cfg.Neo4jPassword = os.Getenv("NEO4J_PASSWORD")
	cfg.Neo4jDatabase = os.Getenv("NEO4J_DATABASE")
	cfg.RedisAddr = os.Getenv("REDIS_ADDR")
	cfg.RedisPassword = os.Getenv("REDIS_PASSWORD")
	cfg.V2BootMode = strings.TrimSpace(getEnvOrDefault("V2_BOOT_MODE", V2BootModeOff))
	cfg.V2LegacyMigrationRequired, err = parseBoolOrDefault("V2_LEGACY_MIGRATION_REQUIRED", false)
	if err != nil {
		return cfg, err
	}
	if err := cfg.validateV2BootMode(); err != nil {
		return cfg, err
	}

	// Integer fields with defaults
	// Fragment rate-limit tiers (AC-54): writes are stricter than reads because
	// a fragment create triggers an embedding call (external network + cost)
	// plus a graph write, whereas a read is a single indexed lookup.
	if err := applyIntEnvSpecs(&cfg, []intEnvSpec{
		{"REDIS_DB", 0, func(c *Config, value int) { c.RedisDB = value }},
		{"POSTGRES_MAX_OPEN_CONNS", 25, func(c *Config, value int) { c.PostgresMaxOpenConns = value }},
		{"POSTGRES_MAX_IDLE_CONNS", 10, func(c *Config, value int) { c.PostgresMaxIdleConns = value }},
		{"POSTGRES_CONN_MAX_LIFETIME_SECONDS", 1800, func(c *Config, value int) { c.PostgresConnMaxLifetimeSeconds = value }},
		{"HTTP_MAX_BODY_BYTES", 1048576, func(c *Config, value int) { c.HTTPMaxBodyBytes = value }},
		{"AUTH_VERIFY_MAX_CONCURRENCY", 8, func(c *Config, value int) { c.AuthVerifyMaxConcurrency = value }},
		{"GRAPH_QUERY_DEFAULT_TIMEOUT_SECONDS", 10, func(c *Config, value int) { c.GraphQueryDefaultTimeoutSeconds = value }},
		{"GRAPH_QUERY_MAX_TIMEOUT_SECONDS", 30, func(c *Config, value int) { c.GraphQueryMaxTimeoutSeconds = value }},
		{"RATE_LIMIT_PER_MINUTE", 100, func(c *Config, value int) { c.RateLimitPerMinute = value }},
		{"FRAGMENT_CREATE_RATE_LIMIT", 60, func(c *Config, value int) { c.FragmentCreateRateLimit = value }},
		{"FRAGMENT_READ_RATE_LIMIT", 300, func(c *Config, value int) { c.FragmentReadRateLimit = value }},
		{"SSE_HEARTBEAT_SECONDS", 30, func(c *Config, value int) { c.SSEHeartbeatSeconds = value }},
		{"SSE_MAX_DURATION_SECONDS", 300, func(c *Config, value int) { c.SSEMaxDurationSeconds = value }},
		{"SSE_MAX_CONCURRENT_STREAMS", 10, func(c *Config, value int) { c.SSEMaxConcurrentStreams = value }},
		{"AI_API_EMBEDDING_DIMENSIONS", 0, func(c *Config, value int) { c.AIEmbeddingDimensions = value }},
		{"AI_API_EMBEDDING_TIMEOUT_SECONDS", 30, func(c *Config, value int) { c.AIEmbeddingTimeoutSeconds = value }},
	}); err != nil {
		return cfg, err
	}
	cfg.RedisTLSEnabled, err = parseBoolOrDefault("REDIS_TLS_ENABLED", false)
	if err != nil {
		return cfg, err
	}
	cfg.DistributedCoordinationRequired, err = parseBoolOrDefault("DISTRIBUTED_COORDINATION_REQUIRED", false)
	if err != nil {
		return cfg, err
	}

	// AI embedding configuration
	cfg.AIAPIURL = os.Getenv("AI_API_URL")
	cfg.AIAPIKey = os.Getenv("AI_API_KEY")
	cfg.AIEmbeddingModel = os.Getenv("AI_API_EMBEDDING_MODEL")

	if cfg.AIEmbeddingDimensions > 0 {
		cfg.EmbeddingDimensions = cfg.AIEmbeddingDimensions
	} else {
		cfg.EmbeddingDimensions = 3072
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
	cfg.AIVerifierDisableTemperature, err = parseBoolOrDefault("AI_VERIFIER_DISABLE_TEMPERATURE", false)
	if err != nil {
		return cfg, err
	}

	if err := applyIntEnvSpecs(&cfg, []intEnvSpec{
		{"AI_VERIFIER_TIMEOUT_SECONDS", 60, func(c *Config, value int) { c.AIVerifierTimeoutSeconds = value }},
		{"AI_VERIFIER_MAX_CONCURRENCY", 5, func(c *Config, value int) { c.AIVerifierMaxConcurrency = value }},
		{"CLAIM_WRITE_RATE_LIMIT", 60, func(c *Config, value int) { c.ClaimWriteRateLimit = value }},
		{"CLAIM_READ_RATE_LIMIT", 300, func(c *Config, value int) { c.ClaimReadRateLimit = value }},
	}); err != nil {
		return cfg, err
	}

	cfg.RecallValidatedClaimWeight, err = parseFloatOrDefault("RECALL_VALIDATED_CLAIM_WEIGHT", 0.5)
	if err != nil {
		return cfg, err
	}

	if err := applyIntEnvSpecs(&cfg, []intEnvSpec{
		{"PROMOTE_TX_TIMEOUT_SECONDS", 10, func(c *Config, value int) { c.PromoteTxTimeoutSeconds = value }},
	}); err != nil {
		return cfg, err
	}
	cfg.SkillPackImportHistoryDays, err = parseMemoryPackImportHistoryDays(30)
	if err != nil {
		return cfg, err
	}
	if err := applyIntEnvSpecs(&cfg, []intEnvSpec{
		{"AI_COMMUNITY_MAX_NODES", 500000, func(c *Config, value int) { c.AICommunityMaxNodes = value }},
	}); err != nil {
		return cfg, err
	}

	cfg.ControlHTTPAddr = getEnvOrDefault("CONTROL_HTTP_ADDR", ":8090")
	cfg.ControlPortalToken = os.Getenv("CONTROL_PORTAL_TOKEN")
	cfg.TelemetryEnabled, err = parseBoolOrDefault("TELEMETRY_ENABLED", false)
	if err != nil {
		return cfg, err
	}
	cfg.TelemetryPrometheusURL = os.Getenv("TELEMETRY_PROMETHEUS_URL")
	cfg.TelemetryPrometheusJob = strings.TrimSpace(os.Getenv("TELEMETRY_PROMETHEUS_JOB"))
	if err := applyIntEnvSpecs(&cfg, []intEnvSpec{
		{"TELEMETRY_QUERY_TIMEOUT_SECONDS", 5, func(c *Config, value int) { c.TelemetryQueryTimeoutSeconds = value }},
	}); err != nil {
		return cfg, err
	}
	cfg.TelemetryScrapeToken = os.Getenv("TELEMETRY_SCRAPE_TOKEN")
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

	if cfg.TelemetryEnabled && strings.TrimSpace(cfg.TelemetryScrapeToken) == "" {
		return cfg, &ValidationError{
			Field:   "TELEMETRY_SCRAPE_TOKEN",
			Message: "required when TELEMETRY_ENABLED=true",
		}
	}
	if cfg.DistributedCoordinationRequired && strings.TrimSpace(cfg.RedisAddr) == "" {
		return cfg, &ValidationError{
			Field:   "DISTRIBUTED_COORDINATION_REQUIRED",
			Message: "REDIS_ADDR is required when distributed coordination is required",
		}
	}

	// Validate numeric limits > 0
	numericFields := []struct {
		name  string
		value int
	}{
		{"POSTGRES_MAX_OPEN_CONNS", cfg.PostgresMaxOpenConns},
		{"POSTGRES_MAX_IDLE_CONNS", cfg.PostgresMaxIdleConns},
		{"POSTGRES_CONN_MAX_LIFETIME_SECONDS", cfg.PostgresConnMaxLifetimeSeconds},
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
		{"MEMORY_PACK_IMPORT_HISTORY_DAYS", cfg.SkillPackImportHistoryDays},
		{"AI_COMMUNITY_MAX_NODES", cfg.AICommunityMaxNodes},
		{"TELEMETRY_QUERY_TIMEOUT_SECONDS", cfg.TelemetryQueryTimeoutSeconds},
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
	if cfg.PostgresMaxIdleConns > cfg.PostgresMaxOpenConns {
		return cfg, &ValidationError{
			Field:   "POSTGRES_MAX_IDLE_CONNS",
			Message: fmt.Sprintf("must be less than or equal to POSTGRES_MAX_OPEN_CONNS, got %d > %d", cfg.PostgresMaxIdleConns, cfg.PostgresMaxOpenConns),
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
