// Package communityservice owns scheduled community snapshot derivation. It
// never runs from boot, writes, or the first recall request.
package communityservice

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

type AppConfig interface {
	CommunityDetectionRuntimeConfig(ctx context.Context) (domain.CommunityDetectionRuntimeConfig, error)
}

type ProfileService interface {
	List(ctx context.Context, limit, offset int) ([]*domain.Profile, error)
}

// SummaryProvider is deliberately narrower than the semantic verifier
// interface. The service supplies only already-authorized, bounded graph
// context and accepts no durable policy decisions from the provider.
type SummaryProvider interface {
	ModelName() string
	SummarizeCommunity(ctx context.Context, input domain.CommunitySummaryInput) (domain.CommunitySummary, error)
}

type Dependencies struct {
	Store     repository.CommunityRepository
	AppConfig AppConfig
	Profiles  ProfileService
	Summary   SummaryProvider
	Metrics   observability.DiscoverabilityMetrics
	Now       func() time.Time
}

type Service interface {
	RunScheduled(ctx context.Context, teamID string, windowAt time.Time) (*RunResult, error)
	Status(ctx context.Context, teamID string) (*StatusResult, error)
}

type RunResult struct {
	RunID             string    `json:"run_id"`
	TeamID            string    `json:"team_id"`
	WindowKey         string    `json:"window_key"`
	Status            string    `json:"status"`
	NodeCount         int       `json:"node_count"`
	EdgeCount         int       `json:"edge_count"`
	CommunityCount    int       `json:"community_count"`
	SourceFingerprint string    `json:"source_fingerprint,omitempty"`
	ProviderModel     string    `json:"provider_model,omitempty"`
	ProviderAttempts  int       `json:"provider_attempts,omitempty"`
	Error             string    `json:"error,omitempty"`
	StartedAt         time.Time `json:"started_at,omitempty"`
	CompletedAt       time.Time `json:"completed_at,omitempty"`
}

type StatusResult struct {
	EffectiveConfig       domain.CommunityDetectionRuntimeConfig `json:"effective_config"`
	LatestRun             *RunResult                             `json:"latest_run,omitempty"`
	CurrentCommunityCount int                                    `json:"current_community_count"`
}

func uuidString(value string) uuid.UUID {
	parsed, _ := uuid.Parse(value)
	return parsed
}
