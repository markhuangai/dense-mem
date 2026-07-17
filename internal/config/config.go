package config

import (
	"fmt"
	"math"
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
	PostgresDSN                     string
	PostgresMaxOpenConns            int
	PostgresMaxIdleConns            int
	PostgresConnMaxLifetimeSeconds  int
	PostgresVectorMaxConcurrency    int
	PGVectorExtensionRequired       bool
	PostgresStatementTimeoutSeconds int
	PostgresLockTimeoutSeconds      int
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
	AIEmbeddingMaxConcurrency       int
	// Knowledge-pipeline knobs (AC-X3)
	AIVerifierAPIURL                        string
	AIVerifierAPIKey                        string `json:"-"`
	AIReviewerModel                         string
	AIVerifierModel                         string
	AIVerifierDisableTemperature            bool
	AIVerifierTimeoutSeconds                int
	AIVerifierMaxConcurrency                int
	AIVerifierCooldownPollSeconds           int
	AIVerifierMaxEntityResults              int
	AIVerifierMaxRelationshipResults        int
	AIVerifierMaxInputBytes                 int
	AIVerifierMaxOutputBytes                int
	AIVerifierMaxResponseRegenerations      int
	RelationshipMatchMaxCandidates          int
	MemoryPlacementWorkerCount              int
	MemoryPlacementLeaseSeconds             int
	MemoryPlacementHeartbeatSeconds         int
	MemoryPlacementPollSeconds              int
	MemoryPlacementMaxAttempts              int
	EmbeddingWorkerCount                    int
	EmbeddingBatchSize                      int
	EmbeddingJobLeaseSeconds                int
	EmbeddingJobPollSeconds                 int
	EmbeddingJobMaxAttempts                 int
	EmbeddingJobRetryMaxSeconds             int
	EmbeddingPendingStaleSeconds            int
	SearchDocumentFormatVersion             string
	EmbeddingNormalizationVersion           string
	PGVectorDistance                        string
	PGVectorANNStrategy                     string
	PGVectorHNSWM                           int
	PGVectorHNSWEFConstruction              int
	PGVectorIndexBuildMaxConcurrency        int
	RecallRRFEnabled                        bool
	RecallRRFK                              int
	RecallRRFBranchWeights                  string
	RecallBranchPriority                    string
	RecallBranchLimitMultiplier             int
	RecallBranchLimitFloor                  int
	RecallBranchLimitMax                    int
	RecallDeterministicRerankEnabled        bool
	RecallMaxEntitySeeds                    int
	RecallDiscoveryRelationshipsPerEvidence int
	RecallMaxGraphDepth                     int
	RecallMaxEdges                          int
	RecallGraphTimeoutMilliseconds          int
	RecallRequiredBranchProfile             string
	PGVectorExactFilteredMaxRows            int
	PGVectorHNSWEFSearch                    int
	PGVectorHNSWIterativeScan               string
	PGVectorHNSWMaxScanTuples               int
	PGVectorHNSWScanMemMultiplier           int
	PredicateRegistryVersion                string
	PredicateRegistryCacheTTLSeconds        int
	PredicateUnknownAction                  string
	RedisTLSEnabled                         bool
	DistributedCoordinationRequired         bool
	RememberRateLimitPerMinute              int
	RecallRateLimitPerMinute                int
	TraceRateLimitPerMinute                 int
	ClaimWriteRateLimit                     int
	ClaimReadRateLimit                      int
	RecallValidatedClaimWeight              float64
	PromoteTxTimeoutSeconds                 int
	SkillPackImportHistoryDays              int
	AICommunityMaxNodes                     int
	ControlHTTPAddr                         string
	ControlPortalToken                      string `json:"-"`
	V2BootMode                              string
	TelemetryEnabled                        bool
	TelemetryPrometheusURL                  string
	TelemetryPrometheusJob                  string
	TelemetryQueryTimeoutSeconds            int
	TelemetryScrapeToken                    string `json:"-"`
}

// Ensure Config implements ConfigProvider
var _ ConfigProvider = (*Config)(nil)

