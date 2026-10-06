package domain

import "time"

const (
	AppConfigOntologyEnabled      = "ONTOLOGY_MAINTENANCE_ENABLED"
	AppConfigOntologyCadenceHours = "ONTOLOGY_MAINTENANCE_CADENCE_HOURS"
	AppConfigOntologyStartTime    = "ONTOLOGY_MAINTENANCE_START_TIME_LOCAL"
	AppConfigOntologyModel        = "ONTOLOGY_MAINTENANCE_MODEL"
	AppConfigOntologyConcurrency  = "ONTOLOGY_MAINTENANCE_MAX_CONCURRENCY"
	AppConfigOntologyInputTokens  = "ONTOLOGY_MAINTENANCE_INPUT_TOKENS"
	AppConfigOntologyOutputTokens = "ONTOLOGY_MAINTENANCE_OUTPUT_TOKENS"
)

type OntologyMaintenanceConfig struct {
	Enabled         bool   `json:"enabled"`
	CadenceHours    int    `json:"cadence_hours"`
	StartTimeLocal  string `json:"start_time_local"`
	Timezone        string `json:"timezone"`
	Model           string `json:"model"`
	MaxConcurrency  int    `json:"max_concurrency"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	SettingsVersion string `json:"settings_version"`
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
}
