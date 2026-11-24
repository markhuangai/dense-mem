package domain

import "time"

const (
	AppConfigTopicMaintenanceEnabled         = "TOPIC_MAINTENANCE_ENABLED"
	AppConfigTopicMaintenanceCadenceHours    = "TOPIC_MAINTENANCE_CADENCE_HOURS"
	AppConfigTopicMaintenanceStartTime       = "TOPIC_MAINTENANCE_START_TIME_LOCAL"
	AppConfigTopicMaintenanceModel           = "TOPIC_MAINTENANCE_MODEL"
	AppConfigTopicMaintenanceConcurrency     = "TOPIC_MAINTENANCE_MAX_CONCURRENCY"
	AppConfigTopicMaintenanceTeamConcurrency = "TOPIC_MAINTENANCE_TEAM_MAX_CONCURRENCY"
	AppConfigTopicMaintenanceTeamOverrides   = "TOPIC_MAINTENANCE_TEAM_OVERRIDES_JSON"
	AppConfigTopicMaintenanceContextWindow   = "TOPIC_MAINTENANCE_CONTEXT_WINDOW_TOKENS"
	AppConfigTopicMaintenanceInputTokens     = "TOPIC_MAINTENANCE_INPUT_TOKENS"
	AppConfigTopicMaintenanceOutputTokens    = "TOPIC_MAINTENANCE_OUTPUT_TOKENS"
)

const DefaultTopicTeamConcurrency = 1

type TopicTeamConcurrencyOverride struct {
	TeamID         string `json:"team_id"`
	MaxConcurrency int    `json:"max_concurrency"`
}

type TopicMaintenanceConfig struct {
	Enabled                  bool                           `json:"enabled"`
	CadenceHours             int                            `json:"cadence_hours"`
	StartTimeLocal           string                         `json:"start_time_local"`
	Timezone                 string                         `json:"timezone"`
	Model                    string                         `json:"model"`
	MaxConcurrency           int                            `json:"max_concurrency"`
	TeamMaxConcurrency       int                            `json:"team_max_concurrency"`
	TeamConcurrencyOverrides []TopicTeamConcurrencyOverride `json:"team_concurrency_overrides"`
	ContextWindowTokens      int64                          `json:"context_window_tokens"`
	SettingsVersion          string                         `json:"settings_version"`
}

func (c TopicMaintenanceConfig) TeamConcurrency(teamID string) int {
	limit := c.TeamMaxConcurrency
	if limit == 0 {
		limit = DefaultTopicTeamConcurrency
	}
	for _, override := range c.TeamConcurrencyOverrides {
		if override.TeamID == teamID {
			limit = override.MaxConcurrency
			break
		}
	}
	return min(limit, c.MaxConcurrency)
}

type TopicMaintenanceConfigItem struct {
	Key            string    `json:"key"`
	Value          string    `json:"value"`
	EffectiveValue string    `json:"effective_value"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type TopicMaintenanceSettings struct {
	UpdateTime string                       `json:"update_time"`
	Items      []TopicMaintenanceConfigItem `json:"items"`
	Effective  TopicMaintenanceConfig       `json:"effective"`
}

type TopicMaintenanceCommand struct {
	OperationKey string `json:"operation_key"`
	Action       string `json:"action"`
	MaxBatches   int    `json:"max_batches"`
	RetryRunID   string `json:"retry_run_id,omitempty"`
}
