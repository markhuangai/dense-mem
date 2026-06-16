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

	AppConfigDreamingEnabled           = "DREAMING_ENABLED"
	AppConfigDreamingForceEnabled      = "DREAMING_FORCE_ENABLED"
	AppConfigDreamingStartTimeLocal    = "DREAMING_START_TIME_LOCAL"
	AppConfigDreamingReflectEnabled    = "DREAMING_REFLECT_ENABLED"
	AppConfigDreamingReevaluateEnabled = "DREAMING_REEVALUATE_ENABLED"
	AppConfigDreamingDreamEnabled      = "DREAMING_DREAM_ENABLED"
	AppConfigDreamingMaxOutputs        = "DREAMING_MAX_OUTPUTS"

	AppConfigCommunityDetectionEnabled        = "COMMUNITY_DETECTION_ENABLED"
	AppConfigCommunityDetectionStartTimeLocal = "COMMUNITY_DETECTION_START_TIME_LOCAL"
	AppConfigCommunityDetectionMaxConcurrency = "COMMUNITY_DETECTION_MAX_CONCURRENCY"
	AppConfigCommunityDetectionJitterSeconds  = "COMMUNITY_DETECTION_JITTER_SECONDS"

	AppConfigOperationLogRetentionDays = "OPERATION_LOG_RETENTION_DAYS"
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
