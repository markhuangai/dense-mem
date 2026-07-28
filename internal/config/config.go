package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultHTTPPort                        = "8080"
	DefaultHTTPAddr                        = ":" + DefaultHTTPPort
	DefaultPostgresMigrationTimeoutSeconds = 1800
	MaxPostgresMigrationTimeoutSeconds     = 86400
	DefaultAIEmbeddingMaxConcurrency       = 8
	DefaultEmbeddingWorkerCount            = 2
	DefaultEmbeddingBatchSize              = 64
	MaxEmbeddingBatchSize                  = 256
	DefaultEmbeddingJobPollSeconds         = 1
	DefaultEmbeddingJobMaxAttempts         = 20
	MaxEmbeddingJobMaxAttempts             = 100
	DefaultAIVerifierMaxConcurrency        = 5
	DefaultMemoryPlacementWorkerCount      = 1
	DefaultMemoryPlacementPollSeconds      = 5
	DefaultConflictReviewTTLDays           = 7
	DefaultConflictReviewStartTime         = "04:00"
)

var legacyNeo4jEnvVars = []string{
	"NEO4J_URI",
	"NEO4J_USER",
	"NEO4J_PASSWORD",
	"NEO4J_DATABASE",
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
	GetAIReviewerModel() string
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
	AIEmbeddingMaxConcurrency       int
	EmbeddingWorkerCount            int
	EmbeddingBatchSize              int
	EmbeddingJobPollSeconds         int
	EmbeddingJobMaxAttempts         int
	// Knowledge-pipeline knobs (AC-X3)
	AIVerifierAPIURL             string
	AIVerifierAPIKey             string `json:"-"`
	AIReviewerModel              string
	AIVerifierModel              string
	AIVerifierDisableTemperature bool
	AIVerifierTimeoutSeconds     int
	AIVerifierMaxConcurrency     int
	MemoryPlacementWorkerCount   int
	MemoryPlacementPollSeconds   int
	ClaimWriteRateLimit          int
	ClaimReadRateLimit           int
	RecallValidatedClaimWeight   float64
	PromoteTxTimeoutSeconds      int
	SkillPackImportHistoryDays   int
	AICommunityMaxNodes          int
	ControlHTTPAddr              string
	ControlPortalToken           string `json:"-"`
	TelemetryEnabled             bool
	TelemetryPrometheusURL       string
	TelemetryPrometheusJob       string
	TelemetryQueryTimeoutSeconds int
	TelemetryScrapeToken         string `json:"-"`
	AppTimezone                  string
	ConflictReviewTTLDays        int
	ConflictReviewStartTimeLocal string
	ConflictReviewMaxConcurrency int
	ConflictReviewBatchSize      int
	ConflictReviewLeaseSeconds   int
	ConflictReviewMaxAttempts    int
	ConflictReviewJitterSeconds  int
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
func (c *Config) GetAIReviewerModel() string { return c.AIReviewerModel }
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
func (c *Config) GetClaimWriteRateLimit() int            { return c.ClaimWriteRateLimit }
func (c *Config) GetClaimReadRateLimit() int             { return c.ClaimReadRateLimit }
func (c *Config) GetRecallValidatedClaimWeight() float64 { return c.RecallValidatedClaimWeight }
func (c *Config) GetPromoteTxTimeoutSeconds() int        { return c.PromoteTxTimeoutSeconds }
func (c *Config) GetMemoryPackImportHistoryDays() int    { return c.SkillPackImportHistoryDays }
func (c *Config) GetSkillPackImportHistoryDays() int     { return c.SkillPackImportHistoryDays }
func (c *Config) GetAICommunityMaxNodes() int            { return c.AICommunityMaxNodes }
func (c *Config) GetControlHTTPAddr() string             { return c.ControlHTTPAddr }
func (c *Config) GetControlPortalToken() string          { return c.ControlPortalToken }
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
		{"AI_REVIEWER_MODEL", c.AIReviewerModel},
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
	cfg.RedisAddr = os.Getenv("REDIS_ADDR")
	cfg.RedisPassword = os.Getenv("REDIS_PASSWORD")

	// Integer fields with defaults
	// Fragment rate-limit tiers (AC-54): writes are stricter than reads because
	// a fragment create triggers an embedding call (external network + cost)
	// plus a graph write, whereas a read is a single indexed lookup.
	if err := applyIntEnvSpecs(&cfg, []intEnvSpec{
		{"REDIS_DB", 0, func(c *Config, value int) { c.RedisDB = value }},
		{"POSTGRES_MAX_OPEN_CONNS", 25, func(c *Config, value int) { c.PostgresMaxOpenConns = value }},
		{"POSTGRES_MAX_IDLE_CONNS", 10, func(c *Config, value int) { c.PostgresMaxIdleConns = value }},
		{"POSTGRES_CONN_MAX_LIFETIME_SECONDS", 1800, func(c *Config, value int) { c.PostgresConnMaxLifetimeSeconds = value }},
		{"POSTGRES_MIGRATION_TIMEOUT_SECONDS", DefaultPostgresMigrationTimeoutSeconds, func(c *Config, value int) { c.PostgresMigrationTimeoutSeconds = value }},
		{"HTTP_MAX_BODY_BYTES", 1048576, func(c *Config, value int) { c.HTTPMaxBodyBytes = value }},
		{"AUTH_VERIFY_MAX_CONCURRENCY", 8, func(c *Config, value int) { c.AuthVerifyMaxConcurrency = value }},
		{"RATE_LIMIT_PER_MINUTE", 100, func(c *Config, value int) { c.RateLimitPerMinute = value }},
		{"FRAGMENT_CREATE_RATE_LIMIT", 60, func(c *Config, value int) { c.FragmentCreateRateLimit = value }},
		{"FRAGMENT_READ_RATE_LIMIT", 300, func(c *Config, value int) { c.FragmentReadRateLimit = value }},
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
	cfg.AIReviewerModel = os.Getenv("AI_REVIEWER_MODEL")
	cfg.AIVerifierModel = os.Getenv("AI_VERIFIER_MODEL")
	cfg.AIVerifierDisableTemperature, err = parseBoolOrDefault("AI_VERIFIER_DISABLE_TEMPERATURE", false)
	if err != nil {
		return cfg, err
	}

	if err := applyIntEnvSpecs(&cfg, []intEnvSpec{
		{"AI_VERIFIER_TIMEOUT_SECONDS", 60, func(c *Config, value int) { c.AIVerifierTimeoutSeconds = value }},
		{"AI_VERIFIER_MAX_CONCURRENCY", DefaultAIVerifierMaxConcurrency, func(c *Config, value int) { c.AIVerifierMaxConcurrency = value }},
		{"MEMORY_PLACEMENT_WORKER_COUNT", DefaultMemoryPlacementWorkerCount, func(c *Config, value int) { c.MemoryPlacementWorkerCount = value }},
		{"MEMORY_PLACEMENT_POLL_SECONDS", DefaultMemoryPlacementPollSeconds, func(c *Config, value int) { c.MemoryPlacementPollSeconds = value }},
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
		{"MEMORY_PLACEMENT_WORKER_COUNT", cfg.MemoryPlacementWorkerCount},
		{"MEMORY_PLACEMENT_POLL_SECONDS", cfg.MemoryPlacementPollSeconds},
		{"CLAIM_WRITE_RATE_LIMIT", cfg.ClaimWriteRateLimit},
		{"CLAIM_READ_RATE_LIMIT", cfg.ClaimReadRateLimit},
		{"PROMOTE_TX_TIMEOUT_SECONDS", cfg.PromoteTxTimeoutSeconds},
		{"CONFLICT_REVIEW_TTL_DAYS", cfg.ConflictReviewTTLDays},
		{"CONFLICT_REVIEW_MAX_CONCURRENCY", cfg.ConflictReviewMaxConcurrency},
		{"CONFLICT_REVIEW_BATCH_SIZE", cfg.ConflictReviewBatchSize},
		{"CONFLICT_REVIEW_LEASE_SECONDS", cfg.ConflictReviewLeaseSeconds},
		{"CONFLICT_REVIEW_MAX_ATTEMPTS", cfg.ConflictReviewMaxAttempts},
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
