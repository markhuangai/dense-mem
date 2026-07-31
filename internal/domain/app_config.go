package domain

import "time"

const (
	AppConfigUpdateTimeKey = "update_time"

	AppConfigTimezone = "APP_TIMEZONE"

	AppConfigSSOPublicBaseURL              = "SSO_PUBLIC_BASE_URL"
	AppConfigSSOEntitlementCacheTTLSeconds = "SSO_ENTITLEMENT_CACHE_TTL_SECONDS"
	AppConfigSSOSessionTTLSeconds          = "SSO_SESSION_TTL_SECONDS"
	AppConfigSSOStateTTLSeconds            = "SSO_STATE_TTL_SECONDS"
	AppConfigSSOHTTPTimeoutSeconds         = "SSO_HTTP_TIMEOUT_SECONDS"
	AppConfigSSOCookieSecure               = "SSO_COOKIE_SECURE"

	AppConfigDreamingEnabled        = "DREAMING_ENABLED"
	AppConfigDreamingForceEnabled   = "DREAMING_FORCE_ENABLED"
	AppConfigDreamingStartTimeLocal = "DREAMING_START_TIME_LOCAL"
	AppConfigDreamingMaxOutputs     = "DREAMING_MAX_OUTPUTS"

	AppConfigCommunityDetectionEnabled        = "COMMUNITY_DETECTION_ENABLED"
	AppConfigCommunityDetectionStartTimeLocal = "COMMUNITY_DETECTION_START_TIME_LOCAL"
	AppConfigCommunityDetectionMaxConcurrency = "COMMUNITY_DETECTION_MAX_CONCURRENCY"
	AppConfigCommunityDetectionJitterSeconds  = "COMMUNITY_DETECTION_JITTER_SECONDS"

	AppConfigOperationLogRetentionDays = "OPERATION_LOG_RETENTION_DAYS"

	AppConfigRecallFeedbackEnabled       = "RECALL_FEEDBACK_ENABLED"
	AppConfigRecallFeedbackRetentionDays = "RECALL_FEEDBACK_RETENTION_DAYS"

	AppConfigEvaluationModeEnabled   = "EVALUATION_MODE_ENABLED"
	AppConfigEvaluationExportMaxPage = "EVALUATION_EXPORT_MAX_PAGE_SIZE"

	AppConfigTelemetryCostVerifierInputUSDPerMillionTokens  = "TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS"
	AppConfigTelemetryCostVerifierOutputUSDPerMillionTokens = "TELEMETRY_COST_VERIFIER_OUTPUT_USD_PER_MILLION_TOKENS"
	AppConfigTelemetryCostEmbeddingInputUSDPerMillionTokens = "TELEMETRY_COST_EMBEDDING_INPUT_USD_PER_MILLION_TOKENS"
)

type AppConfigEntry struct {
	Key       string
	Value     string
	UpdatedAt time.Time
}

// GeneralConfigSettings is the editable runtime configuration shared across
// feature schedulers.
type GeneralConfigSettings struct {
	UpdateTime string               `json:"update_time"`
	Items      []GeneralConfigItem  `json:"items"`
	Effective  GeneralRuntimeConfig `json:"effective"`
}

// GeneralConfigItem is one control-panel editable general config entry.
type GeneralConfigItem struct {
	Key            string    `json:"key"`
	Value          string    `json:"value"`
	EffectiveValue string    `json:"effective_value"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// GeneralRuntimeConfig is the effective shared runtime config.
type GeneralRuntimeConfig struct {
	Timezone string `json:"timezone"`
}

type SSOConfigItem struct {
	Key            string
	Value          string
	EffectiveValue string
	UpdatedAt      time.Time
}

type SSOConfigSettings struct {
	UpdateTime string
	Items      []SSOConfigItem
}

// OperationLogConfigSettings is the editable runtime configuration for
// database-backed application log retention.
type OperationLogConfigSettings struct {
	UpdateTime string                    `json:"update_time"`
	Items      []OperationLogConfigItem  `json:"items"`
	Effective  OperationLogRuntimeConfig `json:"effective"`
}

// OperationLogConfigItem is one control-panel editable operation log config entry.
type OperationLogConfigItem struct {
	Key            string    `json:"key"`
	Value          string    `json:"value"`
	EffectiveValue string    `json:"effective_value"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// OperationLogRuntimeConfig is the effective operation log runtime config.
type OperationLogRuntimeConfig struct {
	RetentionDays int `json:"retention_days"`
}

// RecallFeedbackConfigSettings is the editable runtime configuration for
// host-LLM recall feedback telemetry.
type RecallFeedbackConfigSettings struct {
	UpdateTime string                      `json:"update_time"`
	Items      []RecallFeedbackConfigItem  `json:"items"`
	Effective  RecallFeedbackRuntimeConfig `json:"effective"`
}

// RecallFeedbackConfigItem is one control-panel editable recall feedback config entry.
type RecallFeedbackConfigItem struct {
	Key            string    `json:"key"`
	Value          string    `json:"value"`
	EffectiveValue string    `json:"effective_value"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// RecallFeedbackRuntimeConfig is the effective recall feedback runtime config.
type RecallFeedbackRuntimeConfig struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retention_days"`
}

// EvaluationConfigSettings is the editable runtime configuration for local
// evaluation exports and recall tracing.
type EvaluationConfigSettings struct {
	UpdateTime string                  `json:"update_time"`
	Items      []EvaluationConfigItem  `json:"items"`
	Effective  EvaluationRuntimeConfig `json:"effective"`
}

// EvaluationConfigItem is one control-panel editable evaluation config entry.
type EvaluationConfigItem struct {
	Key            string    `json:"key"`
	Value          string    `json:"value"`
	EffectiveValue string    `json:"effective_value"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// EvaluationRuntimeConfig is the effective evaluation-mode runtime config.
type EvaluationRuntimeConfig struct {
	Enabled           bool `json:"enabled"`
	ExportMaxPageSize int  `json:"export_max_page_size"`
}

// TelemetryPricingConfigSettings contains editable token pricing used only for
// operational cost telemetry.
type TelemetryPricingConfigSettings struct {
	UpdateTime string                        `json:"update_time"`
	Items      []TelemetryPricingConfigItem  `json:"items"`
	Effective  TelemetryPricingRuntimeConfig `json:"effective"`
}

// TelemetryPricingConfigItem is one editable telemetry pricing entry.
type TelemetryPricingConfigItem struct {
	Key            string    `json:"key"`
	Value          string    `json:"value"`
	EffectiveValue string    `json:"effective_value"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// TelemetryPricingRuntimeConfig is the effective pricing used to estimate
// provider operation cost. A nil value means that operation is unpriced.
type TelemetryPricingRuntimeConfig struct {
	VerifierInputUSDPerMillionTokens  *float64 `json:"verifier_input_usd_per_million_tokens"`
	VerifierOutputUSDPerMillionTokens *float64 `json:"verifier_output_usd_per_million_tokens"`
	EmbeddingInputUSDPerMillionTokens *float64 `json:"embedding_input_usd_per_million_tokens"`
}
