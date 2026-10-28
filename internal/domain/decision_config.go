package domain

import "time"

const (
	AppConfigDecisionDeduplication = "DECISION_DEDUPLICATION_ENABLED"
	AppConfigDecisionSecurity      = "DECISION_SECURITY_REVIEW_ENABLED"
	AppConfigDecisionConflict      = "DECISION_CONFLICT_REVIEW_ENABLED"
	AppConfigDecisionReranking     = "DECISION_RECALL_RERANKING_ENABLED"
)

type DecisionTasks struct {
	Deduplication   bool `json:"deduplication"`
	SecurityReview  bool `json:"security_review"`
	ConflictReview  bool `json:"conflict_review"`
	RecallReranking bool `json:"recall_reranking"`
}

type DecisionProviderStatus struct {
	Configured             bool   `json:"configured"`
	Provider               string `json:"provider"`
	Model                  string `json:"model"`
	MaxInputTokens         int    `json:"max_input_tokens"`
	MaxQuestionInputTokens int    `json:"max_question_input_tokens"`
	LongContextEnabled     bool   `json:"long_context_enabled"`
}

type DecisionRuntimeConfig struct {
	Revision       string                 `json:"revision"`
	Provider       DecisionProviderStatus `json:"provider"`
	Preferences    DecisionTasks          `json:"preferences"`
	Effective      DecisionTasks          `json:"effective"`
	Overrides      DecisionTasks          `json:"overrides"`
	Stale          bool                   `json:"stale"`
	LastVerifiedAt time.Time              `json:"last_verified_at"`
}

type DecisionConfigItem struct {
	Key            string    `json:"key"`
	Value          string    `json:"value"`
	EffectiveValue string    `json:"effective_value"`
	Source         string    `json:"source"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type DecisionConfigSettings struct {
	UpdateTime string                `json:"update_time"`
	Items      []DecisionConfigItem  `json:"items"`
	Effective  DecisionRuntimeConfig `json:"effective"`
}