// Getters for ConfigProvider interface
func (c *Config) GetPostgresDSN() string                  { return c.PostgresDSN }
func (c *Config) GetPostgresMaxOpenConns() int            { return c.PostgresMaxOpenConns }
func (c *Config) GetPostgresMaxIdleConns() int            { return c.PostgresMaxIdleConns }
func (c *Config) GetPostgresConnMaxLifetimeSeconds() int  { return c.PostgresConnMaxLifetimeSeconds }
func (c *Config) GetPostgresVectorMaxConcurrency() int    { return c.PostgresVectorMaxConcurrency }
func (c *Config) GetPGVectorExtensionRequired() bool      { return c.PGVectorExtensionRequired }
func (c *Config) GetPostgresStatementTimeoutSeconds() int { return c.PostgresStatementTimeoutSeconds }
func (c *Config) GetPostgresLockTimeoutSeconds() int      { return c.PostgresLockTimeoutSeconds }
func (c *Config) GetNeo4jURI() string                     { return c.Neo4jURI }
func (c *Config) GetNeo4jUser() string                    { return c.Neo4jUser }
func (c *Config) GetNeo4jPassword() string                { return c.Neo4jPassword }
func (c *Config) GetNeo4jDatabase() string                { return c.Neo4jDatabase }
func (c *Config) GetRedisAddr() string                    { return c.RedisAddr }
func (c *Config) GetRedisPassword() string                { return c.RedisPassword }
func (c *Config) GetRedisDB() int                         { return c.RedisDB }
func (c *Config) GetHTTPMaxBodyBytes() int                { return c.HTTPMaxBodyBytes }
func (c *Config) GetRateLimitPerMinute() int              { return c.RateLimitPerMinute }
func (c *Config) GetFragmentCreateRateLimit() int         { return c.FragmentCreateRateLimit }
func (c *Config) GetFragmentReadRateLimit() int           { return c.FragmentReadRateLimit }
func (c *Config) GetSSEHeartbeatSeconds() int             { return c.SSEHeartbeatSeconds }
func (c *Config) GetSSEMaxDurationSeconds() int           { return c.SSEMaxDurationSeconds }
func (c *Config) GetSSEMaxConcurrentStreams() int         { return c.SSEMaxConcurrentStreams }
func (c *Config) GetEmbeddingDimensions() int             { return c.EmbeddingDimensions }
func (c *Config) GetAIAPIURL() string                     { return c.AIAPIURL }
func (c *Config) GetAIAPIKey() string                     { return c.AIAPIKey }
func (c *Config) GetAIEmbeddingModel() string             { return c.AIEmbeddingModel }
func (c *Config) GetAIEmbeddingDimensions() int           { return c.AIEmbeddingDimensions }
func (c *Config) GetAIEmbeddingTimeoutSeconds() int       { return c.AIEmbeddingTimeoutSeconds }
func (c *Config) GetAIEmbeddingMaxConcurrency() int       { return c.AIEmbeddingMaxConcurrency }
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
func (c *Config) GetAIReviewerModel() string { return strings.TrimSpace(c.AIReviewerModel) }
func (c *Config) GetAIVerifierModel() string { return strings.TrimSpace(c.AIVerifierModel) }
func (c *Config) GetAIVerifierDisableTemperature() bool {
	return c.AIVerifierDisableTemperature
}
func (c *Config) GetAIVerifierTimeoutSeconds() int {
	if c.AIVerifierTimeoutSeconds > 0 {
		return c.AIVerifierTimeoutSeconds
	}
	return 60
}
func (c *Config) GetAIVerifierMaxConcurrency() int         { return c.AIVerifierMaxConcurrency }
func (c *Config) GetAIVerifierCooldownPollSeconds() int    { return c.AIVerifierCooldownPollSeconds }
func (c *Config) GetAIVerifierMaxEntityResults() int       { return c.AIVerifierMaxEntityResults }
func (c *Config) GetAIVerifierMaxRelationshipResults() int { return c.AIVerifierMaxRelationshipResults }
func (c *Config) GetAIVerifierMaxInputBytes() int          { return c.AIVerifierMaxInputBytes }
func (c *Config) GetAIVerifierMaxOutputBytes() int         { return c.AIVerifierMaxOutputBytes }
func (c *Config) GetAIVerifierMaxResponseRegenerations() int {
	return c.AIVerifierMaxResponseRegenerations
}
func (c *Config) GetRelationshipMatchMaxCandidates() int  { return c.RelationshipMatchMaxCandidates }
func (c *Config) GetMemoryPlacementWorkerCount() int      { return c.MemoryPlacementWorkerCount }
func (c *Config) GetMemoryPlacementLeaseSeconds() int     { return c.MemoryPlacementLeaseSeconds }
func (c *Config) GetMemoryPlacementHeartbeatSeconds() int { return c.MemoryPlacementHeartbeatSeconds }
func (c *Config) GetMemoryPlacementPollSeconds() int      { return c.MemoryPlacementPollSeconds }
func (c *Config) GetMemoryPlacementMaxAttempts() int      { return c.MemoryPlacementMaxAttempts }
func (c *Config) GetEmbeddingWorkerCount() int            { return c.EmbeddingWorkerCount }
func (c *Config) GetEmbeddingBatchSize() int              { return c.EmbeddingBatchSize }
func (c *Config) GetEmbeddingJobLeaseSeconds() int        { return c.EmbeddingJobLeaseSeconds }
func (c *Config) GetEmbeddingJobPollSeconds() int         { return c.EmbeddingJobPollSeconds }
func (c *Config) GetEmbeddingJobMaxAttempts() int         { return c.EmbeddingJobMaxAttempts }
func (c *Config) GetEmbeddingJobRetryMaxSeconds() int     { return c.EmbeddingJobRetryMaxSeconds }
func (c *Config) GetEmbeddingPendingStaleSeconds() int    { return c.EmbeddingPendingStaleSeconds }
func (c *Config) GetSearchDocumentFormatVersion() string {
	return strings.TrimSpace(c.SearchDocumentFormatVersion)
}
func (c *Config) GetEmbeddingNormalizationVersion() string {
	return strings.TrimSpace(c.EmbeddingNormalizationVersion)
}
func (c *Config) GetPGVectorDistance() string              { return c.PGVectorDistance }
func (c *Config) GetPGVectorANNStrategy() string           { return c.PGVectorANNStrategy }
func (c *Config) GetPGVectorHNSWM() int                    { return c.PGVectorHNSWM }
func (c *Config) GetPGVectorHNSWEFConstruction() int       { return c.PGVectorHNSWEFConstruction }
func (c *Config) GetPGVectorIndexBuildMaxConcurrency() int { return c.PGVectorIndexBuildMaxConcurrency }
func (c *Config) GetRecallRRFEnabled() bool                { return c.RecallRRFEnabled }
func (c *Config) GetRecallRRFK() int                       { return c.RecallRRFK }
func (c *Config) GetRecallRRFBranchWeights() string        { return c.RecallRRFBranchWeights }
func (c *Config) GetRecallBranchPriority() string          { return c.RecallBranchPriority }
func (c *Config) GetRecallBranchLimitMultiplier() int      { return c.RecallBranchLimitMultiplier }
func (c *Config) GetRecallBranchLimitFloor() int           { return c.RecallBranchLimitFloor }
func (c *Config) GetRecallBranchLimitMax() int             { return c.RecallBranchLimitMax }
func (c *Config) GetRecallDeterministicRerankEnabled() bool {
	return c.RecallDeterministicRerankEnabled
}
func (c *Config) GetRecallMaxEntitySeeds() int { return c.RecallMaxEntitySeeds }
func (c *Config) GetRecallDiscoveryRelationshipsPerEvidence() int {
	return c.RecallDiscoveryRelationshipsPerEvidence
}
func (c *Config) GetRecallMaxGraphDepth() int            { return c.RecallMaxGraphDepth }
func (c *Config) GetRecallMaxEdges() int                 { return c.RecallMaxEdges }
func (c *Config) GetRecallGraphTimeoutMilliseconds() int { return c.RecallGraphTimeoutMilliseconds }
func (c *Config) GetRecallRequiredBranchProfile() string { return c.RecallRequiredBranchProfile }
func (c *Config) GetPGVectorExactFilteredMaxRows() int   { return c.PGVectorExactFilteredMaxRows }
func (c *Config) GetPGVectorHNSWEFSearch() int           { return c.PGVectorHNSWEFSearch }
func (c *Config) GetPGVectorHNSWIterativeScan() string   { return c.PGVectorHNSWIterativeScan }
func (c *Config) GetPGVectorHNSWMaxScanTuples() int      { return c.PGVectorHNSWMaxScanTuples }
func (c *Config) GetPGVectorHNSWScanMemMultiplier() int  { return c.PGVectorHNSWScanMemMultiplier }
func (c *Config) GetPredicateRegistryVersion() string {
	return strings.TrimSpace(c.PredicateRegistryVersion)
}
func (c *Config) GetPredicateRegistryCacheTTLSeconds() int { return c.PredicateRegistryCacheTTLSeconds }
func (c *Config) GetPredicateUnknownAction() string        { return c.PredicateUnknownAction }
func (c *Config) GetRedisTLSEnabled() bool                 { return c.RedisTLSEnabled }
func (c *Config) GetDistributedCoordinationRequired() bool { return c.DistributedCoordinationRequired }
func (c *Config) GetRememberRateLimitPerMinute() int       { return c.RememberRateLimitPerMinute }
func (c *Config) GetRecallRateLimitPerMinute() int         { return c.RecallRateLimitPerMinute }
func (c *Config) GetTraceRateLimitPerMinute() int          { return c.TraceRateLimitPerMinute }
func (c *Config) GetClaimWriteRateLimit() int              { return c.ClaimWriteRateLimit }
func (c *Config) GetClaimReadRateLimit() int               { return c.ClaimReadRateLimit }
func (c *Config) GetRecallValidatedClaimWeight() float64   { return c.RecallValidatedClaimWeight }
func (c *Config) GetPromoteTxTimeoutSeconds() int          { return c.PromoteTxTimeoutSeconds }
func (c *Config) GetMemoryPackImportHistoryDays() int      { return c.SkillPackImportHistoryDays }
func (c *Config) GetSkillPackImportHistoryDays() int       { return c.SkillPackImportHistoryDays }
func (c *Config) GetAICommunityMaxNodes() int              { return c.AICommunityMaxNodes }
func (c *Config) GetControlHTTPAddr() string               { return c.ControlHTTPAddr }
func (c *Config) GetControlPortalToken() string            { return c.ControlPortalToken }
func (c *Config) GetV2BootMode() string                    { return c.V2BootMode }
func (c *Config) GetTelemetryEnabled() bool                { return c.TelemetryEnabled }
func (c *Config) GetTelemetryPrometheusURL() string        { return c.TelemetryPrometheusURL }
func (c *Config) GetTelemetryPrometheusJob() string        { return c.TelemetryPrometheusJob }
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
	mode := strings.TrimSpace(c.V2BootMode)
	return mode == V2BootModeDormant || mode == V2BootModeUAT
}

