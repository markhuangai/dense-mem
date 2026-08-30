// Package dreamservice implements scheduled team dreaming and its reviewable
// hypothesis layer.
package dreamservice

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
	"gorm.io/gorm"
)

const (
	DefaultStartTimeLocal = "03:00"
	DefaultTimezone       = "Local"
	DefaultMaxOutputs     = 5
)

var (
	ErrDreamNotFound      = errors.New("dream not found")
	ErrInvalidDreamStatus = errors.New("invalid dream status")
)

// ConfirmationBusyError reports that another confirmation currently owns the
// Hypothesis admission slot. The MCP registry translates it into bounded
// retry guidance without exposing repository details.
type ConfirmationBusyError struct{}

func (e *ConfirmationBusyError) Error() string {
	return "dream confirmation is already in progress"
}

func (e *ConfirmationBusyError) Unwrap() error {
	return repository.ErrDreamConfirmationBusy
}

type AppConfig interface {
	DreamingRuntimeConfig(ctx context.Context) (domain.DreamingRuntimeConfig, error)
}

type TeamService interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Team, error)
	List(ctx context.Context, limit, offset int) ([]*domain.Team, error)
}

type TeamConfigService interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Team, error)
}

type CycleLocker interface {
	WithCycleLock(ctx context.Context, db *gorm.DB, teamID, runDate string, timeout time.Duration, fn func(tx *gorm.DB) error) error
}

type Generator interface {
	Generate(ctx context.Context, teamID string, req GenerateRequest) ([]GeneratedDream, error)
	Model() string
}

type RememberService interface {
	Remember(context.Context, rememberapp.RememberRequest) (*rememberapp.RememberResult, error)
}

type Dependencies struct {
	Remember           RememberService
	RememberIngests    repository.RememberIngestLookup
	Store              repository.DreamRepository
	ScheduledStore     repository.ScheduledDreamRepository
	AppConfig          AppConfig
	Teams              TeamService
	Locker             CycleLocker
	Postgres           *gorm.DB
	Generator          Generator
	Metrics            observability.DiscoverabilityMetrics
	ProviderCycleLease time.Duration
	Now                func() time.Time
}

type Service interface {
	RunCycle(ctx context.Context, teamID string, req RunCycleRequest) (*RunCycleResult, error)
	RunScheduledCycle(ctx context.Context, teamID string, windowAt time.Time) (*RunCycleResult, error)
	RecordMissedScheduledCycle(ctx context.Context, teamID, runDate string) (*RunCycleResult, error)
	List(ctx context.Context, teamID string, opts ListOptions) ([]*domain.Dream, string, error)
	Get(ctx context.Context, teamID, dreamID string) (*domain.Dream, error)
	ListRuns(ctx context.Context, teamID string, limit int) ([]*RunCycleResult, error)
	Recall(ctx context.Context, teamID, query string, limit int) ([]*domain.Dream, error)
	ResolveFeedback(ctx context.Context, teamID string, req ResolveFeedbackRequest) (*ResolveFeedbackResult, error)
	Status(ctx context.Context, teamID string) (*StatusResult, error)
	EffectiveConfig(ctx context.Context, fallbackTeamID string) (EffectiveConfig, error)
}

type RunCycleRequest struct {
	Manual     bool        `json:"manual,omitempty"`
	MaxOutputs int         `json:"max_outputs,omitempty"`
	SeedDreams []SeedDream `json:"seed_dreams,omitempty"`
}

type RunCycleResult struct {
	RunID                string         `json:"run_id"`
	TeamID               string         `json:"team_id"`
	RunDate              string         `json:"run_date"`
	StartedAt            time.Time      `json:"started_at"`
	CompletedAt          time.Time      `json:"completed_at"`
	InputRelationships   int            `json:"input_relationships"`
	CreatedDreams        int            `json:"created_dreams"`
	RejectedDreams       int            `json:"rejected_dreams"`
	ScheduledFor         time.Time      `json:"scheduled_for,omitempty"`
	AttemptCount         int            `json:"attempt_count,omitempty"`
	ProviderModel        string         `json:"provider_model,omitempty"`
	ProviderTurns        int            `json:"provider_turns,omitempty"`
	ProviderInputTokens  int            `json:"provider_input_tokens,omitempty"`
	ProviderOutputTokens int            `json:"provider_output_tokens,omitempty"`
	AttemptedPaths       int            `json:"attempted_paths,omitempty"`
	ProviderProposals    int            `json:"provider_proposals,omitempty"`
	OutcomeSummary       map[string]int `json:"outcome_summary,omitempty"`
	Status               string         `json:"status"`
	Error                string         `json:"error,omitempty"`
}

