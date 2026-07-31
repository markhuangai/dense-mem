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
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
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

type AppConfig interface {
	DreamingRuntimeConfig(ctx context.Context) (domain.DreamingRuntimeConfig, error)
}

type ProfileService interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Profile, error)
	List(ctx context.Context, limit, offset int) ([]*domain.Profile, error)
}

type TeamConfigService interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Team, error)
}

type CycleLocker interface {
	WithCycleLock(ctx context.Context, db *gorm.DB, profileID, runDate string, timeout time.Duration, fn func(tx *gorm.DB) error) error
}

type Generator interface {
	Generate(ctx context.Context, profileID string, req GenerateRequest) ([]GeneratedDream, error)
	Model() string
}

type Dependencies struct {
	Remember       memoryservice.RememberService
	Store          repository.DreamRepository
	ScheduledStore repository.ScheduledDreamRepository
	AppConfig      AppConfig
	Profiles       ProfileService
	Locker         CycleLocker
	Postgres       *gorm.DB
	Generator      Generator
	Metrics        observability.DiscoverabilityMetrics
	Now            func() time.Time
}

type Service interface {
	RunCycle(ctx context.Context, profileID string, req RunCycleRequest) (*RunCycleResult, error)
	RunScheduledCycle(ctx context.Context, teamID string, windowAt time.Time) (*RunCycleResult, error)
	RecordMissedScheduledCycle(ctx context.Context, teamID, runDate string) (*RunCycleResult, error)
	List(ctx context.Context, profileID string, opts ListOptions) ([]*domain.Dream, string, error)
	Get(ctx context.Context, profileID, dreamID string) (*domain.Dream, error)
	ListRuns(ctx context.Context, profileID string, limit int) ([]*RunCycleResult, error)
	Recall(ctx context.Context, profileID, query string, limit int) ([]*domain.Dream, error)
	ResolveFeedback(ctx context.Context, profileID string, req ResolveFeedbackRequest) (*ResolveFeedbackResult, error)
	Status(ctx context.Context, profileID string) (*StatusResult, error)
	EffectiveConfig(ctx context.Context, profileID string) (EffectiveConfig, error)
}

type RunCycleRequest struct {
	Manual     bool        `json:"manual,omitempty"`
	MaxOutputs int         `json:"max_outputs,omitempty"`
	SeedDreams []SeedDream `json:"seed_dreams,omitempty"`
}

type RunCycleResult struct {
	RunID              string    `json:"run_id"`
	TeamID             string    `json:"team_id"`
	RunDate            string    `json:"run_date"`
	StartedAt          time.Time `json:"started_at"`
	CompletedAt        time.Time `json:"completed_at"`
	InputRelationships int       `json:"input_relationships"`
	CreatedDreams      int       `json:"created_dreams"`
	RejectedDreams     int       `json:"rejected_dreams"`
	Status             string    `json:"status"`
	Error              string    `json:"error,omitempty"`
}

type ListOptions struct {
	Limit     int
	Cursor    string
	Status    string
	Sort      string
	Direction string
}

type ResolveFeedbackRequest struct {
	DreamID           string                                `json:"dream_id"`
	Decision          string                                `json:"decision"`
	Feedback          string                                `json:"feedback,omitempty"`
	Evidence          []memoryservice.RememberEvidenceInput `json:"evidence,omitempty"`
	EntityHints       []map[string]any                      `json:"entity_hints,omitempty"`
	RelationshipHints []map[string]any                      `json:"relationship_hints,omitempty"`
	IdempotencyKey    string                                `json:"idempotency_key,omitempty"`
}

type ResolveFeedbackResult struct {
	Dream   *domain.Dream                 `json:"dream"`
	Memory  *memoryservice.RememberResult `json:"memory,omitempty"`
	Deleted bool                          `json:"deleted,omitempty"`
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

type SeedDream struct {
	Hypothesis      string                  `json:"hypothesis"`
	WhatIf          string                  `json:"what_if,omitempty"`
	PossibleOutcome string                  `json:"possible_outcome,omitempty"`
	Rationale       string                  `json:"rationale,omitempty"`
	Likelihood      float64                 `json:"likelihood,omitempty"`
	Confidence      float64                 `json:"confidence,omitempty"`
	SourceRefs      []domain.DreamSourceRef `json:"source_refs"`
}