func (c *Config) ValidateV2DormantStartup() error {
	if !c.IsV2BootEnabled() {
		return nil
	}
	required := []struct {
		field string
		value string
	}{
		{"AI_API_URL", c.AIAPIURL},
		{"AI_API_KEY", c.AIAPIKey},
		{"AI_API_EMBEDDING_MODEL", c.AIEmbeddingModel},
		{"AI_REVIEWER_MODEL", c.GetAIReviewerModel()},
		{"AI_VERIFIER_MODEL", c.GetAIVerifierModel()},
		{"SEARCH_DOCUMENT_FORMAT_VERSION", c.GetSearchDocumentFormatVersion()},
		{"EMBEDDING_NORMALIZATION_VERSION", c.GetEmbeddingNormalizationVersion()},
		{"PREDICATE_REGISTRY_VERSION", c.GetPredicateRegistryVersion()},
	}
	for _, item := range required {
		if strings.TrimSpace(item.value) == "" {
			return &ValidationError{
				Field:   item.field,
				Message: "required when V2_BOOT_MODE is dormant or uat",
			}
		}
	}
	if c.AIEmbeddingDimensions <= 0 {
		return &ValidationError{
			Field:   "AI_API_EMBEDDING_DIMENSIONS",
			Message: "required when V2_BOOT_MODE is dormant or uat",
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

func validateEnum(key, value string, allowed ...string) error {
	trimmed := strings.TrimSpace(value)
	for _, candidate := range allowed {
		if trimmed == candidate {
			return nil
		}
	}
	return &ValidationError{
		Field:   key,
		Message: fmt.Sprintf("unsupported value %q", value),
	}
}

func validateBranchPriority(key, value string) error {
	allowed := map[string]bool{
		"exact":           true,
		"evidence_text":   true,
		"evidence_vector": true,
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(value, ",") {
		branch := strings.TrimSpace(part)
		if branch == "" || !allowed[branch] {
			return &ValidationError{Field: key, Message: fmt.Sprintf("unsupported branch %q", branch)}
		}
		if seen[branch] {
			return &ValidationError{Field: key, Message: fmt.Sprintf("duplicate branch %q", branch)}
		}
		seen[branch] = true
	}
	return nil
}

func validateRRFBranchWeights(key, value string) error {
	allowed := map[string]bool{
		"exact":           true,
		"evidence_text":   true,
		"evidence_vector": true,
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(value, ",") {
		chunks := strings.Split(strings.TrimSpace(part), "=")
		if len(chunks) != 2 {
			return &ValidationError{Field: key, Message: fmt.Sprintf("invalid branch weight %q", part)}
		}
		branch := strings.TrimSpace(chunks[0])
		if !allowed[branch] {
			return &ValidationError{Field: key, Message: fmt.Sprintf("unsupported branch %q", branch)}
		}
		if seen[branch] {
			return &ValidationError{Field: key, Message: fmt.Sprintf("duplicate branch %q", branch)}
		}
		seen[branch] = true
		weight, err := strconv.ParseFloat(strings.TrimSpace(chunks[1]), 64)
		if err != nil || math.IsNaN(weight) || math.IsInf(weight, 0) || weight <= 0 {
			return &ValidationError{Field: key, Message: fmt.Sprintf("invalid branch weight %q", chunks[1])}
		}
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

	// Integer fields with defaults
	// Fragment rate-limit tiers (AC-54): writes are stricter than reads because
	// a fragment create triggers an embedding call (external network + cost)
	// plus a graph write, whereas a read is a single indexed lookup.
	if err := applyIntEnvSpecs(&cfg, []intEnvSpec{
		{"REDIS_DB", 0, func(c *Config, value int) { c.RedisDB = value }},
		{"POSTGRES_MAX_OPEN_CONNS", 25, func(c *Config, value int) { c.PostgresMaxOpenConns = value }},
		{"POSTGRES_MAX_IDLE_CONNS", 10, func(c *Config, value int) { c.PostgresMaxIdleConns = value }},
		{"POSTGRES_CONN_MAX_LIFETIME_SECONDS", 1800, func(c *Config, value int) { c.PostgresConnMaxLifetimeSeconds = value }},
		{"POSTGRES_VECTOR_MAX_CONCURRENCY", 4, func(c *Config, value int) { c.PostgresVectorMaxConcurrency = value }},
		{"POSTGRES_STATEMENT_TIMEOUT_SECONDS", 30, func(c *Config, value int) { c.PostgresStatementTimeoutSeconds = value }},
		{"POSTGRES_LOCK_TIMEOUT_SECONDS", 5, func(c *Config, value int) { c.PostgresLockTimeoutSeconds = value }},
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
		{"AI_API_EMBEDDING_MAX_CONCURRENCY", 8, func(c *Config, value int) { c.AIEmbeddingMaxConcurrency = value }},
	}); err != nil {
		return cfg, err
	}

	cfg.PGVectorExtensionRequired, err = parseBoolOrDefault("PGVECTOR_EXTENSION_REQUIRED", true)
	if err != nil {
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
	cfg.SearchDocumentFormatVersion = strings.TrimSpace(os.Getenv("SEARCH_DOCUMENT_FORMAT_VERSION"))
	cfg.EmbeddingNormalizationVersion = strings.TrimSpace(os.Getenv("EMBEDDING_NORMALIZATION_VERSION"))
	cfg.PGVectorDistance = strings.TrimSpace(getEnvOrDefault("PGVECTOR_DISTANCE", "cosine"))
	cfg.PGVectorANNStrategy = strings.TrimSpace(getEnvOrDefault("PGVECTOR_ANN_STRATEGY", "auto"))
	cfg.RecallRRFBranchWeights = strings.TrimSpace(getEnvOrDefault("RECALL_RRF_BRANCH_WEIGHTS", "exact=2,evidence_text=1,evidence_vector=1"))
	cfg.RecallBranchPriority = strings.TrimSpace(getEnvOrDefault("RECALL_BRANCH_PRIORITY", "exact,evidence_vector,evidence_text"))
	cfg.RecallRequiredBranchProfile = strings.TrimSpace(getEnvOrDefault("RECALL_REQUIRED_BRANCH_PROFILE", "relationship_v2"))
	cfg.PGVectorHNSWIterativeScan = strings.TrimSpace(getEnvOrDefault("PGVECTOR_HNSW_ITERATIVE_SCAN", "strict_order"))
	cfg.PredicateRegistryVersion = strings.TrimSpace(os.Getenv("PREDICATE_REGISTRY_VERSION"))
	cfg.PredicateUnknownAction = strings.TrimSpace(getEnvOrDefault("PREDICATE_UNKNOWN_ACTION", "review"))

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
	cfg.AIReviewerModel = strings.TrimSpace(os.Getenv("AI_REVIEWER_MODEL"))
	cfg.AIVerifierModel = getEnvOrDefault("AI_VERIFIER_MODEL", "gpt-4o-mini")
	cfg.AIVerifierDisableTemperature, err = parseBoolOrDefault("AI_VERIFIER_DISABLE_TEMPERATURE", false)
	if err != nil {
		return cfg, err
	}

	if err := applyIntEnvSpecs(&cfg, []intEnvSpec{
		{"AI_VERIFIER_TIMEOUT_SECONDS", 60, func(c *Config, value int) { c.AIVerifierTimeoutSeconds = value }},
		{"AI_VERIFIER_MAX_CONCURRENCY", 5, func(c *Config, value int) { c.AIVerifierMaxConcurrency = value }},
		{"AI_VERIFIER_COOLDOWN_POLL_SECONDS", 60, func(c *Config, value int) { c.AIVerifierCooldownPollSeconds = value }},
		{"AI_VERIFIER_MAX_ENTITY_RESULTS", 100, func(c *Config, value int) { c.AIVerifierMaxEntityResults = value }},
		{"AI_VERIFIER_MAX_RELATIONSHIP_RESULTS", 200, func(c *Config, value int) { c.AIVerifierMaxRelationshipResults = value }},
		{"AI_VERIFIER_MAX_INPUT_BYTES", 131072, func(c *Config, value int) { c.AIVerifierMaxInputBytes = value }},
		{"AI_VERIFIER_MAX_OUTPUT_BYTES", 131072, func(c *Config, value int) { c.AIVerifierMaxOutputBytes = value }},
		{"AI_VERIFIER_MAX_RESPONSE_REGENERATIONS", 2, func(c *Config, value int) { c.AIVerifierMaxResponseRegenerations = value }},
		{"RELATIONSHIP_MATCH_MAX_CANDIDATES", 100, func(c *Config, value int) { c.RelationshipMatchMaxCandidates = value }},
		{"MEMORY_PLACEMENT_WORKER_COUNT", 1, func(c *Config, value int) { c.MemoryPlacementWorkerCount = value }},
		{"MEMORY_PLACEMENT_LEASE_SECONDS", 300, func(c *Config, value int) { c.MemoryPlacementLeaseSeconds = value }},
		{"MEMORY_PLACEMENT_HEARTBEAT_SECONDS", 30, func(c *Config, value int) { c.MemoryPlacementHeartbeatSeconds = value }},
		{"MEMORY_PLACEMENT_POLL_SECONDS", 5, func(c *Config, value int) { c.MemoryPlacementPollSeconds = value }},
		{"MEMORY_PLACEMENT_MAX_ATTEMPTS", 5, func(c *Config, value int) { c.MemoryPlacementMaxAttempts = value }},
		{"EMBEDDING_WORKER_COUNT", 2, func(c *Config, value int) { c.EmbeddingWorkerCount = value }},
		{"EMBEDDING_BATCH_SIZE", 64, func(c *Config, value int) { c.EmbeddingBatchSize = value }},
		{"EMBEDDING_JOB_LEASE_SECONDS", 60, func(c *Config, value int) { c.EmbeddingJobLeaseSeconds = value }},
		{"EMBEDDING_JOB_POLL_SECONDS", 1, func(c *Config, value int) { c.EmbeddingJobPollSeconds = value }},
		{"EMBEDDING_JOB_MAX_ATTEMPTS", 20, func(c *Config, value int) { c.EmbeddingJobMaxAttempts = value }},
		{"EMBEDDING_JOB_RETRY_MAX_SECONDS", 300, func(c *Config, value int) { c.EmbeddingJobRetryMaxSeconds = value }},
		{"EMBEDDING_PENDING_STALE_SECONDS", 60, func(c *Config, value int) { c.EmbeddingPendingStaleSeconds = value }},
		{"PGVECTOR_HNSW_M", 16, func(c *Config, value int) { c.PGVectorHNSWM = value }},
		{"PGVECTOR_HNSW_EF_CONSTRUCTION", 64, func(c *Config, value int) { c.PGVectorHNSWEFConstruction = value }},
		{"PGVECTOR_INDEX_BUILD_MAX_CONCURRENCY", 1, func(c *Config, value int) { c.PGVectorIndexBuildMaxConcurrency = value }},
		{"RECALL_RRF_K", 60, func(c *Config, value int) { c.RecallRRFK = value }},
		{"RECALL_BRANCH_LIMIT_MULTIPLIER", 6, func(c *Config, value int) { c.RecallBranchLimitMultiplier = value }},
		{"RECALL_BRANCH_LIMIT_FLOOR", 60, func(c *Config, value int) { c.RecallBranchLimitFloor = value }},
		{"RECALL_BRANCH_LIMIT_MAX", 200, func(c *Config, value int) { c.RecallBranchLimitMax = value }},
		{"RECALL_MAX_ENTITY_SEEDS", 20, func(c *Config, value int) { c.RecallMaxEntitySeeds = value }},
		{"RECALL_DISCOVERY_RELATIONSHIPS_PER_EVIDENCE", 2, func(c *Config, value int) { c.RecallDiscoveryRelationshipsPerEvidence = value }},
		{"RECALL_MAX_GRAPH_DEPTH", 1, func(c *Config, value int) { c.RecallMaxGraphDepth = value }},
		{"RECALL_MAX_EDGES", 100, func(c *Config, value int) { c.RecallMaxEdges = value }},
		{"RECALL_GRAPH_TIMEOUT_MILLISECONDS", 500, func(c *Config, value int) { c.RecallGraphTimeoutMilliseconds = value }},
		{"PGVECTOR_EXACT_FILTERED_MAX_ROWS", 5000, func(c *Config, value int) { c.PGVectorExactFilteredMaxRows = value }},
		{"PGVECTOR_HNSW_EF_SEARCH", 100, func(c *Config, value int) { c.PGVectorHNSWEFSearch = value }},
		{"PGVECTOR_HNSW_MAX_SCAN_TUPLES", 20000, func(c *Config, value int) { c.PGVectorHNSWMaxScanTuples = value }},
		{"PGVECTOR_HNSW_SCAN_MEM_MULTIPLIER", 1, func(c *Config, value int) { c.PGVectorHNSWScanMemMultiplier = value }},
		{"PREDICATE_REGISTRY_CACHE_TTL_SECONDS", 60, func(c *Config, value int) { c.PredicateRegistryCacheTTLSeconds = value }},
		{"REMEMBER_RATE_LIMIT_PER_MINUTE", 60, func(c *Config, value int) { c.RememberRateLimitPerMinute = value }},
		{"RECALL_RATE_LIMIT_PER_MINUTE", 300, func(c *Config, value int) { c.RecallRateLimitPerMinute = value }},
		{"TRACE_RATE_LIMIT_PER_MINUTE", 120, func(c *Config, value int) { c.TraceRateLimitPerMinute = value }},
		{"CLAIM_WRITE_RATE_LIMIT", 60, func(c *Config, value int) { c.ClaimWriteRateLimit = value }},
		{"CLAIM_READ_RATE_LIMIT", 300, func(c *Config, value int) { c.ClaimReadRateLimit = value }},
	}); err != nil {
		return cfg, err
	}

	cfg.RecallRRFEnabled, err = parseBoolOrDefault("RECALL_RRF_ENABLED", false)
	if err != nil {
		return cfg, err
	}
	cfg.RecallDeterministicRerankEnabled, err = parseBoolOrDefault("RECALL_DETERMINISTIC_RERANK_ENABLED", true)
	if err != nil {
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
	if err := validateEnum("V2_BOOT_MODE", cfg.V2BootMode, V2BootModeOff, V2BootModeDormant, V2BootModeUAT); err != nil {
		return cfg, err
	}
	if cfg.DistributedCoordinationRequired && strings.TrimSpace(cfg.RedisAddr) == "" {
		return cfg, &ValidationError{
			Field:   "DISTRIBUTED_COORDINATION_REQUIRED",
			Message: "REDIS_ADDR is required when distributed coordination is required",
		}
	}
	if err := validateEnum("PGVECTOR_DISTANCE", cfg.PGVectorDistance, "cosine"); err != nil {
		return cfg, err
	}
	if err := validateEnum("PGVECTOR_ANN_STRATEGY", cfg.PGVectorANNStrategy, "auto", "exact", "vector_hnsw", "halfvec_hnsw"); err != nil {
		return cfg, err
	}
	if err := validateEnum("PGVECTOR_HNSW_ITERATIVE_SCAN", cfg.PGVectorHNSWIterativeScan, "strict_order", "relaxed_order"); err != nil {
		return cfg, err
	}
	if err := validateEnum("PREDICATE_UNKNOWN_ACTION", cfg.PredicateUnknownAction, "review"); err != nil {
		return cfg, err
	}
	if err := validateBranchPriority("RECALL_BRANCH_PRIORITY", cfg.RecallBranchPriority); err != nil {
		return cfg, err
	}
	if err := validateRRFBranchWeights("RECALL_RRF_BRANCH_WEIGHTS", cfg.RecallRRFBranchWeights); err != nil {
		return cfg, err
	}

	// Validate numeric limits > 0
	numericFields := []struct {
		name  string
		value int
	}{
		{"POSTGRES_MAX_OPEN_CONNS", cfg.PostgresMaxOpenConns},
		{"POSTGRES_MAX_IDLE_CONNS", cfg.PostgresMaxIdleConns},
		{"POSTGRES_CONN_MAX_LIFETIME_SECONDS", cfg.PostgresConnMaxLifetimeSeconds},
		{"POSTGRES_VECTOR_MAX_CONCURRENCY", cfg.PostgresVectorMaxConcurrency},
		{"POSTGRES_STATEMENT_TIMEOUT_SECONDS", cfg.PostgresStatementTimeoutSeconds},
		{"POSTGRES_LOCK_TIMEOUT_SECONDS", cfg.PostgresLockTimeoutSeconds},
		{"HTTP_MAX_BODY_BYTES", cfg.HTTPMaxBodyBytes},
		{"AUTH_VERIFY_MAX_CONCURRENCY", cfg.AuthVerifyMaxConcurrency},
		{"GRAPH_QUERY_DEFAULT_TIMEOUT_SECONDS", cfg.GraphQueryDefaultTimeoutSeconds},
		{"GRAPH_QUERY_MAX_TIMEOUT_SECONDS", cfg.GraphQueryMaxTimeoutSeconds},
		{"RATE_LIMIT_PER_MINUTE", cfg.RateLimitPerMinute},
		{"REMEMBER_RATE_LIMIT_PER_MINUTE", cfg.RememberRateLimitPerMinute},
		{"RECALL_RATE_LIMIT_PER_MINUTE", cfg.RecallRateLimitPerMinute},
		{"TRACE_RATE_LIMIT_PER_MINUTE", cfg.TraceRateLimitPerMinute},
		{"SSE_HEARTBEAT_SECONDS", cfg.SSEHeartbeatSeconds},
		{"SSE_MAX_DURATION_SECONDS", cfg.SSEMaxDurationSeconds},
		{"SSE_MAX_CONCURRENT_STREAMS", cfg.SSEMaxConcurrentStreams},
		{"AI_API_EMBEDDING_MAX_CONCURRENCY", cfg.AIEmbeddingMaxConcurrency},
		{"AI_VERIFIER_TIMEOUT_SECONDS", cfg.AIVerifierTimeoutSeconds},
		{"AI_VERIFIER_MAX_CONCURRENCY", cfg.AIVerifierMaxConcurrency},
		{"AI_VERIFIER_COOLDOWN_POLL_SECONDS", cfg.AIVerifierCooldownPollSeconds},
		{"AI_VERIFIER_MAX_ENTITY_RESULTS", cfg.AIVerifierMaxEntityResults},
		{"AI_VERIFIER_MAX_RELATIONSHIP_RESULTS", cfg.AIVerifierMaxRelationshipResults},
		{"AI_VERIFIER_MAX_INPUT_BYTES", cfg.AIVerifierMaxInputBytes},
		{"AI_VERIFIER_MAX_OUTPUT_BYTES", cfg.AIVerifierMaxOutputBytes},
		{"AI_VERIFIER_MAX_RESPONSE_REGENERATIONS", cfg.AIVerifierMaxResponseRegenerations},
		{"RELATIONSHIP_MATCH_MAX_CANDIDATES", cfg.RelationshipMatchMaxCandidates},
		{"MEMORY_PLACEMENT_WORKER_COUNT", cfg.MemoryPlacementWorkerCount},
		{"MEMORY_PLACEMENT_LEASE_SECONDS", cfg.MemoryPlacementLeaseSeconds},
		{"MEMORY_PLACEMENT_HEARTBEAT_SECONDS", cfg.MemoryPlacementHeartbeatSeconds},
		{"MEMORY_PLACEMENT_POLL_SECONDS", cfg.MemoryPlacementPollSeconds},
		{"MEMORY_PLACEMENT_MAX_ATTEMPTS", cfg.MemoryPlacementMaxAttempts},
		{"EMBEDDING_WORKER_COUNT", cfg.EmbeddingWorkerCount},
		{"EMBEDDING_BATCH_SIZE", cfg.EmbeddingBatchSize},
		{"EMBEDDING_JOB_LEASE_SECONDS", cfg.EmbeddingJobLeaseSeconds},
		{"EMBEDDING_JOB_POLL_SECONDS", cfg.EmbeddingJobPollSeconds},
		{"EMBEDDING_JOB_MAX_ATTEMPTS", cfg.EmbeddingJobMaxAttempts},
		{"EMBEDDING_JOB_RETRY_MAX_SECONDS", cfg.EmbeddingJobRetryMaxSeconds},
		{"EMBEDDING_PENDING_STALE_SECONDS", cfg.EmbeddingPendingStaleSeconds},
		{"PGVECTOR_HNSW_M", cfg.PGVectorHNSWM},
		{"PGVECTOR_HNSW_EF_CONSTRUCTION", cfg.PGVectorHNSWEFConstruction},
		{"PGVECTOR_INDEX_BUILD_MAX_CONCURRENCY", cfg.PGVectorIndexBuildMaxConcurrency},
		{"RECALL_RRF_K", cfg.RecallRRFK},
		{"RECALL_BRANCH_LIMIT_MULTIPLIER", cfg.RecallBranchLimitMultiplier},
		{"RECALL_BRANCH_LIMIT_FLOOR", cfg.RecallBranchLimitFloor},
		{"RECALL_BRANCH_LIMIT_MAX", cfg.RecallBranchLimitMax},
		{"RECALL_MAX_ENTITY_SEEDS", cfg.RecallMaxEntitySeeds},
		{"RECALL_DISCOVERY_RELATIONSHIPS_PER_EVIDENCE", cfg.RecallDiscoveryRelationshipsPerEvidence},
		{"RECALL_MAX_GRAPH_DEPTH", cfg.RecallMaxGraphDepth},
		{"RECALL_MAX_EDGES", cfg.RecallMaxEdges},
		{"RECALL_GRAPH_TIMEOUT_MILLISECONDS", cfg.RecallGraphTimeoutMilliseconds},
		{"PGVECTOR_EXACT_FILTERED_MAX_ROWS", cfg.PGVectorExactFilteredMaxRows},
		{"PGVECTOR_HNSW_EF_SEARCH", cfg.PGVectorHNSWEFSearch},
		{"PGVECTOR_HNSW_MAX_SCAN_TUPLES", cfg.PGVectorHNSWMaxScanTuples},
		{"PGVECTOR_HNSW_SCAN_MEM_MULTIPLIER", cfg.PGVectorHNSWScanMemMultiplier},
		{"PREDICATE_REGISTRY_CACHE_TTL_SECONDS", cfg.PredicateRegistryCacheTTLSeconds},
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
	if cfg.MemoryPlacementHeartbeatSeconds >= cfg.MemoryPlacementLeaseSeconds {
		return cfg, &ValidationError{
			Field:   "MEMORY_PLACEMENT_HEARTBEAT_SECONDS",
			Message: "must be less than MEMORY_PLACEMENT_LEASE_SECONDS",
		}
	}
	if cfg.EmbeddingJobPollSeconds >= cfg.EmbeddingJobLeaseSeconds {
		return cfg, &ValidationError{
			Field:   "EMBEDDING_JOB_POLL_SECONDS",
			Message: "must be less than EMBEDDING_JOB_LEASE_SECONDS",
		}
	}
	if cfg.EmbeddingBatchSize > 256 {
		return cfg, &ValidationError{
			Field:   "EMBEDDING_BATCH_SIZE",
			Message: fmt.Sprintf("must be between 1 and 256, got %d", cfg.EmbeddingBatchSize),
		}
	}
	if cfg.RecallBranchLimitFloor > cfg.RecallBranchLimitMax {
		return cfg, &ValidationError{
			Field:   "RECALL_BRANCH_LIMIT_FLOOR",
			Message: fmt.Sprintf("must be less than or equal to RECALL_BRANCH_LIMIT_MAX, got %d > %d", cfg.RecallBranchLimitFloor, cfg.RecallBranchLimitMax),
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
