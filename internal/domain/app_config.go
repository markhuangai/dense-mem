package domain

import "time"

const (
	AppConfigUpdateTimeKey = "update_time"

	AppConfigSSOPublicBaseURL              = "SSO_PUBLIC_BASE_URL"
	AppConfigSSOEntitlementCacheTTLSeconds = "SSO_ENTITLEMENT_CACHE_TTL_SECONDS"
	AppConfigSSOSessionTTLSeconds          = "SSO_SESSION_TTL_SECONDS"
	AppConfigSSOStateTTLSeconds            = "SSO_STATE_TTL_SECONDS"
	AppConfigSSOHTTPTimeoutSeconds         = "SSO_HTTP_TIMEOUT_SECONDS"
	AppConfigSSOCookieSecure               = "SSO_COOKIE_SECURE"

	AppConfigDreamingEnabled           = "DREAMING_ENABLED"
	AppConfigDreamingForceEnabled      = "DREAMING_FORCE_ENABLED"
	AppConfigDreamingStartTimeLocal    = "DREAMING_START_TIME_LOCAL"
	AppConfigDreamingTimezone          = "DREAMING_TIMEZONE"
	AppConfigDreamingReflectEnabled    = "DREAMING_REFLECT_ENABLED"
	AppConfigDreamingReevaluateEnabled = "DREAMING_REEVALUATE_ENABLED"
	AppConfigDreamingDreamEnabled      = "DREAMING_DREAM_ENABLED"
	AppConfigDreamingMaxOutputs        = "DREAMING_MAX_OUTPUTS"
)

type AppConfigEntry struct {
	Key       string
	Value     string
	UpdatedAt time.Time
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
