package domain

import "time"

const (
	AppConfigOntologyEnabled         = "ONTOLOGY_MAINTENANCE_ENABLED"
	AppConfigOntologyCadenceHours    = "ONTOLOGY_MAINTENANCE_CADENCE_HOURS"
	AppConfigOntologyStartTime       = "ONTOLOGY_MAINTENANCE_START_TIME_LOCAL"
	AppConfigOntologyModel           = "ONTOLOGY_MAINTENANCE_MODEL"
	AppConfigOntologyConcurrency     = "ONTOLOGY_MAINTENANCE_MAX_CONCURRENCY"
	AppConfigOntologyTeamConcurrency = "ONTOLOGY_MAINTENANCE_TEAM_MAX_CONCURRENCY"
	AppConfigOntologyTeamOverrides   = "ONTOLOGY_MAINTENANCE_TEAM_OVERRIDES_JSON"
	AppConfigOntologyInputTokens     = "ONTOLOGY_MAINTENANCE_INPUT_TOKENS"
	AppConfigOntologyOutputTokens    = "ONTOLOGY_MAINTENANCE_OUTPUT_TOKENS"
)

const DefaultOntologyTeamConcurrency = 1

type OntologyTeamConcurrencyOverride struct {
	TeamID         string `json:"team_id"`
	MaxConcurrency int    `json:"max_concurrency"`
}

type OntologyMaintenanceConfig struct {
	Enabled                  bool                              `json:"enabled"`
	CadenceHours             int                               `json:"cadence_hours"`
	StartTimeLocal           string                            `json:"start_time_local"`
	Timezone                 string                            `json:"timezone"`
	Model                    string                            `json:"model"`
	MaxConcurrency           int                               `json:"max_concurrency"`
	TeamMaxConcurrency       int                               `json:"team_max_concurrency"`
	TeamConcurrencyOverrides []OntologyTeamConcurrencyOverride `json:"team_concurrency_overrides"`
	InputTokens              int64                             `json:"input_tokens"`
	OutputTokens             int64                             `json:"output_tokens"`
	SettingsVersion          string                            `json:"settings_version"`
}

func (c OntologyMaintenanceConfig) TeamConcurrency(teamID string) int {
	limit := c.TeamMaxConcurrency
	if limit == 0 {
		limit = DefaultOntologyTeamConcurrency
	}
	for _, override := range c.TeamConcurrencyOverrides {
		if override.TeamID == teamID {
			limit = override.MaxConcurrency
			break
		}
	}
	return min(limit, c.MaxConcurrency)
}

type OntologyMaintenanceConfigItem struct {
	Key            string    `json:"key"`
	Value          string    `json:"value"`
	EffectiveValue string    `json:"effective_value"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type OntologyMaintenanceSettings struct {
	UpdateTime string                          `json:"update_time"`
	Items      []OntologyMaintenanceConfigItem `json:"items"`
	Effective  OntologyMaintenanceConfig       `json:"effective"`
}

type OntologyMaintenanceCommand struct {
	OperationKey string `json:"operation_key"`
	Action       string `json:"action"`
	MaxBatches   int    `json:"max_batches"`
	RetryRunID   string `json:"retry_run_id,omitempty"`
	WindowID     string `json:"window_id,omitempty"`
	InputTokens  int64  `json:"input_tokens,omitempty"`
	OutputTokens int64  `json:"output_tokens,omitempty"`
}
