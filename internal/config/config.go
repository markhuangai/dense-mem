package config

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	DefaultHTTPPort                            = "8080"
	DefaultHTTPAddr                            = ":" + DefaultHTTPPort
	DefaultPostgresMigrationTimeoutSeconds     = 1800
	MaxPostgresMigrationTimeoutSeconds         = 86400
	DefaultAIEmbeddingMaxConcurrency           = 8
	DefaultEmbeddingWorkerCount                = 2
	DefaultEmbeddingBatchSize                  = 64
	MaxEmbeddingBatchSize                      = 256
	DefaultEmbeddingJobPollSeconds             = 1
	DefaultEmbeddingJobMaxAttempts             = 20
	MaxEmbeddingJobMaxAttempts                 = 100
	DefaultAIVerifierMaxConcurrency            = 5
	DefaultAIVerifierMaxInputTokens            = 200000
	DefaultAIVerifierMaxOutputTokens           = 65536
	DefaultAIVerifierMaxCandidateContextTokens = 50000
	DefaultAIVerifierMaxPredicateOptions       = 100
	MaxAIVerifierMaxPredicateOptions           = 2000
	DefaultAIVerifierTokenizer                 = "o200k_base"
	DefaultMemoryAutoWriteConfidenceThreshold  = 0.7
	DefaultMemoryPlacementWorkerCount          = 1
	DefaultMemoryPlacementPollSeconds          = 5
	DefaultConflictReviewTTLDays               = 7
	DefaultConflictReviewStartTime             = "04:00"
)

var legacyNeo4jEnvVars = []string{
	"NEO4J_URI",
	"NEO4J_USER",
	"NEO4J_PASSWORD",
	"NEO4J_DATABASE",
}

var obsoleteAssessorEnvVars = []string{
	"AI_VERIFIER_MAX_INPUT_BYTES",
	"AI_VERIFIER_MAX_OUTPUT_BYTES",
	"AI_VERIFIER_MAX_CANDIDATE_CONTEXT_BYTES",
}

// ConfigProvider is the companion interface for Config.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type ConfigProvider interface {
	GetPostgresDSN() string
	GetRedisAddr() string
	GetRedisPassword() string
	GetRedisDB() int
	GetHTTPMaxBodyBytes() int
	GetRateLimitPerMinute() int
	GetSSEHeartbeatSeconds() int
	GetSSEMaxDurationSeconds() int
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
	GetPromoteTxTimeoutSeconds() int
	GetControlHTTPAddr() string
	GetControlPortalToken() string
}

type aiVerifierTemperatureConfig interface {
	GetAIVerifierDisableTemperature() bool
}