type ListOptions struct {
	Limit     int
	Cursor    string
	Status    string
	Sort      string
	Direction string
}

type ResolveFeedbackRequest struct {
	DreamID           string                              `json:"dream_id"`
	Decision          string                              `json:"decision"`
	Feedback          string                              `json:"feedback,omitempty"`
	Evidence          []rememberapp.RememberEvidenceInput `json:"evidence,omitempty"`
	EntityHints       []map[string]any                    `json:"entity_hints,omitempty"`
	RelationshipHints []map[string]any                    `json:"relationship_hints,omitempty"`
	IdempotencyKey    string                              `json:"idempotency_key,omitempty"`
}

type ResolveFeedbackResult struct {
	Dream   *domain.Dream               `json:"dream"`
	Memory  *rememberapp.RememberResult `json:"memory,omitempty"`
	Deleted bool                        `json:"deleted,omitempty"`
}

type StatusResult struct {
	EffectiveConfig EffectiveConfig `json:"effective_config"`
	LatestRun       *RunCycleResult `json:"latest_run,omitempty"`
	PendingCount    int             `json:"pending_count"`
}

type EffectiveConfig struct {
	domain.DreamingRuntimeConfig
	TeamEnabled bool   `json:"team_enabled"`
	Source      string `json:"source"`
}

type GenerateRequest struct {
	MaxOutputs     int
	Inputs         []DreamInput
	Paths          []DreamPath
	Existing       []*domain.Dream
	GeneratorModel string
}

type DreamInput struct {
	Type      string
	ID        string
	Subject   string
	Predicate string
	Object    string
	Content   string
	Status    string
}

type GeneratedDream struct {
	PathRef          string
	PredicateRef     string
	EvidenceRefs     []string
	Hypothesis       string
	WhatIf           string
	PossibleOutcome  string
	Rationale        string
	Likelihood       float64
	Confidence       float64
	SubjectEntityID  string
	PredicateKey     string
	PredicateVersion int
	ObjectEntityID   string
	ObjectValueID    string
	SourceRefs       []domain.DreamSourceRef
}

type GenerationDiagnostics struct {
	ProviderTurns        int
	ProviderInputTokens  int
	ProviderOutputTokens int
	ProviderProposals    int
}

type DiagnosticsGenerator interface {
	GenerateWithDiagnostics(ctx context.Context, teamID string, req GenerateRequest) ([]GeneratedDream, GenerationDiagnostics, error)
}

// DreamPath is a server-derived, directed two-premise path. Database IDs stay
// in this process; the generator receives only its opaque references.
type DreamPath struct {
	PathRef           string
	Subject           DreamPathNode
	Middle            DreamPathNode
	Object            DreamPathNode
	Premises          []DreamPathPremise
	AllowedPredicates []repository.DreamTargetPredicate
}

type DreamPathNode struct {
	Ref     string
	ID      string
	Display string
	Kind    string
}

type DreamPathPremise struct {
	PremiseRef      string
	RelationshipRef string
	Input           repository.DreamInput
}

type SeedDream struct {
	Hypothesis      string                  `json:"hypothesis"`
	WhatIf          string                  `json:"what_if,omitempty"`
	PossibleOutcome string                  `json:"possible_outcome,omitempty"`
	Rationale       string                  `json:"rationale,omitempty"`
	Likelihood      float64                 `json:"likelihood,omitempty"`
	Confidence      float64                 `json:"confidence,omitempty"`
	SourceRefs      []domain.DreamSourceRef `json:"source_refs"`
}
