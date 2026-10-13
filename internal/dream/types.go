// Package dream retains read-only operator history and diagnostic retention.
package dream

import (
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

var (
	ErrDreamNotFound      = errors.New("dream not found")
	ErrInvalidDreamStatus = errors.New("invalid dream status")
)

type RunCycleResult struct {
	RunID                    string           `json:"run_id"`
	TeamID                   string           `json:"team_id"`
	RunDate                  string           `json:"run_date"`
	StartedAt                time.Time        `json:"started_at"`
	CompletedAt              time.Time        `json:"completed_at"`
	InputRelationships       int              `json:"input_relationships"`
	CreatedDreams            int              `json:"created_dreams"`
	RejectedDreams           int              `json:"rejected_dreams"`
	ScheduledFor             time.Time        `json:"scheduled_for,omitempty"`
	AttemptCount             int              `json:"attempt_count,omitempty"`
	ProviderModel            string           `json:"provider_model,omitempty"`
	ProviderTurns            int              `json:"provider_turns,omitempty"`
	ProviderInputTokens      int              `json:"provider_input_tokens,omitempty"`
	ProviderOutputTokens     int              `json:"provider_output_tokens,omitempty"`
	AttemptedPaths           int              `json:"attempted_paths,omitempty"`
	ProviderProposals        int              `json:"provider_proposals,omitempty"`
	OutcomeSummary           map[string]int   `json:"outcome_summary,omitempty"`
	Status                   string           `json:"status"`
	Error                    string           `json:"error,omitempty"`
	Lane                     domain.DreamLane `json:"lane"`
	EvidenceTargets          int              `json:"evidence_targets,omitempty"`
	EvaluatedEvidenceTargets int              `json:"evaluated_evidence_targets,omitempty"`
}

type ListOptions struct {
	Limit     int
	Cursor    string
	Status    string
	Sort      string
	Direction string
}

type StatusResult struct {
	LatestRun               *RunCycleResult `json:"latest_run,omitempty"`
	Retired                 bool            `json:"retired"`
	HistoricalProposedCount int             `json:"historical_proposed_count"`
}