type aiEmbeddingConcurrencyConfig interface {
	GetAIEmbeddingMaxConcurrency() int
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

// AIEmbeddingMaxConcurrency returns the process-wide embedding request limit.
func AIEmbeddingMaxConcurrency(cfg ConfigProvider) int {
	if cfg == nil {
		return DefaultAIEmbeddingMaxConcurrency
	}
	concurrencyConfig, ok := cfg.(aiEmbeddingConcurrencyConfig)
	if !ok || concurrencyConfig.GetAIEmbeddingMaxConcurrency() <= 0 {
		return DefaultAIEmbeddingMaxConcurrency
	}
	return concurrencyConfig.GetAIEmbeddingMaxConcurrency()
}

// AIVerifierMaxConcurrency returns the process-wide verifier request limit.
func AIVerifierMaxConcurrency(cfg ConfigProvider) int {
	if cfg == nil || cfg.GetAIVerifierMaxConcurrency() <= 0 {
		return DefaultAIVerifierMaxConcurrency
	}
	return cfg.GetAIVerifierMaxConcurrency()
}

// Config holds all configuration for the application.
// All fields are populated from environment variables with sensible defaults.
type Config struct {
	PostgresDSN                     string `json:"-"`
	PostgresMaxOpenConns            int
	PostgresMaxIdleConns            int
	PostgresConnMaxLifetimeSeconds  int
	PostgresMigrationTimeoutSeconds int
	RedisAddr                       string
	RedisPassword                   string `json:"-"`
	RedisDB                         int
	RedisTLSEnabled                 bool
	DistributedCoordinationRequired bool
	HTTPMaxBodyBytes                int
	AuthVerifyMaxConcurrency        int
	RateLimitPerMinute              int
	SSEHeartbeatSeconds             int
	SSEMaxDurationSeconds           int
	SSEMaxConcurrentStreams         int
	EmbeddingDimensions             int
	AIAPIURL                        string
	AIAPIKey                        string `json:"-"`
	AIEmbeddingModel                string
	AIEmbeddingDimensions           int
	AIEmbeddingTimeoutSeconds       int
	AIEmbeddingMaxConcurrency       int
	EmbeddingWorkerCount            int
	EmbeddingBatchSize              int
	EmbeddingJobPollSeconds         int
	EmbeddingJobMaxAttempts         int
	// Knowledge-pipeline knobs (AC-X3)
	AIVerifierAPIURL                      string
	AIVerifierAPIKey                      string `json:"-"`
	AIVerifierModel                       string
	AIVerifierDisableTemperature          bool
	AIVerifierTimeoutSeconds              int
	AIVerifierMaxConcurrency              int
	AIVerifierMaxInputTokens              int
	AIVerifierMaxOutputTokens             int
	AIVerifierMaxCandidateContextTokens   int
	AIVerifierMaxPredicateOptions         int
	AIVerifierTokenizer                   string
	MemoryAutoWriteConfidenceThreshold    float64
	memoryAutoWriteConfidenceThresholdSet bool
	MemoryPlacementWorkerCount            int
	MemoryPlacementPollSeconds            int
	PromoteTxTimeoutSeconds               int
	ControlHTTPAddr                       string
	ControlPortalToken                    string `json:"-"`
	TelemetryEnabled                      bool
	TelemetryPrometheusURL                string
	TelemetryPrometheusJob                string
	TelemetryQueryTimeoutSeconds          int
	TelemetryScrapeToken                  string `json:"-"`
	AppTimezone                           string
	ConflictReviewTTLDays                 int
	ConflictReviewStartTimeLocal          string
	ConflictReviewMaxConcurrency          int
	ConflictReviewBatchSize               int
	ConflictReviewLeaseSeconds            int
	ConflictReviewMaxAttempts             int
	ConflictReviewJitterSeconds           int
}

// Ensure Config implements ConfigProvider
var _ ConfigProvider = (*Config)(nil)

// Getters for ConfigProvider interface
func (c *Config) GetPostgresDSN() string                 { return c.PostgresDSN }
func (c *Config) GetPostgresMaxOpenConns() int           { return c.PostgresMaxOpenConns }
func (c *Config) GetPostgresMaxIdleConns() int           { return c.PostgresMaxIdleConns }
func (c *Config) GetPostgresConnMaxLifetimeSeconds() int { return c.PostgresConnMaxLifetimeSeconds }
func (c *Config) GetPostgresMigrationTimeoutSeconds() int {
	return c.PostgresMigrationTimeoutSeconds
}
func (c *Config) GetRedisAddr() string     { return c.RedisAddr }
func (c *Config) GetRedisPassword() string { return c.RedisPassword }
func (c *Config) GetRedisDB() int          { return c.RedisDB }
func (c *Config) GetRedisTLSEnabled() bool { return c.RedisTLSEnabled }
func (c *Config) GetDistributedCoordinationRequired() bool {
	return c.DistributedCoordinationRequired
}
func (c *Config) GetHTTPMaxBodyBytes() int          { return c.HTTPMaxBodyBytes }
func (c *Config) GetRateLimitPerMinute() int        { return c.RateLimitPerMinute }
func (c *Config) GetSSEHeartbeatSeconds() int       { return c.SSEHeartbeatSeconds }
func (c *Config) GetSSEMaxDurationSeconds() int     { return c.SSEMaxDurationSeconds }
func (c *Config) GetEmbeddingDimensions() int       { return c.EmbeddingDimensions }
func (c *Config) GetAIAPIURL() string               { return c.AIAPIURL }
func (c *Config) GetAIAPIKey() string               { return c.AIAPIKey }
func (c *Config) GetAIEmbeddingModel() string       { return c.AIEmbeddingModel }
func (c *Config) GetAIEmbeddingDimensions() int     { return c.AIEmbeddingDimensions }
func (c *Config) GetAIEmbeddingTimeoutSeconds() int { return c.AIEmbeddingTimeoutSeconds }
func (c *Config) GetAIEmbeddingMaxConcurrency() int {
	if c.AIEmbeddingMaxConcurrency <= 0 {
		return DefaultAIEmbeddingMaxConcurrency
	}
	return c.AIEmbeddingMaxConcurrency
}
func (c *Config) GetEmbeddingWorkerCount() int {
	if c.EmbeddingWorkerCount <= 0 {
		return DefaultEmbeddingWorkerCount
	}
	return c.EmbeddingWorkerCount
}
func (c *Config) GetEmbeddingBatchSize() int {
	if c.EmbeddingBatchSize <= 0 {
		return DefaultEmbeddingBatchSize
	}
	return c.EmbeddingBatchSize
}
func (c *Config) GetEmbeddingJobPollSeconds() int {
	if c.EmbeddingJobPollSeconds <= 0 {
		return DefaultEmbeddingJobPollSeconds
	}
	return c.EmbeddingJobPollSeconds
}
func (c *Config) GetEmbeddingJobMaxAttempts() int { return c.EmbeddingJobMaxAttempts }
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
func (c *Config) GetAIVerifierMaxConcurrency() int { return c.AIVerifierMaxConcurrency }
func (c *Config) GetMemoryPlacementWorkerCount() int {
	if c.MemoryPlacementWorkerCount <= 0 {
		return DefaultMemoryPlacementWorkerCount
	}
	return c.MemoryPlacementWorkerCount
}
func (c *Config) GetMemoryPlacementPollSeconds() int {
	if c.MemoryPlacementPollSeconds <= 0 {
		return DefaultMemoryPlacementPollSeconds
	}
	return c.MemoryPlacementPollSeconds
}
func (c *Config) GetPromoteTxTimeoutSeconds() int   { return c.PromoteTxTimeoutSeconds }
func (c *Config) GetControlHTTPAddr() string        { return c.ControlHTTPAddr }
func (c *Config) GetControlPortalToken() string     { return c.ControlPortalToken }
func (c *Config) GetTelemetryEnabled() bool         { return c.TelemetryEnabled }
func (c *Config) GetTelemetryPrometheusURL() string { return c.TelemetryPrometheusURL }
func (c *Config) GetTelemetryPrometheusJob() string { return c.TelemetryPrometheusJob }
func (c *Config) GetTelemetryQueryTimeoutSeconds() int {
	if c.TelemetryQueryTimeoutSeconds > 0 {
		return c.TelemetryQueryTimeoutSeconds
	}
	return 5
}
func (c *Config) GetTelemetryScrapeToken() string { return c.TelemetryScrapeToken }
func (c *Config) GetAppTimezone() string {
	if strings.TrimSpace(c.AppTimezone) == "" {
		return "Local"
	}
	return c.AppTimezone
}
func (c *Config) GetConflictReviewTTLDays() int {
	if c.ConflictReviewTTLDays <= 0 {
		return DefaultConflictReviewTTLDays
	}
	return c.ConflictReviewTTLDays
}
func (c *Config) GetConflictReviewStartTimeLocal() string {
	if strings.TrimSpace(c.ConflictReviewStartTimeLocal) == "" {
		return DefaultConflictReviewStartTime
	}
	return c.ConflictReviewStartTimeLocal
}
func (c *Config) GetConflictReviewMaxConcurrency() int {
	if c.ConflictReviewMaxConcurrency <= 0 {
		return 1
	}
	return c.ConflictReviewMaxConcurrency
}
func (c *Config) GetConflictReviewBatchSize() int {
	if c.ConflictReviewBatchSize <= 0 {
		return 100
	}
	return c.ConflictReviewBatchSize
}
func (c *Config) GetConflictReviewLeaseSeconds() int {
	if c.ConflictReviewLeaseSeconds <= 0 {
		return 300
	}
	return c.ConflictReviewLeaseSeconds
}
func (c *Config) GetConflictReviewMaxAttempts() int {
	if c.ConflictReviewMaxAttempts <= 0 {
		return 5
	}
	return c.ConflictReviewMaxAttempts
}
func (c *Config) GetConflictReviewJitterSeconds() int {
	if c.ConflictReviewJitterSeconds < 0 {
		return 0
	}
	return c.ConflictReviewJitterSeconds
}

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
		{"AI_VERIFIER_MODEL", c.AIVerifierModel},
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
	if c.AIEmbeddingDimensions > domain.MaxEmbeddingDimensions {
		return &ValidationError{
			Field:   "AI_API_EMBEDDING_DIMENSIONS",
			Message: fmt.Sprintf("must be at most %d", domain.MaxEmbeddingDimensions),
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
	return loadWithPostgresDSN("")
}

// LoadWithPostgresDSN validates the normal environment configuration while
// using a PostgreSQL DSN already resolved by an operator entry point.
func LoadWithPostgresDSN(postgresDSN string) (Config, error) {
	return loadWithPostgresDSN(strings.TrimSpace(postgresDSN))
}

func loadWithPostgresDSN(postgresDSN string) (Config, error) {
	cfg := Config{}
	var err error
	if err := rejectObsoleteAssessorConfig(); err != nil {
		return cfg, err
	}

	// String fields with defaults
	cfg.PostgresDSN = postgresDSN
	if cfg.PostgresDSN == "" {
		cfg.PostgresDSN = os.Getenv("POSTGRES_DSN")
		if cfg.PostgresDSN == "" {
			cfg.PostgresDSN, err = buildPostgresDSNFromComponents()
			if err != nil {
				return cfg, err
			}
		}
	}
	if strings.TrimSpace(os.Getenv("POSTGRES_READ_DSN")) != "" {
		return cfg, &ValidationError{
			Field:   "POSTGRES_READ_DSN",
			Message: "replica reads are not supported in this release",
		}
	}
	cfg.RedisAddr = os.Getenv("REDIS_ADDR")
	cfg.RedisPassword = os.Getenv("REDIS_PASSWORD")

	// Integer fields with defaults.
	if err := applyIntEnvSpecs(&cfg, []intEnvSpec{
		{"REDIS_DB", 0, func(c *Config, value int) { c.RedisDB = value }},
		{"POSTGRES_MAX_OPEN_CONNS", 25, func(c *Config, value int) { c.PostgresMaxOpenConns = value }},
		{"POSTGRES_MAX_IDLE_CONNS", 10, func(c *Config, value int) { c.PostgresMaxIdleConns = value }},
		{"POSTGRES_CONN_MAX_LIFETIME_SECONDS", 1800, func(c *Config, value int) { c.PostgresConnMaxLifetimeSeconds = value }},
		{"POSTGRES_MIGRATION_TIMEOUT_SECONDS", DefaultPostgresMigrationTimeoutSeconds, func(c *Config, value int) { c.PostgresMigrationTimeoutSeconds = value }},
		{"HTTP_MAX_BODY_BYTES", 1048576, func(c *Config, value int) { c.HTTPMaxBodyBytes = value }},
		{"AUTH_VERIFY_MAX_CONCURRENCY", 8, func(c *Config, value int) { c.AuthVerifyMaxConcurrency = value }},
		{"RATE_LIMIT_PER_MINUTE", 100, func(c *Config, value int) { c.RateLimitPerMinute = value }},
		{"SSE_HEARTBEAT_SECONDS", 30, func(c *Config, value int) { c.SSEHeartbeatSeconds = value }},
		{"SSE_MAX_DURATION_SECONDS", 300, func(c *Config, value int) { c.SSEMaxDurationSeconds = value }},
		{"SSE_MAX_CONCURRENT_STREAMS", 10, func(c *Config, value int) { c.SSEMaxConcurrentStreams = value }},
		{"AI_API_EMBEDDING_DIMENSIONS", 0, func(c *Config, value int) { c.AIEmbeddingDimensions = value }},
		{"AI_API_EMBEDDING_TIMEOUT_SECONDS", 30, func(c *Config, value int) { c.AIEmbeddingTimeoutSeconds = value }},
		{"AI_API_EMBEDDING_MAX_CONCURRENCY", DefaultAIEmbeddingMaxConcurrency, func(c *Config, value int) { c.AIEmbeddingMaxConcurrency = value }},
		{"EMBEDDING_WORKER_COUNT", DefaultEmbeddingWorkerCount, func(c *Config, value int) { c.EmbeddingWorkerCount = value }},
		{"EMBEDDING_BATCH_SIZE", DefaultEmbeddingBatchSize, func(c *Config, value int) { c.EmbeddingBatchSize = value }},
		{"EMBEDDING_JOB_POLL_SECONDS", DefaultEmbeddingJobPollSeconds, func(c *Config, value int) { c.EmbeddingJobPollSeconds = value }},
		{"EMBEDDING_JOB_MAX_ATTEMPTS", DefaultEmbeddingJobMaxAttempts, func(c *Config, value int) { c.EmbeddingJobMaxAttempts = value }},
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
	cfg.AIVerifierModel = os.Getenv("AI_VERIFIER_MODEL")
	cfg.AIVerifierDisableTemperature, err = parseBoolOrDefault("AI_VERIFIER_DISABLE_TEMPERATURE", false)
	if err != nil {
		return cfg, err
	}

	if err := applyIntEnvSpecs(&cfg, []intEnvSpec{
		{"AI_VERIFIER_TIMEOUT_SECONDS", 60, func(c *Config, value int) { c.AIVerifierTimeoutSeconds = value }},
		{"AI_VERIFIER_MAX_CONCURRENCY", DefaultAIVerifierMaxConcurrency, func(c *Config, value int) { c.AIVerifierMaxConcurrency = value }},
		{"AI_VERIFIER_MAX_INPUT_TOKENS", DefaultAIVerifierMaxInputTokens, func(c *Config, value int) { c.AIVerifierMaxInputTokens = value }},
		{"AI_VERIFIER_MAX_OUTPUT_TOKENS", DefaultAIVerifierMaxOutputTokens, func(c *Config, value int) { c.AIVerifierMaxOutputTokens = value }},
		{"AI_VERIFIER_MAX_CANDIDATE_CONTEXT_TOKENS", DefaultAIVerifierMaxCandidateContextTokens, func(c *Config, value int) { c.AIVerifierMaxCandidateContextTokens = value }},
		{"AI_VERIFIER_MAX_PREDICATE_OPTIONS", DefaultAIVerifierMaxPredicateOptions, func(c *Config, value int) { c.AIVerifierMaxPredicateOptions = value }},
		{"MEMORY_PLACEMENT_WORKER_COUNT", DefaultMemoryPlacementWorkerCount, func(c *Config, value int) { c.MemoryPlacementWorkerCount = value }},
		{"MEMORY_PLACEMENT_POLL_SECONDS", DefaultMemoryPlacementPollSeconds, func(c *Config, value int) { c.MemoryPlacementPollSeconds = value }},
	}); err != nil {
		return cfg, err
	}
	cfg.AIVerifierTokenizer = strings.TrimSpace(getEnvOrDefault("AI_VERIFIER_TOKENIZER", DefaultAIVerifierTokenizer))

	cfg.MemoryAutoWriteConfidenceThreshold, err = parseFloatOrDefault("MEMORY_AUTO_WRITE_CONFIDENCE_THRESHOLD", DefaultMemoryAutoWriteConfidenceThreshold)
	if err != nil {
		return cfg, err
	}
	cfg.memoryAutoWriteConfidenceThresholdSet = true
	if err := applyIntEnvSpecs(&cfg, []intEnvSpec{
		{"PROMOTE_TX_TIMEOUT_SECONDS", 10, func(c *Config, value int) { c.PromoteTxTimeoutSeconds = value }},
	}); err != nil {
		return cfg, err
	}
	cfg.AppTimezone = getEnvOrDefault("APP_TIMEZONE", "Local")
	cfg.ConflictReviewStartTimeLocal = getEnvOrDefault("CONFLICT_REVIEW_START_TIME_LOCAL", DefaultConflictReviewStartTime)
	if err := applyIntEnvSpecs(&cfg, []intEnvSpec{
		{"CONFLICT_REVIEW_TTL_DAYS", DefaultConflictReviewTTLDays, func(c *Config, value int) { c.ConflictReviewTTLDays = value }},
		{"CONFLICT_REVIEW_MAX_CONCURRENCY", 1, func(c *Config, value int) { c.ConflictReviewMaxConcurrency = value }},
		{"CONFLICT_REVIEW_BATCH_SIZE", 100, func(c *Config, value int) { c.ConflictReviewBatchSize = value }},
		{"CONFLICT_REVIEW_LEASE_SECONDS", 300, func(c *Config, value int) { c.ConflictReviewLeaseSeconds = value }},
		{"CONFLICT_REVIEW_MAX_ATTEMPTS", 5, func(c *Config, value int) { c.ConflictReviewMaxAttempts = value }},
		{"CONFLICT_REVIEW_JITTER_SECONDS", 600, func(c *Config, value int) { c.ConflictReviewJitterSeconds = value }},
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
	if err := rejectLegacyNeo4jConfig(); err != nil {
		return cfg, err
	}
	if cfg.PostgresDSN == "" {
		return cfg, &ValidationError{
			Field:   "POSTGRES_DSN",
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
		{"POSTGRES_MIGRATION_TIMEOUT_SECONDS", cfg.PostgresMigrationTimeoutSeconds},
		{"HTTP_MAX_BODY_BYTES", cfg.HTTPMaxBodyBytes},
		{"AUTH_VERIFY_MAX_CONCURRENCY", cfg.AuthVerifyMaxConcurrency},
		{"RATE_LIMIT_PER_MINUTE", cfg.RateLimitPerMinute},
		{"SSE_HEARTBEAT_SECONDS", cfg.SSEHeartbeatSeconds},
		{"SSE_MAX_DURATION_SECONDS", cfg.SSEMaxDurationSeconds},
		{"SSE_MAX_CONCURRENT_STREAMS", cfg.SSEMaxConcurrentStreams},
		{"AI_API_EMBEDDING_MAX_CONCURRENCY", cfg.AIEmbeddingMaxConcurrency},
		{"EMBEDDING_WORKER_COUNT", cfg.EmbeddingWorkerCount},
		{"EMBEDDING_BATCH_SIZE", cfg.EmbeddingBatchSize},
		{"EMBEDDING_JOB_POLL_SECONDS", cfg.EmbeddingJobPollSeconds},
		{"EMBEDDING_JOB_MAX_ATTEMPTS", cfg.EmbeddingJobMaxAttempts},
		{"AI_VERIFIER_TIMEOUT_SECONDS", cfg.AIVerifierTimeoutSeconds},
		{"AI_VERIFIER_MAX_CONCURRENCY", cfg.AIVerifierMaxConcurrency},
		{"AI_VERIFIER_MAX_INPUT_TOKENS", cfg.AIVerifierMaxInputTokens},
		{"AI_VERIFIER_MAX_OUTPUT_TOKENS", cfg.AIVerifierMaxOutputTokens},
		{"AI_VERIFIER_MAX_CANDIDATE_CONTEXT_TOKENS", cfg.AIVerifierMaxCandidateContextTokens},
		{"AI_VERIFIER_MAX_PREDICATE_OPTIONS", cfg.AIVerifierMaxPredicateOptions},
		{"MEMORY_PLACEMENT_WORKER_COUNT", cfg.MemoryPlacementWorkerCount},
		{"MEMORY_PLACEMENT_POLL_SECONDS", cfg.MemoryPlacementPollSeconds},
		{"PROMOTE_TX_TIMEOUT_SECONDS", cfg.PromoteTxTimeoutSeconds},
		{"CONFLICT_REVIEW_TTL_DAYS", cfg.ConflictReviewTTLDays},
		{"CONFLICT_REVIEW_MAX_CONCURRENCY", cfg.ConflictReviewMaxConcurrency},
		{"CONFLICT_REVIEW_BATCH_SIZE", cfg.ConflictReviewBatchSize},
		{"CONFLICT_REVIEW_LEASE_SECONDS", cfg.ConflictReviewLeaseSeconds},
		{"CONFLICT_REVIEW_MAX_ATTEMPTS", cfg.ConflictReviewMaxAttempts},
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
	if cfg.EmbeddingJobMaxAttempts > MaxEmbeddingJobMaxAttempts {
		return cfg, &ValidationError{
			Field:   "EMBEDDING_JOB_MAX_ATTEMPTS",
			Message: fmt.Sprintf("must be less than or equal to %d, got %d", MaxEmbeddingJobMaxAttempts, cfg.EmbeddingJobMaxAttempts),
		}
	}
	if cfg.EmbeddingBatchSize > MaxEmbeddingBatchSize {
		return cfg, &ValidationError{
			Field:   "EMBEDDING_BATCH_SIZE",
			Message: fmt.Sprintf("must be less than or equal to %d, got %d", MaxEmbeddingBatchSize, cfg.EmbeddingBatchSize),
		}
	}
	if cfg.EmbeddingWorkerCount > cfg.AIEmbeddingMaxConcurrency {
		return cfg, &ValidationError{
			Field:   "EMBEDDING_WORKER_COUNT",
			Message: fmt.Sprintf("must be less than or equal to AI_API_EMBEDDING_MAX_CONCURRENCY, got %d > %d", cfg.EmbeddingWorkerCount, cfg.AIEmbeddingMaxConcurrency),
		}
	}
	if cfg.MemoryPlacementWorkerCount > cfg.AIVerifierMaxConcurrency {
		return cfg, &ValidationError{
			Field:   "MEMORY_PLACEMENT_WORKER_COUNT",
			Message: fmt.Sprintf("must be less than or equal to AI_VERIFIER_MAX_CONCURRENCY, got %d > %d", cfg.MemoryPlacementWorkerCount, cfg.AIVerifierMaxConcurrency),
		}
	}
	if cfg.AIVerifierMaxCandidateContextTokens > cfg.AIVerifierMaxInputTokens {
		return cfg, &ValidationError{
			Field:   "AI_VERIFIER_MAX_CANDIDATE_CONTEXT_TOKENS",
			Message: fmt.Sprintf("must be less than or equal to AI_VERIFIER_MAX_INPUT_TOKENS, got %d > %d", cfg.AIVerifierMaxCandidateContextTokens, cfg.AIVerifierMaxInputTokens),
		}
	}
	if cfg.AIVerifierMaxPredicateOptions > MaxAIVerifierMaxPredicateOptions {
		return cfg, &ValidationError{
			Field:   "AI_VERIFIER_MAX_PREDICATE_OPTIONS",
			Message: fmt.Sprintf("must be less than or equal to %d, got %d", MaxAIVerifierMaxPredicateOptions, cfg.AIVerifierMaxPredicateOptions),
		}
	}
	if math.IsNaN(cfg.MemoryAutoWriteConfidenceThreshold) ||
		math.IsInf(cfg.MemoryAutoWriteConfidenceThreshold, 0) ||
		cfg.MemoryAutoWriteConfidenceThreshold < 0 ||
		cfg.MemoryAutoWriteConfidenceThreshold > 1 {
		return cfg, &ValidationError{
			Field:   "MEMORY_AUTO_WRITE_CONFIDENCE_THRESHOLD",
			Message: "must be between 0 and 1",
		}
	}
	if !supportedAIVerifierTokenizer(cfg.AIVerifierTokenizer) {
		return cfg, &ValidationError{
			Field:   "AI_VERIFIER_TOKENIZER",
			Message: fmt.Sprintf("unsupported tokenizer %q", cfg.AIVerifierTokenizer),
		}
	}
	if cfg.ConflictReviewTTLDays > 30 {
		return cfg, &ValidationError{
			Field:   "CONFLICT_REVIEW_TTL_DAYS",
			Message: fmt.Sprintf("must be less than or equal to 30, got %d", cfg.ConflictReviewTTLDays),
		}
	}
	if cfg.ConflictReviewMaxConcurrency > 16 {
		return cfg, &ValidationError{
			Field:   "CONFLICT_REVIEW_MAX_CONCURRENCY",
			Message: fmt.Sprintf("must be less than or equal to 16, got %d", cfg.ConflictReviewMaxConcurrency),
		}
	}
	if cfg.ConflictReviewBatchSize > 500 {
		return cfg, &ValidationError{
			Field:   "CONFLICT_REVIEW_BATCH_SIZE",
			Message: fmt.Sprintf("must be less than or equal to 500, got %d", cfg.ConflictReviewBatchSize),
		}
	}
	if cfg.ConflictReviewLeaseSeconds < 30 || cfg.ConflictReviewLeaseSeconds > 1800 {
		return cfg, &ValidationError{
			Field:   "CONFLICT_REVIEW_LEASE_SECONDS",
			Message: fmt.Sprintf("must be between 30 and 1800, got %d", cfg.ConflictReviewLeaseSeconds),
		}
	}
	if cfg.ConflictReviewMaxAttempts > 20 {
		return cfg, &ValidationError{
			Field:   "CONFLICT_REVIEW_MAX_ATTEMPTS",
			Message: fmt.Sprintf("must be less than or equal to 20, got %d", cfg.ConflictReviewMaxAttempts),
		}
	}
	if cfg.ConflictReviewJitterSeconds < 0 || cfg.ConflictReviewJitterSeconds > 3600 {
		return cfg, &ValidationError{
			Field:   "CONFLICT_REVIEW_JITTER_SECONDS",
			Message: fmt.Sprintf("must be between 0 and 3600, got %d", cfg.ConflictReviewJitterSeconds),
		}
	}
	if _, err := time.Parse("15:04", cfg.ConflictReviewStartTimeLocal); err != nil {
		return cfg, &ValidationError{
			Field:   "CONFLICT_REVIEW_START_TIME_LOCAL",
			Message: fmt.Sprintf("invalid HH:MM value: %s", cfg.ConflictReviewStartTimeLocal),
		}
	}
	if _, err := time.LoadLocation(cfg.GetAppTimezone()); err != nil {
		return cfg, &ValidationError{
			Field:   "APP_TIMEZONE",
			Message: fmt.Sprintf("invalid timezone: %s", cfg.GetAppTimezone()),
		}
	}

	if cfg.PostgresMaxIdleConns > cfg.PostgresMaxOpenConns {
		return cfg, &ValidationError{
			Field:   "POSTGRES_MAX_IDLE_CONNS",
			Message: fmt.Sprintf("must be less than or equal to POSTGRES_MAX_OPEN_CONNS, got %d > %d", cfg.PostgresMaxIdleConns, cfg.PostgresMaxOpenConns),
		}
	}
	if cfg.PostgresMigrationTimeoutSeconds > MaxPostgresMigrationTimeoutSeconds {
		return cfg, &ValidationError{
			Field:   "POSTGRES_MIGRATION_TIMEOUT_SECONDS",
			Message: fmt.Sprintf("must be less than or equal to %d, got %d", MaxPostgresMigrationTimeoutSeconds, cfg.PostgresMigrationTimeoutSeconds),
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
		if cfg.AIEmbeddingDimensions > domain.MaxEmbeddingDimensions {
			return cfg, &ValidationError{
				Field:   "AI_API_EMBEDDING_DIMENSIONS",
				Message: fmt.Sprintf("must be at most %d", domain.MaxEmbeddingDimensions),
			}
		}
	}

	return cfg, nil
}

func buildPostgresDSNFromComponents() (string, error) {
	host := strings.TrimSpace(os.Getenv("POSTGRES_HOST"))
	user := os.Getenv("POSTGRES_USER")
	password := os.Getenv("POSTGRES_PASSWORD")
	database := os.Getenv("POSTGRES_DB")
	if host == "" && user == "" && password == "" && database == "" {
		return "", nil
	}
	missing := []struct {
		field string
		value string
	}{
		{"POSTGRES_HOST", host},
		{"POSTGRES_USER", user},
		{"POSTGRES_PASSWORD", password},
		{"POSTGRES_DB", database},
	}
	for _, item := range missing {
		if item.value == "" {
			return "", &ValidationError{Field: item.field, Message: "required when POSTGRES_DSN is not set"}
		}
	}
	port := strings.TrimSpace(os.Getenv("POSTGRES_PORT"))
	if port == "" {
		port = "5432"
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	dsnURL := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     net.JoinHostPort(host, port),
		Path:     "/" + strings.TrimPrefix(database, "/"),
		RawQuery: url.Values{"sslmode": []string{getEnvOrDefault("POSTGRES_SSLMODE", "disable")}}.Encode(),
	}
	return dsnURL.String(), nil
}

func rejectLegacyNeo4jConfig() *ValidationError {
	for _, name := range legacyNeo4jEnvVars {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return &ValidationError{
				Field:   name,
				Message: "legacy Neo4j configuration is no longer supported; run v2.1.2 to complete migration before upgrading",
			}
		}
	}
	return nil
}

func rejectObsoleteAssessorConfig() *ValidationError {
	for _, raw := range os.Environ() {
		name, value, found := strings.Cut(raw, "=")
		if found && strings.HasPrefix(name, "AI_ASSESSOR_") && strings.TrimSpace(value) != "" {
			return &ValidationError{
				Field:   name,
				Message: "AI_ASSESSOR_* configuration is unsupported; use AI_VERIFIER_*",
			}
		}
	}
	for _, name := range obsoleteAssessorEnvVars {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return &ValidationError{
				Field:   name,
				Message: "byte budgets are unsupported; use the corresponding AI_VERIFIER_*_TOKENS setting",
			}
		}
	}
	return nil
}

func supportedAIVerifierTokenizer(value string) bool {
	switch strings.TrimSpace(value) {
	case "o200k_base", "cl100k_base", "p50k_base", "p50k_edit", "r50k_base":
		return true
	default:
		return false
	}
}
